# Agent API and execution contract — v0.3.0

This contract extends the independent IM product. IM services do not depend on Eino, model configuration, runtime sessions, or sandbox state. New services use separate Go modules, PostgreSQL databases/roles, container images and authenticated Kitex contracts in `contracts/agent.thrift`.

## Product decisions

- A human creates and owns each Agent. Agent ID equals its independent IM participant ID. Ownership does not confer access to other participants' conversations.
- Owner settings are name, description, selected workspace IDs, and enabled state. No user-supplied model, provider, key, prompt, tool, or budget controls in this release.
- Selected teams are workspace-level visibility. Only workspaces where the owner is currently a member may be selected. Agents use ordinary workspace/conversation membership and IM ACLs. Removing the granting owner revokes delegated memberships. Moderator removals must not be undone by background reconciliation.
- One durable Session per Agent (`agent:<id>`) intentionally carries working memory across its authorized conversations. Session compaction preserves provenance and original message references. IM history is never deleted by compaction.
- Newly committed message bodies, sender identity, workspace/conversation/message/event IDs enter the runtime inbox promptly. Self-authored messages are not new work. Each Agent has one Session writer and one serial native TurnLoop. Worker/Attempt/lease/fence are private implementation details; they do not add architecture services or owner settings. Fresh input delivery is independent of the current run batch.
- Distant history is read through explicit IM tools. Every chat write is an explicit tool invocation with an explicit destination. Ordinary model output and operational errors are never automatically published to IM.
- Platform administrators configure the shared OpenAI-compatible model gateway. A healthy but unconfigured gateway produces `model_unconfigured`; Agents wait without losing their inbox. Production will initially be unconfigured at the user's request.
- Tools and Sandbox are platform presets. Execution children have no provider/IM credentials, host Docker socket, network, or arbitrary host mounts. Workspace lifetime and execution-container lifetime are independent.

## Owner and discovery HTTP

All endpoints require a human cookie session. Writes require the existing CSRF and trusted Origin checks. Agent bearer credentials cannot manage definitions. Platform admin is not an ownership bypass.

`Agent` JSON: `{id,principalId,ownerId,ownerName,name,description,workspaceIds,enabled,revision,createdAt,updatedAt,deletedAt?,runtimeState,sessionId,lastActiveAt?,lastError?}`. `principalId=id`; `sessionId="agent:"+id`. Runtime states: `provisioning`, `waiting_configuration`, `idle`, `running`, `disabled`, `error`. Errors are safe status summaries, not model text, credentials, prompts or raw provider errors.

- `GET /api/v1/agents` -> `{agents:[Agent]}` owned by caller.
- `POST /api/v1/agents` body `{name,description?,workspaceIds,enabled?}` -> 201 `{agent}`. Empty workspace IDs are allowed; the Agent has no team membership until a team is selected.
- `GET /api/v1/agents/:id` -> `{agent}` owner only.
- `PATCH /api/v1/agents/:id` accepts optional `name`, `description`, `workspaceIds`, `enabled` -> `{agent}` owner only. Profile/visibility changes fence old configuration revisions. Disable revokes IM credentials and stops active work.
- `DELETE /api/v1/agents/:id` -> 204 owner only. Tombstone definition, revoke credentials/membership and cancel work; preserve attributable IM message history.
- `GET /api/v1/workspaces/:id/agents` -> `{agents:[Agent]}` discovery only for current members, selected visibility and actual Agent membership. Do not expose secrets or runtime internals.

Public capabilities: `agents.enabled`, `agents.create`, `agents.update`, `agents.delete`, `agents.discover`. Existing `im.dm.create` and `im.group.create` govern conversations. Disabled write features preserve persisted profiles/history. Frontend routes: `/app/agents`, `/app/agents/:id`, `/app/w/:workspaceId/agents`.

## Administrator model gateway HTTP

Gateway owns encrypted provider secrets and configuration in its own database. Only `platformRole=admin` human sessions may access these APIs. Private key bytes must never appear in reads, logs, frontend caches, URLs, or Sandbox requests.

