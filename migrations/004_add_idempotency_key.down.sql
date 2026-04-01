DROP INDEX IF EXISTS jobs_idempotency_key_idx;
ALTER TABLE jobs DROP COLUMN IF EXISTS idempotency_key;
