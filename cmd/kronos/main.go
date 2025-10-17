package main

import (
	"context"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	kronosv1 "github.com/rahulmodugula/kronos/gen/kronos/v1"
	"github.com/rahulmodugula/kronos/internal/api"
	"github.com/rahulmodugula/kronos/internal/middleware"
	"github.com/rahulmodugula/kronos/internal/retry"
	"github.com/rahulmodugula/kronos/internal/scheduler"
	"github.com/rahulmodugula/kronos/internal/store"
	"github.com/rahulmodugula/kronos/internal/worker"

	"github.com/jackc/pgx/v5/pgxpool"

	migrate "github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

func main() {
	log := zerolog.New(os.Stdout).With().Timestamp().Logger()

	dsn := env("DATABASE_URL", "postgres://kronos:kronos@localhost:5432/kronos?sslmode=disable")
	grpcAddr := env("GRPC_ADDR", ":50051")
	concurrency := 10

	// Run migrations
	m, err := migrate.New("file://migrations", dsn)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to init migrations")
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		log.Fatal().Err(err).Msg("migration failed")
	}
	log.Info().Msg("migrations applied")

	// Connect to Postgres
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to postgres")
	}
	defer pool.Close()

	if err := pool.Ping(context.Background()); err != nil {
		log.Fatal().Err(err).Msg("postgres ping failed")
	}
	log.Info().Msg("connected to postgres")

	// Wire components
	pgStore := store.NewPGStore(pool)
	registry := worker.NewRegistry()
	registerHandlers(registry, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sched := scheduler.New(pgStore, nil, scheduler.Config{
		PollInterval: 2 * time.Second,
		BatchSize:    10,
		Backoff:      retry.DefaultConfig,
	}, log)

	pool_ := worker.NewPool(concurrency, registry, log, sched.OnComplete)
	// Re-create scheduler with pool wired in (avoid circular init)
	sched = scheduler.New(pgStore, pool_, scheduler.Config{
		PollInterval: 2 * time.Second,
		BatchSize:    10,
		Backoff:      retry.DefaultConfig,
	}, log)

	pool_.Start(ctx)
	go sched.Run(ctx)

	// gRPC server — interceptor order: RequestID first so logger and recovery always have an ID
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			middleware.UnaryRequestID(),
			middleware.UnaryRecovery(log),
			middleware.UnaryLogger(log),
		),
	)
	kronosv1.RegisterKronosServiceServer(grpcServer, api.New(pgStore, log))
	reflection.Register(grpcServer)

	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		log.Fatal().Err(err).Str("addr", grpcAddr).Msg("failed to listen")
	}
	log.Info().Str("addr", grpcAddr).Msg("gRPC server listening")

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-quit
		log.Info().Msg("shutting down...")
		cancel()            // stop scheduler
		grpcServer.GracefulStop()
		pool_.Stop()        // drain in-flight jobs
		log.Info().Msg("shutdown complete")
	}()

	if err := grpcServer.Serve(lis); err != nil {
		log.Fatal().Err(err).Msg("gRPC server error")
	}
}

// registerHandlers is where users wire their job types. Add real handlers here.
func registerHandlers(r *worker.Registry, log zerolog.Logger) {
	// Example: a no-op "echo" job that logs its payload.
	r.Register("echo", func(ctx context.Context, payload []byte) error {
		log.Info().RawJSON("payload", payload).Msg("echo job")
		return nil
	})
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
