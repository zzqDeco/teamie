# Phase two: human-owned Agents

This phase extends the independent IM product with Agent Control Plane, an Eino v0.10.0-alpha.35 runtime, a platform-admin model gateway and isolated Sandbox execution. Existing invitation, workspace, messaging, history and reconnect flows remain usable without Agent services.

## Product scope

- A human owns each Agent. Owner settings are name, description, selected workspaces and enabled state. Ownership is private; platform admin is not a bypass. Users do not configure providers, prompts, tools or budgets.
- Agent ID is its independent IM principal ID. Selected workspace visibility is limited to the owner's current memberships; empty visibility is valid. Ordinary workspace/public-channel scope applies, and direct/private group membership remains explicit.
- Normal member selection supports humans and Agents. Group creator or workspace owner can add/remove current workspace participants. Group/workspace removal revokes history/send/queued-sync access; losing the granting human removes delegated Agents. Background reconciliation does not undo moderation.
- One durable Session `agent:<id>` spans the Agent's authorized conversations. New message body and sender kind/IDs, team, conversation, message and event IDs enter the turn input. Source-aware compaction retains references and recent coherent context; older history is fetched through tools.
- Every public send/reply is an explicit IM tool with a destination. Ordinary assistant output, private runtime state and errors never automatically become chat messages.
- Platform admins configure the shared gateway with redacted reads and encrypted provider-key storage. Initial production is intentionally unconfigured. Agents show a waiting state and retain pending inputs until configuration is available.
- Sandbox provides a persistent private workspace with bounded short-lived execution children, no network/host socket and no provider/IM credentials. Execution receipts preserve known results and distinguish interruption/unknown effects.

## Durability and host budget

The runtime owns durable input deduplication and cursor advancement, per-Agent leases/fencing, Session events, checkpoints and model/tool receipts. Replays preserve stable message-send IDs and known effects. Disable/delete/configuration revisions revoke participant credentials and cancel stale work. Temporary dependency failure must preserve retryable inputs.

The initial single runtime process has one global turn slot. This intentionally bounds model execution on a 2-vCPU/2-GB host; it is not a throughput or horizontal scaling claim. CP, runtime, gateway and Sandbox are separate modules/services; Identity/Chat/Realtime import no Eino types. Five database owners remain isolated, with filesystem persistence for Sandbox.

## Required acceptance gates

| Gate | Evidence required |
| --- | --- |
| Existing IM | Strict single-use invitations, current membership checks, message idempotence/order, history, reconnect and WebSocket hints still pass against real services |
| Ownership | Other humans/platform admins cannot read or mutate an owner's private definition; machine/bearer credentials cannot use owner browser APIs |
| Membership | Visibility validates owner membership; public/private scope, group removal, delegated owner removal and no automatic rejoin are exercised |
| Secrets | Admin gateway reads redact provider keys; non-admin requests fail; no provider/service secrets enter browser reads, IM rows or Sandbox children |
| Runtime | Fresh-body/ID ingestion, self-event suppression, durable cursor/inbox, continuous Session and source-aware compaction are verified |
| Recovery | Worker exclusion/fences, interrupted retry, idempotent sends, known tool replay, unknown execution handling and cancellation are exercised |
| Execution | Actual Docker children demonstrate filesystem/network/resource boundaries, cancellation and cleanup on the execution host |
| Delivery | Exact checked SHA, twelve digest-pinned images, schema vector `2/2/1/1/1`, flag revision, verified backup/restore, private service ports and public trusted HTTPS |
| Provider | Separate real-provider test after an administrator supplies configuration and authorizes a call; fixture success does not satisfy this gate |

Automated protocol fixtures are verification infrastructure, not production providers. Build/lint/unit checks, real service acceptance, actual Docker isolation, deployed health/HTTPS and live model behavior are separate gates. Do not report an unconfigured healthy gateway as a verified real-model deployment.

## Release boundaries

Only `production` is deployed. Release v0.2.0 follows the exact checked `main` SHA and creates a new permanent archive/tag; release/v0.1.0 remains untouched. Forward migrations preserve existing users, memberships and message history. A pre-upgrade backup/restore check is required; cross-schema image rollback is rejected. No destructive reset of production data is part of this phase.

Attachments, reactions, threads, general IM search, owner-defined tools/prompts/provider policy, multi-host scheduling and off-host backup storage remain outside this release. See the [Agent contract](../contracts/agents-v1.md), [integration boundary](../integrations/agent-boundary.md) and [architecture](../architecture/overview.md).
