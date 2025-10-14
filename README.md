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
- **`SELECT FOR UPDATE SKIP LOCKED`** — contention-free job claiming
- **Configurable worker pool** — goroutine-based, channel-dispatched
- **Exponential backoff with jitter** — retries don't thunderherd
- **Dead-letter state** — jobs that exhaust retries move to `dead`
- **Graceful shutdown** — drains in-flight jobs on SIGTERM

## Job Lifecycle

```
pending → running → completed
                 ↘ failed → pending (retry, with backoff)
                          → dead   (max retries exceeded)
pending → cancelled
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

## Configuration

| Environment Variable | Default | Description |
|---|---|---|
| `DATABASE_URL` | `postgres://kronos:kronos@localhost:5432/kronos?sslmode=disable` | PostgreSQL DSN |
| `GRPC_ADDR` | `:50051` | gRPC listen address |

## Adding Job Handlers

Register handlers at startup in `cmd/kronos/main.go`:

```go
registry.Register("send-email", func(ctx context.Context, payload json.RawMessage) error {
    var p struct{ To, Subject, Body string }
    if err := json.Unmarshal(payload, &p); err != nil {
        return err
    }
    return sendEmail(ctx, p.To, p.Subject, p.Body)
})
```

Handlers must be **idempotent** — they may be called more than once on retry.

## Running Tests

```bash
make test
```

Unit tests cover the retry backoff logic and worker pool dispatch. Integration tests (coming in v2) will use `testcontainers-go` to spin up a real Postgres instance.

## Project Structure

```
kronos/
├── cmd/kronos/         main entrypoint, handler registration
├── internal/
│   ├── api/            gRPC server implementation
│   ├── scheduler/      polling loop + retry dispatch
│   ├── worker/         goroutine pool + handler registry
│   ├── store/          Store interface + PostgreSQL implementation
│   └── retry/          exponential backoff
├── proto/kronos/v1/    protobuf definitions
├── gen/kronos/v1/      generated gRPC code
└── migrations/         SQL migrations (golang-migrate)
```

## Roadmap

- [ ] Integration tests with testcontainers-go
- [ ] Cron scheduling (submit recurring jobs)
- [ ] Multi-node leader election via etcd
- [ ] Prometheus metrics endpoint
- [ ] Job DAG dependencies
