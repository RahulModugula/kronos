# kronos

**Durable workflows you can debug like regular programs.**

Kronos is a Postgres-native durable workflow engine with a time-travel debugger. Record every step of every run, scrub the timeline in a visual UI, replay any production run locally with breakpoints in your IDE, and fork failed runs from any midpoint to test a fix. All from a single Go binary.

Built for reliability at scale: 2,800+ jobs/sec on Postgres, multi-node clustering via advisory locks, graceful shutdown, and comprehensive observability.

## What makes Kronos different

**Time-travel debugging.** When a workflow fails in production, don't redeploy with print statements. Open the debugger, scrub through the timeline of the failed run, see exact inputs/outputs at each step, replay it locally in your IDE with breakpoints, and test fixes without restarting from the beginning.

**Deterministic replay.** Run `kronos replay <run-id>` to re-execute any production workflow locally. The replay streams recorded step outputs from Postgres and re-invokes your handlers against them. Attach Delve or your IDE debugger; every step is just a Go function call. Test fixes before deploying.

**Fork from any midpoint.** Failed at step 5 of 10? Fork the run from step 5 with a fixed handler, reuse the recorded outputs of steps 1–4, and skip re-executing expensive earlier work.

**Workflow versioning.** Every run pins its workflow definition. Deploy a fix without breaking in-flight runs. Fork failed runs under the new version to test the fix.

This is what backend developers actually need for reliable asynchronous work.

## Architecture

```
       SubmitWorkflow RPC
              │
              ▼
   ┌──────────────────────┐
   │  workflow_runs row   │      RunStarted event
   │   (status=running)   │              │
   └──────────┬───────────┘              ▼
              │              ┌──────────────────────┐
              │              │  workflow_events     │
              │              │   (append-only log)  │
              ▼              └──────────────────────┘
   For each ready step:
              │
              ▼
   ┌──────────────────────┐
   │   jobs row inserted  │  ◄── existing scheduler/worker
   │ type=kronos.workflow │      infrastructure handles this
   │       _step          │      (zero code changes)
   └──────────┬───────────┘
              ▼
   workflow_step handler:
     • load workflow definition (pinned version)
     • assemble step input from upstream outputs
     • invoke user's StepFunc
     • write events transactionally
     • schedule downstream steps
```

The workflow engine **layers on top of the existing job system** — no changes to the scheduler, worker pool, retries, dead letter, or cron. Workflows execute as ordinary jobs through the production-proven pipeline.

## Core Features

### Durable Workflows
- **DAG-based execution** — steps form a directed acyclic graph with parallel branches and fan-in
- **Per-step checkpointing** — step outputs are durably recorded; retries resume from the last completed step
- **Event sourcing** — every run is an append-only log of events (RunStarted, StepStarted, StepInputRecorded, StepOutputRecorded, StepCompleted, etc.)
- **Workflow versioning** — in-flight runs pin their definition; new runs use the latest version; no compatibility checks needed
- **Blob deduplication** — large step inputs/outputs are content-addressed and shared across forks

### Time-Travel Debugger
- **Web UI** — browse runs, filter by workflow/status, timeline scrubber to rewind any execution
- **Step inspection** — view exact inputs, outputs, errors, duration, and retry attempts
- **Live streaming** — SSE updates for in-progress runs
- **Fork interface** — one click to fork a failed run from any step

### Local Replay & IDE Debugging
- `kronos replay <run-id>` — re-executes a production run locally using recorded outputs
- `kronos replay <run-id> --break-at step_name` — pause at a step; attach Delve / VS Code / GoLand
- IDE debugger support — no custom protocol; just plain Go functions
- `kronos replay <run-id> --fork-from step_name` — test a fix without re-executing upstream steps

### Production Infrastructure
- **PostgreSQL persistence** — single source of truth, survives crashes
- **Leader election** — advisory locks ensure one active scheduler per cluster
- **Multi-node clustering** — stateless API nodes, one scheduler; scale horizontally
- **Prometheus metrics** — workflow throughput, step latency, queue depth
- **gRPC health checks** — ready for Kubernetes liveness/readiness probes
- **Graceful shutdown** — drains in-flight steps on SIGTERM

### Built-in Job Queue (legacy, still useful)
For simple one-off background work, Kronos also functions as a traditional job queue:
- **Job priorities** — 1–10 scale
- **Cron scheduling** — recurring jobs
- **Exponential backoff with jitter** — intelligent retries
- **Dead-letter handling** — failed jobs preserved for manual inspection
- **Idempotency keys** — deduplication across retries

