# IM backend

Go 1.27.2; Hertz v0.10.6 for HTTP/WebSocket transport; Kitex v0.16.4 with generated Thrift clients and servers; pgx v5.11.0; go-redis v9.23.0; x/crypto v0.58.0; Gorilla WebSocket v1.5.3. Identity owns its PostgreSQL database; Chat owns another. Realtime uses Identity/Chat RPC only. The IM packages do not import Eino or Agent runtime types. Future agents must authenticate as participants through the public IM contracts.

The shared HTTP contract is [`../docs/contracts/http-v1.md`](../docs/contracts/http-v1.md); the generic Agent participant integration is in [`../docs/contracts/agents-v1.md`](../docs/contracts/agents-v1.md). Production deployment instructions live in `../deploy` and `../ops`; production is the only deployed environment. Loopback commands below are disposable acceptance checks.

## Generate and verify

From this directory:

```sh
go run github.com/cloudwego/kitex/tool/cmd/kitex@v0.16.4 -module teamie/backend ../contracts/im.thrift
go mod tidy
go test -race ./...
go vet ./...
go build ./services/identity/cmd
go build ./services/chat/cmd
go build ./services/realtime/cmd
```

On Go 1.27/ARM64, upstream optional DynamicGo/Sonic AST fast paths currently print compatibility notices and use their standard fallbacks. This backend uses standard `encoding/json` and typed generated Thrift; real RPC, PostgreSQL, WebSocket and race checks validate correctness. No accelerated-path performance claim is made.

Generated code in `kitex_gen` is checked in. Regenerate it with the pinned generator after changing `contracts/im.thrift`. Coordinate HTTP and RPC changes with dependent components before implementation.

Meaningful PostgreSQL tests require `DATABASE_URL_TEST`, a URL for a disposable PostgreSQL 17 database whose role may create schemas. Tests create isolated schemas and drop them afterward. Without this variable, database integration tests explicitly skip; unit/build results alone are not product acceptance. The parent acceptance script `../tools/acceptance/im_acceptance.py` exercises HTTP, actual RPC, PostgreSQL, Redis and WebSocket against running services.

## Configuration and first account

All services require `APP_ORIGIN`, the exact browser origin including scheme and any port, and optionally `FEATURE_FLAGS_FILE`. Identity/Chat require distinct `DATABASE_URL` values. Defaults are Identity HTTP `:8081`/RPC `:8881`, Chat HTTP `:8082`/RPC `:8882`, Realtime HTTP `:8083`; override with `HTTP_ADDR`, `RPC_ADDR`, `IDENTITY_RPC_ADDR`, `CHAT_RPC_ADDR`, `REDIS_ADDR`. Defaults for internal discovery are `identity:8881`, `chat:8882`, `redis:6379`.

Registration is strictly invitation-only. When no accounts exist, register the first human with `BOOTSTRAP_INVITE_CODE` (default `teamie-bootstrap` when unset). The registration transaction serializes the empty-account check and account/session creation; exactly one concurrent request becomes platform admin. Once any account exists, the bootstrap code cannot register another user. `GET /api/v1/auth/registration` reports only `{mode:"invite",bootstrapRequired}` and never returns the configured code. There is no preset account or password. `REGISTRATION_CODE` is no longer used.

The first platform administrator uses `/app/admin` to generate a new registration invitation for each additional user. Codes contain 256 random bits, expire after seven days and are persisted only as SHA-256 hashes. Raw code/link is returned once by `POST /api/v1/auth/admin/invites`; listing returns status metadata only. Revocation is idempotent; a consumed code never becomes reusable. User creation, session creation and invitation consumption commit together, so invalid input, duplicate email or session-insert failure cannot spend the code. Platform admin is an Identity role, grants invitation management only and does not bypass workspace membership/owner permissions. `auth.public_signup` is always unsupported/false even when requested true in the flag file. Registration codes are separate from workspace admission invitations.

Passwords require at least 12 characters and at most 72 UTF-8 bytes. Sessions last seven days and can be individually revoked by logout. `COOKIE_SECURE` defaults to `true`; use `false` only for disposable loopback acceptance. Keep production HTTPS and `COOKIE_SECURE=true`.

Every service exposes `/healthz`, `/readyz`, `/version` (`{version,sha}`). Liveness does not prove dependencies. Identity readiness pings its database; Chat readiness pings its database and Identity RPC; Realtime readiness pings both RPC dependencies. Redis is deliberately optional for availability: durable HTTP sync stays authoritative, Chat retries its PostgreSQL outbox, and Realtime sends a periodic sync hint while Redis recovers.

