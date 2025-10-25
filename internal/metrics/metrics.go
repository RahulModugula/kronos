package metrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all Kronos Prometheus instruments.
// Instantiated once at startup and injected where needed.
type Metrics struct {
	JobsSubmitted *prometheus.CounterVec
	JobsCompleted *prometheus.CounterVec
	JobDuration   *prometheus.HistogramVec
}

// New registers all metrics against the given registerer.
// Use a non-global registerer so tests can isolate their own registry.
func New(reg prometheus.Registerer) *Metrics {
	f := promauto.With(reg)
	return &Metrics{
		JobsSubmitted: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kronos_jobs_submitted_total",
			Help: "Total jobs submitted, by type.",
		}, []string{"type"}),

		JobsCompleted: f.NewCounterVec(prometheus.CounterOpts{
			Name: "kronos_jobs_completed_total",
			Help: "Total jobs reaching a terminal state, by type and status.",
		}, []string{"type", "status"}),

		JobDuration: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "kronos_job_duration_seconds",
			Help:    "Elapsed time from job claim to completion.",
			Buckets: prometheus.ExponentialBuckets(0.01, 2, 12), // 10ms → ~40s
		}, []string{"type", "status"}),
	}
}

// RegisterQueueDepthGauge registers a GaugeFunc that polls the DB on every
// Prometheus scrape. Using GaugeFunc (rather than a background goroutine)
// means the DB is only queried when someone actually scrapes /metrics.
func RegisterQueueDepthGauge(reg prometheus.Registerer, depthFn func(ctx context.Context) (int64, error)) {
	reg.MustRegister(prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "kronos_queue_depth",
			Help: "Number of pending jobs waiting to be claimed.",
		},
		func() float64 {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			n, err := depthFn(ctx)
			if err != nil {
				return -1
			}
			return float64(n)
		},
	))
}
