package workflow

import (
	"context"
	"encoding/json"
	"time"
)

// WorkflowStore is the persistence interface for workflows and runs.
type WorkflowStore interface {
	// CreateWorkflow inserts a new workflow definition.
	// Returns the created workflow with ID populated.
	CreateWorkflow(ctx context.Context, w *Workflow) error

	// GetWorkflow retrieves a workflow definition by ID.
	GetWorkflow(ctx context.Context, id string) (*Workflow, error)

	// GetWorkflowByNameFingerprint retrieves a workflow by name and fingerprint.
	// Returns nil if not found.
	GetWorkflowByNameFingerprint(ctx context.Context, name, fingerprint string) (*Workflow, error)

	// GetLatestWorkflow retrieves the most recent workflow definition by name.
	GetLatestWorkflow(ctx context.Context, name string) (*Workflow, error)

	// CreateRun inserts a new workflow run.
	CreateRun(ctx context.Context, run *WorkflowRun) (*WorkflowRun, error)

	// GetRun retrieves a run by ID.
	GetRun(ctx context.Context, id string) (*WorkflowRun, error)

	// ListRuns retrieves runs, optionally filtered by workflow name and/or status.
	ListRuns(ctx context.Context, workflowName *string, status *string, pageSize int, pageToken string) ([]*WorkflowRun, string, error)

	// UpdateRunStatus sets the status (and optionally error and finished_at) on a run.
	UpdateRunStatus(ctx context.Context, runID, status string, errMsg *string, finishedAt *time.Time) error

	// UpdateRunOutput sets the output on a completed run.
	UpdateRunOutput(ctx context.Context, runID string, output json.RawMessage) error

	// CancelRun transitions a pending run to cancelled.
	CancelRun(ctx context.Context, runID string) error

	// AppendEvent appends an event to the event log and returns the assigned sequence number.
	AppendEvent(ctx context.Context, event *Event) (int, error)

	// GetEvents retrieves all events for a run in order.
	GetEvents(ctx context.Context, runID string) ([]*Event, error)

	// GetEventsAfterSeq retrieves events for a run starting after a given sequence number.
	GetEventsAfterSeq(ctx context.Context, runID string, afterSeq int) ([]*Event, error)

	// PutBlob stores a large input/output in blob storage by content hash.
	// If the blob already exists, only the ref_count is incremented.
	PutBlob(ctx context.Context, hash string, data []byte) error

	// GetBlob retrieves a blob by content hash.
	GetBlob(ctx context.Context, hash string) ([]byte, error)

	// DeleteBlob decrements the ref_count and deletes the blob if it reaches 0.
	DeleteBlob(ctx context.Context, hash string) error

	// UpsertStepExecution inserts or updates a step execution record.
	UpsertStepExecution(ctx context.Context, exec *StepExecution) error

	// GetStepExecution retrieves a step execution.
	GetStepExecution(ctx context.Context, runID, stepName string) (*StepExecution, error)

	// ListStepExecutions retrieves all step executions for a run.
	ListStepExecutions(ctx context.Context, runID string) ([]*StepExecution, error)
}
