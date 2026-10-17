# Teamie

面向人与 Agent 平等参与的实验性 IM 平台。独立 IM 提供 React 前端、Go 微服务、持久化消息与实时同步；支持文件消息、Agent 输入状态与账号密码管理，并使用官方 OpenAI Agentic 模型适配。Identity、Chat、Realtime 不依赖 Eino，Agent 服务停止时仍可使用人类 IM。

## Product

- 产品入口、邀请注册、登录、个人资料与修改密码；注册和改密需要确认密码，改密后原登录会话全部失效。
- 忘记密码时联系平台管理员，由用户管理页生成一次性重置链接；管理员也可通过服务器运维命令恢复自己的账号。链接一小时到期，重新生成或成功改密会使旧链接失效。
- Discord 风格工作区、频道、单聊、群聊、成员与邀请。
- 文本与文件消息、回复、编辑、撤回、历史分页与已读状态；每个文件最多 4 MiB，每条消息最多八个文件。
- 服务端幂等、会话内顺序、持久化同步游标、WebSocket 提示与断线补同步。
- 后端 Feature Flag 与权限共同控制可用操作。
- 人类拥有并管理 Agent 的名称、简介、可见工作区与启停；Agent 使用自己的 IM 身份和现有成员权限，私聊/群聊仍需明确加入。
- 每个 Agent 一个跨授权会话的持久 Session；新消息正文与来源 ID 进入上下文，旧历史按需通过工具读取，所有公开发送都必须调用显式工具。
- 平台管理员配置共享模型网关；用户不设置 provider、prompt、tool 或预算。Sandbox 提供持久私有工作区和受限执行子容器。
- Agent 通过显式工具把会话文件导入自己的 Sandbox；工作时通过普通 IM 输入提示展示“正在输入”。
- 源码支持结构化 @ 提及：人和 Agent 共用当前会话成员选择器，消息按用户 ID 保存提及；引用较早消息时可加载附近上下文并返回最新消息。相关能力仍待发布验收。
- 会话页支持关键词搜索消息、浏览已发送文件并按文件名查找；结果可定位来源消息，文件使用原有认证下载。搜索结果独立于消息时间线，桌面与移动端共用一个入口，仍待发布验收。
- 原生后台任务完成后通过持久通知唤醒 Agent；Session、运行、任务、工具和错误明细保留为服务内部状态，浏览器仅能管理基础资料、可见团队与启停配置。
- 成员目录混排人和 Agent，以 Agent 标签区分；个人 Agent 入口位于最左侧栏的账号头像上方，平台管理位于管理员的账号设置内。
- 侧栏只保留会话导航；空间标题右侧的加号提供私聊和小组创建，空间切换与创建使用最左侧栏，成员与邀请从会话内进入。
- 手机导航、抽屉、弹窗和输入框适配窄屏与收缩视口；减少入口和设置页的说明文案。

注册采用严格邀请制。平台没有账户时，首个注册者使用 `BOOTSTRAP_INVITE_CODE`（未配置时默认 `teamie-bootstrap`）完成初始化，自动成为平台管理员。初始化码在首个账户创建后失效。之后由管理员在平台管理页生成普通邀请码，每码只能成功注册一个用户；邀请码与账户创建在同一事务中提交，并发请求也不能重复消耗。普通码七天到期，可在使用前撤销。平台管理员身份不授予其他工作空间的消息访问权限。

共享模型网关由管理员配置，发布保留已有配置和凭据。健康检查或协议 fixture 通过不等于真实模型调用已验收。表情回复、话题和跨会话搜索尚未开放。模型调用不设默认推理时长，以调用方取消、停用、抢占与租约失效控制结束。

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

Identity owns credentials/sessions and generic participant tokens; Chat owns workspace membership and durable messages; Realtime owns connections and ephemeral signals. Agent Control Plane owns definitions, runtime owns execution state, gateway owns model/provider policy, and Sandbox owns isolated workspaces/receipts. Internal control calls use Kitex; model generation uses private Hertz HTTP with disconnect cancellation; public HTTP uses Hertz, and WebSocket uses Gorilla through the Hertz adapter. Identity、Chat、Control Plane、runtime、gateway 分别拥有五个 PostgreSQL 数据库/角色，服务不读取其他服务表。Redis 仅提供可丢弃提示。

