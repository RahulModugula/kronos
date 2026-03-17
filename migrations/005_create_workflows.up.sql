-- Workflow definitions: immutable, versioned
CREATE TABLE workflows (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    version TEXT NOT NULL,
    code_fingerprint TEXT NOT NULL,
    dag JSONB NOT NULL, -- DAG structure: list of steps with dependencies
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- Unique index ensures one definition per (name, fingerprint)
CREATE UNIQUE INDEX idx_workflows_name_fingerprint ON workflows (name, code_fingerprint);

-- Workflow runs: one row per execution
CREATE TYPE workflow_status AS ENUM (
    'pending',
    'running',
    'completed',
    'failed',
    'cancelled',
    'forked'
);

CREATE TABLE workflow_runs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workflow_id UUID NOT NULL REFERENCES workflows(id),
    status workflow_status NOT NULL DEFAULT 'pending',
    input JSONB,
    output JSONB,
    error TEXT,
    parent_run_id UUID REFERENCES workflow_runs(id), -- for forked runs
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_workflow_runs_workflow_id ON workflow_runs (workflow_id, started_at DESC);
CREATE INDEX idx_workflow_runs_status ON workflow_runs (status, created_at DESC);
CREATE INDEX idx_workflow_runs_parent_id ON workflow_runs (parent_run_id);

-- Workflow steps: materialized projection for fast UI queries
CREATE TABLE workflow_steps (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id UUID NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    step_name TEXT NOT NULL,
    attempt INT NOT NULL DEFAULT 0,
    status TEXT NOT NULL, -- 'pending', 'running', 'completed', 'failed', 'retried'
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    duration_ms INT,
    input_blob_id TEXT, -- hash reference to workflow_blobs
    output_blob_id TEXT, -- hash reference to workflow_blobs
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_workflow_steps_run_id ON workflow_steps (run_id, step_name);
CREATE INDEX idx_workflow_steps_status ON workflow_steps (run_id, status);

-- Workflow events: append-only event log (source of truth for time-travel)
CREATE TYPE workflow_event_type AS ENUM (
    'run_started',
    'step_scheduled',
    'step_started',
    'step_input_recorded',
    'step_output_recorded',
    'step_completed',
    'step_failed',
    'step_retried',
    'run_completed',
    'run_failed',
    'run_forked',
    'run_cancelled'
);

CREATE TABLE workflow_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id UUID NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    seq INT NOT NULL,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    event_type workflow_event_type NOT NULL,
    payload JSONB NOT NULL, -- step_name, input, output, error, etc.
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_workflow_events_run_id_seq ON workflow_events (run_id, seq);
CREATE INDEX idx_workflow_events_ts ON workflow_events (ts DESC);

-- Content-addressed blob storage for large step inputs/outputs
CREATE TABLE workflow_blobs (
    hash TEXT PRIMARY KEY,
    data BYTEA NOT NULL,
    size INT NOT NULL,
    ref_count INT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_workflow_blobs_created_at ON workflow_blobs (created_at DESC);
