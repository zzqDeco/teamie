# Contributing to Teamie

Before changing behavior or contracts, read:
- [`AGENTS.md`](AGENTS.md) — non-negotiable engineering and product invariants
- [`docs/architecture/overview.md`](docs/architecture/overview.md) — service topology and delivery guarantees
- [`docs/architecture/ddd-idl.md`](docs/architecture/ddd-idl.md) — 4-layer Hexagonal Clean DDD and unified Thrift IDL architecture
- [`docs/contracts/http-v1.md`](docs/contracts/http-v1.md) and [`docs/contracts/agents-v1.md`](docs/contracts/agents-v1.md) — HTTP, WebSocket, and Agent contracts

---

## 1. Service Ownership & Module Boundaries

- **IM Independence**: `services/{identity,chat,realtime}` and `contracts/gen/im` must never import CloudWeGo Eino, `contracts/gen/agent`, `services/agent-*`, or model provider configuration. Agents integrate strictly as authenticated principals through generic IM APIs and RPC contracts.
- **Database Ownership**: `identity`, `chat`, `agent-control`, `agent-runtime`, and `llm-gateway` each own an isolated PostgreSQL database and application role. `sandbox` owns isolated host workspace directories and durable execution receipts. Services communicate exclusively over RPC or authenticated HTTP and never query another service's tables.
- **Peer Service Isolation**: Every service (`identity`, `chat`, `realtime`, `agent-control`, `agent-runtime`, `llm-gateway`, `sandbox`) is a standalone Go module under `services/<svc>` with its own `Dockerfile` and `cmd/main.go` composition root. Services never import another service's `internal/` packages.

---

## 2. 4-Layer Hexagonal Clean DDD Rules

Every peer service (`services/{identity,chat,realtime,agent-control,agent-runtime,llm-gateway,sandbox}`) follows the exact same 4-layer package structure and inward dependency rule:

```text
cmd/main.go ──► adapter/{http,ws,rpc,cli} ──► app ──► domain ◄── infra/{pg,fs,crypto,flags,redis,docker,eino,rpcclient,upstream}
```

| Layer | Responsibility | Allowed Imports |
| :--- | :--- | :--- |
| `domain/` | Entities, value objects, domain invariants, coded `domain.Error`, and outbound port interfaces (`Repository`, `TxManager`, gateway ports) | **Go standard library only** (zero third-party, `pgx`, `hertz`, `kitex`, `eino`, or cross-package imports) |
| `app/` | Application use-case orchestration and transaction boundaries (`TxManager.WithinTx`) | **Go standard library + own `domain` package only** |
| `adapter/` | Inbound transport adapters (`http`, `ws`, `rpc`, `cli`) binding generated `hertz_gen` / `kitex_gen` DTOs and translating `*domain.Error` via `pkg/httpx` | `app`, `domain`, `contracts/gen/{im,agent}`, `pkg/httpx`, `pkg/observability` (**never imports `infra`**) |
| `infra/` | Outbound port implementations (`pg`, `fs`, `crypto`, `flags`, `redis`, `docker`, `eino`, `rpcclient`, `upstream`) | `domain`, `pkg/postgres`, `pkg/observability`, `contracts/gen/{im,agent}`, and infrastructure SDKs (`pgx`, `eino`, `kitex`). (**never imports `app` or `adapter`**) |

- **No Flat Legacy Files**: `services/<svc>/internal/` may contain only `{domain,app,adapter,infra}` subdirectories and `arch_test.go`. Flat non-test `.go` files or compatibility facades in `internal/` are forbidden.
- **Shared Technical Mechanisms**: Reuse `pkg/postgres` (`Open`, UTC `TimestamptzCodec`, `WithinTx`, advisory-locked `Migrate`), `pkg/httpx`, `pkg/rpcx`, and `pkg/observability` (`module teamie/pkg`) across services rather than duplicating connection-pool, route-responder, or error-mapping boilerplate.
- **Architecture Gate (`arch_test.go`)**: Every service includes an AST-level `internal/arch_test.go` that fails `go test` if any layer violates its import boundary or if flat `.go` files appear in `internal/`.

---

## 3. Contract-First IDL & Code Generation

All external HTTP/SSE/WebSocket DTOs and internal Kitex RPC contracts live in [`contracts/`](contracts):

- `contracts/common/common.thrift` — shared base primitives
- `contracts/im/rpc.thrift` — internal IM Kitex RPC (`contracts/gen/im/kitex_gen`)
- `contracts/im/http_identity.thrift` — Identity HTTP DTOs, service interface & route bindings (`contracts/gen/im/hertz_gen/im/identity`)
- `contracts/im/http_chat.thrift` — Chat HTTP DTOs, service interface & route bindings (`contracts/gen/im/hertz_gen/im/chat`)
- `contracts/im/ws_realtime.thrift` — Realtime WebSocket frames, service interface & route bindings (`contracts/gen/im/hertz_gen/im/realtime`)
- `contracts/agent/rpc.thrift` — internal Agent Kitex RPC (`contracts/gen/agent/kitex_gen`)
- `contracts/agent/http_control.thrift` — Agent Control HTTP DTOs, service interface & route bindings (`contracts/gen/agent/hertz_gen/agent/control`)
- `contracts/agent/http_gateway.thrift` — Model Gateway HTTP/SSE DTOs, service interface & route bindings (`contracts/gen/agent/hertz_gen/agent/gateway`)
- `contracts/tpl/hz_package.yaml` — custom Hertz package template specification driving `tools/thrift-go-http-codegen`

