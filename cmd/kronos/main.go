package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	kronosv1 "github.com/rahulmodugula/kronos/gen/kronos/v1"
	"github.com/rahulmodugula/kronos/internal/admin"
	"github.com/rahulmodugula/kronos/internal/api"
	"github.com/rahulmodugula/kronos/internal/config"
	"github.com/rahulmodugula/kronos/internal/cron"
	"github.com/rahulmodugula/kronos/internal/health"
	"github.com/rahulmodugula/kronos/internal/leader"
	"github.com/rahulmodugula/kronos/internal/metrics"
	"github.com/rahulmodugula/kronos/internal/middleware"
	"github.com/rahulmodugula/kronos/internal/retry"
	"github.com/rahulmodugula/kronos/internal/scheduler"
	"github.com/rahulmodugula/kronos/internal/store"
	"github.com/rahulmodugula/kronos/internal/worker"
	"github.com/rahulmodugula/kronos/internal/workflow"

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

	// Workflow engine
	workflowStore := workflow.NewPGWorkflowStore(pool)
	workflowRegistry := workflow.NewRegistry()
	workflowEngine := workflow.NewEngine(workflowStore, workflowRegistry, log)

	// /metrics on a separate port so it doesn't share the gRPC listener
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.HandlerFor(promReg, promhttp.HandlerOpts{}))
		log.Info().Str("addr", cfg.MetricsAddr).Msg("metrics server listening")
		if err := http.ListenAndServe(cfg.MetricsAddr, mux); err != nil {
			log.Error().Err(err).Msg("metrics server error")
		}
	}()

	// Admin HTTP server — dead-letter queue management endpoints
	go func() {
		adminMux := http.NewServeMux()
		adminHandler := admin.NewHandler(instrumentedStore, log)
		adminHandler.Register(adminMux)
		log.Info().Str("addr", cfg.AdminAddr).Msg("admin server listening")
		if err := http.ListenAndServe(cfg.AdminAddr, adminMux); err != nil {
			log.Error().Err(err).Msg("admin server error")
		}
	}()

	// Worker pool + scheduler (closure breaks the circular init dependency).
	// We use atomic.Pointer so the race detector doesn't flag reads of sched
	// before it's assigned — even though no job can complete before the
	// scheduler starts, the closure and the assignment are on different lines
	// and the race detector reasons purely about memory ordering, not timing.
	registry := worker.NewRegistry()
	registerHandlers(registry, log, workflowEngine)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start cache-cleanup goroutine now that ctx is available
	instrumentedStore.Start(ctx)

	var schedPtr atomic.Pointer[scheduler.Scheduler]
	workerPool := worker.NewPool(cfg.WorkerConcurrency, registry, log, cfg.JobTimeout, func(ctx context.Context, j *store.Job, execErr error) {
		if s := schedPtr.Load(); s != nil {
			s.OnComplete(ctx, j, execErr)
		}
	})
	sched := scheduler.New(instrumentedStore, workerPool, scheduler.Config{
		PollInterval: cfg.PollInterval,
		BatchSize:    cfg.BatchSize,
		Backoff: retry.Config{
			Base:       cfg.RetryBaseDelay,
			Max:        cfg.RetryMaxDelay,
			Multiplier: cfg.RetryMultiplier,
			Jitter:     cfg.RetryJitter,
		},
	}, log)
	schedPtr.Store(sched)

	workerPool.Start(ctx)

	// Leader election: only the node holding the advisory lock runs the
	// scheduler. On lock loss (crash, network partition), the scheduler on
	// this node stops and a standby node takes over within one poll interval.
	cronStore := cron.NewPGCronStore(pool)
	elector := leader.New(cfg.DatabaseURL, log)
	go elector.Run(ctx,
		func(leaderCtx context.Context) {
			log.Info().Msg("elected as leader, starting scheduler")

			cronRunner := cron.NewRunner(instrumentedStore, cronStore, log)
			if err := cronRunner.LoadAndStart(leaderCtx); err != nil {
				log.Error().Err(err).Msg("cron runner failed to start")
			} else {
				defer cronRunner.Stop()
			}

			schedPtr.Load().Run(leaderCtx) // blocks until leaderCtx is cancelled
		},
		func() {
			log.Warn().Msg("lost leadership — scheduler paused, waiting for re-election")
		},
	)

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

