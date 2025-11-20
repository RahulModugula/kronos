DROP INDEX IF EXISTS idx_jobs_claimable;
CREATE INDEX idx_jobs_status_scheduled_at ON jobs (status, scheduled_at)
    WHERE status IN ('pending', 'failed');
ALTER TABLE jobs DROP COLUMN IF EXISTS priority;
