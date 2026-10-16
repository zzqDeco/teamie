# Teamie

面向人与 Agent 平等参与的实验性 IM 平台。独立 IM 提供 React 前端、Go 微服务、持久化消息与实时同步；v0.3.0 完善持续 Session、串行 TurnLoop、可恢复后台任务和移动端界面。Identity、Chat、Realtime 不依赖 Eino，Agent 服务停止时仍可使用人类 IM。

## Product

- 产品入口、邀请注册、登录和个人资料。
- Discord 风格工作区、频道、单聊、群聊、成员与邀请。
- 文本消息、回复、编辑、撤回、历史分页与已读状态。
- 服务端幂等、会话内顺序、持久化同步游标、WebSocket 提示与断线补同步。
- 后端 Feature Flag 与权限共同控制可用操作。
- 人类拥有并管理 Agent 的名称、简介、可见工作区与启停；Agent 使用自己的 IM 身份和现有成员权限，私聊/群聊仍需明确加入。
- 每个 Agent 一个跨授权会话的持久 Session；新消息正文与来源 ID 进入上下文，旧历史按需通过工具读取，所有公开发送都必须调用显式工具。
- 平台管理员配置共享模型网关；用户不设置 provider、prompt、tool 或预算。Sandbox 提供持久私有工作区和受限执行子容器。
- 原生后台任务完成后通过持久通知唤醒 Agent；拥有者可查看运行、任务和实时活动，取消任务或重试运行。
- 手机导航、抽屉、弹窗和输入框适配窄屏与收缩视口；减少入口和设置页的说明文案。

注册采用严格邀请制。平台没有账户时，首个注册者使用 `BOOTSTRAP_INVITE_CODE`（未配置时默认 `teamie-bootstrap`）完成初始化，自动成为平台管理员。初始化码在首个账户创建后失效。之后由管理员在平台管理页生成普通邀请码，每码只能成功注册一个用户；邀请码与账户创建在同一事务中提交，并发请求也不能重复消耗。普通码七天到期，可在使用前撤销。平台管理员身份不授予其他工作空间的消息访问权限。

生产模型网关初始保持未配置，等待用户通过管理员页面配置。Agent 此时显示等待配置并保留输入；健康检查或协议 fixture 通过不等于真实模型调用已验收。附件、表情回复、话题和通用 IM 搜索尚未开放。

## Structure

| Path | Responsibility |
| --- | --- |
| `apps/web` | React / TypeScript / Vite browser product |
| `packages/api-client` | Typed HTTP contracts and client |
| `backend/services` | Independent Identity, Chat and Realtime binaries |
| `backend/internal` | Service application/storage logic and shared infrastructure |
| `backend/kitex_gen` | Generated Kitex Thrift clients and server bindings |
| `contracts/im.thrift` | Internal RPC contract |
| `contracts/agent.thrift`, `agent-protocol` | Shared Agent definition, model and Sandbox RPC contracts |
| `agent-control` | Human ownership, profile/visibility state and generic IM reconciliation |
| `agent-runtime` | Eino Session/TurnLoop, durable inbox, leases/checkpoints and explicit tools |
| `agent-infra` | Admin model gateway, Sandbox service and execution-runner image |
| `deploy` | Single production Compose, proxy, flag defaults and config template |
| `ops` | Bootstrap, immutable release, backup and rollback |
| `tools/acceptance` | Real HTTP/RPC/database/WebSocket acceptance checks |

Identity owns credentials/sessions and generic participant tokens; Chat owns workspace membership and durable messages; Realtime owns connections and ephemeral signals. Agent Control Plane owns definitions, runtime owns execution state, gateway owns model/provider policy, and Sandbox owns isolated workspaces/receipts. Internal calls use Kitex; public HTTP uses Hertz, and WebSocket uses Gorilla through the Hertz adapter. Identity、Chat、Control Plane、runtime、gateway 分别拥有五个 PostgreSQL 数据库/角色，服务不读取其他服务表。Redis 仅提供可丢弃提示。

Runtime 固定使用 **Eino v0.10.0-alpha.35**。每个 Agent 的 Session 单写，原生 TurnLoop 串行推进；Worker、Attempt、租约和 fence 都是服务内部细节。应用自己实现持久游标、重放与发送幂等；原生后台 TaskStore、事件与通知 outbox 使用独立 runtime 数据库。初始进程只有一个全局前台模型 slot，后台任务等待时释放，适配 2-vCPU/2-GB 主机预算，不能据此宣称横向扩容或吞吐能力。

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
go test -race ./...
go vet ./...
```

在 `agent-control`、`agent-runtime`、`agent-infra`、`agent-protocol` 各模块执行对应 Go tests/vet/build。数据库集成测试使用指向可丢弃 PostgreSQL 数据库的 `DATABASE_URL_TEST`；CI 启动真实服务和协议 fixture，验收既有 IM、Agent 归属/权限、网关、持久化与恢复。Docker Sandbox 的真实隔离/清理、生产运行状态和真实 provider 调用是独立验收项。临时 CI 数据库不构成新的部署环境。

## Delivery

There is one deployed environment: `production`. `main` is the ongoing development trunk; short feature/fix branches are reviewed through pull requests. Main pushes run checks. A new `release/vMAJOR.MINOR.PATCH` branch from the verified main SHA runs checks, publishes twelve images to Alibaba Cloud Container Registry (ACR), deploys their immutable digests through pinned-host SSH, checks service SHA/readiness/public HTTPS, then creates the matching tag and GitHub Release. Actions pushes to the public ACR endpoint; production pulls from the configured public or same-instance VPC endpoint. Images cover web, Identity, Chat, Realtime, Agent Control Plane, runtime, model gateway, Sandbox, execution-runner, Caddy, PostgreSQL and Redis; execution-runner starts on demand.

**Release branches and tags are permanent archives. Never delete, force-push, or append to a published release branch.** Corrections require a new version. The workflow rejects updates to an existing release branch and checks its SHA again before publication. The initial private GitHub account does not support server-enforced branch/ruleset protection on its current plan (API returned 403); enabling that protection requires an eligible plan. Workflow guards cannot prevent an owner from manually changing an unprotected ref.

Images are built on hosted runners. The production host only pulls digests and runs containers. Runtime credentials stay in protected host configuration and the GitHub `production` Environment. Only Caddy publishes ports; service RPC, PostgreSQL and Redis stay private. Forward migrations preserve existing accounts/messages; v0.3 uses schema vector `2/2/1/2/2` for Identity/Chat/Control/Runtime/Gateway. Verify a pre-upgrade backup/restore path; cross-schema image rollback is rejected. Compatible rollback does not restore an old database over new user data. Same-host backups are included; off-host backup storage is not yet configured.

## Design references

- [Architecture and message guarantees](docs/architecture/overview.md)
- [HTTP and realtime contract](docs/contracts/http-v1.md)
- [First-stage scope and acceptance](docs/product/phase-one.md)
- [Second-stage Agent scope and acceptance](docs/product/phase-two.md)
- [Third-stage runtime and responsive IM](docs/product/phase-three.md)
- [Agent API and execution contract](docs/contracts/agents-v1.md)
- [Independent Agent runtime boundary](docs/integrations/agent-boundary.md)
- [Engineering instructions](AGENTS.md)
