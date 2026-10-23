# Model Gateway & Sandbox (`teamie/agent-infra`)

Independent Go 1.27.2 module providing two strictly separated infrastructure services: **Model Gateway** (`cmd/llm-gateway`, `internal/gateway`) and **Sandbox** (`cmd/sandbox`, `internal/sandbox`).

- **Module & Contract Boundaries**:
  - Shared Agent RPC and HTTP contracts live in `../agent-protocol` (generated from `../contracts/agent.thrift` and `../contracts/agent_gateway_http.thrift` via `./ops/codegen.sh`).
  - Only generated Identity RPC client types are imported from `../backend`; no IM service imports `agent-infra`.
  - **4-Layer Hexagonal Clean DDD**: Both `internal/gateway` and `internal/sandbox` are structured into `domain` (stdlib-only entities, policies, and ports), `app` (use-case orchestration), `adapter` (`http`, `rpc`), and `infra` (`pg`, `crypto`, `upstream`, `docker`, `fs`). AST architecture tests (`arch_test.go`) enforce inward dependency direction and guarantee that `sandbox` never imports `gateway`.

See [`../docs/contracts/agents-v1.md`](../docs/contracts/agents-v1.md) for the service contracts, [`../docs/architecture/model-gateway.md`](../docs/architecture/model-gateway.md) for the OpenAI Agentic model boundary, and [`../ops/README.md`](../ops/README.md) for production operations.

---

## 1. Model Gateway (`cmd/llm-gateway`)

### Configuration & Administration
- **Listeners**: Private HTTP `:8086` / Kitex RPC `:8886`.
- **Required Environment**: `DATABASE_URL` (dedicated PostgreSQL database/role), `APP_ORIGIN`, `GATEWAY_MASTER_KEY` (64 hex characters for AES-256-GCM), `GATEWAY_RUNTIME_TOKEN` (at least 24 characters). `IDENTITY_RPC_ADDR` defaults to `identity:8881`.
- **Admin HTTP API (`/api/v1/admin/model-config`)**: Authenticated via human platform-admin cookies and Identity RPC, with Origin and CSRF protection on writes.
- **Secret Storage (`gateway_schema_migrations` v3)**:
  - Provider API keys and Tavily web-search API keys are encrypted with AES-256-GCM in Gateway's PostgreSQL database.
  - Omitted keys preserve saved credentials; explicit `clearApiKey` / `clearWebSearchApiKey` flags delete them; read responses expose only `hasApiKey` / `hasWebSearchApiKey`.
  - Includes `contextWindowTokens` metadata (default `16,384`, range `4,096`–`2,000,000`) returned on authenticated `Ping` for runtime context budgeting.
  - Saved configuration status is `unconfigured`, `disabled`, or `ready`. The explicit connection-test endpoint (`POST /api/v1/admin/model-config/test`) executes one bounded live probe against the saved, enabled provider using the `teamie-agentic-chat-v1` transport contract.

### Model Streaming, Web Research & Security Guards
- **Incremental Streaming (`POST /internal/v1/chat/stream`)**: Authenticated with `Authorization: Bearer <GATEWAY_RUNTIME_TOKEN>` and optional `X-Teamie-Model-Config-Id`. Forwards upstream OpenAI-compatible Chat Completions SSE chunks as NDJSON (`{type:"chunk",responseJson:"..."}`) terminated by `{type:"done"}` or `{type:"error",errorCode,errorMessage,statusCode}`.
- **Web Search & Extract (`POST /internal/v1/web/search`, `POST /internal/v1/web/extract`)**: Executes bounded Tavily search and content-extraction requests when enabled by administrator policy and `agents.web.v1`.
- **SSRF & Credential-Leakage Guards**:
  - Only public HTTPS endpoints are permitted; DNS resolution is verified against private/loopback/link-local ranges and pinned on dial; HTTP redirects and environment proxies are disabled.
  - Streaming and unary responses scan decoded text and tool-argument deltas (including nested JSON and Unicode escapes) for provider credentials, holding only ambiguous prefix chunks until disambiguated.
  - Usage records retain at most ~100,000 rows / 30 days via hourly pruning and never log prompts, completions, or secrets.

---

## 2. Sandbox Service (`cmd/sandbox`)

### Configuration & Runner Image Policy
- **Listeners**: Private HTTP `:8087` (`/healthz`, `/readyz`, `/version`) / Kitex RPC `:8887`.
- **Required Environment**: `SANDBOX_RUNTIME_TOKEN`, `SANDBOX_RUNNER_IMAGE`, and disjoint host paths `SANDBOX_WORKSPACE_ROOT` and `SANDBOX_STATE_ROOT`. Docker socket defaults to `/var/run/docker.sock`.
- **Immutable Runner References**: Production Compose pins `SANDBOX_RUNNER_IMAGE` to an immutable Alibaba Cloud Container Registry (ACR) digest (`<registry>/<namespace>/teamie-sandbox-runner@sha256:<64 lowercase hex characters>`). Tags, URL schemes, credentials, ports, and nested paths are rejected before Sandbox initialization.

### Container Isolation & Resource Policy
- Fixed runner image, one per-Agent hashed `/workspace` bind mount, `UID 10001`, `--network=none`, no secrets or Docker socket, read-only root filesystem, `--cap-drop=ALL`, `--security-opt=no-new-privileges`.
- Bounded per-container resources: `0.5 CPU`, `128 MiB` memory (no swap), `32 PIDs`, `128 FDs`, `8 MiB` file-size limit, `16 MiB` `/tmp`, `64 KiB` combined stdout/stderr cap, and a `2 GiB` host disk-reserve check every `100 ms`.

### Durable Task Lifecycle & Confined Workspace Filesystem
- **Async Execution (`Start` / `Get` / `ReadOutput` / `CancelTask`)**:
  - Fsyncs protected task intent (`0600` outside the Agent workspace) before `docker create`.
  - Terminal results (`completed`, `failed`, `canceled`, `unknown`) are fsynced before container removal. Completed, cleaned-up output expires after a 24-hour retention window (renewed on authenticated read).
  - On supervisor restart, Sandbox adopts only known containers matching exact durable names and Agent/Task labels, never rerunning unknown effects.
- **Confined Workspace Operations**:
  - Provides `ls`, `read`, `write`, `edit`, `delete`, `glob`, and `grep` under virtual `/workspace`, plus `ImportFile` / `ExportFile` for IM attachment and file-delivery flows.
  - Rejects path traversal and absolute host paths, enforcing `os.Root` confinement against symlink races and bounding reads/writes to `256 KiB` with fsynced mutation receipts.

---

## Verification

```sh
cd agent-infra
DATABASE_URL_TEST="postgres://127.0.0.1:5432/teamie_test?sslmode=disable" go test -race ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...
```

- `DATABASE_URL_TEST` enables isolated PostgreSQL encryption, persistence, and usage-pruning tests for `internal/gateway`.
- `SANDBOX_TEST_RUNNER_IMAGE` enables real Docker container isolation, resource-limit, and supervisor-restart tests for `internal/sandbox`.
