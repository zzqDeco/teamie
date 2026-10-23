# Unified Thrift IDL & Homogeneous Clean DDD Architecture

This document defines the target multi-dimensional architecture for Teamie's backend services and full-stack contracts, complementing [AGENTS.md](../../AGENTS.md) and [logging.md](./logging.md).

## 1. Core Principles

1. **Single Source of Truth (Thrift IDL)**:
   - All internal RPC contracts (`kitex`) and external HTTP/WebSocket/SSE wire contracts (`hz` + TypeScript client types) are defined in `contracts/`.
   - Generated code (`kitex_gen` and `hertz_gen`) is strictly isolated from handwritten domain and application code.
   - `cwgo` is not used; `hz` and `kitex` act purely as protocol and DTO compilers generating stateless server interfaces.
2. **Symmetric 4-Layer Hexagonal Clean DDD**:
   - Every bounded context (`identity`, `chat`, `realtime`, `agent-control`, `agent-runtime`, `llm-gateway`, `sandbox`) is structured into four inward-pointing layers:
     - `domain`: Pure Go entities, value objects, domain invariants, domain errors, and outbound repository/port interfaces. Zero imports of `pgx`, `hertz`, `kitex`, `net/http`, `eino`, or generated wire structs.
     - `app`: Use-case orchestration and atomic transaction coordination (`TxManager`). Depends only on `domain`.
     - `adapter`: Inbound driving adapters (`adapter/http`, `adapter/rpc`, `adapter/ws`, `adapter/worker`) that bind generated wire DTOs and invoke `app` use cases.
     - `infra`: Outbound driven adapters (`infra/pg`, `infra/crypto`, `infra/rpcclient`, `infra/eino`, `infra/docker`, `infra/upstream`) that implement `domain` port interfaces.
3. **Share Mechanism (`pkg/`), Never Share Data/Schema (`internal/infra/`)**:
   - Generic technical mechanisms (`pkg/postgres`, `pkg/httpx`, `pkg/observability`) are written once in `pkg/`.
   - Service-specific SQL queries, schema migrations, and anti-corruption RPC adapters stay private inside each service's `internal/infra/`.
   - Each service connects only to its own PostgreSQL database and role.
4. **Independent IM Pillar**:
   - `identity`, `chat`, and `realtime` never import `eino`, Agent runtime types, or Agent contracts.
