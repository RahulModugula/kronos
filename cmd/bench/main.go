// cmd/bench is a throughput benchmark for Kronos.
// It connects directly to Postgres, pre-inserts N jobs, then starts the
// scheduler + worker pool and measures time-to-completion.
//
// Usage:
//
//	# Start Postgres first
//	docker-compose up -d
//
//	# Run benchmark (defaults: 10000 jobs, 50 workers)
//	DATABASE_URL=postgres://kronos:kronos@localhost:5432/kronos?sslmode=disable \
//	  go run ./cmd/bench
//
//	# Override
//	DATABASE_URL=... go run ./cmd/bench -jobs=50000 -workers=100
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/rahulmodugula/kronos/internal/config"
	"github.com/rahulmodugula/kronos/internal/retry"
	"github.com/rahulmodugula/kronos/internal/scheduler"
	"github.com/rahulmodugula/kronos/internal/store"
	"github.com/rahulmodugula/kronos/internal/worker"

	migrate "github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

func main() {
	numJobs := flag.Int("jobs", 10_000, "number of jobs to submit")
	numWorkers := flag.Int("workers", 50, "worker pool concurrency")
	flag.Parse()

	log := zerolog.New(os.Stdout).With().Timestamp().Logger()

	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("config error")
	}

	// Run migrations so the bench works against a fresh DB.
	m, err := migrate.New("file://migrations", cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to init migrations")
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		log.Fatal().Err(err).Msg("migration failed")
	}

	pool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("postgres connect failed")
	}
	defer pool.Close()

	pgStore := store.NewPGStore(pool)

	// Pre-insert all jobs before starting the scheduler so we're measuring
	// throughput, not insert + execute in serial.
	log.Info().Int("jobs", *numJobs).Msg("pre-inserting jobs")
	insertStart := time.Now()
	if err := insertJobs(context.Background(), pgStore, *numJobs); err != nil {
		log.Fatal().Err(err).Msg("failed to insert jobs")
	}
	log.Info().
		Int("jobs", *numJobs).
		Dur("insert_ms", time.Since(insertStart)).
		Msg("jobs inserted")

	// Collect per-job latency to compute percentiles.
	var (
		mu        sync.Mutex
		latencies []float64 // milliseconds
	)

	var completed atomic.Int64
	done := make(chan struct{})

	registry := worker.NewRegistry()
	registry.Register("bench-noop", func(_ context.Context, _ json.RawMessage) error {
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var sched *scheduler.Scheduler
	wp := worker.NewPool(*numWorkers, registry, zerolog.Nop(), cfg.JobTimeout, func(_ context.Context, j *store.Job, execErr error) {
		sched.OnComplete(ctx, j, execErr)

		latMs := float64(time.Since(j.CreatedAt).Milliseconds())
		mu.Lock()
		latencies = append(latencies, latMs)
		mu.Unlock()

		n := completed.Add(1)
		if int(n) >= *numJobs {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})

	sched = scheduler.New(pgStore, wp, scheduler.Config{
		PollInterval: 50 * time.Millisecond, // fast poll for bench
		BatchSize:    500,
		Backoff:      retry.DefaultConfig,
	}, zerolog.Nop())

	wp.Start(ctx)
	go sched.Run(ctx)

	start := time.Now()
	log.Info().Int("workers", *numWorkers).Msg("scheduler started, waiting for completion")

	select {
	case <-done:
	case <-time.After(5 * time.Minute):
		log.Fatal().Msg("benchmark timed out — check scheduler logs")
	}

	elapsed := time.Since(start)
	cancel()
	wp.Stop()

	// Compute percentiles.
	sort.Float64s(latencies)
	p50 := percentile(latencies, 50)
	p95 := percentile(latencies, 95)
	p99 := percentile(latencies, 99)

	jobsPerSec := float64(*numJobs) / elapsed.Seconds()

	fmt.Printf("\n── Kronos Benchmark Results ──────────────────────────\n")
	fmt.Printf("  Jobs:          %d\n", *numJobs)
	fmt.Printf("  Workers:       %d\n", *numWorkers)
	fmt.Printf("  Total time:    %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("  Throughput:    %.0f jobs/sec\n", jobsPerSec)
	fmt.Printf("  Latency p50:   %.0fms\n", p50)
	fmt.Printf("  Latency p95:   %.0fms\n", p95)
	fmt.Printf("  Latency p99:   %.0fms\n", p99)
	fmt.Printf("──────────────────────────────────────────────────────\n\n")
}

func insertJobs(ctx context.Context, s store.Store, n int) error {
	payload := []byte(`{"bench": true}`)
	for i := 0; i < n; i++ {
		_, err := s.CreateJob(ctx, &store.Job{
			Name:       fmt.Sprintf("bench-job-%d", i),
			Type:       "bench-noop",
			Payload:    payload,
			MaxRetries: 0,
			Priority:   5,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(float64(len(sorted))*p/100)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
