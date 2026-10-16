# IM backend

Go 1.27.2; Hertz v0.10.6 for HTTP/WebSocket transport; Kitex v0.16.4 with generated Thrift clients and servers; pgx v5.11.0; go-redis v9.23.0; x/crypto v0.58.0; Gorilla WebSocket v1.5.3. Identity owns its PostgreSQL database; Chat owns another. Realtime uses Identity/Chat RPC only. The IM packages do not import Eino or Agent runtime types. Future agents must authenticate as participants through the public IM contracts.

The shared HTTP contract is [`../docs/contracts/http-v1.md`](../docs/contracts/http-v1.md). Production deployment instructions live in `../deploy` and `../ops`; production is the only deployed environment. Loopback commands below are disposable acceptance checks.

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

Set a private high-entropy `REGISTRATION_CODE` in protected production configuration. Register the first human through the ordinary registration screen using that code; there is no preset account, public admin password or database bootstrap bypass. Registration codes are separate from workspace invitation codes. Passwords require at least 12 characters and at most 72 UTF-8 bytes. Sessions last seven days and can be individually revoked by logout. `COOKIE_SECURE` defaults to `true`; use `false` only for disposable loopback acceptance. Keep production HTTPS and `COOKIE_SECURE=true`.

Every service exposes `/healthz`, `/readyz`, `/version` (`{version,sha}`). Liveness does not prove dependencies. Identity readiness pings its database; Chat readiness pings its database and Identity RPC; Realtime readiness pings both RPC dependencies. Redis is deliberately optional for availability: durable HTTP sync stays authoritative, Chat retries its PostgreSQL outbox, and Realtime sends a periodic sync hint while Redis recovers.

Only the edge publishes ports in production. Internal Kitex calls trust the private container network; RPC ports must remain private. Chat/Realtime validate sessions over Identity RPC; they never query Identity tables. Workspace membership is rechecked for reads/writes/sync and typing delivery. WebSocket requires the exact Origin and revalidates the session every ten seconds. Write queues are bounded; slow clients disconnect, then recover through HTTP sync.

## Migrations and durable delivery

Identity and Chat apply their own embedded, forward-only v1 schemas inside transactions, protected by service-specific PostgreSQL advisory locks. The service records version 1 in `identity_schema_migrations` or `chat_schema_migrations`; reapplying startup is idempotent. Rollout scripts must serialize migrations/service startup and run an explicit backup/restore procedure for destructive future changes. Do not edit a published migration or release archive; introduce a new version.

Workspace creation, owner membership and the default `general` channel commit atomically. Message sequence allocation, immutable sender/client idempotency key, per-user durable fanout and outbox insertion commit together. A per-user `sync_heads` row is locked until commit, so concurrent transactions cannot expose a later cursor while an earlier event remains uncommitted. Cursor pages advance over filtered revoked payloads. Retraction redacts text from both history and historical sync responses. Redis carries only replaceable sync hints and short-lived typing signals; it never stores authoritative messages or cursors.

Flag file format:

```json
{"revision":"im-v1","flags":{"auth.public_signup":false,"im.dm.create":true,"im.group.create":true,"im.channel.create":true,"im.message.edit":true,"im.message.retract":true,"im.typing.publish":true,"im.presence.publish":false,"im.attachments.upload":false,"im.reactions.create":false,"im.threads.create":false}}
```

The service rereads the mounted file for capability/write decisions. Missing keys in a supplied file are false; unsupported features remain false even if requested true. A malformed file at startup prevents startup; an invalid later reload disables flag-controlled writes until repaired. Disabling write flags never removes previously stored content from reads. Attachment persistence is reserved for a later version: a future attachment must belong to an authorized conversation, have durable metadata and a separately authorized upload/download path; no current attachment endpoint or capability is enabled.