- `GET /api/v1/admin/model-gateway` -> `{config:{baseUrl,model,hasApiKey,enabled,configured,contextWindowTokens,updatedAt},status:"unconfigured"|"ready"|"disabled"}`. `ready` describes saved configuration, not observed upstream health.
- `PUT /api/v1/admin/model-gateway` body `{baseUrl,model,apiKey?,enabled,clearApiKey?,contextWindowTokens?}` -> same redacted response. Omitted `apiKey` preserves the stored key. `clearApiKey` explicitly removes it and cannot accompany a new key.
- `POST /api/v1/admin/model-gateway/test` -> `{ok,model,latencyMs}`. Test saved configuration using one small real model request; the UI labels this effect. CI uses a disposable protocol fixture; production uses only administrator-supplied credentials.

`contextWindowTokens` defaults to 16384, accepts 4096–2000000 and is preserved when omitted. It is an administrator declaration for bounded runtime summarization, not a capability probe. Frontend route `/app/admin/gateway`; existing invitation admin route is preserved. Service readiness is independent of whether a model is configured.

## Internal RPC, storage and authentication

| Service/module | HTTP / RPC | Database | Schema ledger |
| --- | --- | --- | --- |
| `agent-control` | 8084 / 8884 | `teamie_agent_control` | `agent_control_schema_migrations` |
| `agent-runtime` | 8085 / 8885 | `teamie_agent_runtime` | `agent_runtime_schema_migrations` |
| `llm-gateway` (`agent-infra`) | 8086 / 8886 | `teamie_gateway` | `gateway_schema_migrations` |
| `sandbox` (`agent-infra`) | 8087 / 8887 | protected execution receipts/workspaces | no SQL schema |

All versions expose private `/healthz`, `/readyz`, `/version`. Only Caddy publishes host ports. The deployment stays a single production environment.

- `AgentControlService.ListAgents/GetAgent/ReportRuntime` use `AGENT_RUNTIME_TOKEN` and shared generated Agent definitions. List includes disabled/deleted definitions so workers cancel stale work. Runtime reports contain only safe operational state.
- `ModelGatewayService.Chat` uses `GATEWAY_RUNTIME_TOKEN`; attributes calls to owner, Agent, Session and run. Request/response JSON follows the OpenAI chat-completion wire protocol. Gateway owns model selection; worker-supplied model names cannot bypass the configured policy. Propagate cancellation, limits, errors and usage without exposing upstream credentials.
- `SandboxService.Start/Get/ReadOutput/CancelTask` uses `SANDBOX_RUNTIME_TOKEN`; accepts Agent/task IDs, command and optional execution deadline. Persist intent, bounded output, cancellation and result outside the workspace. Known async containers are adopted after supervisor restart; unknown effects are not rerun. `Get` returns `ok=false/errorCode=task_not_found` for an absent receipt; a durably accepted queued task is `ok=true/state=not_started` and remains recoverable. File RPC is confined to the Agent workspace and mutation IDs recover write/edit/delete receipts. Legacy bounded `Exec` remains compatible. Image, mounts, network and resources are platform configuration.
- IM generic principal provisioning uses `IM_PRINCIPAL_CONTROL_TOKEN`. Runtime-only principal token issuance/validation uses `IM_PRINCIPAL_RUNTIME_TOKEN`. Tokens expire and are revoked on disable/delete. Ordinary IM HTTP requests use `Authorization: Bearer <principal-token>` and `X-Teamie-Service-Key`; no human browser can impersonate an Agent. Tools use current IM APIs and ACLs, not another service's database.

## Runtime durability and tools

The runtime owns inbox cursor, pending inputs, worker leases/fencing, run state, Eino Session events, checkpoints, compaction state and tool receipts. IM sync is the durable input source; WS/Redis only accelerate wakeups. Persist events before advancing the source cursor, deduplicate source events, revalidate membership when processing delayed inputs, and keep recovery isolated from configuration revisions.

Preset tools: list authorized conversations, query/paginate IM history, fetch a referenced message or sequence context, explicitly send/reply, native filesystem/PlanTask tools, recoverable workspace execution, and native task progress/control tools. Each result is a runtime observation, not an automatic chat post. Send intent and stable `clientMessageId` survive retries; output is acknowledged only after IM persistence. Tool-call recovery must not duplicate already committed sends or unknown external side effects. Gateway calls and cancellation use a distinct execution-attempt ID. Sandbox preserves durable run/request IDs for receipt replay and carries a separate optional executionId for cancellation, so stopping one attempt cannot poison its later retry.

Native summarization stores a bounded source-aware summary plus recent coherent message/tool-call groups. Its model request fits the configured context window and transport limit. Long-term Session persistence and interrupted execution checkpoints are separate. Compatible native checkpoints use Resume; recovery retains model/tool receipts and the run's pinned Session view. A model configuration error leaves durable inputs available for retry after configuration.

