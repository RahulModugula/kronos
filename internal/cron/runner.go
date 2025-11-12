package cron

import (
	"context"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog"

	"github.com/rahulmodugula/kronos/internal/store"
)

// Runner manages cron job definitions and spawns one-off job entries into
// the jobs table when a cron expression fires.
//
// Design: cron entries create rows in the jobs table; the existing scheduler
// claims and executes them. This gives cron jobs retry semantics for free
// and makes them visible in ListJobs without any special-casing.
type Runner struct {
	jobStore  store.Store
	cronStore Store
	cron      *cron.Cron
	log       zerolog.Logger
}

func NewRunner(js store.Store, cs Store, log zerolog.Logger) *Runner {
	return &Runner{
		jobStore:  js,
		cronStore: cs,
		// UTC, standard 5-field cron expressions (no seconds)
		cron: cron.New(cron.WithLocation(time.UTC)),
		log:  log,
	}
}

// LoadAndStart reads all enabled cron definitions from the DB, registers them
// with robfig/cron, then starts the internal ticker goroutine.
// Must be called once after migrations complete.
func (r *Runner) LoadAndStart(ctx context.Context) error {
	jobs, err := r.cronStore.ListEnabledCronJobs(ctx)
	if err != nil {
		return fmt.Errorf("list cron jobs: %w", err)
	}

	for _, cj := range jobs {
		cj := cj // capture loop variable
		entryID, err := r.cron.AddFunc(cj.Schedule, func() {
			r.fire(context.Background(), cj)
		})
		if err != nil {
			return fmt.Errorf("invalid schedule %q for cron job %q: %w", cj.Schedule, cj.Name, err)
		}
		r.log.Info().
			Str("cron_job", cj.Name).
			Str("schedule", cj.Schedule).
			Int("entry_id", int(entryID)).
			Msg("registered cron job")
	}

	r.cron.Start()
	r.log.Info().Int("count", len(jobs)).Msg("cron runner started")
	return nil
}

// fire spawns a one-off job row for a single cron execution.
func (r *Runner) fire(ctx context.Context, cj *CronJob) {
	j, err := r.jobStore.CreateJob(ctx, &store.Job{
		Name:       fmt.Sprintf("%s/%s", cj.Name, time.Now().UTC().Format(time.RFC3339)),
		Type:       cj.Type,
		Payload:    cj.Payload,
		MaxRetries: cj.MaxRetries,
	})
	if err != nil {
		r.log.Error().Err(err).Str("cron_job", cj.Name).Msg("failed to spawn job instance")
		return
	}
	_ = r.cronStore.UpdateLastFired(ctx, cj.ID, time.Now().UTC())
	r.log.Info().
		Str("cron_job", cj.Name).
		Str("job_id", j.ID.String()).
		Msg("cron job fired")
}

// Stop gracefully waits for the currently running cron tick to finish.
func (r *Runner) Stop() {
	<-r.cron.Stop().Done()
}
