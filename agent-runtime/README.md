# Agent Runtime — v0.3.2

Independent Go service pinned to CloudWeGo Eino **v0.10.0-alpha.35**. Identity, Chat and Realtime remain independently usable and import no Eino or runtime types. Ordinary authenticated IM APIs, generated Kitex clients, the model gateway and Sandbox are the service boundaries.

Each user-owned Agent has one durable global Session (`agent:<id>`) and one fenced PostgreSQL writer lease. One native Eino TurnLoop serializes that Session's Run/Resume calls. A one-second ordinary IM sync poll commits source envelopes and cursor advancement atomically. New input delivery has a separate query from active/retrying batches, so an in-flight batch cannot hide a fresh message. Relevant new human input from an active source conversation requests native safe-point preemption; other inputs buffer normally. Sender kind and workspace/conversation/message/event/sender IDs remain attached to source bodies. Self-authored messages do not create new work.

The host initially has **one foreground slot**. A model call obtains it, and independently supervised command/task waits return it. This setting provides host-budget serialization and does not establish horizontal capacity. Turns have no three-minute or twelve-iteration completion deadline. Individual network operations remain bounded; explicit cancellation, lost authority, dependency failure and lease loss stop affected work. The native iteration counter is set to the platform integer ceiling rather than an ordinary work-completion policy.

## Sessions, replay and compaction

Native Eino Session events and replacement boundaries persist in Runtime's PostgreSQL database. Summarization follows the administrator-configured model window at 75%, with a conservative 12,000-token transport ceiling and a 32-message trigger. Small windows also cap generated output. Input batches are bounded to 48 KiB of source-envelope JSON. Compaction retains coherent recent user/tool groups and an exact, bounded source-reference index; complete provenance remains in the durable inbox and Session audit. Original IM history is read through explicit server-side search, pagination, neighborhood and reference tools under current Agent ACLs.

Summary requests explicitly disable tool calls and request plain text. Empty or oversized summaries and summaries containing tool calls are rejected before tools can execute. Normal Agent turns retain their explicit IM tools.

Every durable run pins its original Session event position and immutable acceptance watermark. Later accepted attempts cannot change the context used to replay an older model/tool tape. Model responses, file-read observations, tool intents/results and stable IM send IDs persist before dependent effects. A tool receipt recovers a send whose server commit preceded a lost response without posting it twice.

A real native checkpoint can Resume only when its recorded Eino/runtime version, Agent revision, run identity and native interrupt targets match. Targeted Resume preserves prior attempt context and model-step position. An incompatible checkpoint explicitly uses StartNewTurn against the pinned durable inbox/tape boundary. Abrupt loss without a valid native checkpoint uses deterministic replay; it does not fabricate resumable runner state. Revoked/retracted/changed sources invalidate that replay and requeue unrelated inputs. Ordinary assistant text and operational errors stay private and never become IM messages; all public sends require `im_send_message` with an explicit destination and the Agent's own credentials.

## Native background work and workspace

One native `backgroundtask.Manager` per active Agent uses real PostgreSQL `TaskStore`, `TaskEventStore`, `NotificationWriter` and `NotificationOutbox` implementations. Row locks, native CAS versions, task-attempt leases, first-write cancellation and outbox acknowledgement receipts survive worker loss. A dispatcher recovers pending tasks. Task-created/completion/waiting notifications become deduplicated durable inputs before acknowledgement, so a completed task can wake the Session after restart.

Native filesystem middleware uses a remote Sandbox Backend and `RecoverableBackgroundConfig`. Commands always become durable native tasks before Sandbox Start. Start and Recover use the same stable task ID; a known queued `not_started` command waits, a confirmed busy host waits cancelably, and missing/unknown execution is observed without speculative re-execution. Sandbox commands have independent lifetimes. Native `task_output`/`task_stop` are injected once; stable fixed-boundary output updates persist in the native task event store and are available to the private progress reader. The Sandbox service confines paths, workspaces, process resources and network access; children receive no service/provider credentials.

Native PlanTask middleware persists plan JSON in `/workspace/.teamie/plan`. Native tool receipts, stable Sandbox mutation receipts and persisted read observations keep plan IDs stable when a process stops between file changes and the final tool receipt. Plans are private workspace data.

Disable/delete/revision changes revoke active authority and cancel affected work. Existing task history and source attribution remain persisted. A disabled or deleted Agent cannot accept new work, and platform admin never bypasses owner or IM authorization.

## Private runtime RPC and configuration

Private Kitex `AgentRuntimeService` listens on `RPC_ADDR` (default `:8885`); health/readiness/version HTTP stays on `HTTP_ADDR` (default `:8085`). State, Runs, Run, Tasks, Task, Events, CancelRun, CancelTask and RetryRun remain private service functionality. Runtime verifies its service token and current definition owner. Agent Control no longer has an outgoing runtime client; browser runtime inspection and control routes return 404, including for owners and platform administrators. Session IDs, activity, task descriptions, counts and operational error codes stay inside the service boundary. Event cursors are monotonic decimal IDs.

`GATEWAY_STREAM_URL` defaults to `http://llm-gateway:8086/internal/v1/chat/stream`. The adapter consumes real incremental gateway NDJSON backed by upstream streaming; complete model tapes commit before the stream closes and tools can run. There is no Generate-as-Stream adapter. `FEATURE_FLAGS_FILE` uses the shared `{revision,flags}` format: `agents.runtime.v3=false` pauses new foreground work while preserving ingestion/history; `agents.tasks.execute=false` blocks new execution-task submission while preserving existing tasks/results. No second engine is retained.

Runtime uses only its own database/role. Forward-only schema **2** preserves v1 inboxes, cursors, runs, model tapes, receipts and Session events. Existing service tokens and private network boundaries remain required. Only the edge publishes host ports; the only deployed environment is production. A healthy unconfigured gateway preserves inputs and reports `waiting_configuration`. Real provider acceptance remains pending until an administrator configures and authorizes a real model.

## Verification

```sh
go test -race ./...
go vet ./...
go build ./...
```

Set `TEAMIE_RUNTIME_TEST_DATABASE_URL` to a disposable PostgreSQL database for integration coverage; each test creates and drops an isolated schema. The suite runs all four official Eino task-store/event/outbox/notification conformance suites and exercises source-aware native compaction/restart, fresh input during an active model call, targeted checkpoint Resume, pinned-context retry after later work, send and native task/file crash windows, lease exclusion, cancellation, queued/busy/unknown command behavior, task completion wakeup, genuine incremental model streaming and owner metadata privacy. Protocol fixtures are validation infrastructure and do not prove any real provider works.
