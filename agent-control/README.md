# Agent Control Plane (`teamie/agent-control`)

Independent Go 1.27.2 module using CloudWeGo Hertz v0.10.6, Kitex v0.16.4, and PostgreSQL (`pgx` v5.11.0 via `teamie/backend/pkg/postgres`).

`agent-control` owns human-managed Agent profiles, ownership attribution, desired workspace visibility, enabled state, reconciliation revisions, and safe runtime status. It contains no model, provider, prompt, tool, or budget settings and imports no Eino or runtime execution packages. Identity and Chat are called exclusively through generated Kitex RPC clients; their database tables are never read directly.

- **Contract & Architecture**:
  - Public and internal contract: [`../docs/contracts/agents-v1.md`](../docs/contracts/agents-v1.md).
  - HTTP Thrift IDL: [`../contracts/agent_control_http.thrift`](../contracts/agent_control_http.thrift) (generated into `../agent-protocol/hertz_gen/agent/control`).
  - Internal RPC Thrift IDL: [`../contracts/agent.thrift`](../contracts/agent.thrift) (generated into `../agent-protocol/kitex_gen/agent`).
  - **4-Layer Hexagonal Clean DDD**: `internal/control` is structured into `domain` (pure Go entities, visibility/revision invariants, coded errors, and ports), `app` (use-case orchestration and reconciliation logic), `adapter/{http,rpc}`, and `infra/{pg,rpcclient}`, enforced by `internal/control/arch_test.go`.

---

## Ownership & Privacy Boundaries

- Agent ID equals its IM principal ID; its single durable runtime Session is `agent:<id>`.
- Human cookie sessions own the control API; mutating requests require a trusted `Origin` and CSRF token. Platform administrators do not bypass Agent ownership.
- Browser responses expose only the public profile DTO: ID/principal, owner attribution, name, description, selected workspace IDs, enabled state, and timestamps.
- Workspace discovery (`GET /api/v1/workspaces/:workspaceId/agents`) verifies both the caller's membership and the Agent's current normal IM membership, returning only `[workspaceId]` in `workspaceIds` without leaking other selected workspaces.
- Internal revisions, Session state, runs, tasks, tools, errors, and activity never enter browser responses; retired runtime inspection routes return `404` for all callers.

---

## Configuration

- **Required Environment**: `DATABASE_URL` (dedicated PostgreSQL database/role), `APP_ORIGIN`, `IM_PRINCIPAL_CONTROL_TOKEN`, `AGENT_RUNTIME_TOKEN`.
- **Defaults**: `HTTP_ADDR=:8084`, `RPC_ADDR=:8884`, `IDENTITY_RPC_ADDR=identity:8881`, `CHAT_RPC_ADDR=chat:8882`.
- **Feature Flags**: Set `FEATURE_FLAGS_FILE` to the backend-authoritative capability file ([`../deploy/feature-flags.default.json`](../deploy/feature-flags.default.json)).
- **Service Scopes**: `IM_PRINCIPAL_CONTROL_TOKEN` authorizes generic principal provisioning and delegated membership RPCs. `AGENT_RUNTIME_TOKEN` authorizes internal `ListAgents`, `GetAgent`, and `ReportRuntime` RPCs.
- **Health Endpoints**: `/healthz` checks the process; `/readyz` checks the Control Plane database and both IM RPC dependencies; `/version` returns `{version,sha}`.

---

## Storage & Reconciliation (`agent_control_schema_migrations` v1)

Startup applies schema version **1** under a PostgreSQL advisory lock (`pkg/postgres.Migrate`) and rejects unknown future schema versions.

- Desired state commits before remote provisioning. Reconciliation disables the Identity principal, applies ordinary delegated workspace memberships in Chat, and then enables the fully applied revision.
- Revisions fence stale retries and revoke older IM participant tokens. Failed disable/delete transitions report `503` while retaining durable desired state for background retry.
- Membership revisions are tracked separately from profile revisions: background reconciliation, restarts, and name/description edits never recreate a moderator-removed workspace membership. Only an explicit new workspace visibility edit by the human owner requests a fresh grant.

---

## Code Generation & Verification

Regenerate contracts from the repository root with `./ops/codegen.sh`, then run tests and builds from `agent-control/`:

```sh
cd agent-control
DATABASE_URL_TEST="postgres://127.0.0.1:5432/teamie_test?sslmode=disable" go test -race ./...
go vet ./...
go build ./cmd
```

`DATABASE_URL_TEST` runs PostgreSQL integration tests in isolated per-test schemas covering public DTO projections, cross-workspace privacy, owner isolation, CSRF/Origin enforcement, moderator/owner revocation, failed provisioning recovery, pending disable/delete reconciliation, and Clean DDD layer boundaries.
