package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore implements Store against PostgreSQL.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore creates a PGStore using an existing connection pool.
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

const jobColumns = `id, name, type, payload, status, max_retries, retry_count,
	COALESCE(error, ''), scheduled_at, created_at, updated_at`

func scanJob(row pgx.Row) (*Job, error) {
	j := &Job{}
	var status string
	err := row.Scan(
		&j.ID, &j.Name, &j.Type, &j.Payload,
		&status, &j.MaxRetries, &j.RetryCount, &j.Error,
		&j.ScheduledAt, &j.CreatedAt, &j.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	j.Status = Status(status)
	return j, nil
}

func (s *PGStore) CreateJob(ctx context.Context, j *Job) (*Job, error) {
	if j.ScheduledAt.IsZero() {
		j.ScheduledAt = time.Now().UTC()
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO jobs (name, type, payload, max_retries, scheduled_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+jobColumns,
		j.Name, j.Type, j.Payload, j.MaxRetries, j.ScheduledAt,
	)
	return scanJob(row)
}

func (s *PGStore) GetJob(ctx context.Context, id uuid.UUID) (*Job, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+jobColumns+` FROM jobs WHERE id = $1`, id,
	)
	job, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("job %s not found", id)
	}
	return job, err
}

func (s *PGStore) ListJobs(ctx context.Context, f ListFilter) ([]*Job, string, error) {
	pageSize := f.PageSize
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}

	args := []any{}
	where := "1=1"
	argN := 1

	if f.Status != nil {
		where += fmt.Sprintf(" AND status = $%d", argN)
		args = append(args, string(*f.Status))
		argN++
	}
	if f.PageToken != "" {
		where += fmt.Sprintf(" AND created_at < $%d", argN)
		args = append(args, f.PageToken)
		argN++
	}

	args = append(args, pageSize+1)
	query := fmt.Sprintf(`SELECT %s FROM jobs WHERE %s ORDER BY created_at DESC LIMIT $%d`,
		jobColumns, where, argN)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var jobs []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, "", err
		}
		jobs = append(jobs, j)
	}

	var nextToken string
	if len(jobs) > pageSize {
		jobs = jobs[:pageSize]
		nextToken = jobs[len(jobs)-1].CreatedAt.Format(time.RFC3339Nano)
	}
	return jobs, nextToken, rows.Err()
}

func (s *PGStore) ClaimJobs(ctx context.Context, n int) ([]*Job, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE jobs SET status = 'running', updated_at = NOW()
		WHERE id IN (
			SELECT id FROM jobs
			WHERE status IN ('pending', 'failed')
			  AND scheduled_at <= NOW()
			ORDER BY scheduled_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING `+jobColumns, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func (s *PGStore) UpdateStatus(ctx context.Context, id uuid.UUID, status Status, errMsg string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE jobs SET status = $1, error = NULLIF($2, ''), updated_at = NOW()
		WHERE id = $3`,
		string(status), errMsg, id,
	)
	return err
}

func (s *PGStore) IncrementRetry(ctx context.Context, id uuid.UUID, nextRun time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE jobs
		SET retry_count = retry_count + 1,
		    status      = 'pending',
		    scheduled_at = $1,
		    updated_at   = NOW()
		WHERE id = $2`,
		nextRun, id,
	)
	return err
}

func (s *PGStore) QueueDepth(ctx context.Context) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM jobs WHERE status = 'pending'`,
	).Scan(&n)
	return n, err
}

func (s *PGStore) CancelJob(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE jobs SET status = 'cancelled', updated_at = NOW()
		WHERE id = $1 AND status = 'pending'`, id,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("job %s is not pending and cannot be cancelled", id)
	}
	return nil
}
