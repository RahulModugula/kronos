package worker

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/rahulmodugula/kronos/internal/store"
)

// BenchmarkPool_Throughput measures how many jobs per second the worker pool
// can execute as a function of concurrency. Run with:
//
//	go test -bench=BenchmarkPool_Throughput -benchtime=5s ./internal/worker/
//
// The handler is a no-op to isolate pool dispatch overhead from handler cost.
// In a real deployment add Postgres I/O to model end-to-end throughput.
func BenchmarkPool_Throughput(b *testing.B) {
	for _, concurrency := range []int{10, 50, 100} {
		b.Run(concurrencyLabel(concurrency), func(b *testing.B) {
			benchPool(b, concurrency)
		})
	}
}

func benchPool(b *testing.B, concurrency int) {
	b.Helper()

	var completed atomic.Int64
	reg := NewRegistry()
	reg.Register("noop", func(_ context.Context, _ json.RawMessage) error {
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool := NewPool(concurrency, reg, zerolog.Nop(), func(_ context.Context, _ *store.Job, _ error) {
		completed.Add(1)
	})
	pool.Start(ctx)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		pool.Submit(&store.Job{
			ID:      uuid.New(),
			Type:    "noop",
			Payload: []byte(`{}`),
		})
	}

	// Drain: close and wait for all in-flight jobs to finish.
	pool.Stop()

	b.ReportMetric(float64(completed.Load()), "jobs")
	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "jobs/sec")
}

func concurrencyLabel(n int) string {
	switch n {
	case 10:
		return "workers=10"
	case 50:
		return "workers=50"
	case 100:
		return "workers=100"
	default:
		return "workers=N"
	}
}
