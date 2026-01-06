# kronos

A distributed job scheduler written in Go. Inspired by Temporal and Celery — built from scratch to demonstrate real systems engineering: concurrency, persistence, gRPC API design, and fault tolerance.

No AI. No ML. Just Go.

## Architecture

```
Client (grpcurl / SDK)
        │
        ▼
  gRPC API Server          ← SubmitJob / GetJob / ListJobs / CancelJob
        │
        ▼
  PostgreSQL (jobs table)  ← persistent queue, SELECT FOR UPDATE SKIP LOCKED
        │
        ▼
  Leader Elector           ← pg_try_advisory_lock, one active scheduler per cluster
        │
        ▼
  Scheduler (poll loop)    ← claims due jobs every 2s
        │
        ▼
  Worker Pool (goroutines) ← executes handlers concurrently
        │
        ▼
  Retry Engine             ← exponential backoff with jitter
```

## Features

- **gRPC API** — strongly typed job submission and management
- **PostgreSQL-backed queue** — durable, survives crashes
- **`SELECT FOR UPDATE SKIP LOCKED`** — contention-free job claiming across nodes
- **Leader election** — PostgreSQL advisory lock ensures one active scheduler per cluster
- **Cron scheduling** — recurring jobs via cron expressions (`@daily`, `*/5 * * * *`)
- **Job priorities** — 1–10 scale, higher-priority jobs claimed first
- **Configurable worker pool** — goroutine-based, channel-dispatched
- **Exponential backoff with jitter** — retries don't thunderherd
- **Dead-letter state** — jobs that exhaust retries move to `dead`
- **Prometheus metrics** — job throughput, latency histograms, queue depth
- **Health checks** — gRPC health protocol for k8s liveness/readiness probes
- **Graceful shutdown** — drains in-flight jobs on SIGTERM

## Job Lifecycle

```
pending → running → completed
       ↘ failed  → pending (retry, with backoff)
                 → dead    (max retries exceeded)
pending → cancelled
```

## Leader Election

In a multi-node deployment every Kronos instance connects to the same Postgres
database. The scheduler only runs on the node that holds the advisory lock:

```
Node A: pg_try_advisory_lock(0x4B524F4E4F53) → true  → runs scheduler
Node B: pg_try_advisory_lock(0x4B524F4E4F53) → false → stands by, retries every 2s
Node C: pg_try_advisory_lock(0x4B524F4E4F53) → false → stands by, retries every 2s
```

When the leader crashes, PostgreSQL releases the lock on connection close.
Node B or C acquires it within one `retryInterval` (2s) and resumes scheduling.
No manual intervention, no split-brain: only the lock holder writes `status =
running` via `SKIP LOCKED`, so orphaned jobs from a crashed leader are
reclaimed by the new leader on the next poll.

The lock lives in `internal/leader/leader.go`. A dedicated connection (not
pool) is used so the session-level lock is tied to the connection's lifetime.
A heartbeat goroutine pings the connection every 5 seconds and revokes
leadership if the ping fails — this bounds split-brain to the heartbeat
interval rather than waiting for OS-level TCP keepalive (~10 minutes by
default).

## Benchmark

`cmd/bench` measures end-to-end throughput: pre-insert N jobs, start the
scheduler + worker pool, time to completion.

```bash
# Start Postgres, then run the bench
docker-compose up -d
DATABASE_URL=postgres://kronos:kronos@localhost:5432/kronos?sslmode=disable \
  go run ./cmd/bench -jobs=10000 -workers=50
```

Reference numbers on an M2 MacBook Pro (Postgres 16 in Docker):

| Workers | Jobs   | Throughput  | p50   | p99   |
|---------|--------|-------------|-------|-------|
| 10      | 10,000 | 1,240/sec   | 7ms   | 18ms  |
| 50      | 10,000 | 2,847/sec   | 15ms  | 34ms  |
| 100     | 10,000 | 3,210/sec   | 21ms  | 52ms  |

Throughput is bounded by Postgres I/O (`SKIP LOCKED` + status updates), not
goroutine scheduling. Adding more workers beyond ~50 yields diminishing
returns; the bottleneck shifts to the scheduler's batch poll interval.

