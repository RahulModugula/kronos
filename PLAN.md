# Kronos Development Plan & Progress

## Overview

Kronos is being pivoted from a generic job scheduler to **"the durable workflow engine with a time-travel debugger"** — a production-grade system for debugging, replaying, and fixing failed workflows in production.

**Goal:** Build a launch-worthy v1 that becomes the go-to open-source durable execution debugger, gets a Show HN front-page moment, and demonstrates top-tier systems engineering for portfolio purposes.

---

## Completed Work

### Phase 0: Foundation & Positioning ✅
- **Commit:** `fe9caa4` (2026-03-15)
- Rewrote README with new positioning: "durable workflows you can debug like regular programs"
- Established the three killer features: time-travel debugging, local replay, workflow versioning
- Positioned existing job queue as "execution substrate"

### Phase 1: Workflow Engine MVP ✅
Commits: `2099525` through `3bb6b28` (2026-03-17 through 2026-03-30)

#### Database Schema (Commit `2099525`)
- `workflows` — versioned, immutable definitions (name + fingerprint)
- `workflow_runs` — execution instances with pinned workflow_id
- `workflow_steps` — materialized projection of step state
- `workflow_events` — append-only event log (time-travel source of truth)
- `workflow_blobs` — content-addressed storage for large inputs/outputs

#### Core Types (Commit `5623217`)
- `Workflow`, `Step`, `StepFunc`, `StepDef` — definition primitives
- `WorkflowRun`, `StepExecution` — execution records
- `Event`, `EventType` — 12 event types covering the full lifecycle
- Workflow DAG validation with cycle detection

#### WorkflowStore Interface & Postgres Implementation (Commit `c2988d0`)
- Interface-based design for testability
- Postgres implementation with proper transactional semantics
- Event appending, blob management, run/step querying
- Mirrors `internal/store/postgres.go` patterns

#### Workflow Registry (Commit `0d70551`)
- Name → versioned definition lookup
- Handles multiple versions of the same workflow
- Thread-safe with RWMutex

#### Workflow Engine (Commit `ed9707e`)
- `ExecuteStep()` — executes a single step in a run
- Constructs step input from upstream recorded outputs
- Records events transactionally
- Detects workflow completion and marks run as done
- Handles blob storage for large payloads

#### Protocol Buffers (Commits `a57bdfa`, `72f09ab`, `97a4a49`)
- Workflow RPC definitions: `StartWorkflow`, `GetRun`, `ListRuns`, `CancelRun`, `GetRunEvents`, `StreamRunHistory`
- Generated gRPC stubs with proper messaging
- Merged workflow types into `kronos.proto` to avoid import complications
- Workflow service integrated into `KronosService`

#### Main.go Wiring (Commit `1599579`)
- Instantiated `WorkflowStore`, `WorkflowRegistry`, `WorkflowEngine`
- Registered `kronos.workflow_step` handler that delegates to engine
- Workflow steps execute as ordinary jobs through existing pipeline (zero changes to scheduler/worker/retries)

#### Tests (Commits `3bb6b28`, `85e1bbc`)
- Unit tests for engine logic with mock store
- Linear workflow test, cycle detection test
- Integration test with real Postgres via testcontainers
- End-to-end test: create run, execute steps, verify events and completion

### Phase 1.5: API Server Implementation ✅
Commit: `a6d2882` (2026-04-05)

- Implemented all six workflow RPC methods:
  - `StartWorkflow()` — creates run, records RunStarted event, returns run ID
  - `GetRun()` — retrieves run by ID with proto conversion
  - `ListRuns()` — paginated listing with workflow/status filters
  - `CancelRun()` — cancels pending runs
  - `GetRunEvents()` — returns all events for a run
  - `StreamRunHistory()` — server-side streaming for replay clients
- Proto conversion helpers (run, status, event)
- Wired into gRPC server in main.go
- Proper error handling with gRPC status codes

---

## Architecture Summary

### How Workflows Execute