func registerHandlers(r *worker.Registry, log zerolog.Logger, workflowEngine *workflow.Engine) {
	// kronos.workflow_step — internal handler for executing workflow steps
	r.Register("kronos.workflow_step", func(ctx context.Context, payload json.RawMessage) error {
		var p struct {
			RunID    string `json:"run_id"`
			StepName string `json:"step_name"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("kronos.workflow_step: invalid payload: %w", err)
		}
		return workflowEngine.ExecuteStep(ctx, p.RunID, p.StepName)
	})

	// echo — log the payload and return. Useful for smoke-testing the pipeline.
	r.Register("echo", func(ctx context.Context, payload json.RawMessage) error {
		log.Info().RawJSON("payload", payload).Msg("echo job")
		return nil
	})

	// webhook-delivery — POST a JSON payload to a URL with retries delegated
	// to Kronos's retry engine. The handler is idempotent: duplicate deliveries
	// are acceptable because the receiver should deduplicate on event_id.
	r.Register("webhook-delivery", func(ctx context.Context, payload json.RawMessage) error {
		var p struct {
			URL     string          `json:"url"`
			EventID string          `json:"event_id"`
			Body    json.RawMessage `json:"body"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("webhook-delivery: invalid payload: %w", err)
		}
		if p.URL == "" || p.EventID == "" {
			return fmt.Errorf("webhook-delivery: url and event_id are required")
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL, bytes.NewReader(p.Body))
		if err != nil {
			return fmt.Errorf("webhook-delivery: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Event-ID", p.EventID) // receiver can use this to deduplicate

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("webhook-delivery: http: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 500 {
			return fmt.Errorf("webhook-delivery: server error %d (will retry)", resp.StatusCode)
		}
		log.Info().Str("url", p.URL).Str("event_id", p.EventID).Int("status", resp.StatusCode).Msg("webhook delivered")
		return nil
	})

	// send-email — mock SMTP handler. Swap the log line for a real smtp.SendMail
	// call in production. The Payload schema mirrors what a transactional email
	// service (Postmark, SES) expects.
	r.Register("send-email", func(ctx context.Context, payload json.RawMessage) error {
		var p struct {
			To      string `json:"to"`
			Subject string `json:"subject"`
			Body    string `json:"body"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("send-email: invalid payload: %w", err)
		}
		if p.To == "" || p.Subject == "" {
			return fmt.Errorf("send-email: to and subject are required")
		}

		// In production: smtp.SendMail(smtpAddr, auth, from, []string{p.To}, msg)
		log.Info().
			Str("to", p.To).
			Str("subject", p.Subject).
			Msg("send-email: dispatched (mock SMTP)")
		return nil
	})

	// resize-image — simulate a CPU-bound image processing job to demonstrate
	// that the worker pool handles blocking operations without stalling other
	// job types. In production, shell out to ImageMagick or call an image API.
	r.Register("resize-image", func(ctx context.Context, payload json.RawMessage) error {
		var p struct {
			ImageURL string `json:"image_url"`
			Width    int    `json:"width"`
			Height   int    `json:"height"`
			OutputS3 string `json:"output_s3_key"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("resize-image: invalid payload: %w", err)
		}
		if p.ImageURL == "" || p.Width == 0 || p.Height == 0 {
			return fmt.Errorf("resize-image: image_url, width, and height are required")
		}

		// Simulate image processing time (in production: download → resize → upload).
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}

		log.Info().
			Str("image_url", p.ImageURL).
			Int("width", p.Width).
			Int("height", p.Height).
			Str("output", p.OutputS3).
			Msg("resize-image: complete (mock)")
		return nil
	})
}
