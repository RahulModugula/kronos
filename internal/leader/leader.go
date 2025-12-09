// Package leader implements single-node leader election via PostgreSQL
// session-level advisory locks. Only one Kronos node holds the lock at a
// time; the others block in the election loop. When the leader's connection
// drops or context is cancelled, PostgreSQL releases the lock automatically
// and one waiting node takes over.
//
// Advisory locks are a lightweight alternative to etcd/ZooKeeper for
// deployments already using Postgres. The tradeoff is that split-brain is
// bounded by the TCP keepalive timeout rather than a lease TTL — acceptable
// for job schedulers where a few seconds of overlap is recoverable via
// idempotent handlers.
package leader

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"
)

const (
	// advisoryKey is the int64 identifying the Kronos scheduler lock.
	// Chosen to not collide with application advisory locks.
	advisoryKey = 0x4B524F4E4F53 // "KRONOS" in hex

	retryInterval     = 2 * time.Second
	heartbeatInterval = 5 * time.Second
)

// lockConn is the subset of pgx.Conn used by the elector.
// Defined as an interface so tests can inject a stub without a real database.
type lockConn interface {
	Ping(ctx context.Context) error
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Close(ctx context.Context) error
}

// connFactory opens a dedicated connection for the advisory lock.
// Defaults to pgx.Connect; tests inject a stub.
type connFactory func(ctx context.Context, dsn string) (lockConn, error)

// Elector manages leader election for a single Kronos node.
type Elector struct {
	dsn     string
	log     zerolog.Logger
	connect connFactory
}

// New creates an Elector. dsn is a standard PostgreSQL connection string.
func New(dsn string, log zerolog.Logger) *Elector {
	return &Elector{
		dsn: dsn,
		log: log,
		connect: func(ctx context.Context, dsn string) (lockConn, error) {
			return pgx.Connect(ctx, dsn)
		},
	}
}

// Run blocks until ctx is cancelled. It repeatedly tries to acquire the
// advisory lock. Once acquired, it calls onElected with a derived context
// and blocks until leadership is lost (connection failure or ctx cancel).
// onRevoked is called after leadership ends, before retrying.
//
// Typical usage in main.go:
//
//	go elector.Run(ctx,
//	    func(leaderCtx context.Context) { scheduler.Run(leaderCtx) },
//	    func() { log.Info().Msg("lost leadership, standing by") },
//	)
func (e *Elector) Run(ctx context.Context, onElected func(context.Context), onRevoked func()) {
	for {
		if ctx.Err() != nil {
			return
		}
		if err := e.elect(ctx, onElected, onRevoked); err != nil {
			e.log.Warn().Err(err).Dur("retry", retryInterval).Msg("election attempt failed, retrying")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryInterval):
		}
	}
}

func (e *Elector) elect(ctx context.Context, onElected func(context.Context), onRevoked func()) error {
	// A dedicated connection (not pool) is required: session-level advisory
	// locks are bound to the connection, not the transaction. Using a pool
	// connection would cause the lock to be silently released on recycling.
	conn, err := e.connect(ctx, e.dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(context.Background())

	acquired, err := tryLock(ctx, conn)
	if err != nil {
		return fmt.Errorf("pg_try_advisory_lock: %w", err)
	}
	if !acquired {
		e.log.Debug().Msg("advisory lock held by another node, standing by")
		return nil
	}

	e.log.Info().Msg("acquired leader lock — this node is now the scheduler leader")

	leaderCtx, loseLeadership := context.WithCancel(ctx)
	defer loseLeadership()

	// Heartbeat detects silent connection loss before OS keepalive fires.
	go func() {
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-leaderCtx.Done():
				return
			case <-ticker.C:
				if err := conn.Ping(leaderCtx); err != nil {
					e.log.Warn().Err(err).Msg("leader heartbeat failed — releasing leadership")
					loseLeadership()
					return
				}
			}
		}
	}()

	onElected(leaderCtx)

	// Explicitly release so the next standby picks up immediately.
	if err := unlock(context.Background(), conn); err != nil {
		e.log.Warn().Err(err).Msg("pg_advisory_unlock failed (connection may already be dead)")
	}

	e.log.Info().Msg("released leader lock")
	if onRevoked != nil {
		onRevoked()
	}
	return nil
}

func tryLock(ctx context.Context, conn lockConn) (bool, error) {
	var acquired bool
	err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", advisoryKey).Scan(&acquired)
	return acquired, err
}

func unlock(ctx context.Context, conn lockConn) error {
	_, err := conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", advisoryKey)
	return err
}