```
User calls StartWorkflow RPC
    ↓
API creates workflow_runs row, records RunStarted event
    ↓
For each step with satisfied dependencies:
    ↓
Insert jobs row (type=kronos.workflow_step, payload={run_id, step_name})
    ↓
Scheduler claims job → Worker executes
    ↓
WorkflowEngine.ExecuteStep():
  1. Load workflow definition (pinned version)
  2. Assemble step input from upstream outputs
  3. Invoke user's StepFunc
  4. Write events transactionally (StepStarted, StepInput, StepOutput, StepCompleted)
  5. Check for newly-ready downstream steps
  6. Mark run completed when all steps done
    ↓
Replay client calls StreamRunHistory to fetch event log
```

### Key Design Properties

1. **No new scheduler** — Workflow steps use existing `SELECT FOR UPDATE SKIP LOCKED` job claiming
2. **Versioning baked in** — In-flight runs pin `workflow_id` (specific content hash); new runs get latest version
3. **Event sourcing** — Every state change is an append-only event; deterministic replay from event log
4. **Blob deduplication** — Large inputs/outputs stored by content hash; shared across forks
5. **Single binary** — Everything compiles to one executable; no separate services
6. **Zero regression** — Existing job queue, cron, retries, dead letter, metrics all unchanged

---

## Outstanding Work

### Phase 1.75: StepFunc Signature Change ✅
Commit: (2026-04-17)

- Changed `StepFunc` from `func(input json.RawMessage)` to `func(ctx context.Context, input json.RawMessage)`
- Unblocks cancellation propagation, timeout enforcement, and OpenTelemetry tracing
- Updated engine.go, engine_test.go, workflow_test.go, all handler registrations

### Phase 2: Visual Time-Travel Debugger UI ✅
Commit: (2026-04-17)

- **Handler:** `internal/debugger/handler.go` — HTTP handlers for runs list, run detail, events, SSE, fork
- **Templates:** `internal/debugger/templates/` — Go templates with `[[` `]]` delimiters (avoids Alpine.js conflict)
  - `layout.html`, `runs.html`, `run_detail.html`
  - Partials: `step_tree.html`, `timeline.html`, `step_detail.html`
- **Static assets:** `internal/debugger/static/` — CSS (dark mode, custom properties), JS (Alpine.js components, syntax highlighter)
- **Embed:** `internal/debugger/embed.go` — `go:embed` for single-binary deployment
- **Routes:** `/debugger/`, `/debugger/runs`, `/debugger/runs/{id}`, `/debugger/runs/{id}/events`, `/debugger/runs/{id}/live`, `/debugger/runs/{id}/fork`, `/debugger/static/`
- **Features:** Runs list with filters, 3-pane run detail, timeline scrubber, step inspector, JSON syntax highlighting, SSE live updates, fork from any step
- Wired into admin HTTP server in `cmd/kronos/main.go`

### Phase 2.5: ForkRun RPC ✅
Commit: (2026-04-17)

- Added `ForkRun` RPC to `KronosService` in proto
- Implemented `Engine.ForkFromStep()` in `internal/workflow/engine.go`
- Implemented `Server.ForkRun()` in `internal/api/server.go`
- Fork creates new run with parent_run_id, copies upstream events, schedules downstream steps

### Phase 3: Local Replay & IDE Debugging ✅
Commit: (2026-04-17)

- **CLI:** `kronos replay <run-id> [--break-at step] [--fork-from step] [--server addr]`
- **Client:** `internal/replay/client.go` — gRPC client fetching run history + events
- **Replayer:** `internal/replay/replayer.go` — walks DAG re-invoking handlers against recorded outputs, breakpoint support (PID print + stdin wait), fork-from support
- **Diff:** `internal/replay/diff.go` — structural JSON diff for recorded vs replayed outputs
- **Tests:** `internal/replay/diff_test.go` — 10 tests covering diff, arg parsing
- **Subcommand dispatch:** Added to `cmd/kronos/main.go` before server startup
- **Workflow registration:** Extracted `registerWorkflows()` shared by server and replay

### Phase 3.5: Integration Test Fix ✅
Commit: (2026-04-17)

- Added missing `//go:build integration` tag to `workflow_test.go`
- Integration tests no longer run during `go test ./...`

### Phase 3.75: README Overhaul ✅
Commit: (2026-04-17)

