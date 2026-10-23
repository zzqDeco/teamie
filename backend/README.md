# IM Backend (`teamie/backend`)

Independent Go 1.27.2 module providing **Identity**, **Chat**, and **Realtime** services. Built with CloudWeGo Hertz v0.10.6 (HTTP/WebSocket), Kitex v0.16.4 (Thrift RPC), `pgx` v5.11.0 (PostgreSQL 17), `go-redis` v9.23.0, `x/crypto` v0.58.0, and Gorilla WebSocket v1.5.3.

- **Strict IM Independence**: `backend` never imports CloudWeGo Eino, Agent runtime packages, or model provider configuration. Agents authenticate as generic participants through public IM APIs and RPC contracts.
- **Data Ownership**: Identity and Chat each own an isolated PostgreSQL database and role. Realtime is stateless and validates authority through Identity and Chat RPCs.
- **4-Layer Hexagonal Clean DDD**: `internal/identity`, `internal/chat`, and `internal/realtime` are structured into `domain` (stdlib-only entities, invariants, coded errors, and outbound ports), `app` (use-case orchestration), `adapter` (`http`, `ws`, `rpc` bindings), and `infra` (`pg`, `fs`, `crypto` implementations), enforced by AST tests (`arch_test.go`). Shared technical mechanisms live in `pkg/postgres`, `pkg/httpx`, and `pkg/observability`.

See [`../docs/contracts/http-v1.md`](../docs/contracts/http-v1.md) for the HTTP/WebSocket contract, [`../docs/contracts/agents-v1.md`](../docs/contracts/agents-v1.md) for generic participant integration, and [`../docs/architecture/ddd-idl.md`](../docs/architecture/ddd-idl.md) for the Clean DDD and Thrift IDL layout.

---

## Code Generation & Verification

From the repository root, regenerate all Kitex RPC (`kitex_gen`) and Hertz HTTP/WebSocket (`hertz_gen`) bindings from `contracts/im*.thrift`:

```sh
./ops/codegen.sh
```

From `backend/`, run tests, static analysis, and binary builds:

```sh
cd backend
DATABASE_URL_TEST="postgres://127.0.0.1:5432/teamie_test?sslmode=disable" go test -race ./...
go vet ./...
go build ./services/identity/cmd
go build ./services/identityctl/cmd
go build ./services/chat/cmd
go build ./services/realtime/cmd
```

- Generated code in `kitex_gen/` and `hertz_gen/` is checked in. Coordinate IDL changes across dependent modules and regenerate with `./ops/codegen.sh`.
- Meaningful PostgreSQL integration tests require `DATABASE_URL_TEST` pointing to a disposable PostgreSQL 17 database whose role can create and drop isolated schemas. Without `DATABASE_URL_TEST`, database tests explicitly skip; unit/build checks alone do not constitute product acceptance.
- End-to-end process acceptance is exercised via `../tools/acceptance/im_acceptance.py`.

---

## Configuration, Onboarding & Account Recovery

All services require `APP_ORIGIN` (exact browser origin including scheme and port) and optionally `FEATURE_FLAGS_FILE`. Identity and Chat require distinct `DATABASE_URL` values. Default listen addresses are:
- **Identity**: HTTP `:8081` / RPC `:8881` (`identity:8881`)
- **Chat**: HTTP `:8082` / RPC `:8882` (`chat:8882`)
- **Realtime**: HTTP `:8083` (`redis:6379`)

### Invitation-Only Registration
Registration is strictly invitation-only (`auth.public_signup` is permanently disabled):
1. **Bootstrap Admin**: When no accounts exist, registering with `BOOTSTRAP_INVITE_CODE` (defaults to `teamie-bootstrap`) atomically creates the first account and grants it the platform administrator role. Once any account exists, the bootstrap code can never be used again.
2. **Admin-Issued Invitations**: Platform administrators generate single-use registration invitations at `/app/admin` (`POST /api/v1/auth/admin/invites`). Each code has 256 random bits, expires in 7 days, and is stored only as a SHA-256 hash. Account creation, session creation, and invitation consumption commit in one transaction. Platform admin grants user/invitation/gateway administration only and never bypasses workspace or conversation permissions.

