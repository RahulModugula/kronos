package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockWorkflowStore is a simple in-memory mock for testing.
type mockWorkflowStore struct {
	workflows   map[string]*Workflow
	runs        map[string]*WorkflowRun
	events      map[string][]*Event
	stepCounter map[string]int
}

func newMockWorkflowStore() *mockWorkflowStore {
	return &mockWorkflowStore{
		workflows:   make(map[string]*Workflow),
		runs:        make(map[string]*WorkflowRun),
		events:      make(map[string][]*Event),
		stepCounter: make(map[string]int),
	}
}

func (m *mockWorkflowStore) CreateWorkflow(ctx context.Context, w *Workflow) error {
	m.workflows[w.Name] = w
	return nil
}

func (m *mockWorkflowStore) GetWorkflow(ctx context.Context, id string) (*Workflow, error) {
	for _, w := range m.workflows {
		if w.Name == id { // simplified: use name as id
			return w, nil
		}
	}
	return nil, nil
}

func (m *mockWorkflowStore) GetWorkflowByNameFingerprint(ctx context.Context, name, fingerprint string) (*Workflow, error) {
	return m.workflows[name], nil
}

func (m *mockWorkflowStore) GetLatestWorkflow(ctx context.Context, name string) (*Workflow, error) {
	return m.workflows[name], nil
}

func (m *mockWorkflowStore) CreateRun(ctx context.Context, run *WorkflowRun) (*WorkflowRun, error) {
	m.runs[run.ID] = run
	m.events[run.ID] = []*Event{}
	return run, nil
}

func (m *mockWorkflowStore) GetRun(ctx context.Context, id string) (*WorkflowRun, error) {
	return m.runs[id], nil
}

func (m *mockWorkflowStore) ListRuns(ctx context.Context, workflowName *string, status *string, pageSize int, pageToken string) ([]*WorkflowRun, string, error) {
	var runs []*WorkflowRun
	for _, r := range m.runs {
		runs = append(runs, r)
	}
	return runs, "", nil
}

func (m *mockWorkflowStore) UpdateRunStatus(ctx context.Context, runID, status string, errMsg *string, finishedAt *time.Time) error {
	if run, ok := m.runs[runID]; ok {
		run.Status = status
		if errMsg != nil {
			run.Error = *errMsg
		}
		if finishedAt != nil {
			run.FinishedAt = finishedAt
		}
	}
	return nil
}

func (m *mockWorkflowStore) UpdateRunOutput(ctx context.Context, runID string, output json.RawMessage) error {
	if run, ok := m.runs[runID]; ok {
		run.Output = output
	}
	return nil
}

func (m *mockWorkflowStore) CancelRun(ctx context.Context, runID string) error {
	if run, ok := m.runs[runID]; ok {
		run.Status = "cancelled"
	}
	return nil
}

func (m *mockWorkflowStore) AppendEvent(ctx context.Context, event *Event) (int, error) {
	m.events[event.RunID] = append(m.events[event.RunID], event)
	return len(m.events[event.RunID]), nil
}

func (m *mockWorkflowStore) GetEvents(ctx context.Context, runID string) ([]*Event, error) {
	return m.events[runID], nil
}

func (m *mockWorkflowStore) GetEventsAfterSeq(ctx context.Context, runID string, afterSeq int) ([]*Event, error) {
	var result []*Event
	for _, e := range m.events[runID] {
		if e.Seq > afterSeq {
			result = append(result, e)
		}
	}
	return result, nil
}

func (m *mockWorkflowStore) PutBlob(ctx context.Context, hash string, data []byte) error {
	return nil
}

func (m *mockWorkflowStore) GetBlob(ctx context.Context, hash string) ([]byte, error) {
	return nil, nil
}

func (m *mockWorkflowStore) DeleteBlob(ctx context.Context, hash string) error {
	return nil
}

func (m *mockWorkflowStore) UpsertStepExecution(ctx context.Context, exec *StepExecution) error {
	return nil
}

func (m *mockWorkflowStore) GetStepExecution(ctx context.Context, runID, stepName string) (*StepExecution, error) {
	return nil, nil
}

func (m *mockWorkflowStore) ListStepExecutions(ctx context.Context, runID string) ([]*StepExecution, error) {
	return nil, nil
}

// Test: Simple linear workflow with two steps.
func TestEngineLinearWorkflow(t *testing.T) {
	ctx := context.Background()
	log := zerolog.Nop()

	// Create a simple workflow
	store := newMockWorkflowStore()
	registry := NewRegistry()

	// Define two simple steps
	step1Executed := false
	step2Executed := false

	wf := NewWorkflow("test-workflow", "v1").
		AddStep("step1", func(input json.RawMessage) (json.RawMessage, error) {
			step1Executed = true
			return json.Marshal(map[string]string{"step1_output": "done"})
		}, nil).
		AddStep("step2", func(input json.RawMessage) (json.RawMessage, error) {
			step2Executed = true
			return json.Marshal(map[string]string{"step2_output": "finished"})
		}, []string{"step1"})

	// Register workflow
	err := registry.Register(wf)
	require.NoError(t, err)
	err = store.CreateWorkflow(ctx, wf)
	require.NoError(t, err)

	// Create engine
	engine := NewEngine(store, registry, log)

	// Create a run
	run := &WorkflowRun{
		ID:         "run-123",
		WorkflowID: wf.Name,
		Status:     "running",
		Input:      json.RawMessage(`{}`),
	}
	createdRun, err := store.CreateRun(ctx, run)
	require.NoError(t, err)

	// Execute step1
	err = engine.ExecuteStep(ctx, createdRun.ID, "step1")
	require.NoError(t, err)
	assert.True(t, step1Executed)

	// Execute step2
	err = engine.ExecuteStep(ctx, createdRun.ID, "step2")
	require.NoError(t, err)
	assert.True(t, step2Executed)

	// Check that events were recorded
	events, err := store.GetEvents(ctx, createdRun.ID)
	require.NoError(t, err)
	assert.True(t, len(events) > 0, "events should be recorded")

	// Check for step completed events
	var completedSteps int
	for _, e := range events {
		if e.EventType == EventStepCompleted {
			completedSteps++
		}
	}
	assert.Equal(t, 2, completedSteps, "both steps should have completed events")
}

// Test: Workflow validation catches cycles.
func TestWorkflowValidationDetectsCycles(t *testing.T) {
	// Create a workflow with a cycle: step1 -> step2 -> step1
	// This should be prevented by the validation
	wf := NewWorkflow("cyclic", "v1").
		AddStep("step1", func(input json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		}, []string{"step2"}).
		AddStep("step2", func(input json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		}, []string{"step1"})

	err := wf.Validate()
	assert.Error(t, err, "cyclic workflow should fail validation")
	assert.Contains(t, err.Error(), "cycle")
}