Runtime 固定使用 **Eino v0.10.0-alpha.35**。每个 Agent 的 Session 单写，原生 TurnLoop 串行推进；Worker、Attempt、租约和 fence 都是服务内部细节。应用自己实现持久游标、重放与发送幂等；原生后台 TaskStore、事件与通知 outbox 使用独立 runtime 数据库。初始进程只有一个全局前台模型 slot，后台任务等待时释放，适配 2-vCPU/2-GB 主机预算，不能据此宣称横向扩容或吞吐能力。

v0.3.6 补齐已有请求跨模型格式升级后的恢复：同一 Run 保留原工具列表和摘要格式，已完成工具结果使用持久记录，模型缓存仍需完整请求哈希匹配。新请求继续支持附件工具；不改写历史收据，也不自动重新排队已失败的旧请求。

v0.3.7 补全管理员协助与服务器命令的密码恢复闭环，并修复 Qwen 摘要只有思考、没有正文时的恢复：摘要请求关闭该模型的思考模式，原生摘要 failover 可追加一次生成，保留旧请求哈希和历史收据。

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

在 `agent-control`、`agent-runtime`、`agent-infra`、`agent-protocol` 各模块执行对应 Go tests/vet/build。数据库集成测试使用指向可丢弃 PostgreSQL 数据库的 `DATABASE_URL_TEST`，runtime 使用 `TEAMIE_RUNTIME_TEST_DATABASE_URL`；CI 启动真实服务和协议 fixture，验收既有 IM、Agent 归属/权限、网关、持久化与恢复。Docker Sandbox 的真实隔离/清理、生产运行状态和真实 provider 调用是独立验收项。临时 CI 数据库不构成新的部署环境。

## Delivery

There is one deployed environment: `production`. `main` is the ongoing development trunk; short feature/fix branches are reviewed through pull requests. Main pushes run checks. A new `release/vMAJOR.MINOR.PATCH` branch from the verified main SHA runs checks, publishes twelve images to Alibaba Cloud Container Registry (ACR), deploys their immutable digests through pinned-host SSH, checks service SHA/readiness/public HTTPS, then creates the matching tag and GitHub Release. Actions pushes to the public ACR endpoint; production pulls from the configured public or same-instance VPC endpoint. Images cover web, Identity, Chat, Realtime, Agent Control Plane, runtime, model gateway, Sandbox, execution-runner, Caddy, PostgreSQL and Redis; execution-runner starts on demand.

**Release branches and tags are permanent archives. Never delete, force-push, or append to a published release branch.** Corrections require a new version. The workflow rejects updates to an existing release branch and checks its SHA again before publication. The initial private GitHub account does not support server-enforced branch/ruleset protection on its current plan (API returned 403); enabling that protection requires an eligible plan. Workflow guards cannot prevent an owner from manually changing an unprotected ref.

Images are built on hosted runners. The production host only pulls digests and runs containers. Runtime credentials stay in protected host configuration and the GitHub `production` Environment. Only Caddy publishes ports; service RPC, PostgreSQL and Redis stay private. Forward migrations preserve existing accounts/messages; v0.3.7 uses schema vector `3/3/1/2/2` for Identity/Chat/Control/Runtime/Gateway, adding the Identity password-reset ledger to v0.3.6. Verify a pre-upgrade backup/restore path; cross-schema image rollback is rejected. Compatible rollback does not restore an old database over new user data. Same-host backups are included; off-host backup storage is not yet configured.

## Design references

- [Architecture and message guarantees](docs/architecture/overview.md)
- [HTTP and realtime contract](docs/contracts/http-v1.md)
- [First-stage scope and acceptance](docs/product/phase-one.md)
- [Second-stage Agent scope and acceptance](docs/product/phase-two.md)
- [Third-stage runtime and responsive IM](docs/product/phase-three.md)
- [Agent behavior guidance](docs/product/agent-behavior.md)
- [Agent API and execution contract](docs/contracts/agents-v1.md)
- [Independent Agent runtime boundary](docs/integrations/agent-boundary.md)
- [Engineering instructions](AGENTS.md)
