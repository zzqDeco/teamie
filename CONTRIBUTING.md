# Contributing to Teamie

Before changing behavior or contracts, read:
- [`AGENTS.md`](AGENTS.md) — non-negotiable engineering and product invariants
- [`docs/architecture/overview.md`](docs/architecture/overview.md) — service topology and delivery guarantees
- [`docs/architecture/ddd-idl.md`](docs/architecture/ddd-idl.md) — 4-layer Hexagonal Clean DDD and unified Thrift IDL architecture
- [`docs/contracts/http-v1.md`](docs/contracts/http-v1.md) and [`docs/contracts/agents-v1.md`](docs/contracts/agents-v1.md) — HTTP, WebSocket, and Agent contracts

---

## 1. Service Ownership & Module Boundaries

- **IM Independence**: `backend/services/{identity,chat,realtime}` and `backend/internal/{identity,chat,realtime}` must never import CloudWeGo Eino, `agent-*` modules, or model provider configuration. Agents integrate strictly as authenticated principals through generic IM APIs and RPC contracts.
- **Database Ownership**: `identity`, `chat`, `agent-control`, `agent-runtime`, and `llm-gateway` each own an isolated PostgreSQL database and application role. `sandbox` owns isolated host workspace directories and durable execution receipts. Services communicate exclusively over RPC or authenticated HTTP and never query another service's tables.
- **Gateway vs. Sandbox Isolation**: Within `agent-infra`, `internal/gateway` and `internal/sandbox` are independent bounded contexts and must never import each other.

---

## 2. 4-Layer Hexagonal Clean DDD Rules

Every bounded context (`backend/internal/{identity,chat,realtime}`, `agent-control/internal/control`, `agent-runtime/internal/engine`, `agent-infra/internal/{gateway,sandbox}`) follows the same 4-layer package structure and inward dependency rule:

```text
adapter/{http,ws,rpc} ──► app ──► domain ◄── infra/{pg,fs,crypto,docker,eino,rpcclient,upstream}
```

| Layer | Responsibility | Allowed Imports |
| :--- | :--- | :--- |
| `domain/` | Entities, value objects, domain invariants, coded `domain.Error`, and outbound port interfaces (`Repository`, `TxManager`, gateway ports) | **Go standard library only** (zero third-party, `pgx`, `hertz`, `kitex`, `eino`, or cross-package imports) |
| `app/` | Application use-case orchestration and transaction boundaries (`TxManager.WithinTx`) | **Go standard library + own `domain` package only** |
| `adapter/` | Inbound transport adapters (`http`, `ws`, `rpc`) binding generated `hertz_gen` / `kitex_gen` DTOs and translating `*domain.Error` via `pkg/httpx` | `app`, `domain`, `hertz_gen`, `kitex_gen`, `pkg/httpx`, `pkg/observability` |
| `infra/` | Outbound port implementations (`pg`, `fs`, `crypto`, `docker`, `eino`, `rpcclient`, `upstream`) | `domain`, `pkg/postgres`, `pkg/observability`, and infrastructure SDKs (`pgx`, `eino`, `kitex`). **Never imports `app` or `adapter`.** |

- **Shared Technical Mechanisms**: Reuse `backend/pkg/postgres` (`Open`, UTC `TimestamptzCodec`, `WithinTx`, advisory-locked `Migrate`) and `backend/pkg/httpx` across services rather than duplicating connection-pool or error-mapping boilerplate.
- **Architecture Gate (`arch_test.go`)**: Every bounded context includes an AST-level `arch_test.go` that fails `go test` if any layer violates its import boundary.

---

## 3. Contract-First IDL & Code Generation

All external HTTP/SSE/WebSocket DTOs and internal Kitex RPC contracts live in [`contracts/`](contracts):

- `contracts/im.thrift` — internal IM Kitex RPC (`backend/kitex_gen`)
- `contracts/im_identity_http.thrift` — Identity HTTP DTOs & service interface (`backend/hertz_gen/im/identity`)
- `contracts/im_chat_http.thrift` — Chat HTTP DTOs & service interface (`backend/hertz_gen/im/chat`)
- `contracts/im_realtime_ws.thrift` — Realtime WebSocket frames & service interface (`backend/hertz_gen/im/realtime`)
- `contracts/agent.thrift` — internal Agent Kitex RPC (`agent-protocol/kitex_gen`)
- `contracts/agent_control_http.thrift` — Agent Control HTTP DTOs & service interface (`agent-protocol/hertz_gen/agent/control`)
- `contracts/agent_gateway_http.thrift` — Model Gateway HTTP/SSE DTOs & service interface (`agent-protocol/hertz_gen/agent/gateway`)

