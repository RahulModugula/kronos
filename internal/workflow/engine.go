package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rs/zerolog"
)

// Engine orchestrates workflow execution.
// It runs on a single node (the leader) and dispatches steps as jobs
// to the worker pool via the existing job system.
type Engine struct {
	store    WorkflowStore
	registry *Registry
	log      zerolog.Logger
}

// NewEngine creates a new workflow engine.
func NewEngine(store WorkflowStore, registry *Registry, log zerolog.Logger) *Engine {
	return &Engine{
		store:    store,
		registry: registry,
		log:      log,
	}
}

// ExecuteStep executes a single workflow step.
// This is called by the job handler (internal/worker) when a kronos.workflow_step job executes.
// The step input is constructed from upstream recorded outputs,
// and execution is recorded as events in the workflow_events table.
func (e *Engine) ExecuteStep(ctx context.Context, runID, stepName string) error {
	e.log.Info().Str("run_id", runID).Str("step_name", stepName).Msg("executing workflow step")

	// Get the run
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}

	// Get the workflow definition
	wf, err := e.store.GetWorkflow(ctx, run.WorkflowID)
	if err != nil {
		return fmt.Errorf("get workflow: %w", err)
	}

	// Get the registered workflow definition (with handlers)
	registeredWf := e.registry.Get(wf.Name)
	if registeredWf == nil {
		return fmt.Errorf("workflow %q not registered", wf.Name)
	}

	// Get the step definition
	stepDef := registeredWf.GetStepDef(stepName)
	if stepDef == nil {
		return fmt.Errorf("step %q not found in workflow", stepName)
	}

	// Get all events so far to reconstruct state
	events, err := e.store.GetEvents(ctx, runID)
	if err != nil {
		return fmt.Errorf("get events: %w", err)
	}

	// Reconstruct completed steps and their outputs
	completed := make(map[string]json.RawMessage)
	for _, event := range events {
		if event.EventType == EventStepOutputRecorded {
			var payload StepOutputRecordedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return fmt.Errorf("unmarshal step output event: %w", err)
			}
			output := payload.Output
			// If output is in a blob, fetch it
			if payload.BlobID != nil {
				blob, err := e.store.GetBlob(ctx, *payload.BlobID)
				if err != nil {
					return fmt.Errorf("get blob: %w", err)
				}
				output = json.RawMessage(blob)
			}
			completed[payload.StepName] = output
		}
	}

	// Assemble step input from upstream outputs or run input
	var stepInput json.RawMessage
	if len(stepDef.Dependencies) == 0 {
		// No dependencies: use run input
		stepInput = run.Input
	} else {
		// Dependencies exist: assemble from upstream outputs
		inputs := make(map[string]json.RawMessage)
		for _, dep := range stepDef.Dependencies {
			depOutput, ok := completed[dep]
			if !ok {
				return fmt.Errorf("dependency step %q not completed", dep)
			}
			inputs[dep] = depOutput
		}
		// If single dependency, pass its output directly; if multiple, wrap in object
		if len(inputs) == 1 {
			for _, v := range inputs {
				stepInput = v
			}
		} else {
			inputsJSON, err := json.Marshal(inputs)
			if err != nil {
				return fmt.Errorf("marshal inputs: %w", err)
			}
			stepInput = inputsJSON
		}
	}

	// Record StepStarted event
	startEvent, err := NewEvent(runID, len(events), EventStepStarted, StepStartedPayload{
		StepName: stepName,
		Attempt:  0, // First attempt
	})
	if err != nil {
		return fmt.Errorf("new event: %w", err)
	}
	_, err = e.store.AppendEvent(ctx, startEvent)
	if err != nil {
		return fmt.Errorf("append start event: %w", err)
	}

	// Record StepInputRecorded event
	events, err = e.store.GetEvents(ctx, runID)
	if err != nil {
		return fmt.Errorf("get events: %w", err)
	}
	inputEvent, err := NewEvent(runID, len(events), EventStepInputRecorded, StepInputRecordedPayload{
		StepName: stepName,
		Input:    stepInput,
	})
	if err != nil {
		return fmt.Errorf("new event: %w", err)
	}
	_, err = e.store.AppendEvent(ctx, inputEvent)
	if err != nil {
		return fmt.Errorf("append input event: %w", err)
	}

	// Execute the step
	startTime := time.Now()
	stepOutput, execErr := stepDef.Handler(stepInput)
	duration := time.Since(startTime)
	durationMs := int(duration.Milliseconds())

	// Record StepOutputRecorded or StepFailed event
	events, err = e.store.GetEvents(ctx, runID)
	if err != nil {
		return fmt.Errorf("get events: %w", err)
	}

	if execErr != nil {
		// Step failed
		failEvent, err := NewEvent(runID, len(events), EventStepFailed, StepFailedPayload{
			StepName: stepName,
			Error:    execErr.Error(),
			Attempt:  0,
		})
		if err != nil {
			return fmt.Errorf("new fail event: %w", err)
		}
		_, err = e.store.AppendEvent(ctx, failEvent)
		if err != nil {
			return fmt.Errorf("append fail event: %w", err)
		}
		e.log.Error().Err(execErr).Str("step", stepName).Msg("step failed")
		return fmt.Errorf("step %s failed: %w", stepName, execErr)
	}

	// Step succeeded
	outputEvent, err := NewEvent(runID, len(events), EventStepOutputRecorded, StepOutputRecordedPayload{
		StepName:   stepName,
		Output:     stepOutput,
		DurationMs: durationMs,
	})
	if err != nil {
		return fmt.Errorf("new output event: %w", err)
	}
	_, err = e.store.AppendEvent(ctx, outputEvent)
	if err != nil {
		return fmt.Errorf("append output event: %w", err)
	}

	// Record StepCompleted event
	events, err = e.store.GetEvents(ctx, runID)
	if err != nil {
		return fmt.Errorf("get events: %w", err)
	}
	completedEvent, err := NewEvent(runID, len(events), EventStepCompleted, StepCompletedPayload{
		StepName: stepName,
	})
	if err != nil {
		return fmt.Errorf("new completed event: %w", err)
	}
	_, err = e.store.AppendEvent(ctx, completedEvent)
	if err != nil {
		return fmt.Errorf("append completed event: %w", err)
	}

	e.log.Info().Str("step", stepName).Int("duration_ms", durationMs).Msg("step completed")

	// Check if all steps are completed; if so, mark run as completed
	events, err = e.store.GetEvents(ctx, runID)
	if err != nil {
		return fmt.Errorf("get events: %w", err)
	}

	completedSteps := make(map[string]bool)
	for _, event := range events {
		if event.EventType == EventStepCompleted {
			var payload StepCompletedPayload
			if err := json.Unmarshal(event.Payload, &payload); err == nil {
				completedSteps[payload.StepName] = true
			}
		}
	}

	// Check if all steps in the registered workflow are completed
	allDone := true
	for _, step := range registeredWf.Steps {
		if !completedSteps[step.Name] {
			allDone = false
			break
		}
	}

	if allDone {
		// Workflow is complete
		now := time.Now()
		runCompletedEvent, err := NewEvent(runID, len(events), EventRunCompleted, RunCompletedPayload{
			Output: stepOutput, // last step output is the final output
		})
		if err != nil {
			return fmt.Errorf("new run completed event: %w", err)
		}
		_, err = e.store.AppendEvent(ctx, runCompletedEvent)
		if err != nil {
			return fmt.Errorf("append run completed event: %w", err)
		}

		// Update run status
		if err := e.store.UpdateRunStatus(ctx, runID, "completed", nil, &now); err != nil {
			return fmt.Errorf("update run status: %w", err)
		}
		if err := e.store.UpdateRunOutput(ctx, runID, stepOutput); err != nil {
			return fmt.Errorf("update run output: %w", err)
		}
		e.log.Info().Str("run_id", runID).Msg("workflow completed")
	}

	return nil
}

// ComputeBlobHash computes a content hash for a blob.
func ComputeBlobHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
