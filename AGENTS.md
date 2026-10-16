# Teamie engineering instructions

- Build the IM product first. Identity, Chat and Realtime must not import Eino or Agent runtime types. Future Agent integration uses the same authenticated participant and public IM API/event contracts.
- React/TypeScript frontend; Go Hertz HTTP/WebSocket and Kitex internal RPC with Thrift. Read current framework documentation through Context7 before framework-specific work.
- The only deployed environment is `production`. CI jobs may use disposable database containers. Do not introduce dev, staging or preview deployments.
- `main` is the only ongoing development branch. `release/vx.x.x` branches are permanent archives. Never delete or rewrite a published release branch/tag. Fixes require a new version.
- Read `docs/contracts/http-v1.md` before implementation. Coordinate contract changes with dependent components.
- Each service owns its data. Other services use RPC; never read another service's database tables. Persistent IM messages/sync events live in PostgreSQL; Redis is disposable.
- Feature flags are enforced by the backend. Disabled write features must not hide persisted content.
- Never commit credentials, private keys, production configuration, database exports, generated runtime data or local environment files.
- Keep service ports and databases on the private container network. Only the edge publishes HTTP/HTTPS.
- Meaningful tests should exercise authorization, message idempotence, ordering and reconnect behavior. Build/lint alone does not prove the product works.