Whenever you modify any `.thrift` file:

```sh
# Regenerate all Go (Kitex + Hertz) bindings and the TypeScript SDK
./ops/codegen.sh

# Verify that packages/api-client/src/generated.ts is in sync with contracts/*.thrift
node tools/thrift-ts-codegen/index.mjs --check
```

Commit the `.thrift` change, regenerated `kitex_gen` / `hertz_gen` / `packages/api-client/src/generated.ts`, and implementation updates together in the same pull request.

---

## 4. Feature Flags, Authorization & Privacy

- **Backend Enforcement**: Feature flags ([`deploy/feature-flags.default.json`](deploy/feature-flags.default.json)) allow optional write capabilities to be gated safely. Always enforce feature flags on the backend; a frontend check is never an authorization boundary.
- **Read Preservation**: Disabling a write feature flag must never hide or break previously persisted content.
- **Session & Cache Isolation**: Logout, password changes, and cookie/session mismatches must clear all previous-account client caches (`chatStore`, composer drafts, query caches).
- **Agent Privacy**: Browser endpoints for Agents expose only the public profile, selected workspace visibility, and enabled state. Never expose internal Session state, runs, tasks, tool receipts, or operational error details to browser clients.

---

## 5. Observability & Secret Hygiene

- **Structured ECS Logging**: Configure logging once per service via [`backend/pkg/observability`](backend/pkg/observability) (native Zap + official `ecszap` encoder) and reuse the shared HTTP/Kitex context and completion hooks. See [`docs/architecture/logging.md`](docs/architecture/logging.md).
- **Strict Redaction**: Never log prompts, model reasoning, message or file bodies, tool arguments, shell commands, provider URLs, credentials, or unrestricted structs. Untrusted error strings and framework arguments must remain redacted or fingerprinted.
- **No Committed Secrets**: Never commit `.env` files, private keys, certificates, database dumps, or `.runtime/` artifacts.

---

## 6. Local Verification Matrix

A local build, passing unit tests, passing PostgreSQL/Docker integration suites, and a healthy production deployment are distinct claims—always verify and report the exact checks you ran.

### Frontend & Contract Sync
```sh
node tools/thrift-ts-codegen/index.mjs --check
cd apps/web
npm ci
npm run lint
npm test
npm run build
```

### IM Backend (`backend`)
```sh
cd backend
DATABASE_URL_TEST="postgres://127.0.0.1:5432/teamie_test?sslmode=disable" go test -race ./...
go vet ./...
go build ./services/identity/cmd ./services/identityctl/cmd ./services/chat/cmd ./services/realtime/cmd
```

### Agent Modules (`agent-protocol`, `agent-control`, `agent-infra`, `agent-runtime`)
```sh
cd agent-protocol && go test -race ./... && go vet ./...
cd ../agent-control && DATABASE_URL_TEST="postgres://127.0.0.1:5432/teamie_test?sslmode=disable" go test -race ./... && go vet ./... && go build ./...
cd ../agent-infra && DATABASE_URL_TEST="postgres://127.0.0.1:5432/teamie_test?sslmode=disable" go test -race ./... && go vet ./... && go build ./...
cd ../agent-runtime && TEAMIE_RUNTIME_TEST_DATABASE_URL="postgres://127.0.0.1:5432/runtime_test?sslmode=disable" go test -race ./... && go vet ./... && go build ./...
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
2. **Pull Requests**: Develop in focused `feat/*`, `fix/*`, or `refactor/*` branches and open a pull request targeting `main`. Ensure all 13 CI checks and automated code review pass before squash-merging.
3. **Forward-Only Migrations**: Database migrations must be forward-compatible and protected by advisory locks. Update `SCHEMA_V2` in [`ops/manifest.py`](ops/manifest.py) and [`ops/ci-restore.sh`](ops/ci-restore.sh) when introducing a new schema version. Never pair an image rollback with an automatic database restore over user data.
4. **Immutable Releases**: Cut releases by pushing a new `release/vMAJOR.MINOR.PATCH` branch from the exact verified `main` commit (`git push origin main:refs/heads/release/vX.Y.Z`). Published `release/v*` branches and tags are permanent archives—never delete, force-push, or append commits to them. Any fix requires merging to `main` and cutting a new version.
