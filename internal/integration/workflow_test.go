package integration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/rahulmodugula/kronos/internal/workflow"
)

func TestWorkflowEngine(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	log := zerolog.Nop()

	// Start Postgres container
	req := testcontainers.ContainerRequest{
		Image:        "postgres:17-alpine",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_DB":       "kronos_test",
			"POSTGRES_USER":     "kronos",
			"POSTGRES_PASSWORD": "kronos",
		},
		WaitingFor: wait.ForLog("database system is ready to accept connections"),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err)
	defer container.Terminate(ctx)

	// Get connection string
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432")
	require.NoError(t, err)

	dsn := "postgres://kronos:kronos@" + host + ":" + port.Port() + "/kronos_test?sslmode=disable"
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	// Create workflow tables manually for this test (migrations not loaded)
	createTables := `
		CREATE TYPE workflow_status AS ENUM ('pending', 'running', 'completed', 'failed', 'cancelled', 'forked');
		CREATE TYPE workflow_event_type AS ENUM ('run_started', 'step_scheduled', 'step_started', 'step_input_recorded', 'step_output_recorded', 'step_completed', 'step_failed', 'step_retried', 'run_completed', 'run_failed', 'run_forked', 'run_cancelled');

		CREATE TABLE workflows (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			name TEXT NOT NULL,
			version TEXT NOT NULL,
			code_fingerprint TEXT NOT NULL,
			dag JSONB NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE UNIQUE INDEX idx_workflows_name_fingerprint ON workflows (name, code_fingerprint);

		CREATE TABLE workflow_runs (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			workflow_id UUID NOT NULL REFERENCES workflows(id),
			status workflow_status NOT NULL DEFAULT 'pending',
			input JSONB,
			output JSONB,
			error TEXT,
			parent_run_id UUID REFERENCES workflow_runs(id),
			started_at TIMESTAMPTZ,
			finished_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX idx_workflow_runs_workflow_id ON workflow_runs (workflow_id, started_at DESC);
		CREATE INDEX idx_workflow_runs_status ON workflow_runs (status, created_at DESC);

		CREATE TABLE workflow_steps (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			run_id UUID NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
			step_name TEXT NOT NULL,
			attempt INT NOT NULL DEFAULT 0,
			status TEXT NOT NULL,
			started_at TIMESTAMPTZ,
			finished_at TIMESTAMPTZ,
			duration_ms INT,
			input_blob_id TEXT,
			output_blob_id TEXT,
			error TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX idx_workflow_steps_run_id ON workflow_steps (run_id, step_name);

		CREATE TABLE workflow_events (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			run_id UUID NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
			seq INT NOT NULL,
			ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			event_type workflow_event_type NOT NULL,
			payload JSONB NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX idx_workflow_events_run_id_seq ON workflow_events (run_id, seq);

		CREATE TABLE workflow_blobs (
			hash TEXT PRIMARY KEY,
			data BYTEA NOT NULL,
			size INT NOT NULL,
			ref_count INT NOT NULL DEFAULT 1,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
	`
	_, err = pool.Exec(ctx, createTables)
	require.NoError(t, err, "failed to create tables")

	// Create stores and engine
	store := workflow.NewPGWorkflowStore(pool)
	registry := workflow.NewRegistry()
	engine := workflow.NewEngine(store, registry, log)

	// Define a simple two-step workflow
	step1Called := false
	step2Called := false

	wf := workflow.NewWorkflow("test-workflow", "v1").
		AddStep("step1", func(input json.RawMessage) (json.RawMessage, error) {
			step1Called = true
			return json.Marshal(map[string]string{"result": "step1_done"})
		}, nil).
		AddStep("step2", func(input json.RawMessage) (json.RawMessage, error) {
			step2Called = true
			return json.Marshal(map[string]string{"result": "step2_done"})
		}, []string{"step1"})

	// Register and store the workflow
	err = registry.Register(wf)
	require.NoError(t, err)
	err = store.CreateWorkflow(ctx, wf)
	require.NoError(t, err)

	// Create a workflow run
	run := &workflow.WorkflowRun{
		ID:         uuid.New().String(),
		WorkflowID: wf.Name,
		Status:     "running",
		Input:      json.RawMessage(`{}`),
	}
	createdRun, err := store.CreateRun(ctx, run)
	require.NoError(t, err)

	// Execute step1
	err = engine.ExecuteStep(ctx, createdRun.ID, "step1")
	require.NoError(t, err)
	require.True(t, step1Called, "step1 should have been called")

	// Execute step2
	err = engine.ExecuteStep(ctx, createdRun.ID, "step2")
	require.NoError(t, err)
	require.True(t, step2Called, "step2 should have been called")

	// Verify events were recorded
	events, err := store.GetEvents(ctx, createdRun.ID)
	require.NoError(t, err)
	require.True(t, len(events) > 0, "events should be recorded")

	// Check for key events
	eventTypes := make(map[workflow.EventType]int)
	for _, e := range events {
		eventTypes[e.EventType]++
	}

	require.Greater(t, eventTypes[workflow.EventRunStarted], 0, "should have run started event")
	require.Equal(t, 2, eventTypes[workflow.EventStepCompleted], "should have 2 step completed events")

	// Verify the run is marked as completed
	finalRun, err := store.GetRun(ctx, createdRun.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", finalRun.Status)
	require.NotNil(t, finalRun.Output)
}
