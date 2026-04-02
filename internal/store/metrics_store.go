package store

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/rahulmodugula/kronos/internal/metrics"
)

// claimRecord holds the data needed for completion metrics, cached at claim time.
type claimRecord struct {
	jobType   string
	claimedAt time.Time
}

// MetricsStore wraps a Store and transparently records Prometheus metrics.
// Callers interact with it through the Store interface — instrumentation is invisible.
//
// Completion metrics (job duration, terminal status counts) are recorded without
// an extra DB query by caching (type, claimedAt) when jobs are claimed, then
// consuming the cache entry when they reach a terminal state. The cache is
// bounded: entries are removed on first terminal transition, so a job that
// completes exactly once never accumulates memory.
type MetricsStore struct {
	inner Store
	m     *metrics.Metrics
	// claimCache maps job ID → claimRecord, populated in ClaimJobs and
	// consumed (deleted) in UpdateStatus on terminal transitions.
	// sync.Map is appropriate here: writes (ClaimJobs) and reads (UpdateStatus)
	// are from different goroutines and keys are disjoint per job lifecycle.
	claimCache sync.Map
}

func NewMetricsStore(inner Store, m *metrics.Metrics) *MetricsStore {
	return &MetricsStore{inner: inner, m: m}
}

func (s *MetricsStore) CreateJob(ctx context.Context, j *Job) (*Job, error) {
	created, err := s.inner.CreateJob(ctx, j)
	if err == nil {
		s.m.JobsSubmitted.WithLabelValues(j.Type).Inc()
	}
	return created, err
}

func (s *MetricsStore) GetJob(ctx context.Context, id uuid.UUID) (*Job, error) {
	return s.inner.GetJob(ctx, id)
}

func (s *MetricsStore) GetJobByIdempotencyKey(ctx context.Context, key string) (*Job, error) {
	return s.inner.GetJobByIdempotencyKey(ctx, key)
}

func (s *MetricsStore) ListJobs(ctx context.Context, f ListFilter) ([]*Job, string, error) {
	return s.inner.ListJobs(ctx, f)
}

// ClaimJobs delegates to inner and caches (type, claimedAt) for each claimed job.
// The cache is consumed by UpdateStatus when the job reaches a terminal state,
// eliminating the GetJob() call that was previously needed to look up job type.
func (s *MetricsStore) ClaimJobs(ctx context.Context, n int) ([]*Job, error) {
	jobs, err := s.inner.ClaimJobs(ctx, n)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for _, j := range jobs {
		s.claimCache.Store(j.ID, claimRecord{
			jobType:   j.Type,
			claimedAt: now,
		})
	}
	return jobs, nil
}

// UpdateStatus intercepts terminal transitions to record completion metrics.
// Uses the claimCache to look up job type and execution start time without
// an extra DB round-trip.
func (s *MetricsStore) UpdateStatus(ctx context.Context, id uuid.UUID, status Status, errMsg string) error {
	isTerminal := status == StatusCompleted || status == StatusDead || status == StatusCancelled
	if isTerminal {
		if rec, ok := s.claimCache.LoadAndDelete(id); ok {
			cr := rec.(claimRecord)
			elapsed := time.Since(cr.claimedAt).Seconds()
			statusStr := string(status)
			s.m.JobsCompleted.WithLabelValues(cr.jobType, statusStr).Inc()
			s.m.JobDuration.WithLabelValues(cr.jobType, statusStr).Observe(elapsed)
		}
		// If not in cache (e.g. job was claimed before a restart, or a
		// cancellation from the API), skip metrics — don't pay a GetJob query
		// for a path that should be rare.
	}
	return s.inner.UpdateStatus(ctx, id, status, errMsg)
}

func (s *MetricsStore) IncrementRetry(ctx context.Context, id uuid.UUID, nextRun time.Time) error {
	// On retry, evict the stale claim record — the job will be re-cached
	// with a fresh claimedAt when ClaimJobs picks it up again.
	s.claimCache.Delete(id)
	return s.inner.IncrementRetry(ctx, id, nextRun)
}

func (s *MetricsStore) CancelJob(ctx context.Context, id uuid.UUID) error {
	return s.inner.CancelJob(ctx, id)
}

func (s *MetricsStore) QueueDepth(ctx context.Context) (int64, error) {
	return s.inner.QueueDepth(ctx)
}
