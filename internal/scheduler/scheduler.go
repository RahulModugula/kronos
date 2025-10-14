package scheduler

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"github.com/rahulmodugula/kronos/internal/retry"
	"github.com/rahulmodugula/kronos/internal/store"
	"github.com/rahulmodugula/kronos/internal/worker"
)

// Scheduler polls the store for due jobs and dispatches them to the worker pool.
type Scheduler struct {
	store    store.Store
	pool     *worker.Pool
	backoff  retry.Config
	interval time.Duration
	batch    int
	log      zerolog.Logger
}

// Config holds scheduler tuning parameters.
type Config struct {
	PollInterval time.Duration // how often to check for new jobs (default 2s)
	BatchSize    int           // max jobs to claim per poll (default 10)
	Backoff      retry.Config
}

func New(s store.Store, p *worker.Pool, cfg Config, log zerolog.Logger) *Scheduler {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 2 * time.Second
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = 10
	}
	return &Scheduler{
		store:    s,
		pool:     p,
		backoff:  cfg.Backoff,
		interval: cfg.PollInterval,
		batch:    cfg.BatchSize,
		log:      log,
	}
}

// Run starts the polling loop. Blocks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	s.log.Info().Dur("interval", s.interval).Msg("scheduler started")
	for {
		select {
		case <-ctx.Done():
			s.log.Info().Msg("scheduler stopping")
			return
		case <-ticker.C:
			s.poll(ctx)
		}
	}
}

func (s *Scheduler) poll(ctx context.Context) {
	jobs, err := s.store.ClaimJobs(ctx, s.batch)
	if err != nil {
		s.log.Error().Err(err).Msg("failed to claim jobs")
		return
	}
	for _, j := range jobs {
		s.pool.Submit(j)
	}
}

// OnComplete is the callback wired into the worker pool.
// It updates job status and schedules retries as needed.
func (s *Scheduler) OnComplete(ctx context.Context, job *store.Job, execErr error) {
	if execErr == nil {
		if err := s.store.UpdateStatus(ctx, job.ID, store.StatusCompleted, ""); err != nil {
			s.log.Error().Err(err).Str("job_id", job.ID.String()).Msg("failed to mark job completed")
		}
		return
	}

	if job.RetryCount >= job.MaxRetries {
		s.log.Warn().Str("job_id", job.ID.String()).Msg("job exceeded max retries, marking dead")
		if err := s.store.UpdateStatus(ctx, job.ID, store.StatusDead, execErr.Error()); err != nil {
			s.log.Error().Err(err).Str("job_id", job.ID.String()).Msg("failed to mark job dead")
		}
		return
	}

	nextRun := s.backoff.NextTime(job.RetryCount)
	s.log.Info().
		Str("job_id", job.ID.String()).
		Int("retry", job.RetryCount+1).
		Time("next_run", nextRun).
		Msg("scheduling retry")

	if err := s.store.IncrementRetry(ctx, job.ID, nextRun); err != nil {
		s.log.Error().Err(err).Str("job_id", job.ID.String()).Msg("failed to schedule retry")
	}
}
