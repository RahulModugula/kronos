package workflow

import (
	"encoding/json"
	"time"
)

// EventType is the type of workflow event.
type EventType string

const (
	EventRunStarted         EventType = "run_started"
	EventStepScheduled      EventType = "step_scheduled"
	EventStepStarted        EventType = "step_started"
	EventStepInputRecorded  EventType = "step_input_recorded"
	EventStepOutputRecorded EventType = "step_output_recorded"
	EventStepCompleted      EventType = "step_completed"
	EventStepFailed         EventType = "step_failed"
	EventStepRetried        EventType = "step_retried"
	EventRunCompleted       EventType = "run_completed"
	EventRunFailed          EventType = "run_failed"
	EventRunForked          EventType = "run_forked"
	EventRunCancelled       EventType = "run_cancelled"
)

// Event is a single entry in a workflow run's event log.
type Event struct {
	ID        string
	RunID     string
	Seq       int       // monotonically increasing sequence number
	Ts        time.Time // timestamp when the event was recorded
	EventType EventType
	Payload   json.RawMessage
}

// RunStartedPayload is the payload for a RunStarted event.
type RunStartedPayload struct {
	WorkflowName    string          `json:"workflow_name"`
	WorkflowVersion string          `json:"workflow_version"`
	Input           json.RawMessage `json:"input"`
}

// StepScheduledPayload is the payload for a StepScheduled event.
type StepScheduledPayload struct {
	StepName     string   `json:"step_name"`
	Dependencies []string `json:"dependencies"`
}

// StepStartedPayload is the payload for a StepStarted event.
type StepStartedPayload struct {
	StepName string `json:"step_name"`
	Attempt  int    `json:"attempt"`
}

// StepInputRecordedPayload is the payload for a StepInputRecorded event.
type StepInputRecordedPayload struct {
	StepName string          `json:"step_name"`
	Input    json.RawMessage `json:"input"`
	BlobID   *string         `json:"blob_id,omitempty"` // if input is large
}

// StepOutputRecordedPayload is the payload for a StepOutputRecorded event.
type StepOutputRecordedPayload struct {
	StepName   string          `json:"step_name"`
	Output     json.RawMessage `json:"output"`
	BlobID     *string         `json:"blob_id,omitempty"` // if output is large
	DurationMs int             `json:"duration_ms"`
}

// StepCompletedPayload is the payload for a StepCompleted event.
type StepCompletedPayload struct {
	StepName string `json:"step_name"`
}

// StepFailedPayload is the payload for a StepFailed event.
type StepFailedPayload struct {
	StepName string `json:"step_name"`
	Error    string `json:"error"`
	Attempt  int    `json:"attempt"`
}

// StepRetriedPayload is the payload for a StepRetried event.
type StepRetriedPayload struct {
	StepName string `json:"step_name"`
	Attempt  int    `json:"attempt"`
}

// RunCompletedPayload is the payload for a RunCompleted event.
type RunCompletedPayload struct {
	Output json.RawMessage `json:"output,omitempty"`
}

// RunFailedPayload is the payload for a RunFailed event.
type RunFailedPayload struct {
	Error string `json:"error"`
}

// RunForkedPayload is the payload for a RunForked event.
type RunForkedPayload struct {
	ForkedRunID  string `json:"forked_run_id"`
	FromStepName string `json:"from_step_name"`
}

// NewEvent creates a new event with a payload.
func NewEvent(runID string, seq int, eventType EventType, payload interface{}) (*Event, error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &Event{
		RunID:     runID,
		Seq:       seq,
		Ts:        time.Now().UTC(),
		EventType: eventType,
		Payload:   payloadBytes,
	}, nil
}
