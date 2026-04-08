package workflow

import (
	"fmt"
	"sync"
)

// Registry maps workflow name to a list of versioned definitions.
type Registry struct {
	mu        sync.RWMutex
	workflows map[string]*Workflow // name -> latest definition
	all       map[string]*Workflow // name+fingerprint -> definition
}

// NewRegistry creates a new workflow registry.
func NewRegistry() *Registry {
	return &Registry{
		workflows: make(map[string]*Workflow),
		all:       make(map[string]*Workflow),
	}
}

// Register registers a workflow definition.
// If a workflow with the same name and fingerprint is already registered, it's a no-op.
// Panics if the workflow is invalid.
func (r *Registry) Register(w *Workflow) error {
	if w.Name == "" {
		panic("workflow: name is required")
	}
	if w.Version == "" {
		panic("workflow: version is required")
	}

	// Validate the workflow
	if err := w.Validate(); err != nil {
		panic(fmt.Sprintf("workflow: invalid: %v", err))
	}

	// Compute fingerprint
	fingerprint := w.ComputeFingerprint()

	r.mu.Lock()
	defer r.mu.Unlock()

	key := w.Name + ":" + fingerprint
	if _, exists := r.all[key]; exists {
		// Already registered
		return nil
	}

	// Store both by (name, fingerprint) and by name (for latest lookup)
	r.all[key] = w
	r.workflows[w.Name] = w

	return nil
}

// Get retrieves the latest registered workflow by name.
// Returns nil if not found.
func (r *Registry) Get(name string) *Workflow {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.workflows[name]
}

// GetByFingerprint retrieves a workflow by name and fingerprint.
// Returns nil if not found.
func (r *Registry) GetByFingerprint(name, fingerprint string) *Workflow {
	r.mu.RLock()
	defer r.mu.RUnlock()
	key := name + ":" + fingerprint
	return r.all[key]
}

// ListAll returns all registered workflows.
func (r *Registry) ListAll() []*Workflow {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var wfs []*Workflow
	for _, w := range r.workflows {
		wfs = append(wfs, w)
	}
	return wfs
}
