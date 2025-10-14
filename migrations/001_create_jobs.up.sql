CREATE TYPE job_status AS ENUM (
    'pending',
    'running',
    'completed',
    'failed',
    'cancelled',
    'dead'
);

CREATE TABLE jobs (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name         TEXT NOT NULL,
    type         TEXT NOT NULL,
    payload      JSONB NOT NULL DEFAULT '{}',
    status       job_status NOT NULL DEFAULT 'pending',
    max_retries  INT NOT NULL DEFAULT 3,
    retry_count  INT NOT NULL DEFAULT 0,
    error        TEXT,
    scheduled_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_jobs_status_scheduled_at ON jobs (status, scheduled_at)
    WHERE status IN ('pending', 'failed');

CREATE INDEX idx_jobs_created_at ON jobs (created_at DESC);
