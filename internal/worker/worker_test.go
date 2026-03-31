package worker_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/rahulmodugula/kronos/internal/store"
	"github.com/rahulmodugula/kronos/internal/worker"
)

func TestPool_ExecutesJobs(t *testing.T) {
	var executed atomic.Int64

	reg := worker.NewRegistry()
	reg.Register("test", func(ctx context.Context, payload json.RawMessage) error {
		executed.Add(1)
		return nil
	})

	var completed atomic.Int64
	onComplete := func(ctx context.Context, j *store.Job, err error) {
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		completed.Add(1)
	}

	log := zerolog.Nop()
	pool := worker.NewPool(4, reg, log, 0, onComplete)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx)

	const n = 20
	for i := 0; i < n; i++ {
		pool.Submit(&store.Job{
			ID:   uuid.New(),
			Type: "test",
		})
	}

	pool.Stop()

	if executed.Load() != n {
		t.Errorf("expected %d executions, got %d", n, executed.Load())
	}
	if completed.Load() != n {
		t.Errorf("expected %d completions, got %d", n, completed.Load())
	}
}

func TestPool_UnknownHandler(t *testing.T) {
	reg := worker.NewRegistry()

	var gotErr atomic.Bool
	onComplete := func(ctx context.Context, j *store.Job, err error) {
		if err != nil {
			gotErr.Store(true)
		}
	}

	log := zerolog.Nop()
	pool := worker.NewPool(1, reg, log, 0, onComplete)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx)

	pool.Submit(&store.Job{ID: uuid.New(), Type: "unknown"})

	pool.Stop()

	if !gotErr.Load() {
		t.Error("expected error for unknown job type")
	}
}

func TestRegistry_PanicsOnDuplicate(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on duplicate registration")
		}
	}()

	reg := worker.NewRegistry()
	fn := func(ctx context.Context, payload json.RawMessage) error { return nil }
	reg.Register("dup", fn)
	reg.Register("dup", fn) // should panic
}

// TestPool_StopDrainsInFlightJobs verifies that Stop() waits for all
// in-flight jobs to complete before returning, even when handlers are slow.
// This is critical for graceful shutdown — we must not return from Stop()
// while a handler is mid-execution and writing to the DB.
func TestPool_StopDrainsInFlightJobs(t *testing.T) {
	const n = 10
	const handlerDelay = 20 * time.Millisecond

	var completed atomic.Int64
	start := make(chan struct{})

	reg := worker.NewRegistry()
	reg.Register("slow", func(ctx context.Context, _ json.RawMessage) error {
		<-start // wait for all jobs to be submitted before processing
		time.Sleep(handlerDelay)
		return nil
	})

	pool := worker.NewPool(n, reg, zerolog.Nop(), 0, func(_ context.Context, _ *store.Job, _ error) {
		completed.Add(1)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx)

	for i := 0; i < n; i++ {
		pool.Submit(&store.Job{ID: uuid.New(), Type: "slow"})
	}
	close(start) // release all handlers simultaneously
	pool.Stop()  // must block until all n completions

	if got := completed.Load(); got != n {
		t.Errorf("Stop() returned before all jobs completed: got %d/%d", got, n)
	}
}

// TestPool_ContextCancellationPropagatedToHandler verifies that when the pool
// context is cancelled, handlers receive a cancelled context on the next
// execute cycle. Handlers that respect ctx.Done() can exit early on shutdown.
func TestPool_ContextCancellationPropagatedToHandler(t *testing.T) {
	ctxSeen := make(chan context.Context, 1)

	reg := worker.NewRegistry()
	reg.Register("ctx-check", func(ctx context.Context, _ json.RawMessage) error {
		ctxSeen <- ctx
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	pool := worker.NewPool(1, reg, zerolog.Nop(), 0, func(_ context.Context, _ *store.Job, _ error) {})
	pool.Start(ctx)

	cancel() // cancel before submitting so handler receives a cancelled ctx
	pool.Submit(&store.Job{ID: uuid.New(), Type: "ctx-check"})
	pool.Stop()

	select {
	case handlerCtx := <-ctxSeen:
		if handlerCtx.Err() == nil {
			t.Error("handler received a non-cancelled context after pool ctx was cancelled")
		}
	case <-time.After(2 * time.Second):
		t.Error("handler was never called")
	}
}

// ensure time import used
var _ = time.Second
