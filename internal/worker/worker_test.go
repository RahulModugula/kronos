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
	pool := worker.NewPool(4, reg, log, onComplete)

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
	pool := worker.NewPool(1, reg, log, onComplete)

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

// ensure time import used
var _ = time.Second
