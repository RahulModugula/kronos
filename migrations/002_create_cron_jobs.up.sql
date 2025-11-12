CREATE TABLE cron_jobs (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name          TEXT NOT NULL UNIQUE,
    type          TEXT NOT NULL,
    payload       JSONB NOT NULL DEFAULT '{}',
    schedule      TEXT NOT NULL,
    max_retries   INT NOT NULL DEFAULT 3,
    enabled       BOOLEAN NOT NULL DEFAULT true,
    last_fired_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
