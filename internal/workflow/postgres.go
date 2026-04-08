package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGWorkflowStore implements WorkflowStore against PostgreSQL.
type PGWorkflowStore struct {
	pool *pgxpool.Pool
}

// NewPGWorkflowStore creates a PGWorkflowStore.
func NewPGWorkflowStore(pool *pgxpool.Pool) *PGWorkflowStore {
	return &PGWorkflowStore{pool: pool}
}

// CreateWorkflow inserts a workflow definition.
func (s *PGWorkflowStore) CreateWorkflow(ctx context.Context, w *Workflow) error {
	// Validate the workflow
	if err := w.Validate(); err != nil {
		return fmt.Errorf("invalid workflow: %w", err)
	}

	// Compute fingerprint
	fingerprint := w.ComputeFingerprint()

	// Serialize the DAG
	dagBytes, err := json.Marshal(w.DAGStructure)
	if err != nil {
		return fmt.Errorf("failed to marshal DAG: %w", err)
	}

	// Insert workflow
	_, err = s.pool.Exec(ctx, `
		INSERT INTO workflows (name, version, code_fingerprint, dag)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (name, code_fingerprint) DO NOTHING`,
		w.Name, w.Version, fingerprint, dagBytes,
	)
	return err
}

// GetWorkflow retrieves a workflow by ID.
func (s *PGWorkflowStore) GetWorkflow(ctx context.Context, id string) (*Workflow, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, name, version, code_fingerprint, dag FROM workflows WHERE id = $1`,
		id,
	)
	return s.scanWorkflow(row)
}

// GetWorkflowByNameFingerprint retrieves a workflow by name and fingerprint.
func (s *PGWorkflowStore) GetWorkflowByNameFingerprint(ctx context.Context, name, fingerprint string) (*Workflow, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, name, version, code_fingerprint, dag FROM workflows
		WHERE name = $1 AND code_fingerprint = $2`,
		name, fingerprint,
	)
	w, err := s.scanWorkflow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return w, err
}

// GetLatestWorkflow retrieves the most recent workflow by name.
func (s *PGWorkflowStore) GetLatestWorkflow(ctx context.Context, name string) (*Workflow, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, name, version, code_fingerprint, dag FROM workflows
		WHERE name = $1 ORDER BY created_at DESC LIMIT 1`,
		name,
	)
	return s.scanWorkflow(row)
}

func (s *PGWorkflowStore) scanWorkflow(row pgx.Row) (*Workflow, error) {
	var id, name, version, fingerprint string
	var dagJSON []byte
	err := row.Scan(&id, &name, &version, &fingerprint, &dagJSON)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	var dag map[string][]string
	if err := json.Unmarshal(dagJSON, &dag); err != nil {
		return nil, fmt.Errorf("failed to unmarshal DAG: %w", err)
	}

	w := &Workflow{
		Name:            name,
		Version:         version,
		CodeFingerprint: fingerprint,
		DAGStructure:    dag,
		Steps:           []*StepDef{}, // Note: handlers are nil when loading from DB
	}
	return w, nil
}

// CreateRun inserts a new workflow run.
func (s *PGWorkflowStore) CreateRun(ctx context.Context, run *WorkflowRun) (*WorkflowRun, error) {
	var parentRunID interface{}
	if run.ParentRunID != nil {
		parentRunID = *run.ParentRunID
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO workflow_runs (workflow_id, status, input, parent_run_id)
		VALUES ($1, $2, $3, $4)
		RETURNING id, workflow_id, status, input, output, error, parent_run_id, started_at, finished_at, created_at, updated_at`,
		run.WorkflowID, run.Status, run.Input, parentRunID,
	)
	return s.scanRun(row)
}