- Added badges (Go Report Card, MIT License, Go Reference, CI)
- Added comparison table (Kronos vs Temporal vs Hatchet vs River)
- Added "Why Kronos?" section with 4 key differentiators
- Restructured: badges → tagline → comparison → quick start → define workflow → replay → architecture → features → performance → config → tests → structure → roadmap
- Updated project structure to include debugger/ and replay/ packages
- Updated roadmap to reflect completed phases
- **CLI subcommand:** `cmd/kronos/replay.go` — dispatch on argv before server startup
- **Client:** `internal/replay/client.go` — gRPC streaming client to fetch event history
- **Replayer:** `internal/replay/replayer.go` — takes event stream + local registry + breakpoint config; walks DAG re-invoking handlers
- **Diff:** `internal/replay/diff.go` — structural JSON diff for recorded vs. replayed outputs
- **Debugger API:** `proto/kronos/v1/debugger.proto` — `StreamRunHistory`, `ForkRun` RPC

**Developer Experience:**
```bash
$ kronos replay abc-123-def --break-at fetch_user_data
[replay] connecting to kronos://localhost:50051
[replay] fetched 47 events for run abc-123-def
[replay] step 1/8: validate_input  ok   (diff: 0 bytes)
[replay] step 2/8: fetch_user_data BREAK
[replay] pid=48291 — attach your debugger now, press enter to continue
```

**Features:**
- Re-executes production run locally against recorded outputs
- `--break-at step_name` — pause and attach Delve/VS Code/GoLand
- `--fork-from step_name` — test a fix without re-executing upstream (reuse recorded outputs)
- `--skip-step` — skip expensive steps
- Output diffs surface divergence from production

**Key Constraint:**
- Developer must compile workflow definitions into replay binary themselves (e.g., `github.com/me/myapp/cmd/replay`)
- This is the same model as `go run ./cmd/myapp` — documented clearly

### Phase 4: Post-Launch Polish (Not Started)
- Workflow version diffing in debugger and CLI (`kronos workflows diff v3 v5`)
- Typed step I/O via Go generics: `workflow.NewStep[Input, Output](name, handler, deps)`
- Per-step cost tracking via metadata (`step.Emit("cost_usd", 0.042)`)
- Optional auth middleware on debugger UI (HTTP basic / bearer token)
- **`contrib/ai/llmstep.go`** — optional AI primitives (OpenAI/Anthropic clients, rate-limit retries, token counting, streaming)
- Python client SDK for workflow submission (not replay)

---

## Roadmap & Timeline

### Immediate (Next 2-3 weeks for Phase 2)
1. Design + spike HTMX + Alpine timeline scrubber (2 days)
2. Build debugger server and handlers (3 days)
3. Implement templates and static assets (4 days)
4. Polish UI design and styling (3 days)
5. **Launch Phase 2** — Show HN post with screen recording of debugger

### Follow-up (1-2 weeks for Phase 3)
1. Implement replay CLI subcommand (3 days)
2. Streaming client + replayer engine (4 days)
3. IDE debugger integration (Delve attach flow) (2 days)
4. Fork workflow execution (2 days)
5. **Launch Phase 3** — GitHub release, follow-up HN comments

### Post-Launch (Weeks 4-8 for Phase 4)
- Polish versioning UX
- Add AI primitives (optional, conditional on community interest)
- Improve documentation
- Address user feedback from Phase 2/3 launch

---

## Critical Files & References

### Core Workflow Subsystem
- `internal/workflow/definition.go` — Workflow, Step, StepFunc, validation
- `internal/workflow/events.go` — Event types and payloads
- `internal/workflow/store.go` — WorkflowStore interface
- `internal/workflow/postgres.go` — Postgres implementation
- `internal/workflow/registry.go` — Versioned workflow lookup
- `internal/workflow/engine.go` — Step executor and event recorder

### API & gRPC
- `internal/api/server.go` — All six workflow RPC implementations
- `proto/kronos/v1/kronos.proto` — Workflow messages and service
- `gen/kronos/v1/*.pb.go` — Generated stubs

### Database
- `migrations/005_create_workflows.up.sql` — Schema
- `migrations/005_create_workflows.down.sql` — Teardown

