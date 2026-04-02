package store

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore implements Store against PostgreSQL.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore creates a PGStore using an existing connection pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

const jobColumns = `id, name, type, payload, status, max_retries, retry_count,
	COALESCE(error, ''), scheduled_at, created_at, updated_at, priority,
	COALESCE(idempotency_key, '')`

func scanJob(row pgx.Row) (*Job, error) {
	j := &Job{}
	var status string
	err := row.Scan(
		&j.ID, &j.Name, &j.Type, &j.Payload,
		&status, &j.MaxRetries, &j.RetryCount, &j.Error,
		&j.ScheduledAt, &j.CreatedAt, &j.UpdatedAt, &j.Priority,
		&j.IdempotencyKey,
	)
	if err != nil {
		return nil, err
	}
	j.Status = Status(status)
	return j, nil
}

// GetJobByIdempotencyKey returns an existing non-terminal job with the given
// idempotency key, or nil if none exists.
func (s *PGStore) GetJobByIdempotencyKey(ctx context.Context, key string) (*Job, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+jobColumns+` FROM jobs
		 WHERE idempotency_key = $1
		   AND status NOT IN ('cancelled', 'dead')
		 LIMIT 1`,
		key,
	)
	job, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return job, err
}

func (s *PGStore) CreateJob(ctx context.Context, j *Job) (*Job, error) {
	if j.ScheduledAt.IsZero() {
		j.ScheduledAt = time.Now().UTC()
	}
	if j.Priority == 0 {
		j.Priority = 5
	}
	var idempotencyKey *string
	if j.IdempotencyKey != "" {
		idempotencyKey = &j.IdempotencyKey
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO jobs (name, type, payload, max_retries, scheduled_at, priority, idempotency_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+jobColumns,
		j.Name, j.Type, j.Payload, j.MaxRetries, j.ScheduledAt, j.Priority, idempotencyKey,
	)
	return scanJob(row)
}

func (s *PGStore) GetJob(ctx context.Context, id uuid.UUID) (*Job, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+jobColumns+` FROM jobs WHERE id = $1`, id,
	)
	job, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("job %s not found", id)
	}
	return job, err
}

// encodeCursor encodes a (created_at, id) pair as a base64 page token.
func encodeCursor(t time.Time, id uuid.UUID) string {
	raw := t.UTC().Format(time.RFC3339Nano) + "|" + id.String()
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

// decodeCursor unpacks a page token produced by encodeCursor.
func decodeCursor(token string) (time.Time, uuid.UUID, error) {
	b, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("invalid page token: %w", err)
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, uuid.Nil, fmt.Errorf("invalid page token format")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("invalid page token timestamp: %w", err)
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("invalid page token id: %w", err)
	}
	return t, id, nil
}

func (s *PGStore) ListJobs(ctx context.Context, f ListFilter) ([]*Job, string, error) {
	pageSize := f.PageSize
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}

	args := []any{}
	where := "1=1"
	argN := 1

	if f.Status != nil {
		where += fmt.Sprintf(" AND status = $%d", argN)
		args = append(args, string(*f.Status))
		argN++
	}
	if f.PageToken != "" {
		cursorTime, cursorID, err := decodeCursor(f.PageToken)
		if err != nil {
			return nil, "", fmt.Errorf("bad page_token: %w", err)
		}
		where += fmt.Sprintf(" AND (created_at, id) < ($%d, $%d)", argN, argN+1)
		args = append(args, cursorTime, cursorID)
		argN += 2
	}

	args = append(args, pageSize+1)
	query := fmt.Sprintf(`SELECT %s FROM jobs WHERE %s ORDER BY created_at DESC, id DESC LIMIT $%d`,
		jobColumns, where, argN)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var jobs []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, "", err
		}
		jobs = append(jobs, j)
	}

	var nextToken string
	if len(jobs) > pageSize {
		jobs = jobs[:pageSize]
		last := jobs[len(jobs)-1]
		nextToken = encodeCursor(last.CreatedAt, last.ID)
	}
	return jobs, nextToken, rows.Err()
}

func (s *PGStore) ClaimJobs(ctx context.Context, n int) ([]*Job, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE jobs SET status = 'running', updated_at = NOW()
		WHERE id IN (
			SELECT id FROM jobs
			WHERE status IN ('pending', 'failed')
			  AND scheduled_at <= NOW()
			ORDER BY priority DESC, scheduled_at ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING `+jobColumns, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func (s *PGStore) UpdateStatus(ctx context.Context, id uuid.UUID, status Status, errMsg string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE jobs SET status = $1, error = NULLIF($2, ''), updated_at = NOW()
		WHERE id = $3`,
		string(status), errMsg, id,
	)
	return err
}

func (s *PGStore) IncrementRetry(ctx context.Context, id uuid.UUID, nextRun time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE jobs
		SET retry_count = retry_count + 1,
		    status      = 'pending',
		    scheduled_at = $1,
		    updated_at   = NOW()
		WHERE id = $2`,
		nextRun, id,
	)
	return err
}

func (s *PGStore) QueueDepth(ctx context.Context) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM jobs WHERE status = 'pending'`,
	).Scan(&n)
	return n, err
}

func (s *PGStore) CancelJob(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE jobs SET status = 'cancelled', updated_at = NOW()
		WHERE id = $1 AND status = 'pending'`, id,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("job %s is not pending and cannot be cancelled", id)
	}
	return nil
}
