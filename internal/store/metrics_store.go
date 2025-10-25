package store

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/rahulmodugula/kronos/internal/metrics"
)

// MetricsStore wraps a Store and transparently records Prometheus metrics.
// Callers interact with it through the Store interface — instrumentation is invisible.
type MetricsStore struct {
	inner Store
	m     *metrics.Metrics
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

func (s *MetricsStore) ListJobs(ctx context.Context, f ListFilter) ([]*Job, string, error) {
	return s.inner.ListJobs(ctx, f)
}

func (s *MetricsStore) ClaimJobs(ctx context.Context, n int) ([]*Job, error) {
	return s.inner.ClaimJobs(ctx, n)
}

// UpdateStatus intercepts terminal state transitions to record completion
// metrics. We need the job type for label cardinality, so we look it up
// from the inner store before delegating the update.
func (s *MetricsStore) UpdateStatus(ctx context.Context, id uuid.UUID, status Status, errMsg string) error {
	isTerminal := status == StatusCompleted || status == StatusDead || status == StatusCancelled
	if isTerminal {
		if j, err := s.inner.GetJob(ctx, id); err == nil {
			start := j.UpdatedAt // best proxy for when the job started running
			elapsed := time.Since(start).Seconds()
			statusStr := string(status)
			s.m.JobsCompleted.WithLabelValues(j.Type, statusStr).Inc()
			s.m.JobDuration.WithLabelValues(j.Type, statusStr).Observe(elapsed)
		}
	}
	return s.inner.UpdateStatus(ctx, id, status, errMsg)
}

func (s *MetricsStore) IncrementRetry(ctx context.Context, id uuid.UUID, nextRun time.Time) error {
	return s.inner.IncrementRetry(ctx, id, nextRun)
}

func (s *MetricsStore) CancelJob(ctx context.Context, id uuid.UUID) error {
	return s.inner.CancelJob(ctx, id)
}

func (s *MetricsStore) QueueDepth(ctx context.Context) (int64, error) {
	return s.inner.QueueDepth(ctx)
}