### Password Management & Recovery
Passwords require 12 characters to 72 UTF-8 bytes. Changing a password (`POST /api/v1/auth/password`) revokes all existing sessions for that user. When `auth.password_reset` is enabled, a platform administrator (`/app/admin`) or server operator (`identityctl password-reset-link`) can issue a one-hour, single-use password reset link (`identity_password_resets`, persisted as SHA-256 token hashes). Redeeming the reset token updates the password and revokes all active sessions atomically without auto-signing in.

### Health & Network Boundaries
Every service exposes `/healthz`, `/readyz`, and `/version` (`{version,sha}`). Identity readiness pings its database; Chat readiness pings its database and Identity RPC; Realtime readiness pings both Identity and Chat RPCs. Redis is disposable: durable HTTP sync in PostgreSQL remains authoritative while Redis recovers. In production, only the Caddy edge publishes ports.

---

## Migrations & Durable Messaging Guarantees

Identity and Chat apply embedded forward migrations inside transactions protected by PostgreSQL advisory locks (`pkg/postgres.Migrate`):
- **`identity_schema_migrations` (current version: 3)**:
  - `v1`: Human accounts, sessions, and registration invitations.
  - `v2`: Generic machine principals, expiring credentials, and revision fencing.
  - `v3`: One-time password-reset token ledger (`identity_password_resets`).
- **`chat_schema_migrations` (current version: 5)**:
  - `v1`: Workspaces, memberships, channels/DMs/groups, durable messages, sync events, and Redis outbox.
  - `v2`: Delegated workspace grants and group creator metadata.
  - `v3`: Conversation file attachments (`attachments`) and message attachment bindings.
  - `v4`: Idempotent attachment upload keys (`attachment_uploads`).
  - `v5`: Structured `@` mention spans (`message_mentions`), reply indexing, and conversation search/attention indexes.

Key transactional invariants in `internal/chat`:
- Workspace creation, owner membership, and the default `general` channel commit atomically.
- Message sequence allocation, sender/client idempotency key (`client_message_id`), attachment binding, structured `@` mentions, per-recipient durable sync events, and outbox hints commit in a single transaction with sorted `sync_heads` row locks so cursors never skip earlier concurrent commits.
- Message retraction scrubs text, attachments, and mentions into a redacted tombstone across history, point lookup, attention inbox, and historical sync.

---

## Generic Participants & Feature Flags

Private Identity RPC provisioning requires `IM_PRINCIPAL_CONTROL_TOKEN`; runtime token issuance/validation requires the separate `IM_PRINCIPAL_RUNTIME_TOKEN`. Runtime tokens expire after 15 minutes and are revoked immediately on rotation, newer revision, disable, or delete. Participant HTTP calls require both `Authorization: Bearer <principal-token>` and `X-Teamie-Service-Key: <IM_PRINCIPAL_RUNTIME_TOKEN>`, which browser CORS/Origin gates reject.

Chat's delegated membership RPC (`IM_PRINCIPAL_CONTROL_TOKEN`) records the granting human owner. Removing the owner from a workspace cascades revocation to their delegated participants. Replaying an already-applied membership revision never rejoins a moderator-removed participant.

Services dynamically reload `FEATURE_FLAGS_FILE` (default revision `mentions-v1` in [`../deploy/feature-flags.default.json`](../deploy/feature-flags.default.json)). Disabling any write flag (`im.dm.create`, `im.group.create`, `im.channel.create`, `im.message.edit`, `im.message.retract`, `im.attachments.upload`, `im.message.mentions`, `auth.password_reset`, or `agents.*`) blocks new mutations on the backend while keeping all previously persisted data readable.
