package replay

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/rs/zerolog"

	"github.com/rahulmodugula/kronos/internal/workflow"
)

type Replayer struct {
	registry *workflow.Registry
	run      *workflow.WorkflowRun
	events   []*workflow.Event
	log      zerolog.Logger
}

func NewReplayer(registry *workflow.Registry, run *workflow.WorkflowRun, events []*workflow.Event, log zerolog.Logger) *Replayer {
	return &Replayer{
		registry: registry,
		run:      run,
		events:   events,
		log:      log,
	}
}

type StepResult struct {
	StepName   string
	Status     string
	DurationMs int64
	DiffBytes  int
	Error      string
}

func (r *Replayer) Replay(ctx context.Context, breakAt, forkFrom string) error {
	var wfName string
	var wfVersion string
	for _, evt := range r.events {
		if evt.EventType == workflow.EventRunStarted {
			var payload workflow.RunStartedPayload
			json.Unmarshal(evt.Payload, &payload)
			wfName = payload.WorkflowName
			wfVersion = payload.WorkflowVersion
			break
		}
	}

	if wfName == "" {
		return fmt.Errorf("no RunStarted event found")
	}

	registeredWf := r.registry.Get(wfName)
	if registeredWf == nil {
		return fmt.Errorf("workflow %q not registered — make sure your workflow definitions are compiled into this binary", wfName)
	}

	fmt.Printf("[replay] run %s (workflow: %s %s)\n", r.run.ID, wfName, wfVersion)
	fmt.Printf("[replay] fetched %d events\n\n", len(r.events))

	completedOutputs := make(map[string]json.RawMessage)
	completedInputs := make(map[string]json.RawMessage)
	for _, evt := range r.events {
		switch evt.EventType {
		case workflow.EventStepOutputRecorded:
			var payload workflow.StepOutputRecordedPayload
			json.Unmarshal(evt.Payload, &payload)
			completedOutputs[payload.StepName] = payload.Output
		case workflow.EventStepInputRecorded:
			var payload workflow.StepInputRecordedPayload
			json.Unmarshal(evt.Payload, &payload)
			completedInputs[payload.StepName] = payload.Input
		}
	}

	stepOrder := make([]string, 0)
	completed := make(map[string]bool)
	for _, evt := range r.events {
		if evt.EventType == workflow.EventStepCompleted {
			var payload workflow.StepCompletedPayload
			json.Unmarshal(evt.Payload, &payload)
			if !completed[payload.StepName] {
				stepOrder = append(stepOrder, payload.StepName)
				completed[payload.StepName] = true
			}
		}
		if evt.EventType == workflow.EventStepFailed {
			var payload workflow.StepFailedPayload
			json.Unmarshal(evt.Payload, &payload)
			if !completed[payload.StepName] {
				stepOrder = append(stepOrder, payload.StepName)
				completed[payload.StepName] = true
			}
		}
	}

	total := len(stepOrder)
	for i, stepName := range stepOrder {
		stepNum := i + 1

		if forkFrom != "" && !isPastForkPoint(stepName, forkFrom, registeredWf) {
			fmt.Printf("[replay] step %d/%d: %s  SKIP (using recorded output)\n", stepNum, total, stepName)
			continue
		}

		stepDef := registeredWf.GetStepDef(stepName)
		if stepDef == nil {
			fmt.Printf("[replay] step %d/%d: %s  MISSING (not in registered workflow)\n", stepNum, total, stepName)
			continue
		}

		recordedInput := completedInputs[stepName]
		if recordedInput == nil {
			recordedInput = r.run.Input
		}

		if breakAt == stepName {
			fmt.Printf("[replay] step %d/%d: %s  BREAK\n", stepNum, total, stepName)
			fmt.Printf("[replay] pid=%d — attach your debugger now, press enter to continue\n", os.Getpid())
			bufio.NewReader(os.Stdin).ReadString('\n')
			fmt.Println("[replay] continuing...")
		} else {
			fmt.Printf("[replay] step %d/%d: %s  ", stepNum, total, stepName)
		}

		startTime := time.Now()
		output, execErr := stepDef.Handler(ctx, recordedInput)
		duration := time.Since(startTime)

		if execErr != nil {
			fmt.Printf("FAIL (%s)\n", duration.Round(time.Millisecond))
			fmt.Printf("         error: %s\n", execErr.Error())
			continue
		}

		recordedOutput := completedOutputs[stepName]
		diffBytes := 0
		if recordedOutput != nil {
			diff, _ := DiffJSON(output, recordedOutput)
			if diff != nil {
				diffBytes = diff.DiffBytes
			}
		}

		status := "OK"
		if diffBytes > 0 {
			status = fmt.Sprintf("DIVERGED (diff: %d bytes)", diffBytes)
		}
		fmt.Printf("%s (%s, diff: %d bytes)\n", status, duration.Round(time.Millisecond), diffBytes)
	}

	fmt.Printf("\n[replay] complete\n")
	return nil
}

func isPastForkPoint(stepName, forkFrom string, wf *workflow.Workflow) bool {
	visited := make(map[string]bool)
	var visit func(name string) bool
	visit = func(name string) bool {
		if visited[name] {
			return false
		}
		visited[name] = true
		if name == forkFrom {
			return true
		}
		stepDef := wf.GetStepDef(name)
		if stepDef == nil {
			return false
		}
		for _, dep := range stepDef.Dependencies {
			if visit(dep) {
				return true
			}
		}
		return false
	}
	return visit(stepName)
}
