package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Status mirrors the DB enum.
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
	StatusDead      Status = "dead"
)

// Job is the in-memory representation of a job row.
type Job struct {
	ID             uuid.UUID
	Name           string
	Type           string
	Payload        []byte
	Status         Status
	MaxRetries     int
	RetryCount     int
	Priority       int // 1 (lowest) to 10 (highest), default 5
	Error          string
	IdempotencyKey string // optional; deduplicates non-terminal jobs
	ScheduledAt    time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ListFilter controls ListJobs queries.
type ListFilter struct {
	Status    *Status // nil = all statuses
	PageSize  int
	PageToken string // opaque cursor (created_at:id)
}

// Store is the persistence interface for jobs.
type Store interface {
	// CreateJob inserts a new job and returns it with ID/timestamps populated.
	CreateJob(ctx context.Context, j *Job) (*Job, error)

	// GetJob retrieves a job by ID.
	GetJob(ctx context.Context, id uuid.UUID) (*Job, error)

	// GetJobByIdempotencyKey returns an active (non-terminal) job matching the
	// given idempotency key, or nil if none exists.
	GetJobByIdempotencyKey(ctx context.Context, key string) (*Job, error)

	// ListJobs returns jobs matching the filter.
	ListJobs(ctx context.Context, f ListFilter) ([]*Job, string, error)

	// ClaimJobs atomically marks up to n pending/due jobs as running.
	// Uses SELECT FOR UPDATE SKIP LOCKED.
	ClaimJobs(ctx context.Context, n int) ([]*Job, error)

	// UpdateStatus sets status (and optionally error) on a job.
	UpdateStatus(ctx context.Context, id uuid.UUID, status Status, errMsg string) error

	// IncrementRetry bumps retry_count and sets status back to pending with next scheduled_at.
	IncrementRetry(ctx context.Context, id uuid.UUID, nextRun time.Time) error

	// CancelJob transitions a pending job to cancelled.
	CancelJob(ctx context.Context, id uuid.UUID) error

	// QueueDepth returns the count of pending jobs. Used for Prometheus metrics.
	QueueDepth(ctx context.Context) (int64, error)

	// RetryDeadJob transitions a dead job back to pending and resets its retry count.
	RetryDeadJob(ctx context.Context, id uuid.UUID) error

	// ListDeadJobs returns a paginated list of dead jobs.
	ListDeadJobs(ctx context.Context, pageSize int, pageToken string) ([]*Job, string, error)
}