### Integration & Testing
- `internal/integration/workflow_test.go` — End-to-end test with Postgres
- `internal/workflow/engine_test.go` — Unit tests with mock store

### Entry Points
- `cmd/kronos/main.go` — Startup, wiring, handler registration (line 80-100 for workflow setup)
- TODO: `cmd/kronos/replay.go` — Replay subcommand (Phase 3)

### Patterns to Reuse
- `internal/store/postgres.go` — SQL patterns (SELECT FOR UPDATE, transactional updates, cursor-based pagination)
- `internal/admin/handler.go` — HTTP handler pattern for mounting on admin mux
- `internal/scheduler/scheduler.go` — Poll loop pattern for background work
- `internal/worker/worker.go` — Handler registry pattern

---

## Success Criteria for v1 Launch

### Phase 2 (Debugger)
- [ ] Visual UI renders workflow runs with step tree
- [ ] Timeline scrubber allows scrubbing through events
- [ ] Step details show exact input/output/error
- [ ] Fork workflow button creates new run
- [ ] SSE live updates for in-progress runs
- [ ] UI looks professional (screenshot worthy for HN)

### Phase 3 (Local Replay)
- [ ] `kronos replay <run-id>` fetches event history and replays locally
- [ ] `--break-at step_name` pauses and allows IDE debugger attachment
- [ ] Output diffs surface divergence from production
- [ ] `--fork-from` creates fork and skips upstream steps
- [ ] All existing tests still pass with `-race`

### Regression
- [ ] Job queue functionality unchanged (echo, webhook-delivery handlers work)
- [ ] Cron scheduling unchanged
- [ ] Retries and dead letter unchanged
- [ ] Metrics endpoints work
- [ ] Leader election unchanged

### Launch
- [ ] README updated with debugger screenshots/GIF
- [ ] Show HN post with compelling demo of time-travel debugging
- [ ] GitHub release v1.0.0
- [ ] 1k+ upvotes on HN (stretch goal: 5k+ stars on GitHub)

---

## Key Risks & Mitigations

| Risk | Mitigation |
|------|-----------|
| **UI quality is critical.** Bootstrap-style UI = 300 stars; polished = 5k+ stars. | Hire designer 20h on Upwork or use Tailwind UI templates. Spend full week on polish. Screenshots at 2x retina. |
| **HTMX + Alpine scrubber might feel janky.** | 2-day spike in Phase 2. Fallback: minimal Preact for scrubber only. |
| **Replay assumes determinism in recorded outputs.** Users expect re-execution of upstream. | Clear docs: "replay uses recorded outputs; use `--rerun-from` to re-execute upstream." |
| **Event log growth on long runs balloons Postgres.** | Hard cap: 100k events per run. Auto-archive completed runs older than 30d. Ship `kronos workflows prune` command. |
| **Hatchet (well-funded YC) ships debugger in 6 months.** | Speed to Phase 2 is everything. Moats: open-source purity, single-binary, first-mover on keyword. |
| **Code-version pinning vs in-place patches.** Users ask "apply fix to live runs." | v1 cannot do this safely. Be explicit: "in-flight runs use pinned version; fork to apply fix." Defensible. |

---

## Commit History Summary

**Phase 0 (3/15):** README pivot
**Phase 1 (3/17–3/30):** Workflow types, store, registry, engine, proto, wiring, unit tests
**Phase 1.5 (4/03–4/07):** Proto generation, API RPC methods, integration tests

**Total commits so far:** ~15 commits spanning March 15 – April 7, 2026
**Lines of code (Phase 1):** ~3,500 lines (workflows + tests)
**Test coverage:** Unit + integration tests with mock and real Postgres

---

## Next Session

1. **Setup:** Ensure protoc + plugins are available
2. **Design spike:** Sketch debugger UI in Figma or by hand
3. **Start Phase 2:** Build debugger server, templates, and static assets
4. **Target:** Debugger UI functional by end of next session

**Estimated effort for Phase 2:** 3-4 weeks of focused development
**Estimated effort for Phase 3:** 1-2 weeks after Phase 2 launch

---

*Last updated: 2026-04-11*
*Status: Phase 1 complete, Phase 2 ready to start*
