# kronos

**Durable workflows you can debug like regular programs.**

[![Go Report Card](https://goreportcard.com/badge/github.com/rahulmodugula/kronos)](https://goreportcard.com/report/github.com/rahulmodugula/kronos)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Reference](https://pkg.go.dev/badge/github.com/rahulmodugula/kronos.svg)](https://pkg.go.dev/github.com/rahulmodugula/kronos)
[![CI](https://github.com/rahulmodugula/kronos/actions/workflows/ci.yml/badge.svg)](https://github.com/rahulmodugula/kronos/actions/workflows/ci.yml)

> **Time-travel debugger for durable workflows.** Record every step, scrub the timeline, replay in your IDE with breakpoints, fork failed runs from any midpoint.

Kronos is a Postgres-native durable workflow engine with a time-travel debugger. Single Go binary, zero external dependencies beyond Postgres. 2,800+ workflow steps/sec.

## Why Kronos?

Debugging failed workflows in production is painful. You get stack traces and logs, but you can't actually **step through what happened**. Kronos fixes this:

- **Time-travel debugger** — scrub through any workflow run's timeline, inspect exact inputs/outputs at each step
- **Local replay with breakpoints** — `kronos replay <run-id>` re-executes production runs locally; attach Delve or your IDE debugger
- **Fork from any midpoint** — failed at step 5 of 10? Fork from step 5 with a fixed handler, skip re-executing steps 1–4
- **Single binary** — no cluster, no Elasticsearch, no separate services. Just Postgres.

## Comparison

| | Kronos | Temporal | Hatchet | River |
|---|---|---|---|---|
| Time-travel debugger | ✅ | ❌ | ❌ | ❌ |
| Local IDE replay | ✅ | ❌ | ❌ | ❌ |
| Fork from midpoint | ✅ | ❌ | ❌ | ❌ |
| Single binary | ✅ | ❌ | ❌ | ✅ |
| Postgres-native | ✅ | ✅* | ✅ | ✅ |
| Workflow versioning | ✅ | ✅ | ❌ | ❌ |
| Zero external deps | ✅ | ❌ | ❌ | ✅ |

\* Temporal supports Postgres but requires a separate Temporal server cluster + Elasticsearch for production visibility.

## Quick Start

```bash
# Start Postgres
docker-compose up -d

# Build and run
make build && ./bin/kronos
```

Open the debugger UI at **http://localhost:8080/debugger/**

```bash
# Submit a workflow
grpcurl -plaintext -d '{
  "workflow_name": "example-workflow",
  "input": "{\"message\": \"hello\"}"
}' localhost:50051 kronos.v1.KronosService/StartWorkflow
```

## Define a Workflow

```go
wf := workflow.NewWorkflow("data-pipeline", "v1").
    AddStep("fetch-user", fetchUserData, nil).
    AddStep("enrich", enrichProfile, []string{"fetch-user"}).
    AddStep("validate", validate, []string{"enrich"}).
    AddStep("notify-1", sendEmail, []string{"enrich"}).
    AddStep("notify-2", slackMessage, []string{"enrich"}).
    AddStep("finish", summarize, []string{"validate", "notify-1", "notify-2"})

registry.Register(wf)
```

Step functions accept a context and JSON input:

```go
func enrichProfile(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
    var data struct{ UserID string }
    json.Unmarshal(input, &data)
    // ... do work ...
    return json.Marshal(result)
}
```

## Replay & Debug

```bash
# Replay a production run locally
kronos replay <run-id>

# Pause at a step — attach Delve or VS Code
kronos replay <run-id> --break-at enrich

# Fork from a step — test your fix without re-running upstream
kronos replay <run-id> --fork-from enrich
```

Output:
```
[replay] run abc-123 (workflow: data-pipeline v1)
[replay] fetched 24 events

[replay] step 1/6: fetch-user  OK (12ms, diff: 0 bytes)
[replay] step 2/6: enrich      BREAK
[replay] pid=48291 — attach your debugger now, press enter to continue
```

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
- **Event sourcing** — every run is an append-only log of events
- **Workflow versioning** — in-flight runs pin their definition; new runs use the latest version
- **Blob deduplication** — large step inputs/outputs are content-addressed and shared across forks

### Time-Travel Debugger UI
- **Runs list** — filter by workflow/status, card layout with duration and status badges
- **Run detail** — 3-pane layout: step tree, timeline scrubber, step inspector
- **Timeline scrubber** — click/drag to rewind through events, see step state at any point
- **Step inspection** — syntax-highlighted JSON for inputs, outputs, errors
- **Live streaming** — SSE auto-updates for in-progress runs
- **Fork interface** — one click to fork a failed run from any step

### Local Replay & IDE Debugging
- `kronos replay <run-id>` — re-executes a production run locally using recorded outputs
- `kronos replay <run-id> --break-at step_name` — pause at a step; attach Delve / VS Code / GoLand
- `kronos replay <run-id> --fork-from step_name` — test a fix without re-executing upstream steps
- Output diffs surface divergence from production

### Production Infrastructure
- **PostgreSQL persistence** — single source of truth, survives crashes
- **Leader election** — advisory locks ensure one active scheduler per cluster
- **Multi-node clustering** — stateless API nodes, one scheduler; scale horizontally
- **Prometheus metrics** — workflow throughput, step latency, queue depth
- **gRPC health checks** — ready for Kubernetes liveness/readiness probes
- **Graceful shutdown** — drains in-flight steps on SIGTERM

### Built-in Job Queue
For simple one-off background work, Kronos also functions as a traditional job queue:
- **Job priorities** — 1–10 scale
- **Cron scheduling** — recurring jobs
- **Exponential backoff with jitter** — intelligent retries
- **Dead-letter handling** — failed jobs preserved for manual inspection
- **Idempotency keys** — deduplication across retries

## Performance

**Worker pool throughput (CPU-bound, no Postgres):**
- 10 workers: 1,256,838 jobs/sec (795ns/op)
- 50 workers: 1,135,330 jobs/sec (880ns/op)
- 100 workers: 1,117,641 jobs/sec (894ns/op)

**End-to-end throughput (with Postgres 17, 50 workers):**
- ~2,800 workflow steps/sec

**Benchmark locally:**
```bash
docker-compose up -d
make bench BENCH_FLAGS="-jobs=10000 -workers=50"
```

## Configuration

| Environment Variable | Default | Description |
|---|---|---|
| `DATABASE_URL` | `postgres://kronos:kronos@localhost:5432/kronos?sslmode=disable` | PostgreSQL DSN |
| `GRPC_ADDR` | `:50051` | gRPC API listen address |
| `ADMIN_ADDR` | `:8080` | HTTP admin server (debugger UI, dead-letter) |
| `METRICS_ADDR` | `:2112` | Prometheus metrics address |
| `WORKER_CONCURRENCY` | `10` | Worker pool size |
| `SCHEDULER_POLL_INTERVAL` | `2s` | Job polling interval |
| `SCHEDULER_BATCH_SIZE` | `10` | Max jobs to claim per poll |
| `JOB_TIMEOUT` | `30m` | Maximum execution time per step |

## Running Tests

```bash
make test                # Unit tests (race detector enabled)
make test-integration    # Integration tests (requires Docker)
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
│   ├── cron/                cron scheduling
│   ├── debugger/            web UI for time-travel debugging (HTMX + Alpine + Tailwind)
│   ├── health/              gRPC health protocol
│   ├── leader/              PostgreSQL advisory lock leader election
│   ├── metrics/             Prometheus instrumentation
│   ├── middleware/          gRPC interceptors
│   ├── replay/              local replay client + replayer engine + JSON diff
│   ├── retry/               exponential backoff
│   ├── scheduler/           job polling loop
│   ├── store/               job persistence (Store interface + Postgres)
│   ├── worker/              goroutine pool + handler registry
│   └── workflow/            workflow engine, event log, step dispatcher, fork
├── proto/kronos/v1/         protobuf definitions
├── gen/kronos/v1/           generated gRPC code
└── migrations/              SQL migrations
```

## Roadmap

**v1 (current):**
- [x] Durable workflow engine with DAG support
- [x] Time-travel debugger UI
- [x] Local replay and IDE debugging
- [x] Fork from any midpoint
- [x] Workflow versioning

**v1.1:**
- [ ] Typed step I/O via Go generics
- [ ] Embeddable library mode (no separate binary)
- [ ] Workflow signals & queries
- [ ] Human-in-the-loop approval steps

**v2:**
- [ ] AI step primitives (LLM calls, token counting, cost tracking)
- [ ] Python / TypeScript client SDKs
- [ ] Nested fan-out (map steps)

## License

MIT