// GetRun retrieves a run by ID.
func (s *PGWorkflowStore) GetRun(ctx context.Context, id string) (*WorkflowRun, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, workflow_id, status, input, output, error, parent_run_id, started_at, finished_at, created_at, updated_at
		FROM workflow_runs WHERE id = $1`,
		id,
	)
	run, err := s.scanRun(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("run %s not found", id)
	}
	return run, err
}

// ListRuns retrieves runs with optional filtering.
func (s *PGWorkflowStore) ListRuns(ctx context.Context, workflowName *string, status *string, pageSize int, pageToken string) ([]*WorkflowRun, string, error) {
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}

	args := []interface{}{}
	where := "1=1"
	argN := 1

	// Filter by workflow name (requires join with workflows table)
	if workflowName != nil {
		where += fmt.Sprintf(" AND w.name = $%d", argN)
		args = append(args, *workflowName)
		argN++
	}

	// Filter by status
	if status != nil {
		where += fmt.Sprintf(" AND wr.status = $%d", argN)
		args = append(args, *status)
		argN++
	}

	// Pagination (would add cursor logic here if needed)
	args = append(args, pageSize+1)
	query := fmt.Sprintf(`
		SELECT wr.id, wr.workflow_id, wr.status, wr.input, wr.output, wr.error,
		       wr.parent_run_id, wr.started_at, wr.finished_at, wr.created_at, wr.updated_at
		FROM workflow_runs wr
		LEFT JOIN workflows w ON wr.workflow_id = w.id
		WHERE %s
		ORDER BY wr.created_at DESC LIMIT $%d`,
		where, argN)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var runs []*WorkflowRun
	for rows.Next() {
		run, err := s.scanRun(rows)
		if err != nil {
			return nil, "", err
		}
		runs = append(runs, run)
	}

	var nextToken string
	if len(runs) > pageSize {
		runs = runs[:pageSize]
		// Simple cursor: last run's ID (in production, use timestamp+id)
		nextToken = runs[len(runs)-1].ID
	}

	return runs, nextToken, rows.Err()
}

func (s *PGWorkflowStore) scanRun(row pgx.Row) (*WorkflowRun, error) {
	run := &WorkflowRun{}
	var parentRunID *string
	err := row.Scan(
		&run.ID, &run.WorkflowID, &run.Status, &run.Input, &run.Output, &run.Error,
		&parentRunID, &run.StartedAt, &run.FinishedAt, &run.CreatedAt, &run.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	run.ParentRunID = parentRunID
	return run, nil
}

// UpdateRunStatus sets status, error, and finished_at.
func (s *PGWorkflowStore) UpdateRunStatus(ctx context.Context, runID, status string, errMsg *string, finishedAt *time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE workflow_runs
		SET status = $1, error = $2, finished_at = $3, updated_at = NOW()
		WHERE id = $4`,
		status, errMsg, finishedAt, runID,
	)
	return err
}

// UpdateRunOutput sets the output on a completed run.
func (s *PGWorkflowStore) UpdateRunOutput(ctx context.Context, runID string, output json.RawMessage) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE workflow_runs SET output = $1, updated_at = NOW() WHERE id = $2`,
		output, runID,
	)
	return err
}

// CancelRun transitions a pending run to cancelled.
func (s *PGWorkflowStore) CancelRun(ctx context.Context, runID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE workflow_runs SET status = 'cancelled', updated_at = NOW()
		WHERE id = $1 AND status = 'pending'`,
		runID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("run %s is not pending and cannot be cancelled", runID)
	}
	return nil
}

// AppendEvent appends an event to the log.
func (s *PGWorkflowStore) AppendEvent(ctx context.Context, event *Event) (int, error) {
	// Get the next sequence number for this run
	var seq int
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM workflow_events WHERE run_id = $1`,
		event.RunID,
	).Scan(&seq)
	if err != nil {
		return 0, err
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO workflow_events (run_id, seq, ts, event_type, payload)
		VALUES ($1, $2, $3, $4, $5)`,
		event.RunID, seq, time.Now().UTC(), string(event.EventType), event.Payload,
	)
	return seq, err
}

