# Independent Agent integration boundary

Teamie v0.2.0 adds human-owned Agents while preserving a standalone IM product. Identity, Chat and Realtime contain no Eino dependency, model provider configuration, runtime Session types or execution scheduler. They continue delivering human IM while Agent services are stopped. Generic principal provisioning and participant authentication are the only IM extension; Agents read/send through the same public HTTP API and current ACLs as humans.

The runtime is a separate Go module pinned to **Eino v0.10.0-alpha.35**. This is an alpha baseline. Eino supplies Session/TurnLoop and model/tool composition; Teamie's runtime owns durable input ingestion, database leases, replay fencing, idempotent sends and cancellation. Those distributed guarantees are application responsibilities.

| Component | Owns | Integration |
| --- | --- | --- |
| Identity | Human accounts/sessions, generic Agent principals and expiring participant credentials | Scoped Kitex provisioning and runtime token issuance |
| Chat | Current normal workspace/conversation membership, messages, durable sync and outbox | Ordinary authenticated participant HTTP APIs |
| Agent Control Plane | Human ownership, profile, desired visibility/enabled state, reconciliation revisions, safe runtime status | Generic IM RPC clients; authenticated Agent definition/status RPC |
| Agent runtime | Durable inbox/cursor, one Session per Agent, TurnLoop, leases, checkpoints, compaction and tool receipts | Agent CP, model gateway and Sandbox Kitex clients; ordinary IM HTTP tools |
| Model gateway | Platform-admin provider/model configuration, encrypted key storage and bounded upstream requests | Redacted admin HTTP API and authenticated model RPC |
| Sandbox | Persistent private Agent workspaces and idempotent execution receipts | Authenticated execution/cancellation RPC; isolated short-lived children |

Identity, Chat, Control Plane, runtime and gateway own five separate PostgreSQL databases and application roles. Sandbox owns protected filesystem state. Services share a host/instance for the initial budget, but never query another service's tables. Shared generated RPC types live in `agent-protocol`; IM's generated contract remains generic in `contracts/im.thrift`.

## Identity and visibility

Agent ID equals its independent IM principal ID. The owning human may edit name, description, workspace IDs and enabled state. Users have no provider, model, prompt, tool or budget settings. Platform admin manages the shared model gateway and registration invitations; it cannot read another owner's Agent definition or bypass workspace privacy.

Visibility is selected at workspace/team granularity from the owner's current memberships. It creates ordinary workspace membership, including the existing public-channel scope. Direct conversations and private groups still require explicit participant membership. Removing a group participant revokes group history, sends and queued protected sync delivery. Removing a granting human from a workspace also revokes its delegated Agent members. Reconciliation intersects desired and current membership; a moderator removal stays removed until an explicit new owner visibility edit requests a grant.

Owner cookie sessions cannot impersonate Agents. CP provisioning and runtime credential issuance use separate service scopes. Participant tokens last 15 minutes, rotate and are revoked on newer configuration revisions, disable and delete. Runtime HTTP requires both the participant bearer token and the scoped service header; the browser path rejects machine credentials. No raw participant token appears in Agent DTOs or browser caches.

## Session, input and output

An IM conversation is not an Eino Session, and an IM message is not automatically a separate turn. Each Agent has one global durable Session `agent:<id>` across its authorized conversations. The input envelope includes the freshly revalidated message body and sender kind/identity, workspace, conversation, message and event IDs. These IDs remain available in compacted memory and tool observations. Incoming content is untrusted user data rather than platform instructions.

The runtime stores inputs before advancing the durable IM cursor, deduplicates source events and ignores self-authored messages. Current membership and tombstones are rechecked before delayed processing. Several pending messages may form one turn. Older history is fetched explicitly through normal list/history/message tools; compaction preserves a bounded summary and coherent recent context without deleting IM history.

Every IM send/reply is an explicit `im_send_message` tool call with a destination. Ordinary assistant text, private runtime state, model errors and execution errors remain runtime state/observations; none are automatically posted to chat. A persisted send intent and stable `clientMessageId` survive retry. Known tool results are replayed; uncertain Sandbox effects are reported as unknown/interrupted rather than automatically repeated.

One production runtime process has one global turn slot, plus a durable per-Agent lease/fence. This fits the initial 2-vCPU/2-GB host and deliberately serializes model work across Agents. It is not a horizontal scheduling or throughput guarantee. Worker cancellation propagates to gateway/Sandbox calls, while persisted inputs and receipts support recovery. Session persistence, turn checkpoints and tool receipts serve different recovery purposes and remain in the runtime database.

## Models and execution

The initial production model gateway is intentionally **unconfigured** pending user configuration. Readiness remains healthy in that state. Agents report `waiting_configuration`/`model_unconfigured`, retain inputs and begin model work only after a platform administrator saves/enables a model. CI protocol fixtures verify integration without implying real-provider acceptance; a live model test is a separate administrator-authorized action.

Sandbox children have fixed platform image/mount/network/resource policy. They receive no IM/provider credentials, Docker socket, arbitrary host mounts or network access. Workspace persistence is separate from a child process lifetime; execution receipts distinguish success, interruption and unknown outcomes. Production Docker isolation and resource cleanup must be verified on the actual execution host.

See the [Agent API/execution contract](../contracts/agents-v1.md), [architecture](../architecture/overview.md) and [phase-two acceptance](../product/phase-two.md).
