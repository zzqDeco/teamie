# Phase three: continuous Agent work and responsive IM

The v0.3 contract keeps a single writer for each Agent's global Session and one native Eino v0.10.0-alpha.35 TurnLoop advancing serially. Workers, attempts, leases and fences remain private implementation metadata. Background execution uses Eino's TaskStore/Manager interfaces and an external Sandbox, with durable task notifications returning through the Agent input queue.

## Product scope

- Owner settings stay name, description, selected teams and enabled state. Neither model configuration nor execution internals become owner settings.
- New IM messages are committed with their sync cursor and promptly delivered independently of the active input batch. Incoming identities and conversation/message/event sources remain intact.
- Native safe-point preemption and checkpoint resumption preserve serial Session writes. Durable model responses and tool receipts remain the recovery boundary for external effects.
- IM history search applies the query before pagination and supports context around a sequence number. All reads and sends use ordinary current IM authorization.
- Long-running work is tracked through native background tasks. Sandbox execution and persisted output survive a disconnected runtime transport when the actual execution remains recoverable. Unknown outcomes never automatically rerun shell effects.
- Completion notifications are durably routed back into the input queue so an Agent can continue after the human leaves. Ordinary assistant text stays private; only explicit IM tools publish messages.
- Owners can inspect metadata-only runs, tasks and live activity, and request cancellation/retry through current human authentication and CSRF checks. Ownership grants no access to foreign conversation content.
- UI copy is concise. Mobile navigation, dialogs and composer sizing are functional at narrow widths; desktop retains the IM workspace/conversation/message layout.

## Feature and deployment contract

Default flag revision is `agents-v2`. `agents.runtime.v3` controls new runtime work; `agents.tasks.execute` controls new task submission. Turning a write flag off cannot hide existing records or disable the ability to stop already-running work. Only production is deployed; disposable local/CI fixtures are verification processes.

The runtime exposes authenticated Kitex RPC on private port 8885. Agent Control proxies owner HTTP routes and SSE; IM does not depend on runtime availability. Gateway adds an authenticated private model stream endpoint on existing port 8086. Runtime and Gateway migrations advance to schema 2, preserving existing data. The schema vector is `2/2/1/2/2` for Identity/Chat/Control/Runtime/Gateway.

Release uses exact checked main, a new permanent `release/v0.3.0` branch and tag. Existing release branches/tags remain immutable. Database migrations are forward-only; an image-only rollback to v0.2 is incompatible with the new schema vector.

## Acceptance

Required evidence includes native Session persistence and source-aware compaction, input arrival during an active model call, serial execution/fenced stale writers, native TaskStore conformance, background completion returning to the Agent, idempotent IM sends across crash windows, cancellation and recovery of known Sandbox execution, unknown-effect handling, real streaming and partial-stream failure handling, owner/admin separation, history search/revocation, and desktop/mobile browser interaction.

Actual Docker isolation is verified on a Docker-capable runner; builds alone do not prove it. Saved Gateway configuration and service health do not prove provider behavior. Real-model acceptance is reported separately after administrator configuration and authorization.
