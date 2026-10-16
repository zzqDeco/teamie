# Agent Runtime

Independent Go service with CloudWeGo Eino **v0.10.0-alpha.35**. Identity, Chat and Realtime do not depend on this module or Eino. Generated IM clients and ordinary authenticated HTTP are its only IM boundary.

Each enabled user-owned Agent has one durable Session (`agent:<id>`) and one fenced PostgreSQL lease. A one-second ordinary IM sync poll commits the cursor and new-message envelopes atomically before pushing inputs into the native Eino TurnLoop. Runtime inputs retain workspace, conversation, message, sender ID/name/kind and body. Own authored messages are not echoed back as new work. Other participants, including other Agents, remain identified inputs.

The native ChatModelAgent exposes explicit IM list/history/point-lookup/send tools plus isolated `workspace_execute`. Plain assistant output is retained in the private Session, never automatically published. The owner configures profile, team visibility and enablement; the platform configures model access and fixed runtime policy. Tools run as the Agent, never as its owner.

Native Eino summarization triggers at 12,000 estimated context tokens or 32 messages. Its replacement events and bounded source-scoped summary survive restarts. Original IM history remains available through tools; the Session audit remains in its own PostgreSQL database. Native replay loads backwards to the latest replacement boundary. One model turn runs at a time across this small production host; a turn has at most 12 generation iterations and a three-minute deadline. New inputs continue to be durably ingested while the slot is busy. Sandbox command limits are enforced independently by its supervisor.

Recovery replays an unfinished turn from the last committed Session head, with its immutable input batch, durable model-response tape and tool receipts. The native TurnLoop checkpoint uses its supported `StartNewTurn` recovery decision; the database inbox is authoritative. Abandoned attempt events remain audit data and cannot enter the restored model context. Stable IM client-message IDs recover a send whose server commit preceded a lost response. Sandbox receipts preserve completed results and report uncertain interrupted effects without blindly rerunning them. Temporary history-read failures preserve inputs for retry; revoked/retracted/changed sources abandon that replay and requeue unrelated inputs.

Gateway attribution and cancellation include a distinct execution-attempt ID, so a canceled attempt cannot poison a later retry of the same durable run. Runtime operations check current control-plane enablement and IM access. Disable/delete/revision changes cancel local work and explicitly cancel Gateway and Sandbox RPC operations; a separate heartbeat renews the fenced lease. IM principal credentials expire after 15 minutes and are rotated by the active worker. Database writes reject stale lease holders. Logs/status contain IDs and safe error codes, not prompts, message text or provider credentials.

Without a configured, enabled gateway, the service remains healthy, preserves pending inputs and reports `waiting_configuration`. Saving/enabling a real model in the administrator page allows processing to resume. No mock model runs in production.

## Verification

```sh
go test -race ./...
go vet ./...
go build ./...
```

Set `TEAMIE_RUNTIME_TEST_DATABASE_URL` to a disposable PostgreSQL database to run native Eino integration tests. Each test creates and drops its own unique schema. Fixtures verify protocol behavior, continuous context/compaction across restarts, explicit sending/silence, crash-window idempotence, lease fencing and durable unconfigured/retry states. These fixtures do not prove a particular real provider works; that acceptance follows administrator configuration.

The only deployed environment remains production. Local fixtures and CI processes are disposable validation, not additional deployments.
