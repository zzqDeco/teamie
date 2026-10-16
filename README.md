# Teamie

面向人与 Agent 平等参与的实验性 IM 平台。第一阶段实现独立 IM：React 前端、Go 微服务、持久化消息与实时同步。Agent runtime 在后续阶段独立接入。

## Product

- 产品入口、邀请注册、登录和个人资料。
- Discord 风格工作区、频道、单聊、群聊、成员与邀请。
- 文本消息、回复、编辑、撤回、历史分页与已读状态。
- 服务端幂等、会话内顺序、持久化同步游标、WebSocket 提示与断线补同步。
- 后端 Feature Flag 与权限共同控制可用操作。

注册采用严格邀请制。平台没有账户时，首个注册者使用 `BOOTSTRAP_INVITE_CODE`（未配置时默认 `teamie-bootstrap`）完成初始化，自动成为平台管理员。初始化码在首个账户创建后失效。之后由管理员在平台管理页生成普通邀请码，每码只能成功注册一个用户；邀请码与账户创建在同一事务中提交，并发请求也不能重复消耗。普通码七天到期，可在使用前撤销。平台管理员身份不授予其他工作空间的消息访问权限。

附件、表情回复、话题、搜索和 Agent 执行尚未开放。客户端不会把未实现能力显示为可用操作。

## Structure

| Path | Responsibility |
| --- | --- |
| `apps/web` | React / TypeScript / Vite browser product |
| `packages/api-client` | Typed HTTP contracts and client |
| `backend/services` | Independent Identity, Chat and Realtime binaries |
| `backend/internal` | Service application/storage logic and shared infrastructure |
| `backend/kitex_gen` | Generated Kitex Thrift clients and server bindings |
| `contracts/im.thrift` | Internal RPC contract |
| `deploy` | Single production Compose, proxy, flag defaults and config template |
| `ops` | Bootstrap, immutable release, backup and rollback |
| `tools/acceptance` | Real HTTP/RPC/database/WebSocket acceptance checks |

Identity owns credentials/sessions; Chat owns workspace membership and durable messages; Realtime owns connections and ephemeral signals. Internal calls use Kitex. Public HTTP uses Hertz, and WebSocket uses Gorilla through the Hertz adapter. Each persistent service owns its PostgreSQL database/role. Redis is disposable.

## Verification

```sh
cd apps/web
npm ci
npm run lint
npm test
npm run build
```

```sh
cd backend
go test ./...
go vet ./...
```

Database integration tests require `DATABASE_URL_TEST` pointing to a disposable PostgreSQL database. CI provisions disposable PostgreSQL/Redis, runs the database tests and starts all three service processes for `tools/acceptance/im_acceptance.py`. These checks do not create another deployed environment.

## Delivery

There is one deployed environment: `production`. `main` is the ongoing development trunk; short feature/fix branches are reviewed through pull requests. Main pushes run checks. A new `release/vMAJOR.MINOR.PATCH` branch from the verified main SHA runs checks, publishes seven immutable GHCR image references, deploys them through pinned-host SSH, checks service SHA/readiness/public HTTPS, then creates the matching tag and GitHub Release.

**Release branches and tags are permanent archives. Never delete, force-push, or append to a published release branch.** Corrections require a new version. The workflow rejects updates to an existing release branch and checks its SHA again before publication. The initial private GitHub account does not support server-enforced branch/ruleset protection on its current plan (API returned 403); enabling that protection requires an eligible plan. Workflow guards cannot prevent an owner from manually changing an unprotected ref.

Images are built on hosted runners. The production host only pulls digests and runs containers. Runtime credentials stay in protected host configuration and the GitHub `production` Environment. Only Caddy publishes ports; service RPC, PostgreSQL and Redis stay private. Release rollback restores compatible images without restoring an old database over new user data. Same-host backups are included; off-host backup storage is not yet configured.

## Design references

- [Architecture and message guarantees](docs/architecture/overview.md)
- [HTTP and realtime contract](docs/contracts/http-v1.md)
- [First-stage scope and acceptance](docs/product/phase-one.md)
- [Independent Agent runtime boundary](docs/integrations/agent-boundary.md)
- [Engineering instructions](AGENTS.md)
