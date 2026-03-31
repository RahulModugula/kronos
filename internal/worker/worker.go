package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/rahulmodugula/kronos/internal/store"
)

// HandlerFunc is the function signature for job handlers.
// Implementations should be idempotent — they may be called more than once on retry.
type HandlerFunc func(ctx context.Context, payload json.RawMessage) error

// Registry maps job types to handlers.
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]HandlerFunc
}

func NewRegistry() *Registry {
	return &Registry{handlers: make(map[string]HandlerFunc)}
}

// Register adds a handler for a given job type.
// Panics if the type is already registered (catches wiring mistakes at startup).
func (r *Registry) Register(jobType string, fn HandlerFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[jobType]; exists {
		panic(fmt.Sprintf("kronos: handler already registered for type %q", jobType))
	}
	r.handlers[jobType] = fn
}

// Get returns the handler for a job type, or an error if not found.
func (r *Registry) Get(jobType string) (HandlerFunc, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fn, ok := r.handlers[jobType]
	if !ok {
		return nil, fmt.Errorf("no handler registered for job type %q", jobType)
	}
	return fn, nil
}

// Pool is a fixed-size pool of goroutines that execute jobs.
type Pool struct {
	concurrency int
	jobs        chan *store.Job
	registry    *Registry
	log         zerolog.Logger
	wg          sync.WaitGroup
	jobTimeout  time.Duration

	// onComplete is called after a job finishes (success or error).
	// It is responsible for updating the DB and scheduling retries.
	onComplete func(ctx context.Context, job *store.Job, err error)
}

// NewPool creates a worker pool. Call Start to launch goroutines.
func NewPool(concurrency int, registry *Registry, log zerolog.Logger, jobTimeout time.Duration, onComplete func(context.Context, *store.Job, error)) *Pool {
	return &Pool{
		concurrency: concurrency,
		jobs:        make(chan *store.Job, concurrency*2),
		registry:    registry,
		log:         log,
		jobTimeout:  jobTimeout,
		onComplete:  onComplete,
	}
}

// Start launches concurrency goroutines that read from the jobs channel.
// They stop when ctx is cancelled (after draining in-flight work).
func (p *Pool) Start(ctx context.Context) {
	for i := 0; i < p.concurrency; i++ {
		p.wg.Add(1)
		go func(id int) {
			defer p.wg.Done()
			log := p.log.With().Int("worker", id).Logger()
			for job := range p.jobs {
				p.execute(ctx, job, log)
			}
		}(i)
	}
}

// Submit sends a job to the pool. Blocks if the channel is full.
// Returns false if the pool's job channel is closed.
func (p *Pool) Submit(job *store.Job) {
	p.jobs <- job
}

// Stop closes the job channel and waits for all in-flight jobs to finish.
func (p *Pool) Stop() {
	close(p.jobs)
	p.wg.Wait()
}

func (p *Pool) execute(ctx context.Context, job *store.Job, log zerolog.Logger) {
	log = log.With().Str("job_id", job.ID.String()).Str("job_type", job.Type).Logger()
	log.Info().Msg("executing job")

	handler, err := p.registry.Get(job.Type)
	if err != nil {
		log.Error().Err(err).Msg("no handler for job type")
		p.onComplete(ctx, job, err)
		return
	}

	timeout := p.jobTimeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	jobCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	execErr := handler(jobCtx, json.RawMessage(job.Payload))
	if execErr != nil {
		log.Error().Err(execErr).Msg("job failed")
	} else {
		log.Info().Msg("job completed")
	}
	p.onComplete(ctx, job, execErr)
}
