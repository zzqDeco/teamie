# Agent Runtime (`teamie/agent-runtime`)

Independent Go 1.27.2 service pinned to CloudWeGo Eino **v0.10.0-alpha.35**. Identity, Chat, and Realtime remain independently usable and import no Eino or runtime packages. Ordinary authenticated IM HTTP APIs, generated Kitex RPC clients, the Model Gateway, and the Sandbox service form the external boundaries.

- **4-Layer Hexagonal Clean DDD (`internal/engine`)**:
  - `domain/` (`session.go`, `provenance.go`, `context_policy.go`, `schedule.go`, `ports.go`): Pure Go entities, lease/revision invariants, source-provenance closure rules, context window budget policies, cron/one-shot schedule validation (with DST protection), and outbound port interfaces. Imports **Go standard library only**.
  - `app/`: Application use-case orchestration for lease validation, revision preemption, schedule authorization, and context policy evaluation. Imports **Go standard library + `engine/domain` only**.
  - `adapter/rpc/`: Inbound Kitex `AgentRuntimeService` server adapter.
  - `infra/{pg,eino,rpcclient}/`: PostgreSQL persistence (backed by `teamie/backend/pkg/postgres`), CloudWeGo Eino ADK/ChatModel/Tool adapters, and outbound RPC clients.
  - Enforced via AST inspection in `internal/engine/arch_test.go`.

---

## Single-Writer Global Session & Ingestion

Each human-owned Agent has one durable global Session (`agent:<id>`) and one fenced PostgreSQL writer lease. A single native Eino `TurnLoop` serializes `Run` and `Resume` calls for that Session.

- **Atomic Inbox Ingestion**: A 1-second IM sync poll commits incoming source envelopes (message body, attachments, structured `@` mentions, and sender/workspace/conversation/message/event IDs) and cursor advancement in one transaction. Self-authored messages never trigger new turns.
- **Responsive Preemption**: Fresh human input arriving in an active source conversation requests native `AfterToolCalls` preemption so the current tool round and durable receipts finish cleanly before the next turn incorporates the new message.
- **Foreground Model Slot**: The initial production process has **one global foreground model slot** to fit a 2-vCPU / 2-GB host budget. A model call acquires the slot, and background Sandbox task waits release it. This setting provides host-budget serialization rather than horizontal throughput claims.

---

## Context Engineering, Replay & Explicit Tools

See [`../docs/architecture/context-engineering.md`](../docs/architecture/context-engineering.md) for the full context pipeline.

- **Source-Closed Compaction & Archive**: Native Eino Session events, context budgets, and evicted artifacts persist in Runtime's PostgreSQL database (`runtime_context_artifacts`, `runtime_context_recoveries`). Summarization triggers at 75% of the configured model context window (or 32 messages), disables tool calls during summary generation, preserves a deterministic source-provenance DAG, and revalidates current IM conversation ACLs before serving archived context pages or search results.
- **Deterministic Replay & Receipts**: Every durable run pins its Session event watermark. Model tapes, file-read observations, tool intents/results, and stable `clientMessageId` send receipts commit before external side effects so crash recovery never posts duplicate IM messages.
- **Explicit Tool Surface**: Ordinary model output and operational errors never automatically become IM messages. All actions go through explicit, ACL-checked tools:
  - **IM Collaboration**: `im_send_message`, `im_send_file` (delivers Sandbox files to authorized conversations), `im_read_history`, `im_search_messages`, `im_import_attachment` (imports conversation attachments into `/workspace`), and typing indicators.
  - **Scheduled Wakeups (`agents.schedule.v1`)**: `schedule_create`, `schedule_list`, `schedule_cancel` supporting `once` and 5-field `cron` schedules with timezone validation, per-Agent active quotas, and source-ACL re-authorization on wakeup.
  - **Web Research (`agents.web.v1`)**: `web_search` and `web_extract` routed through the Model Gateway's Tavily integration with durable tool receipts.

---

## Native Background Tasks & Sandbox Workspace

Each active Agent runs a native Eino `backgroundtask.Manager` backed by PostgreSQL `TaskStore`, `TaskEventStore`, `NotificationWriter`, and `NotificationOutbox` implementations:
- Long-running Sandbox commands persist as durable tasks before `Sandbox.Start` and wake the Agent Session via deduplicated outbox notifications when completed.
- Native filesystem and `PlanTask` middleware operate against the confined remote Sandbox `/workspace` backend with stable mutation receipts.
- Disabling, deleting, or revising an Agent immediately revokes authority and cancels active runs, tasks, and schedules while preserving attributed history.

---

## Private Runtime RPC & Storage (`agent_runtime_schema_migrations` v12)

- **Listeners**: Private Kitex `AgentRuntimeService` listens on `RPC_ADDR` (default `:8885`); `/healthz`, `/readyz`, and `/version` listen on `HTTP_ADDR` (default `:8085`). Browser routes never reach Runtime.
- **Streaming Model Transport**: `GATEWAY_STREAM_URL` defaults to `http://llm-gateway:8086/internal/v1/chat/stream`, consuming real incremental NDJSON chunks backed by upstream SSE.
- **Forward-Only Schema v12**: Runtime owns its dedicated PostgreSQL database and role, applying contiguous forward migrations `v1` through `v12` with a connection writer schema fence trigger (`teamie.runtime_writer_schema`).

---

## Verification

```sh
cd agent-runtime
TEAMIE_RUNTIME_TEST_DATABASE_URL="postgres://127.0.0.1:5432/runtime_test?sslmode=disable" go test -race ./...
go vet ./...
go build ./...
```

Setting `TEAMIE_RUNTIME_TEST_DATABASE_URL` to a disposable PostgreSQL 17 database runs the full integration suite—including official Eino background-task conformance suites, source-closed compaction/archive ACLs, `AfterToolCalls` preemption, checkpoint `Resume`, crash-window send/file/schedule idempotence, and Clean DDD layer boundaries.
