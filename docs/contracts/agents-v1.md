# Agent API and execution contract — v0.2.0

This contract extends the independent IM product. IM services do not depend on Eino, model configuration, runtime sessions, or sandbox state. New services use separate Go modules, PostgreSQL databases/roles, container images and authenticated Kitex contracts in `contracts/agent.thrift`.

## Product decisions

- A human creates and owns each Agent. Agent ID equals its independent IM participant ID. Ownership does not confer access to other participants' conversations.
- Owner settings are name, description, selected workspace IDs, and enabled state. No user-supplied model, provider, key, prompt, tool, or budget controls in this release.
- Selected teams are workspace-level visibility. Only workspaces where the owner is currently a member may be selected. Agents use ordinary workspace/conversation membership and IM ACLs. Removing the granting owner revokes delegated memberships. Moderator removals must not be undone by background reconciliation.
- One durable Session per Agent (`agent:<id>`) intentionally carries working memory across its authorized conversations. Session compaction preserves provenance and original message references. IM history is never deleted by compaction.
- Newly committed message bodies, sender identity, workspace/conversation/message/event IDs enter the runtime inbox promptly. Self-authored messages are not new work. A bounded single worker lease serializes each Agent's main Session.
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

- `GET /api/v1/admin/model-gateway` -> `{config:{baseUrl,model,hasApiKey,enabled,configured,updatedAt},status:"unconfigured"|"ready"|"disabled"}`. `ready` describes saved configuration, not observed upstream health.
- `PUT /api/v1/admin/model-gateway` body `{baseUrl,model,apiKey?,enabled,clearApiKey?}` -> same redacted response. Omitted `apiKey` preserves the stored key. `clearApiKey` explicitly removes it and cannot accompany a new key.
- `POST /api/v1/admin/model-gateway/test` -> `{ok,model,latencyMs}`. Test saved configuration using one small real model request; the UI labels this effect. CI uses a disposable protocol fixture; production uses only administrator-supplied credentials.

Frontend route `/app/admin/gateway`; existing invitation admin route is preserved. Service readiness is independent of whether a model is configured.

## Internal RPC, storage and authentication

| Service/module | HTTP / RPC | Database | Schema ledger |
| --- | --- | --- | --- |
| `agent-control` | 8084 / 8884 | `teamie_agent_control` | `agent_control_schema_migrations` |
| `agent-runtime` | 8085 / worker | `teamie_agent_runtime` | `agent_runtime_schema_migrations` |
| `llm-gateway` (`agent-infra`) | 8086 / 8886 | `teamie_gateway` | `gateway_schema_migrations` |
| `sandbox` (`agent-infra`) | 8087 / 8887 | protected execution receipts/workspaces | no SQL schema |

All versions expose private `/healthz`, `/readyz`, `/version`. Only Caddy publishes host ports. The deployment stays a single production environment.

- `AgentControlService.ListAgents/GetAgent/ReportRuntime` use `AGENT_RUNTIME_TOKEN` and shared generated Agent definitions. List includes disabled/deleted definitions so workers cancel stale work. Runtime reports contain only safe operational state.
- `ModelGatewayService.Chat` uses `GATEWAY_RUNTIME_TOKEN`; attributes calls to owner, Agent, Session and run. Request/response JSON follows the OpenAI chat-completion wire protocol. Gateway owns model selection; worker-supplied model names cannot bypass the configured policy. Propagate cancellation, limits, errors and usage without exposing upstream credentials.
- `SandboxService.Exec` uses `SANDBOX_RUNTIME_TOKEN`; accepts Agent/run/request IDs, command and bounded timeout only. Image, mounts, network and resource limits are platform configuration. Persist idempotent receipts and distinguish interrupted/unknown execution from success; do not automatically rerun unknown side effects.
- IM generic principal provisioning uses `IM_PRINCIPAL_CONTROL_TOKEN`. Runtime-only principal token issuance/validation uses `IM_PRINCIPAL_RUNTIME_TOKEN`. Tokens expire and are revoked on disable/delete. Ordinary IM HTTP requests use `Authorization: Bearer <principal-token>` and `X-Teamie-Service-Key`; no human browser can impersonate an Agent. Tools use current IM APIs and ACLs, not another service's database.

## Runtime durability and tools

The runtime owns inbox cursor, pending inputs, worker leases/fencing, run state, Eino Session events, checkpoints, compaction state and tool receipts. IM sync is the durable input source; WS/Redis only accelerate wakeups. Persist events before advancing the source cursor, deduplicate source events, revalidate membership when processing delayed inputs, and keep recovery isolated from configuration revisions.

Preset tools: list authorized conversations, read/paginate IM history, fetch a referenced message, explicitly send/reply, and execute bounded commands in the Agent workspace. Each result is a runtime observation, not an automatic chat post. Send intent and stable `clientMessageId` survive retries; output is acknowledged only after IM persistence. Tool-call recovery must not duplicate already committed sends or unknown external side effects. Gateway calls and cancellation use a distinct execution-attempt ID. Sandbox preserves durable run/request IDs for receipt replay and carries a separate optional executionId for cancellation, so stopping one attempt cannot poison its later retry.

Compaction stores a bounded summary plus recent coherent message/tool-call groups while preserving sender and conversation identifiers. Long-term Session persistence and interrupted execution checkpoints are separate. A model configuration error must leave durable inputs available for retry after configuration.

## Release acceptance

Required: owner isolation, workspace discovery and membership revocation, non-impersonation, admin-only secret-redacted gateway settings, unchanged invitation/IM flows, current-message ingestion, explicit history/send tools, no automatic model-text publishing, persistent Session/compaction recovery, send deduplication, lease exclusion, cancellation, Sandbox filesystem/network/resource separation and cleanup. Real provider acceptance remains pending until an administrator configures a model; protocol fixtures must be identified as verification infrastructure.

Release `v0.2.0` from exact checked `main` using a new permanent `release/v0.2.0` archive. Preserve `release/v0.1.0` and its tag. Forward-only migrations preserve real accounts/workspaces/messages. New image digests, schema vector, runtime flag revision and execution runner image belong in the release manifest. Cross-schema image rollback is rejected; take and verify a pre-upgrade backup.
