# Agent Control Plane

Independent Go 1.27.2 module using Hertz v0.10.6, Kitex v0.16.4 and PostgreSQL/pgx v5.11.0. It owns Agent profiles, human ownership, desired visibility/enabled state, reconciliation revisions and safe runtime status. It contains no model, provider, prompt, tool or budget settings and imports no Eino/runtime code. Identity and Chat are called through generated generic participant RPC clients; their database tables are never read.

The public and internal contract is [`../docs/contracts/agents-v1.md`](../docs/contracts/agents-v1.md). Agent ID equals the IM principal ID; the single durable runtime Session is `agent:<id>`. A human cookie session owns the API; writes require the existing trusted Origin and CSRF token. Platform administrators have no ownership bypass. Workspace discovery verifies the caller's membership and the Agent's actual normal IM membership. Empty visibility is valid.

Browser responses use an explicit public profile DTO: ID/principal, owner attribution, name, description, selected workspace IDs, enabled state and creation/update timestamps. Discovery includes only the requested workspace ID. Internal revisions, Session, runtime state, errors and activity never enter these responses. All former runtime/run/task/event/SSE/cancel/retry routes return 404, including for owners and administrators. Control has no runtime proxy client or outgoing runtime-address setting; private service RPC remains unchanged.

## Configuration

Required: `DATABASE_URL` (own database/role), `APP_ORIGIN`, `IM_PRINCIPAL_CONTROL_TOKEN`, `AGENT_RUNTIME_TOKEN`. Defaults: `HTTP_ADDR=:8084`, `RPC_ADDR=:8884`, `IDENTITY_RPC_ADDR=identity:8881`, `CHAT_RPC_ADDR=chat:8882`. Set `FEATURE_FLAGS_FILE` to the same backend-authoritative mounted capability file. Missing/invalid flags fail closed; v1 files without Agent capabilities leave them disabled. `COOKIE_SECURE` is not used here because this service does not issue cookies.

`IM_PRINCIPAL_CONTROL_TOKEN` authorizes generic provisioning and membership RPCs. `AGENT_RUNTIME_TOKEN` authorizes CP `ListAgents`, `GetAgent` and `ReportRuntime`. No participant bearer tokens appear in HTTP/CP RPC Agent DTOs. The runtime alone uses the independent `IM_PRINCIPAL_RUNTIME_TOKEN` to obtain expiring IM credentials.

Only private container ports are exposed by this service. `/healthz` checks the process; `/readyz` checks this database and both IM RPC dependencies; `/version` returns `{version,sha}`. A healthy deployment may have an unconfigured model gateway: the runtime reports `waiting_configuration` without losing pending inputs.

## Storage and recovery

Startup applies own schema version 1 under a PostgreSQL advisory lock, recording `agent_control_schema_migrations`; it rejects a newer schema. Desired state is committed before remote changes. Reconciliation disables the Identity principal, applies ordinary workspace memberships, then enables the fully applied revision. Revisions fence stale retries and revoke old IM tokens. A failed disable/delete reports a pending 503 and retains durable desired state; the background loop retries after IM recovers. Runtime definitions remain ineffective until reconciliation completes. Deleted definitions remain visible to internal runtime listing so workers stop while attributed IM history is preserved.

Membership revisions are separate from profile revisions. Background reconciliation, restarts and name changes cannot recreate a moderator-removed grant. Owner membership loss prunes visibility, and Chat also cascades delegated revocation synchronously. An explicit owner workspace edit is needed to request a new grant. Runtime status accepts only fixed states, the matching Session ID and safe error codes; reports cannot enable an Agent.

## Build and verify

From this directory:

```sh
go test -race ./...
go vet ./...
go build ./cmd
```

PostgreSQL integration tests require `DATABASE_URL_TEST` pointing to a disposable PostgreSQL 17 database whose role can create schemas; tests isolate and remove their own schemas. They cover exact public HTTP fields, cross-workspace disclosure, owner isolation, human-only auth/CSRF, strict owner settings, current membership discovery, moderator/owner revocation, failed provisioning recovery, pending disable, deletion, service scopes and safe private durable runtime status. Dependency-free HTTP tests verify retired routes never reach runtime/IM/database work and inject sensitive runtime/task/error sentinels into response tests. Without the variable, database tests explicitly skip.

The module consumes checked-in clients from `../backend/kitex_gen` and shared Agent RPC clients from `../agent-protocol`. Regenerate the IM contract with the pinned command in `../backend/README.md`. Shared Agent contracts are generated from the `agent-protocol` directory with `go run github.com/cloudwego/kitex/tool/cmd/kitex@v0.16.4 -module teamie/agent-protocol ../contracts/agent.thrift`; coordinate IDL changes with all dependent services. Docker uses repository-root context: `docker build -f agent-control/Dockerfile .`. The container binary is `/app/service`; Alpine provides `wget` for readiness checks.