Only the edge publishes ports in production. Internal Kitex calls trust the private container network; RPC ports must remain private. Chat/Realtime validate sessions over Identity RPC; they never query Identity tables. Workspace membership is rechecked for reads/writes/sync and typing delivery. WebSocket requires the exact Origin and revalidates the session every ten seconds. Write queues are bounded; slow clients disconnect, then recover through HTTP sync.

## Migrations and durable delivery

Identity and Chat apply embedded forward migrations inside transactions, protected by service-specific PostgreSQL advisory locks. The current version is 2 in `identity_schema_migrations` and `chat_schema_migrations`. Published v1 schema SQL remains unchanged; v2 adds generic machine principals, expiring credentials, delegated workspace provenance and group creator metadata. Startup adopts the existing v1 ledger without replaying it. Reapplying startup is idempotent; unsupported future/gapped ledgers fail closed. Rollout scripts must serialize migrations/service startup and verify a pre-upgrade backup. Do not edit a published migration or release archive; introduce a new version.

Workspace creation, owner membership and the default `general` channel commit atomically. Message sequence allocation, immutable sender/client idempotency key, per-user durable fanout and outbox insertion commit together. A per-user `sync_heads` row is locked until commit, so concurrent transactions cannot expose a later cursor while an earlier event remains uncommitted. Cursor pages advance over filtered revoked payloads. Retraction redacts text from both history and historical sync responses. Redis carries only replaceable sync hints and short-lived typing signals; it never stores authoritative messages or cursors.

Flag file format:

```json
{"revision":"im-v1","flags":{"auth.public_signup":false,"im.dm.create":true,"im.group.create":true,"im.channel.create":true,"im.message.edit":true,"im.message.retract":true,"im.typing.publish":true,"im.presence.publish":false,"im.attachments.upload":false,"im.reactions.create":false,"im.threads.create":false}}
```

The service rereads the mounted file for capability/write decisions. Missing keys in a supplied file are false; unsupported features remain false even if requested true. A malformed file at startup prevents startup; an invalid later reload disables flag-controlled writes until repaired. Disabling write flags never removes previously stored content from reads. Attachment persistence is reserved for a later version: a future attachment must belong to an authorized conversation, have durable metadata and a separately authorized upload/download path; no current attachment endpoint or capability is enabled.

## Generic participants

IM does not import Agent Control Plane or runtime code. Private Identity RPC provisioning requires `IM_PRINCIPAL_CONTROL_TOKEN`; token issuance/validation requires the separate `IM_PRINCIPAL_RUNTIME_TOKEN`. The control credential cannot mint participant credentials and the runtime credential cannot provision principals. A runtime token lasts 15 minutes, rotation invalidates its predecessor, and every newer principal revision revokes it. Disabled/deleted principals cannot mint tokens. Existing human credentials and sessions retain their behavior; principal tokens cannot be used as human cookies.

Ordinary HTTP participant calls require both `Authorization: Bearer <principal-token>` and `X-Teamie-Service-Key: <IM_PRINCIPAL_RUNTIME_TOKEN>`. Browser Origin/fetch headers reject this credential path; owner browser sessions never receive machine tokens. Identity profile and control-plane owner APIs require human sessions. Workspace creation and workspace invitation acceptance are human-only, so a machine cannot extend its own workspace visibility.

Chat's generic membership RPC requires `IM_PRINCIPAL_CONTROL_TOKEN` and validates profiles through Identity RPC. Delegated workspace grants record the granting human. Removing that human also removes delegated members and their explicit conversation memberships. Replaying an already-applied membership revision returns actual current memberships rather than rejoining a moderator-removed participant. An explicit new owner visibility edit may issue a new grant. Machine participants use identical public-channel, direct/group, history, message, read and durable-sync ACLs. Platform admin never bypasses these permissions.

Group creators and workspace owners can add/remove ordinary current workspace participants with `POST /api/v1/conversations/:id/members` and `DELETE /api/v1/conversations/:id/members/:userId`. Additions require `im.group.create`; security removals remain available when that flag is off. Legacy groups without creator metadata are managed by the workspace owner. Group removal filters already-queued private sync payloads. `GET /api/v1/conversations/:id/messages/:messageId` uses current conversation authorization and the same tombstone redaction as paginated history.

Agent capabilities `agents.enabled`, `agents.create`, `agents.update`, `agents.delete`, `agents.discover` are implemented but default false, including when absent from a v1 flag file. Their definition and runtime state are owned by the independent `agent-control` module/database.
