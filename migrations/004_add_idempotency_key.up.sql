ALTER TABLE jobs ADD COLUMN IF NOT EXISTS idempotency_key TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS jobs_idempotency_key_idx
  ON jobs (idempotency_key)
  WHERE idempotency_key IS NOT NULL AND status NOT IN ('cancelled', 'dead');