Workflows are the recommended approach for orchestrated work; the job queue remains for simple fire-and-forget tasks.

## Workflow Lifecycle

```
pending → running → completed
       ↘ failed  → pending (retry, with backoff)
                 → dead    (max retries exceeded)
pending → cancelled
pending → forked (parent_run_id set, ready to re-execute from fork point)
```

Each workflow run generates an append-only **event log**: `RunStarted`, `StepScheduled`, `StepStarted`, `StepInputRecorded`, `StepOutputRecorded`, `StepCompleted`, `RunCompleted`, `RunFailed`, `RunForked`. The debugger UI reconstructs any point in time by replaying these events. This is how time-travel works.

## Workflow Definition

Define workflows in Go using the builder API:

```go
wf := workflow.NewWorkflow("data-pipeline", "v1").
    AddStep("fetch-user", fetchUserData, nil). // no dependencies
    AddStep("enrich", enrichProfile, []string{"fetch-user"}). // depends on fetch-user
    AddStep("validate", validate, []string{"enrich"}). // depends on enrich
    AddStep("notify-1", sendEmail, []string{"enrich"}). // parallel with validate
    AddStep("notify-2", slackMessage, []string{"enrich"}). // parallel
    AddStep("finish", summarize, []string{"validate", "notify-1", "notify-2"}) // fan-in

registry.Register(wf)

// Submit a run:
run, err := client.StartWorkflow(ctx, &pb.StartWorkflowRequest{
    WorkflowName: "data-pipeline",
    Input: []byte(`{"user_id": "123"}`),
})
```

Steps are Go functions with a standard signature:

```go
func enrichProfile(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
    var data struct{ UserID string }
    json.Unmarshal(input, &data)
    // ... do work ...
    return json.Marshal(result)
}
```

## Leader Election (Multi-Node)

In a multi-node deployment every Kronos instance connects to the same Postgres
database. The scheduler only runs on the node that holds the advisory lock:

```
Node A: pg_try_advisory_lock(0x4B524F4E4F53) → true  → runs scheduler
Node B: pg_try_advisory_lock(0x4B524F4E4F53) → false → stands by, retries every 2s
Node C: pg_try_advisory_lock(0x4B524F4E4F53) → false → stands by, retries every 2s
```

When the leader crashes, PostgreSQL releases the lock. Node B or C acquires it
within 2s and resumes scheduling. No manual intervention, no split-brain.

A dedicated connection (not from the pool) ensures the session-level lock ties
to the connection's lifetime. A heartbeat goroutine pings every 5 seconds and
revokes leadership on ping failure — bounding split-brain to the heartbeat
interval.

## Performance

**Worker pool throughput (CPU-bound, no Postgres):**
- 10 workers: 1,256,838 jobs/sec (795ns/op)
- 50 workers: 1,135,330 jobs/sec (880ns/op)
- 100 workers: 1,117,641 jobs/sec (894ns/op)

**End-to-end throughput (with Postgres 17, 50 workers):**
- ~2,800 workflow steps/sec or jobs/sec
- Bottleneck: Postgres I/O (`SELECT FOR UPDATE SKIP LOCKED` + event log writes)
- Scaling: add more workers or nodes; the scheduler's 2s poll interval becomes the ceiling

**Benchmark locally:**
```bash
docker-compose up -d
make bench BENCH_FLAGS="-jobs=10000 -workers=50"
```

For long-running workflows (minutes to hours), the overhead is negligible — Postgres keeps
the run alive even if the node crashes, and replay resumes from the last completed step.

## Quick Start

### Prerequisites

- Go 1.23+
- Docker + Docker Compose
- (optional) `protoc` for regenerating protos; `grpcurl` for manual testing

### Run Kronos locally

```bash
# Start Postgres
docker-compose up -d

# Build and run (migrations run automatically)
make build && ./bin/kronos
```

The server listens on:
- **gRPC API:** `:50051` (start workflows, get runs, list runs)
- **Admin HTTP:** `:8080` (debugger UI, dead-letter queue)
- **Metrics:** `:2112` (Prometheus)

### Submit your first workflow

Visit the debugger UI at `http://localhost:8080/debugger` or use gRPC:

```bash
grpcurl -plaintext -d '{
  "workflow_name": "example-workflow",
  "input": "{\"message\": \"hello\"}"
}' localhost:50051 kronos.v1.KronosService/StartWorkflow
```

(Workflow registration happens in code; see "Define a Workflow" below.)

### Define a workflow

In your own Go module, import kronos and define workflows:

```go
package main

import (
    "context"
    "encoding/json"
    
    "github.com/rahulmodugula/kronos/internal/workflow"
)

func step1(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
    return json.Marshal(map[string]string{"status": "ok"})
}

func step2(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
    // input is the output from step1
    return json.Marshal(map[string]string{"result": "done"})
}

func registerWorkflows(reg *workflow.Registry) {
    wf := workflow.NewWorkflow("my-pipeline", "v1").
        AddStep("step1", step1, nil).
        AddStep("step2", step2, []string{"step1"})
    
    reg.Register(wf)
}
```

**Important:** Every step function must be deterministic (same input → same output in replay). Steps may be called more than once on retry, so implement idempotency at the application level (e.g., use an idempotency key for external API calls).

### Inspect workflow runs in the debugger

Open `http://localhost:8080/debugger`:
- **Runs list** — filter by workflow, status, time range
- **Run detail** — timeline scrubber, step inspection, live SSE updates
- **Fork** — click "fork from here" on any step to test a fix

### Replay a run locally with breakpoints

```bash
# List recent runs
grpcurl -plaintext -d '{"workflow_name": "my-pipeline"}' \
  localhost:50051 kronos.v1.KronosService/ListRuns

# Replay a specific run
kronos replay <run-id>

# Pause at a step and attach your debugger
kronos replay <run-id> --break-at step2
# [replay] pid=48291 — attach your debugger now, press enter to continue
# (attach Delve: dlv attach 48291)

# Fork from a midpoint and re-execute
kronos replay <run-id> --fork-from step2
```

## Legacy: Job Queue API

For simple one-off background work (no orchestration), Kronos also supports a traditional job queue:

```bash
grpcurl -plaintext -d '{
  "name": "my-job",
  "type": "echo",
  "payload": "{\"msg\": \"hello\"}"
}' localhost:50051 kronos.v1.KronosService/SubmitJob
```

Built-in handlers: `echo`, `webhook-delivery`. Register custom handlers in `cmd/kronos/main.go`.

**Handlers must be idempotent** — they may be called more than once on retry.

## Configuration

| Environment Variable | Default | Description |
|---|---|---|
| `DATABASE_URL` | `postgres://kronos:kronos@localhost:5432/kronos?sslmode=disable` | PostgreSQL DSN |
| `GRPC_ADDR` | `:50051` | gRPC API listen address |
| `ADMIN_ADDR` | `:8080` | HTTP admin server (debugger, dead-letter) |
| `METRICS_ADDR` | `:2112` | Prometheus metrics address |
| `WORKER_CONCURRENCY` | `10` | Worker pool size |
| `SCHEDULER_POLL_INTERVAL` | `2s` | Job polling interval |
| `SCHEDULER_BATCH_SIZE` | `10` | Max jobs to claim per poll |
| `JOB_TIMEOUT` | `30m` | Maximum execution time per step |

## Running Tests

```bash
# Unit tests (race detector enabled)
make test

# Integration tests (spins up Postgres via testcontainers-go)
make test-integration
```

## Project Structure

```
kronos/
├── cmd/
│   ├── kronos/              main entrypoint + replay subcommand
│   └── bench/               throughput benchmark binary
├── internal/
│   ├── api/                 gRPC server (jobs + workflows)
│   ├── config/              environment-based configuration
│   ├── cron/                cron scheduling (robfig/cron)
│   ├── debugger/            web UI for time-travel debugging (HTMX + Alpine)
│   ├── health/              gRPC health protocol
│   ├── leader/              PostgreSQL advisory lock leader election
│   ├── metrics/             Prometheus instrumentation
│   ├── middleware/          gRPC interceptors
│   ├── replay/              local replay client + replayer engine
│   ├── retry/               exponential backoff
│   ├── scheduler/           job polling loop
│   ├── store/               job persistence (Store interface + Postgres)
│   ├── worker/              goroutine pool + handler registry
│   └── workflow/            workflow engine, event log, step dispatcher
├── proto/kronos/v1/         protobuf definitions
├── gen/kronos/v1/           generated gRPC code
└── migrations/              SQL migrations (golang-migrate + workflow tables)
```

## Roadmap

**v1 (current):**
- [x] Durable workflow engine with DAG support
- [x] Time-travel debugger UI
- [x] Local replay and IDE debugging
- [ ] Workflow version diffing
- [ ] Typed step I/O via Go generics

**v2:**
- [ ] Optional AI step primitives (LLM calls, token counting, cost tracking)
- [ ] Python / TypeScript client SDKs
- [ ] Per-step cost tracking and budgets
- [ ] Nested fan-out (map steps)
- [ ] Human-in-the-loop approval steps