Whenever you modify any `.thrift` file:

```sh
# Install the same pinned generators used by CI
go install github.com/cloudwego/kitex/tool/cmd/kitex@v0.16.4
go install github.com/cloudwego/thriftgo@v0.4.5

# Regenerate all Go (Kitex + Hertz + router_gen.go) bindings and the TypeScript SDK
./ops/codegen.sh

# Verify that generated Go HTTP route bindings and TypeScript SDK are in sync
node tools/thrift-go-http-codegen/index.mjs --check
node tools/thrift-ts-codegen/index.mjs --check
```

Full generation fails before changing bindings if either RPC generator is missing.
The Node-only `--check` commands above validate HTTP route bindings and the TypeScript SDK; CI additionally regenerates every Go contract and rejects drift.

Commit the `.thrift` change, regenerated `contracts/gen/{im,agent}` / `packages/api-client/src/generated.ts`, and implementation updates together in the same pull request.

---

## 4. Feature Flags, Authorization & Privacy

- **Backend Enforcement**: Feature flags ([`deploy/feature-flags.default.json`](deploy/feature-flags.default.json)) allow optional write capabilities to be gated safely. Always enforce feature flags on the backend; a frontend check is never an authorization boundary.
- **Read Preservation**: Disabling a write feature flag must never hide or break previously persisted content.
- **Session & Cache Isolation**: Logout, password changes, and cookie/session mismatches must clear all previous-account client caches (`chatStore`, composer drafts, query caches).
- **Agent Privacy**: Browser endpoints for Agents expose only the public profile, selected workspace visibility, and enabled state. Never expose internal Session state, runs, tasks, tool receipts, or operational error details to browser clients.

---

## 5. Observability & Secret Hygiene

- **Structured ECS Logging**: Configure logging once per service via [`pkg/observability`](pkg/observability) (native Zap + official `ecszap` encoder) and reuse the shared HTTP/Kitex context and completion hooks. See [`docs/architecture/logging.md`](docs/architecture/logging.md).
- **Strict Redaction**: Never log prompts, model reasoning, message or file bodies, tool arguments, shell commands, provider URLs, credentials, or unrestricted structs. Untrusted error strings and framework arguments must remain redacted or fingerprinted.
- **No Committed Secrets**: Never commit `.env` files, private keys, certificates, database dumps, or `.runtime/` artifacts.

---

## 6. Local Verification Matrix

A local build, passing unit tests, passing PostgreSQL/Docker integration suites, and a healthy production deployment are distinct claims—always verify and report the exact checks you ran.

### Frontend & Contract Sync
```sh
node tools/thrift-go-http-codegen/index.mjs --check
node tools/thrift-ts-codegen/index.mjs --check
cd apps/web
npm ci
npm run lint
npm test
npm run build
```

### Shared Mechanisms, Contracts & Peer Services
```sh
for mod in pkg contracts/gen/im contracts/gen/agent \
  services/identity services/chat services/realtime \
  services/agent-control services/agent-runtime services/llm-gateway services/sandbox; do
  (cd "$mod" && DATABASE_URL_TEST="postgres://127.0.0.1:5432/teamie_test?sslmode=disable" \
    TEAMIE_RUNTIME_TEST_DATABASE_URL="postgres://127.0.0.1:5432/runtime_test?sslmode=disable" \
    go test -race ./... && go vet ./... && go build ./...)
done
```

### Delivery & Manifest Tooling
```sh
python3 -m unittest discover -s ops -p 'test_*.py' -v
bash ops/check-compose.sh
```

> **Note**: Never point `DATABASE_URL_TEST` or `TEAMIE_RUNTIME_TEST_DATABASE_URL` at production databases. Without these environment variables, PostgreSQL integration tests skip explicitly; skipped integration tests do not constitute acceptance.

---

## 7. Branching, Pull Requests & Releases

1. **Trunk-Based Development**: `main` is the only ongoing development branch. Do not create dev, staging, or preview environments.
2. **Pull Requests**: Develop in focused `feat/*`, `fix/*`, or `refactor/*` branches and open a pull request targeting `main`. Ensure all CI checks and automated code review pass before squash-merging.
3. **Forward-Only Migrations**: Database migrations must be forward-compatible and protected by advisory locks. Update `SCHEMA_V2` in [`ops/manifest.py`](ops/manifest.py) and [`ops/ci-restore.sh`](ops/ci-restore.sh) when introducing a new schema version. Never pair an image rollback with an automatic database restore over user data.
4. **Immutable Releases**: Cut releases by pushing a new `release/vMAJOR.MINOR.PATCH` branch from the exact verified `main` commit (`git push origin main:refs/heads/release/vX.Y.Z`). Published `release/v*` branches and tags are permanent archives—never delete, force-push, or append commits to them. Any fix requires merging to `main` and cutting a new version.
