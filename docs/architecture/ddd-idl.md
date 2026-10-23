# Unified Thrift IDL & Homogeneous Clean DDD Architecture

This document defines the multi-dimensional architecture for Teamie's backend services and full-stack contracts, complementing [AGENTS.md](../../AGENTS.md) and [logging.md](./logging.md).

## 1. Core Principles

1. **Single Source of Truth (Thrift IDL)**:
   - All internal RPC contracts (`kitex`) and external HTTP/WebSocket/SSE wire contracts (`thriftgo` + `contracts/tpl/hz_package.yaml` + TypeScript client types) are defined in `contracts/{common,im,agent}/*.thrift`.
   - Generated Go code lives in isolated contract modules (`contracts/gen/im` and `contracts/gen/agent`), strictly separated from handwritten domain and application code.
   - `cwgo` is not used; `thriftgo`, `kitex`, and `tools/thrift-go-http-codegen` generate stateless DTOs, RPC interfaces, and HTTP route binders (`router_gen.go`).
2. **Symmetric 4-Layer Hexagonal Clean DDD Across 7 Peer Services**:
   - Every bounded context (`services/{identity,chat,realtime,agent-control,agent-runtime,llm-gateway,sandbox}`) is a peer Go module structured into four inward-pointing layers under `internal/`:
     - `domain`: Pure Go entities, value objects, domain invariants, domain errors, and outbound repository/port interfaces. Zero imports of `pgx`, `hertz`, `kitex`, `net/http`, `eino`, or generated wire structs.
     - `app`: Use-case orchestration and atomic transaction coordination (`TxManager`). Depends only on Go stdlib and `domain`.
     - `adapter`: Inbound driving adapters (`adapter/http`, `adapter/rpc`, `adapter/ws`, `adapter/cli`) that bind generated wire DTOs/route binders and invoke `app` use cases. Never imports `infra`.
     - `infra`: Outbound driven adapters (`infra/pg`, `infra/crypto`, `infra/flags`, `infra/redis`, `infra/rpcclient`, `infra/eino`, `infra/docker`, `infra/fs`, `infra/upstream`) that implement `domain` port interfaces. Never imports `app` or `adapter`.
   - Each service's `cmd/main.go` is a genuine Composition Root wiring `infra` -> `app` -> `adapter`. Flat non-test `.go` files in `internal/` are forbidden by `internal/arch_test.go`.
3. **Share Mechanism (`pkg/`), Never Share Data/Schema (`internal/infra/`)**:
   - Generic technical mechanisms (`pkg/postgres`, `pkg/httpx`, `pkg/rpcx`, `pkg/observability`, `pkg/config`, `pkg/crypto`, `pkg/imlimits`, `pkg/imtext`) live in the standalone `teamie/pkg` module.
   - Service-specific SQL queries, schema migrations, and anti-corruption RPC adapters stay private inside each service's `internal/infra/`.
   - Each stateful service connects only to its own PostgreSQL database and role.
4. **Independent IM Pillar & Automated CI Architecture Gates**:
   - `pkg`, `contracts/gen/im`, and `services/{identity,chat,realtime}` never import `eino`, `contracts/gen/agent`, or Agent service packages.
   - `make gen` (`ops/codegen.sh`) and `make check-arch` (`ops/check-architecture.sh`) enforce contract sync, IM independence, and DDD `domain` purity in CI.
