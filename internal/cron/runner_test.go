package cron

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/rahulmodugula/kronos/internal/store"
)

// --- Mock cron.Store ---

type mockCronStore struct {
	mu      sync.Mutex
	jobs    []*CronJob
	fired   map[uuid.UUID]time.Time
	lastErr error
}

func newMockCronStore(jobs ...*CronJob) *mockCronStore {
	return &mockCronStore{jobs: jobs, fired: make(map[uuid.UUID]time.Time)}
}

func (m *mockCronStore) ListEnabledCronJobs(_ context.Context) ([]*CronJob, error) {
	if m.lastErr != nil {
		return nil, m.lastErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*CronJob, len(m.jobs))
	copy(out, m.jobs)
	return out, nil
}

func (m *mockCronStore) UpdateLastFired(_ context.Context, id uuid.UUID, firedAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fired[id] = firedAt
	return nil
}

func (m *mockCronStore) UpsertCronJob(_ context.Context, cj *CronJob) (*CronJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs = append(m.jobs, cj)
	return cj, nil
}

// --- Mock store.Store ---

type mockJobStore struct {
	mu   sync.Mutex
	jobs []*store.Job
}

func (m *mockJobStore) CreateJob(_ context.Context, j *store.Job) (*store.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j.ID = uuid.New()
	j.CreatedAt = time.Now()
	j.UpdatedAt = time.Now()
	cp := *j
	m.jobs = append(m.jobs, &cp)
	return &cp, nil
}

func (m *mockJobStore) GetJob(_ context.Context, id uuid.UUID) (*store.Job, error) {
	return nil, nil
}
func (m *mockJobStore) GetJobByIdempotencyKey(_ context.Context, key string) (*store.Job, error) {
	return nil, nil
}
func (m *mockJobStore) ListJobs(_ context.Context, f store.ListFilter) ([]*store.Job, string, error) {
	return nil, "", nil
}
func (m *mockJobStore) ClaimJobs(_ context.Context, n int) ([]*store.Job, error) {
	return nil, nil
}
func (m *mockJobStore) UpdateStatus(_ context.Context, id uuid.UUID, status store.Status, errMsg string) error {
	return nil
}
func (m *mockJobStore) IncrementRetry(_ context.Context, id uuid.UUID, nextRun time.Time) error {
	return nil
}
func (m *mockJobStore) CancelJob(_ context.Context, id uuid.UUID) error    { return nil }
func (m *mockJobStore) QueueDepth(_ context.Context) (int64, error)        { return 0, nil }
func (m *mockJobStore) RetryDeadJob(_ context.Context, id uuid.UUID) error { return nil }
func (m *mockJobStore) ListDeadJobs(_ context.Context, pageSize int, pageToken string) ([]*store.Job, string, error) {
	return nil, "", nil
}

// --- Tests ---

func TestLoadAndStart_FiresJob(t *testing.T) {
	cronID := uuid.New()
	cronJob := &CronJob{
		ID:         cronID,
		Name:       "test-job",
		Type:       "echo",
		Payload:    json.RawMessage(`{}`),
		Schedule:   "* * * * *", // every minute
		MaxRetries: 3,
	}

	cs := newMockCronStore(cronJob)
	js := &mockJobStore{}
	log := zerolog.Nop()

	runner := NewRunner(js, cs, log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := runner.LoadAndStart(ctx); err != nil {
		t.Fatalf("LoadAndStart returned error: %v", err)
	}
	defer runner.Stop()

	// Trigger fire() directly to validate job creation without waiting a minute
	runner.fire(ctx, cronJob)

	js.mu.Lock()
	count := len(js.jobs)
	js.mu.Unlock()

	if count != 1 {
		t.Fatalf("expected 1 job to be created, got %d", count)
	}
	if js.jobs[0].Type != "echo" {
		t.Errorf("expected job type 'echo', got %q", js.jobs[0].Type)
	}
}

func TestFire_CreatesJobEntry(t *testing.T) {
	cronID := uuid.New()
	cronJob := &CronJob{
		ID:         cronID,
		Name:       "fire-test",
		Type:       "send-email",
		Payload:    json.RawMessage(`{"to":"a@b.com","subject":"hi","body":""}`),
		Schedule:   "0 9 * * *",
		MaxRetries: 1,
	}

	cs := newMockCronStore(cronJob)
	js := &mockJobStore{}
	log := zerolog.Nop()

	runner := NewRunner(js, cs, log)
	ctx := context.Background()

	runner.fire(ctx, cronJob)

	js.mu.Lock()
	jobs := js.jobs
	js.mu.Unlock()

	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	j := jobs[0]
	if j.Type != "send-email" {
		t.Errorf("job type: want 'send-email', got %q", j.Type)
	}
	if j.MaxRetries != 1 {
		t.Errorf("max_retries: want 1, got %d", j.MaxRetries)
	}

	// UpdateLastFired should have been called
	cs.mu.Lock()
	_, updated := cs.fired[cronID]
	cs.mu.Unlock()
	if !updated {
		t.Error("UpdateLastFired was not called after fire()")
	}
}

func TestLoadAndStart_InvalidScheduleReturnsError(t *testing.T) {
	cronJob := &CronJob{
		ID:       uuid.New(),
		Name:     "bad-schedule",
		Type:     "echo",
		Payload:  json.RawMessage(`{}`),
		Schedule: "not-a-cron-expression",
	}

	cs := newMockCronStore(cronJob)
	js := &mockJobStore{}
	log := zerolog.Nop()

	runner := NewRunner(js, cs, log)
	ctx := context.Background()

	err := runner.LoadAndStart(ctx)
	if err == nil {
		runner.Stop()
		t.Fatal("expected an error for invalid cron schedule, got nil")
	}
}

func TestStop_StopsCleanly(t *testing.T) {
	cs := newMockCronStore() // no jobs
	js := &mockJobStore{}
	log := zerolog.Nop()

	runner := NewRunner(js, cs, log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := runner.LoadAndStart(ctx); err != nil {
		t.Fatalf("LoadAndStart returned error: %v", err)
	}

	done := make(chan struct{})
	go func() {
		runner.Stop()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(3 * time.Second):
		t.Fatal("Stop() did not return within 3 seconds")
	}
}
