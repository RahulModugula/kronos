ALTER TABLE jobs ADD COLUMN priority SMALLINT NOT NULL DEFAULT 5
    CONSTRAINT priority_range CHECK (priority BETWEEN 1 AND 10);

-- Replace the old partial index with one that matches the new ClaimJobs query shape.
-- The planner will use this covering index for the ORDER BY inside SKIP LOCKED.
DROP INDEX idx_jobs_status_scheduled_at;
CREATE INDEX idx_jobs_claimable ON jobs (priority DESC, scheduled_at ASC)
    WHERE status IN ('pending', 'failed');
