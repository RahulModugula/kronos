package cron

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// CronJob is the in-memory representation of a cron_jobs row.
type CronJob struct {
	ID          uuid.UUID
	Name        string
	Type        string
	Payload     []byte
	Schedule    string
	MaxRetries  int
	LastFiredAt *time.Time
}

// Store is the persistence interface for cron job definitions.
type Store interface {
	ListEnabledCronJobs(ctx context.Context) ([]*CronJob, error)
	UpdateLastFired(ctx context.Context, id uuid.UUID, firedAt time.Time) error
	UpsertCronJob(ctx context.Context, cj *CronJob) (*CronJob, error)
}