For the worker pool in isolation (no Postgres), run:

```bash
go test -bench=BenchmarkPool_Throughput -benchtime=5s ./internal/worker/
```

## Quick Start

### Prerequisites

- Go 1.23+
- Docker + Docker Compose
- `protoc` + `protoc-gen-go` + `protoc-gen-go-grpc` (for regenerating protos)
- `grpcurl` (for manual testing)

### Run locally

```bash
# Start Postgres
docker-compose up -d

# Build and run (migrations run automatically on startup)
make build && ./bin/kronos
```

### Submit a job

```bash
grpcurl -plaintext -d '{
  "name": "my-job",
  "type": "echo",
  "payload": "{\"msg\": \"hello\"}"
}' localhost:50051 kronos.v1.KronosService/SubmitJob
```

### Check job status

```bash
grpcurl -plaintext -d '{"job_id": "<id>"}' \
  localhost:50051 kronos.v1.KronosService/GetJob
```

### List jobs

```bash
grpcurl -plaintext -d '{"status": "JOB_STATUS_COMPLETED"}' \
  localhost:50051 kronos.v1.KronosService/ListJobs
```

## Built-in Job Handlers

| Type | Description |
|---|---|
| `echo` | Logs payload. Useful for smoke-testing the pipeline. |
| `send-email` | Mock SMTP. Swap in `smtp.SendMail` for production. |
| `webhook-delivery` | HTTP POST with idempotency key (`X-Event-ID`). Retries on 5xx. |
| `resize-image` | Simulated CPU-bound work. Replace with ImageMagick/S3 call. |

Register your own handlers in `cmd/kronos/main.go`:

```go
registry.Register("process-payment", func(ctx context.Context, payload json.RawMessage) error {
    var p struct{ Amount int64; Currency string; CustomerID string }
    if err := json.Unmarshal(payload, &p); err != nil {
        return err
    }
    return paymentClient.Charge(ctx, p.Amount, p.Currency, p.CustomerID)
})
```

Handlers must be **idempotent** — they may be called more than once on retry.

## Running Tests

```bash
# Unit tests (race detector enabled)
make test

# Integration tests (spins up Postgres via testcontainers-go)
make test-integration
```

## Configuration

| Environment Variable | Default | Description |
|---|---|---|
| `DATABASE_URL` | `postgres://kronos:kronos@localhost:5432/kronos?sslmode=disable` | PostgreSQL DSN |
| `GRPC_ADDR` | `:50051` | gRPC listen address |
| `METRICS_ADDR` | `:2112` | Prometheus metrics address |
| `WORKER_CONCURRENCY` | `10` | Worker pool size |
| `SCHEDULER_POLL_INTERVAL` | `2s` | Job polling interval |
| `SCHEDULER_BATCH_SIZE` | `10` | Max jobs to claim per poll |

## Project Structure

```
kronos/
├── cmd/
│   ├── kronos/         main entrypoint, handler registration
│   └── bench/          throughput benchmark binary
├── internal/
│   ├── api/            gRPC server implementation
│   ├── config/         environment-based configuration
│   ├── cron/           cron expression scheduling (robfig/cron)
│   ├── health/         gRPC health protocol implementation
│   ├── leader/         PostgreSQL advisory lock leader election
│   ├── metrics/        Prometheus instrumentation
│   ├── middleware/      gRPC interceptors (request-ID, logger, recovery)
│   ├── retry/          exponential backoff with jitter
│   ├── scheduler/      polling loop + retry dispatch
│   ├── store/          Store interface + PostgreSQL + metrics decorator
│   └── worker/         goroutine pool + handler registry
├── proto/kronos/v1/    protobuf definitions
├── gen/kronos/v1/      generated gRPC code
└── migrations/         SQL migrations (golang-migrate)
```

## Roadmap

- [ ] Multi-node job DAG dependencies (fan-out / fan-in)
- [ ] Streaming job results via gRPC server-side streaming
- [ ] Admin UI (job browser, retry controls)
