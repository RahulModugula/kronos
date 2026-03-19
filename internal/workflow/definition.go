package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// StepFunc is the signature for a workflow step handler.
// It receives the step input (from upstream outputs or workflow input) and returns the step output.
// Implementations should be idempotent — they may be called more than once on retry.
type StepFunc func(input json.RawMessage) (json.RawMessage, error)

// StepDef describes a single step in a workflow DAG.
type StepDef struct {
	Name         string
	Handler      StepFunc
	Dependencies []string // names of steps this depends on; empty = no dependencies
}

// Step is a public builder for defining workflow steps.
type Step struct {
	name         string
	handler      StepFunc
	dependencies []string
}

// Workflow describes a repeatable multi-step process.
// Workflows are versioned and immutable: changes create a new Workflow with a new fingerprint.
type Workflow struct {
	Name            string
	Version         string
	CodeFingerprint string
	Steps           []*StepDef
	DAGStructure    map[string][]string // step name -> list of names that depend on it

	// Internal: the raw definitions for hashing
	stepDefs []*StepDef
}

// NewWorkflow creates a new workflow builder.
func NewWorkflow(name, version string) *Workflow {
	return &Workflow{
		Name:         name,
		Version:      version,
		Steps:        []*StepDef{},
		DAGStructure: make(map[string][]string),
		stepDefs:     []*StepDef{},
	}
}

// AddStep adds a step to the workflow DAG.
// dependencies is a list of step names this step depends on (nil = no dependencies).
func (w *Workflow) AddStep(name string, handler StepFunc, dependencies []string) *Workflow {
	if handler == nil {
		panic(fmt.Sprintf("workflow: step %q handler is nil", name))
	}

	// Check for duplicate step names
	for _, s := range w.Steps {
		if s.Name == name {
			panic(fmt.Sprintf("workflow: step %q already exists", name))
		}
	}

	// Validate dependencies exist or will exist
	depSet := make(map[string]bool)
	for _, dep := range dependencies {
		depSet[dep] = true
	}

	def := &StepDef{
		Name:         name,
		Handler:      handler,
		Dependencies: dependencies,
	}
	w.Steps = append(w.Steps, def)
	w.stepDefs = append(w.stepDefs, def)

	// Update DAG structure: for each dependency, note that this step depends on it
	for _, dep := range dependencies {
		w.DAGStructure[dep] = append(w.DAGStructure[dep], name)
	}
	// Ensure the step itself is in the DAG
	if _, exists := w.DAGStructure[name]; !exists {
		w.DAGStructure[name] = []string{}
	}

	return w
}

// ComputeFingerprint computes a content hash of the workflow definition.
// This is used to detect changes in workflow logic and create new workflow versions.
// We hash: workflow name + version + step names + dependency structure.
// Handler function pointers are not hashed (we can't serialize them reliably).
func (w *Workflow) ComputeFingerprint() string {
	hasher := sha256.New()

	// Hash the metadata
	hasher.Write([]byte(w.Name))
	hasher.Write([]byte(w.Version))

	// Hash the DAG structure deterministically
	for _, step := range w.Steps {
		hasher.Write([]byte(step.Name))
		for _, dep := range step.Dependencies {
			hasher.Write([]byte(dep))
		}
	}

	return hex.EncodeToString(hasher.Sum(nil))
}

// Validate checks that the DAG is acyclic and all dependencies are satisfied.
func (w *Workflow) Validate() error {
	// Simple topological sort to detect cycles
	visited := make(map[string]bool)
	visiting := make(map[string]bool)

	var visit func(name string) error
	visit = func(name string) error {
		if visited[name] {
			return nil
		}
		if visiting[name] {
			return fmt.Errorf("workflow: cycle detected involving step %q", name)
		}

		visiting[name] = true
		def := w.getStepDef(name)
		if def == nil {
			return fmt.Errorf("workflow: step %q not found", name)
		}

		for _, dep := range def.Dependencies {
			if err := visit(dep); err != nil {
				return err
			}
		}

		visiting[name] = false
		visited[name] = true
		return nil
	}

	for _, step := range w.Steps {
		if err := visit(step.Name); err != nil {
			return err
		}
	}
	return nil
}

// GetStepDef returns the StepDef for a given step name, or nil if not found.
func (w *Workflow) GetStepDef(name string) *StepDef {
	for _, step := range w.Steps {
		if step.Name == name {
			return step
		}
	}
	return nil
}

func (w *Workflow) getStepDef(name string) *StepDef {
	return w.GetStepDef(name)
}

// GetReadySteps returns the names of steps that are ready to execute
// (all dependencies have completed).
// completed is a map of step name -> output (json.RawMessage).
func (w *Workflow) GetReadySteps(completed map[string]json.RawMessage) []string {
	var ready []string
	for _, step := range w.Steps {
		if _, isDone := completed[step.Name]; isDone {
			continue // already completed
		}
		// Check if all dependencies are done
		allDepsDone := true
		for _, dep := range step.Dependencies {
			if _, isDone := completed[dep]; !isDone {
				allDepsDone = false
				break
			}
		}
		if allDepsDone {
			ready = append(ready, step.Name)
		}
	}
	return ready
}

// WorkflowRun represents a single execution of a workflow.
type WorkflowRun struct {
	ID          string
	WorkflowID  string // FK to workflows table
	Status      string // pending, running, completed, failed, cancelled, forked
	Input       json.RawMessage
	Output      json.RawMessage
	Error       string
	ParentRunID *string // for forked runs
	StartedAt   *time.Time
	FinishedAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// StepExecution represents a single step execution within a run.
type StepExecution struct {
	ID         string
	RunID      string
	StepName   string
	Attempt    int
	Status     string
	Input      json.RawMessage
	Output     json.RawMessage
	Error      string
	StartedAt  *time.Time
	FinishedAt *time.Time
	DurationMs *int
}

// WorkflowEvent is an entry in the event log.
type WorkflowEvent struct {
	ID        string
	RunID     string
	Seq       int
	Ts        time.Time
	EventType string // run_started, step_started, step_completed, etc.
	Payload   json.RawMessage
}
