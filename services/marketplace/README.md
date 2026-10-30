# Teamie Marketplace Service (`services/marketplace`)

Standalone Clean DDD microservice owning the Marketplace catalog, Developer Portal, Admin Review & Governance workflow, workspace app installations, symmetric HS256 group conversation Context Token minting, and outbound 4-stage Web UI + MCP Streamable HTTP live protocol verification.

## Isolation Boundaries

- **Dedicated Database**: Owns PostgreSQL database and role `teamie_marketplace` (`marketplace_schema_migrations`). Never reads or writes another service's database tables.
- **RPC Authority**: Delegates session/principal authentication and profile resolution to `identity:8881`, and workspace/conversation membership + role checks to `chat:8882` via Kitex Thrift RPC.
- **Egress Boundary**: Runs on `networks: [private, egress]` so `internal/infra/probe` can verify external HTTPS `webUrl` and `mcpUrl` endpoints with SSRF/DNS-rebinding defenses, while `services/chat` remains strictly on the internal `private` network with zero internet access.
