# Future Agent integration boundary

The first IM release contains no model SDK, Eino dependency, execution scheduler or sandbox worker.

The selected future Agent runtime baseline is the Eino v0.10 series. It will live in a separately versioned Go module/service, with its own database, image and deployment permissions. Exact alpha dependency versions will be pinned when that phase starts. IM deployment and message delivery must remain functional while the Agent system is stopped.

| IM owns | Future Agent control plane owns | Future sandbox execution plane owns |
| --- | --- | --- |
| Participant ID and visible kind | Agent definition and runtime configuration | Process/container execution |
| Membership and scoped credentials | Event subscription and routing policy | Filesystem and network restrictions |
| Conversation/message IDs and durable events | IM-event to Session/TurnLoop projection | Tool execution and artifact production |
| Sending, reading, moderation and sync | Scheduling, budgets, run state and checkpoints | Execution resource limits and cleanup |

An IM conversation is not an Eino Session. An IM message is not an Agent turn. An external adapter decides which authorized messages enter a particular Agent's context, labels them with participant identity, and maps results back to ordinary IM messages through a scoped credential. The mapping and replay cursor are owned by that adapter. Several agents in one conversation can have independent sessions, response policies and turn state.

The future credential distinguishes actor identity, credential holder and optional delegated human authority. `kind=agent` is visible provenance, never an authorization shortcut. Reading and sending require the same explicit membership rules as humans. Agent control-plane secrets and sandbox credentials do not enter browser bundles, IM message rows or generic client-supplied metadata.

Future background work may continue without a chat connection. Its lifecycle is an Agent task/run lifecycle; the adapter posts progress or completion as IM messages. IM does not create runtime tasks, hold model streams open, or coordinate sandbox jobs. Sandboxes report to the Agent control plane, which decides what is safe and authorized to send to IM.