Native Eino background TaskStore, TaskEventStore, NotificationOutbox and writer protocols are implemented in PostgreSQL. A stable logical task ID survives replay. Manager/executor activity and Sandbox execution do not write the Session in parallel; durable completion notifications return through its input queue. The foreground model slot is released during task waits. Async shell has no default command lifetime limit; request transport deadlines do not determine command lifetime. Ordinary gateway streams preserve upstream content/tool deltas and usage, with distinct explicit HTTP, in-stream, interruption and cancellation outcomes.

## Owner runtime HTTP and SSE

The same current human cookie, owner check, trusted Origin and CSRF rules apply. An administrator has no ownership bypass. Agent bearer tokens are rejected. Agent Control derives owner identity and proxies private runtime RPC; it never reads the runtime database.

- `GET /api/v1/agents/:id/runtime` -> `{state,sessionId,activeRunId?,pendingInputs,runningTasks,lastActiveAt?,lastError?}`.
- `GET /api/v1/agents/:id/runs?cursor=…&limit=…` -> `{runs:[Run],cursor?}`; `GET .../runs/:runId` -> `{run:Run}`.
- `GET /api/v1/agents/:id/tasks?cursor=…&limit=…` -> `{tasks:[Task],cursor?}`; `GET .../tasks/:taskId` -> `{task:Task}`.
- `GET /api/v1/agents/:id/events?cursor=0&limit=50` -> `{events:[Event],cursor,hasMore}`. Event cursor is a nonnegative decimal integer; limit is 1–100.
- `GET /api/v1/agents/:id/events/stream?cursor=0` emits SSE `runtime` events with `id` and JSON metadata. A reconnect's `Last-Event-ID` supersedes its initial query cursor. Current login/ownership is rechecked between pages. `unavailable` is a safe runtime outage event.
- `POST /api/v1/agents/:id/runs/:runId/cancel`, `POST .../runs/:runId/retry`, and `POST .../tasks/:taskId/cancel` take `{}` and return `{ok:true}`. Retry is limited to retryable states and checks new-work flags; cancellation remains available when new work is disabled.

Run metadata: `{id,status,inputCount,createdAt,updatedAt,startedAt?,finishedAt?,errorCode?}`. Status is `queued`, `running`, `waiting_input`, `interrupted`, `retry`, `waiting_configuration`, `completed`, `failed` or `canceled`.

Task metadata: `{id,runId?,kind,description,status,createdAt,updatedAt,finishedAt?,exitCode?}`. Status is native `pending`, `running`, `waiting_input`, `suspended`, `completed`, `failed` or `canceled`. The description is a fixed safe label, not the submitted command.

Event metadata: `{id,type,runId?,taskId?,toolName?,status?,createdAt,errorCode?}`. No prompt, raw Session event, model response, input body, command, output, secret, worker/attempt or fencing metadata appears in these owner routes. These endpoints stay readable when write flags are disabled.

Default flags revision `agents-v2` adds `agents.runtime.v3` (new runtime work) and `agents.tasks.execute` (new task submission). This is one runtime implementation with feature admission controls, not a permanent second engine. Disabling the submission flag preserves task observation/cancellation.

## Release acceptance

Required: owner isolation, workspace discovery and membership revocation, non-impersonation, redacted admin gateway settings, unchanged invitation/IM flows, arrival of fresh input during an active turn, native checkpoint resumption, source-aware summarization, send deduplication, lease exclusion, native task-store conformance, durable completion wakeup, known Sandbox recovery, cancellation/retry, incremental streaming with partial-failure classification, and desktop/mobile interaction. Real provider acceptance remains pending until administrator configuration and authorization; protocol fixtures must be identified as verification infrastructure.

Release `v0.3.0` from exact checked `main` using a new permanent `release/v0.3.0` archive. Preserve all previous release branches/tags. Forward migrations retain real accounts/workspaces/messages; the schema vector is Identity 2, Chat 2, Control 1, Runtime 2, Gateway 2. Stop the old runtime writer before dependency replacement/migration; quiesce legacy synchronous Sandbox work before its first async upgrade. New image digests, runtime flag identity and execution runner image belong in the release manifest. Cross-schema image rollback is rejected; take and verify a pre-upgrade backup.
