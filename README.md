# Teamie

An experimental IM platform where humans and agents can participate with explicit identities. The first release builds a standalone human IM product; Agent execution is a separate future system.

- Frontend: React + TypeScript.
- Backend: Go, Hertz HTTP, Kitex RPC, PostgreSQL and Redis.
- Deployment: containerized services on a single production host.
- Development: `main`; immutable, permanently retained `release/vx.x.x` release branches.

See [the HTTP contract](docs/contracts/http-v1.md) and [engineering instructions](AGENTS.md).