// GetEvents retrieves all events for a run.
func (s *PGWorkflowStore) GetEvents(ctx context.Context, runID string) ([]*Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, run_id, seq, ts, event_type, payload
		FROM workflow_events WHERE run_id = $1 ORDER BY seq ASC`,
		runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*Event
	for rows.Next() {
		event := &Event{}
		var eventType string
		err := rows.Scan(&event.ID, &event.RunID, &event.Seq, &event.Ts, &eventType, &event.Payload)
		if err != nil {
			return nil, err
		}
		event.EventType = EventType(eventType)
		events = append(events, event)
	}
	return events, rows.Err()
}

// GetEventsAfterSeq retrieves events after a sequence number.
func (s *PGWorkflowStore) GetEventsAfterSeq(ctx context.Context, runID string, afterSeq int) ([]*Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, run_id, seq, ts, event_type, payload
		FROM workflow_events WHERE run_id = $1 AND seq > $2 ORDER BY seq ASC`,
		runID, afterSeq,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*Event
	for rows.Next() {
		event := &Event{}
		var eventType string
		err := rows.Scan(&event.ID, &event.RunID, &event.Seq, &event.Ts, &eventType, &event.Payload)
		if err != nil {
			return nil, err
		}
		event.EventType = EventType(eventType)
		events = append(events, event)
	}
	return events, rows.Err()
}

// PutBlob stores a blob (large input/output).
func (s *PGWorkflowStore) PutBlob(ctx context.Context, hash string, data []byte) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO workflow_blobs (hash, data, size) VALUES ($1, $2, $3)
		ON CONFLICT (hash) DO UPDATE SET ref_count = ref_count + 1`,
		hash, data, len(data),
	)
	return err
}

// GetBlob retrieves a blob.
func (s *PGWorkflowStore) GetBlob(ctx context.Context, hash string) ([]byte, error) {
	var data []byte
	err := s.pool.QueryRow(ctx,
		`SELECT data FROM workflow_blobs WHERE hash = $1`,
		hash,
	).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("blob %s not found", hash)
	}
	return data, err
}

// DeleteBlob decrements ref_count and deletes if needed.
func (s *PGWorkflowStore) DeleteBlob(ctx context.Context, hash string) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM workflow_blobs WHERE hash = $1 AND
		ref_count <= 1`,
		hash,
	)
	if err == nil {
		return nil
	}
	// If delete failed due to ref_count > 1, just decrement
	_, err = s.pool.Exec(ctx, `
		UPDATE workflow_blobs SET ref_count = ref_count - 1 WHERE hash = $1`,
		hash,
	)
	return err
}

// UpsertStepExecution inserts or updates a step execution.
func (s *PGWorkflowStore) UpsertStepExecution(ctx context.Context, exec *StepExecution) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO workflow_steps (run_id, step_name, attempt, status, started_at, finished_at, duration_ms, input_blob_id, output_blob_id, error)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (run_id, step_name) DO UPDATE SET
			status = EXCLUDED.status,
			finished_at = EXCLUDED.finished_at,
			duration_ms = EXCLUDED.duration_ms,
			output_blob_id = EXCLUDED.output_blob_id,
			error = EXCLUDED.error`,
		exec.RunID, exec.StepName, exec.Attempt, exec.Status,
		exec.StartedAt, exec.FinishedAt, exec.DurationMs,
		exec.Input, exec.Output, exec.Error,
	)
	return err
}

// GetStepExecution retrieves a step execution.
func (s *PGWorkflowStore) GetStepExecution(ctx context.Context, runID, stepName string) (*StepExecution, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, run_id, step_name, attempt, status, started_at, finished_at, duration_ms, input_blob_id, output_blob_id, error
		FROM workflow_steps WHERE run_id = $1 AND step_name = $2`,
		runID, stepName,
	)
	return s.scanStepExecution(row)
}

// ListStepExecutions retrieves all step executions for a run.
func (s *PGWorkflowStore) ListStepExecutions(ctx context.Context, runID string) ([]*StepExecution, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, run_id, step_name, attempt, status, started_at, finished_at, duration_ms, input_blob_id, output_blob_id, error
		FROM workflow_steps WHERE run_id = $1 ORDER BY created_at ASC`,
		runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var execs []*StepExecution
	for rows.Next() {
		exec, err := s.scanStepExecution(rows)
		if err != nil {
			return nil, err
		}
		execs = append(execs, exec)
	}
	return execs, rows.Err()
}

func (s *PGWorkflowStore) scanStepExecution(row pgx.Row) (*StepExecution, error) {
	exec := &StepExecution{}
	err := row.Scan(
		&exec.ID, &exec.RunID, &exec.StepName, &exec.Attempt, &exec.Status,
		&exec.StartedAt, &exec.FinishedAt, &exec.DurationMs,
		&exec.Input, &exec.Output, &exec.Error,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return exec, err
}
