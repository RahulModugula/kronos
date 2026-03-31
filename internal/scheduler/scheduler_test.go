package scheduler_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/rahulmodugula/kronos/internal/retry"
	"github.com/rahulmodugula/kronos/internal/scheduler"
	"github.com/rahulmodugula/kronos/internal/store"
	"github.com/rahulmodugula/kronos/internal/worker"
)

// mockStore implements store.Store with only the methods the scheduler uses.
type mockStore struct {
	store.Store // embed to satisfy interface; unused methods will panic

	mu            sync.Mutex
	claimResult   []*store.Job
	claimErr      error
	updatedStatus map[uuid.UUID]store.Status
	retriedJobs   map[uuid.UUID]time.Time
}

func newMockStore() *mockStore {
	return &mockStore{
		updatedStatus: make(map[uuid.UUID]store.Status),
		retriedJobs:   make(map[uuid.UUID]time.Time),
	}
}

func (m *mockStore) ClaimJobs(_ context.Context, _ int) ([]*store.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.claimResult, m.claimErr
}

func (m *mockStore) UpdateStatus(_ context.Context, id uuid.UUID, status store.Status, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updatedStatus[id] = status
	return nil
}

func (m *mockStore) IncrementRetry(_ context.Context, id uuid.UUID, nextRun time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retriedJobs[id] = nextRun
	return nil
}

func TestNew_ReturnsValidScheduler(t *testing.T) {
	ms := newMockStore()
	reg := worker.NewRegistry()
	log := zerolog.Nop()
	pool := worker.NewPool(1, reg, log, 0, func(context.Context, *store.Job, error) {})

	s := scheduler.New(ms, pool, scheduler.Config{}, log)
	if s == nil {
		t.Fatal("New() returned nil")
	}
}

func TestRun_RespectsContextCancellation(t *testing.T) {
	ms := newMockStore()
	reg := worker.NewRegistry()
	log := zerolog.Nop()
	pool := worker.NewPool(1, reg, log, 0, func(context.Context, *store.Job, error) {})

	s := scheduler.New(ms, pool, scheduler.Config{
		PollInterval: 50 * time.Millisecond,
	}, log)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// Run returned promptly after cancellation — good.
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func TestOnComplete_MarksJobCompleted(t *testing.T) {
	ms := newMockStore()
	reg := worker.NewRegistry()
	log := zerolog.Nop()
	pool := worker.NewPool(1, reg, log, 0, func(context.Context, *store.Job, error) {})

	s := scheduler.New(ms, pool, scheduler.Config{}, log)

	jobID := uuid.New()
	job := &store.Job{ID: jobID, MaxRetries: 3}

	s.OnComplete(context.Background(), job, nil)

	ms.mu.Lock()
	defer ms.mu.Unlock()
	status, ok := ms.updatedStatus[jobID]
	if !ok {
		t.Fatal("expected UpdateStatus to be called")
	}
	if status != store.StatusCompleted {
		t.Errorf("expected status %q, got %q", store.StatusCompleted, status)
	}
}

func TestOnComplete_TriggersRetryOnError(t *testing.T) {
	ms := newMockStore()
	reg := worker.NewRegistry()
	log := zerolog.Nop()
	pool := worker.NewPool(1, reg, log, 0, func(context.Context, *store.Job, error) {})

	s := scheduler.New(ms, pool, scheduler.Config{
		Backoff: retry.Config{
			Base:       100 * time.Millisecond,
			Max:        time.Second,
			Multiplier: 2.0,
			Jitter:     0,
		},
	}, log)

	jobID := uuid.New()
	job := &store.Job{ID: jobID, MaxRetries: 3, RetryCount: 0}

	s.OnComplete(context.Background(), job, errors.New("something went wrong"))

	ms.mu.Lock()
	defer ms.mu.Unlock()

	// Should schedule a retry, not mark completed or dead.
	if _, ok := ms.updatedStatus[jobID]; ok {
		t.Fatalf("did not expect UpdateStatus to be called; got status %q", ms.updatedStatus[jobID])
	}
	if _, ok := ms.retriedJobs[jobID]; !ok {
		t.Fatal("expected IncrementRetry to be called")
	}
}

func TestOnComplete_MarksDeadWhenRetriesExhausted(t *testing.T) {
	ms := newMockStore()
	reg := worker.NewRegistry()
	log := zerolog.Nop()
	pool := worker.NewPool(1, reg, log, 0, func(context.Context, *store.Job, error) {})

	s := scheduler.New(ms, pool, scheduler.Config{}, log)

	jobID := uuid.New()
	job := &store.Job{ID: jobID, MaxRetries: 3, RetryCount: 3}

	s.OnComplete(context.Background(), job, errors.New("final failure"))

	ms.mu.Lock()
	defer ms.mu.Unlock()

	status, ok := ms.updatedStatus[jobID]
	if !ok {
		t.Fatal("expected UpdateStatus to be called")
	}
	if status != store.StatusDead {
		t.Errorf("expected status %q, got %q", store.StatusDead, status)
	}
}
