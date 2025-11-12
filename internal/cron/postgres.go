package cron

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGCronStore implements cron.Store against PostgreSQL.
type PGCronStore struct {
	pool *pgxpool.Pool
}

func NewPGCronStore(pool *pgxpool.Pool) *PGCronStore {
	return &PGCronStore{pool: pool}
}

func (s *PGCronStore) ListEnabledCronJobs(ctx context.Context) ([]*CronJob, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, type, payload, schedule, max_retries, last_fired_at
		 FROM cron_jobs WHERE enabled = true ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []*CronJob
	for rows.Next() {
		cj := &CronJob{}
		if err := rows.Scan(&cj.ID, &cj.Name, &cj.Type, &cj.Payload,
			&cj.Schedule, &cj.MaxRetries, &cj.LastFiredAt); err != nil {
			return nil, err
		}
		jobs = append(jobs, cj)
	}
	return jobs, rows.Err()
}

func (s *PGCronStore) UpdateLastFired(ctx context.Context, id uuid.UUID, firedAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE cron_jobs SET last_fired_at = $1, updated_at = NOW() WHERE id = $2`,
		firedAt, id,
	)
	return err
}

func (s *PGCronStore) UpsertCronJob(ctx context.Context, cj *CronJob) (*CronJob, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO cron_jobs (name, type, payload, schedule, max_retries)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (name) DO UPDATE SET
			type        = EXCLUDED.type,
			payload     = EXCLUDED.payload,
			schedule    = EXCLUDED.schedule,
			max_retries = EXCLUDED.max_retries,
			updated_at  = NOW()
		RETURNING id, name, type, payload, schedule, max_retries, last_fired_at`,
		cj.Name, cj.Type, cj.Payload, cj.Schedule, cj.MaxRetries,
	)
	out := &CronJob{}
	err := row.Scan(&out.ID, &out.Name, &out.Type, &out.Payload,
		&out.Schedule, &out.MaxRetries, &out.LastFiredAt)
	return out, err
}
