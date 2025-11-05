//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/rahulmodugula/kronos/internal/retry"
	"github.com/rahulmodugula/kronos/internal/scheduler"
	"github.com/rahulmodugula/kronos/internal/store"
	"github.com/rahulmodugula/kronos/internal/worker"
)

// TestSubmitExecuteComplete is the happy path end-to-end test.
// It submits a job, verifies it gets claimed and executed, and checks
// the final status in the DB.
func TestSubmitExecuteComplete(t *testing.T) {
	pool := setupDB(t)
	ctx := context.Background()
	pg := store.NewPGStore(pool)

	done := make(chan struct{})
	reg := worker.NewRegistry()
	reg.Register("greet", func(ctx context.Context, payload json.RawMessage) error {
		close(done)
		return nil
	})

	var sched *scheduler.Scheduler
	wp := worker.NewPool(2, reg, zerolog.Nop(), func(ctx context.Context, j *store.Job, err error) {
		sched.OnComplete(ctx, j, err)
	})
	sched = scheduler.New(pg, wp, scheduler.Config{
		PollInterval: 100 * time.Millisecond,
		BatchSize:    5,
		Backoff:      retry.DefaultConfig,
	}, zerolog.Nop())

	wp.Start(ctx)
	go sched.Run(ctx)

	j, err := pg.CreateJob(ctx, &store.Job{
		Name:       "hello",
		Type:       "greet",
		Payload:    []byte(`{"name":"world"}`),
		MaxRetries: 1,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("job was not executed within 5s")
	}

	// Give the OnComplete callback time to update the DB
	time.Sleep(200 * time.Millisecond)

	result, err := pg.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if result.Status != store.StatusCompleted {
		t.Errorf("expected status completed, got %s", result.Status)
	}
}

// TestRetryBehavior registers a handler that fails twice then succeeds,
// and asserts the scheduler retries correctly up to completion.
func TestRetryBehavior(t *testing.T) {
	pool := setupDB(t)
	ctx := context.Background()
	pg := store.NewPGStore(pool)

	attempts := 0
	done := make(chan struct{})
	reg := worker.NewRegistry()
	reg.Register("flaky", func(ctx context.Context, payload json.RawMessage) error {
		attempts++
		if attempts < 3 {
			return errors.New("transient failure")
		}
		close(done)
		return nil
	})

	cfg := scheduler.Config{
		PollInterval: 100 * time.Millisecond,
		BatchSize:    5,
		Backoff: retry.Config{
			Base:       50 * time.Millisecond, // fast for tests
			Max:        500 * time.Millisecond,
			Multiplier: 2.0,
			Jitter:     0,
		},
	}

	var sched *scheduler.Scheduler
	wp := worker.NewPool(2, reg, zerolog.Nop(), func(ctx context.Context, j *store.Job, err error) {
		sched.OnComplete(ctx, j, err)
	})
	sched = scheduler.New(pg, wp, cfg, zerolog.Nop())
	wp.Start(ctx)
	go sched.Run(ctx)

	j, err := pg.CreateJob(ctx, &store.Job{
		Name:       "retry-me",
		Type:       "flaky",
		Payload:    []byte(`{}`),
		MaxRetries: 3,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("job did not succeed after retries (attempts so far: %d)", attempts)
	}

	time.Sleep(200 * time.Millisecond)
	result, err := pg.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if result.Status != store.StatusCompleted {
		t.Errorf("expected completed, got %s", result.Status)
	}
	if result.RetryCount != 2 {
		t.Errorf("expected retry_count=2, got %d", result.RetryCount)
	}
}

// TestCancelPendingJob verifies a pending job can be cancelled and
// that cancelling a non-pending job returns an error.
func TestCancelPendingJob(t *testing.T) {
	pool := setupDB(t)
	ctx := context.Background()
	pg := store.NewPGStore(pool)

	j, err := pg.CreateJob(ctx, &store.Job{
		Name:        "cancel-me",
		Type:        "echo",
		Payload:     []byte(`{}`),
		MaxRetries:  0,
		ScheduledAt: time.Now().Add(1 * time.Hour), // far future — won't be claimed
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	if err := pg.CancelJob(ctx, j.ID); err != nil {
		t.Fatalf("cancel job: %v", err)
	}

	result, err := pg.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if result.Status != store.StatusCancelled {
		t.Errorf("expected cancelled, got %s", result.Status)
	}

	// Cancelling again should fail — job is no longer pending
	if err := pg.CancelJob(ctx, j.ID); err == nil {
		t.Error("expected error cancelling non-pending job, got nil")
	}
}
