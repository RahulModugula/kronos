package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	kronosv1 "github.com/rahulmodugula/kronos/gen/kronos/v1"
	"github.com/rahulmodugula/kronos/internal/api"
	"github.com/rahulmodugula/kronos/internal/config"
	"github.com/rahulmodugula/kronos/internal/health"
	"github.com/rahulmodugula/kronos/internal/metrics"
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

	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("config error")
	}

	// Run migrations
	m, err := migrate.New("file://migrations", cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to init migrations")
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		log.Fatal().Err(err).Msg("migration failed")
	}
	log.Info().Msg("migrations applied")

	// Connect to Postgres
	pool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to postgres")
	}
	defer pool.Close()

	if err := pool.Ping(context.Background()); err != nil {
		log.Fatal().Err(err).Msg("postgres ping failed")
	}
	log.Info().Msg("connected to postgres")

	// Prometheus — non-global registry keeps tests isolated
	promReg := prometheus.NewRegistry()
	m_ := metrics.New(promReg)
	pgStore := store.NewPGStore(pool)
	instrumentedStore := store.NewMetricsStore(pgStore, m_)
	metrics.RegisterQueueDepthGauge(promReg, pgStore.QueueDepth)

	// /metrics on a separate port so it doesn't share the gRPC listener
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.HandlerFor(promReg, promhttp.HandlerOpts{}))
		log.Info().Str("addr", cfg.MetricsAddr).Msg("metrics server listening")
		if err := http.ListenAndServe(cfg.MetricsAddr, mux); err != nil {
			log.Error().Err(err).Msg("metrics server error")
		}
	}()

	// Worker pool + scheduler (closure breaks the circular init dependency)
	registry := worker.NewRegistry()
	registerHandlers(registry, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var sched *scheduler.Scheduler
	workerPool := worker.NewPool(cfg.WorkerConcurrency, registry, log, func(ctx context.Context, j *store.Job, execErr error) {
		sched.OnComplete(ctx, j, execErr)
	})
	sched = scheduler.New(instrumentedStore, workerPool, scheduler.Config{
		PollInterval: cfg.PollInterval,
		BatchSize:    cfg.BatchSize,
		Backoff:      retry.DefaultConfig,
	}, log)

	workerPool.Start(ctx)
	go sched.Run(ctx)

	// Health checker — probes real dependencies, not a stub
	healthChecker := health.New()
	healthChecker.Register("postgres", func(ctx context.Context) error {
		return pool.Ping(ctx)
	})

	// gRPC server — interceptor order: RequestID → Recovery → Logger
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			middleware.UnaryRequestID(),
			middleware.UnaryRecovery(log),
			middleware.UnaryLogger(log),
		),
	)
	kronosv1.RegisterKronosServiceServer(grpcServer, api.New(instrumentedStore, log))
	grpc_health_v1.RegisterHealthServer(grpcServer, healthChecker)
	reflection.Register(grpcServer)

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		log.Fatal().Err(err).Str("addr", cfg.GRPCAddr).Msg("failed to listen")
	}
	log.Info().Str("addr", cfg.GRPCAddr).Msg("gRPC server listening")

	// Graceful shutdown on SIGTERM / SIGINT
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-quit
		log.Info().Msg("shutting down...")
		cancel()
		grpcServer.GracefulStop()
		workerPool.Stop()
		log.Info().Msg("shutdown complete")
	}()

	if err := grpcServer.Serve(lis); err != nil {
		log.Fatal().Err(err).Msg("gRPC server error")
	}
}

func registerHandlers(r *worker.Registry, log zerolog.Logger) {
	r.Register("echo", func(ctx context.Context, payload []byte) error {
		log.Info().RawJSON("payload", payload).Msg("echo job")
		return nil
	})
}
