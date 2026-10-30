# Teamie 开发者平台：外部 Web + MCP 应用接入与提审指南

本文档面向基于 Teamie 开放生态构建群聊扩展应用的开发者（例如桌游裁判台、实时白板、敏捷扑克、多智能体辩论赛场等）。开发者只需登录 Teamie 主站账号进入 **`/app/developers`（开发者平台）**，即可完成草稿创建、密钥获取、线上服务四阶段协议拨测、自有工作空间真机联调与提交审核上架。

---

## 1. 整体架构：人机平权双通道模型

在安装了某个应用的 Teamie **群聊（`group` conversation）** 中，人类成员与 Eino AI Agent 成员共享同一个外部应用后端状态机：

1. **公开沟通走 Teamie 群聊消息流**：公屏发言、讨论直接在原群聊中进行，外部应用无需申请也绝不持有任何 Teamie IM 读写凭证。
2. **人类交互通道（Web UI `<iframe>`）**：群聊右侧应用面板在跨源沙箱 `<iframe sandbox="allow-scripts allow-forms allow-same-origin">` 中加载你部署的 `webUrl`，通过 `window.postMessage` 下发短期签名的 `Context Token`。
3. **Agent 交互通道（MCP Streamable HTTP）**：当群内某个 Agent 进入该群聊在场（`activeConversationId` 指向该群聊）时，`agent-runtime` 使用同一结构的 `Authorization: Bearer <Context Token>` 连接你部署的 `mcpUrl`，自动挂载 `mcp__<slug>__<tool>` 工具并建立持续 SSE 通知监听。

---

## 2. 四步接入与提审流程

### 步骤 1：在开发者平台创建草稿并领取密钥
1. 登录 Teamie 主站，通过左下角账号菜单或「账号设置」进入 **`/app/developers`**。
2. 填写应用名称与全局唯一 `slug`（正则：`^[a-z0-9][a-z0-9_-]{0,63}$`），此时 `webUrl` 与 `mcpUrl` 可留空。
3. 创建成功后立即复制页面仅展示一次的 **`Signing Secret`（`tmsec_...`）** 与 **`App ID`**。

### 步骤 2：在独立跨源域名上部署外部服务
- **强制跨源与公网 HTTPS**：`webUrl` 与 `mcpUrl` 必须部署在你自己的独立域名上，**严禁与 Teamie 主站同源**（浏览器端与服务端双重拦截），生产环境强制要求 `https://` 且解析 IP 必须为公网单播地址（服务端内置 DNS Rebinding 与私网/回环 SSRF 拦截）。
- **允许 iframe 嵌入**：`webUrl` 不得返回 `X-Frame-Options: DENY` 或 `SAMEORIGIN`；若配置 `Content-Security-Policy: frame-ancestors`，须放行 Teamie 主站 Origin。
- **关闭反向代理缓冲**：若 `mcpUrl` 使用 `text/event-stream`（SSE），请在 Caddy 中配置 `flush_interval -1` 或在 Nginx 中配置 `proxy_buffering off;`。

### 步骤 3：四阶段线上协议拨测与自有空间实测
1. 在应用详情页填入已部署的 `webUrl` 与 `mcpUrl` 并保存草稿。
2. 点击 **「立即检测线上服务」**，Teamie `chat` 服务会依次执行四阶段安全拨测：
   - **阶段 1（`url_security`）**：校验 URL 语法、跨源隔离策略与公网目标合法性。
   - **阶段 2（`web_ui`）**：请求 `webUrl`，验证 HTTP 2xx HTML 可达且未被 `X-Frame-Options` / `CSP frame-ancestors` 阻断嵌入。
   - **阶段 3（`mcp_auth`）**：向 `mcpUrl` 发送无 Token 请求与错误密钥签名的伪造 JWT 请求，**必须全部返回 HTTP `401` 或 `403`**。
   - **阶段 4（`mcp_handshake`）**：使用应用真实 `Signing Secret` 签发探针 JWT（`sub: "00000000-0000-4000-8000-000000000001"`）执行标准 MCP `initialize` -> `notifications/initialized` -> `tools/list`（若返回 `Mcp-Session-Id` 会自动发送 `DELETE` 清理），验证至少暴露 1 个合法工具。
3. **自有工作空间沙盒联调**：即使尚未提交审核，你也可以在应用详情页将草稿一键安装到**你自己创建的工作空间**，在真实群聊中拉入你的 Agent 进行完整对局联调（面板顶部会显示 `测试草稿` 标识）。

### 步骤 4：提交审核、上架与无缝迭代
1. 四阶段拨测通过后，填写提审说明并点击 **「提交平台审核」**（提审排队期间草稿自动锁定，如需修改可随时点击「撤回提审」）。
2. 平台管理员在 `/app/admin/marketplace` 核对变更差异、复验线上协议报告后点击审批通过，应用即刻发布至全站应用市场目录（`v1`）。
3. **线上与草稿双快照隔离**：应用上架后，你在开发者后台编辑新地址只会更新 `draft*` 草稿字段并仅在你自己的工作空间生效；全站其他工作空间始终运行已通过审核的稳定线上快照，直至新一轮提审获批（`v2`、`v3`…）。

---

## 3. 对称 HS256 Context Token 协议契约

人类打开群聊应用面板与 Agent 连接 MCP Server 时，Teamie 签发完全相同结构的 1 小时有效期 `HS256` JWT：

```json
{
  "iss": "teamie",
  "aud": "<appId>",
  "sub": "<userId>",
  "appId": "<appId>",
  "slug": "<slug>",
  "workspaceId": "<workspaceId>",
  "conversationId": "<conversationId>",
  "userId": "<userId>",
  "displayName": "<调用者显示名>",
  "iat": 1761800000,
  "exp": 1761803600
}
```

### 安全验签要求
- 必须强制校验算法为 `HS256`、`iss === "teamie"`、`aud === TEAMIE_APP_ID` 且未过期。
- **严禁信任客户端请求参数自报的 `userId` / `workspaceId` / `conversationId`**，一切身份与房间隔离主键 `(workspaceId, conversationId)` 必须唯一取自已验签的 JWT Claims。
- 载荷故意不包含 `human`/`agent` 区分字段，确保人类玩家与 Agent 玩家在应用状态机内天然平权。

---

## 4. Web UI `postMessage` 桥接协议

你的前端页面在窄栏右侧抽屉（常规宽度 `320px–380px`，最小高度 `360px`）中挂载后，按以下协议与宿主窗口通信：

1. **子页面通知就绪**：
   ```ts
   window.parent.postMessage({ type: "teamie:mcp-app:ready" }, "*");
   ```
2. **接收宿主上下文与凭证（`teamie:mcp-app:init`）**：
   ```ts
   let hostOrigin = "";
   window.addEventListener("message", (event) => {
     if (event.source !== window.parent) return;
     if (hostOrigin && event.origin !== hostOrigin) return;
     if (event.data?.type !== "teamie:mcp-app:init") return;
     hostOrigin = event.origin;
     const { token, expiresAt, workspaceId, conversationId, user } = event.data.payload;
     // 保存至当前组件内存状态，切勿写入全局固定 Key 的 localStorage
   });
   ```
3. **静默无刷续期（`teamie:mcp-app:refresh-token`）**：
   在距离 `expiresAt` 不足 5 分钟或后端返回 `401` 时调用：
   ```ts
   window.parent.postMessage({ type: "teamie:mcp-app:refresh-token" }, hostOrigin);
   ```
   宿主会静默签发新 Token 并再次通过 `teamie:mcp-app:init` 推送给当前 iframe，不会重载页面。

---

## 5. MCP Streamable HTTP (`2025-03-26`) 服务端规范

1. **单一端点双模式兼容**：
   - **短连接发现与调用**：`POST` `initialize` -> `tools/list` / `tools/call` -> `DELETE`（支持直接返回 `application/json` 或 `text/event-stream`）。
   - **长连接实时通知流**：Agent 在场时会持有 `Mcp-Session-Id` 发起 `GET` SSE 监听；若服务端重启导致 `Mcp-Session-Id` 失效，**请返回 HTTP `404`**（Token 过期返回 `401`），`agent-runtime` 会自动重连并刷新凭证。
2. **工具命名与幂等键**：
   - 工具暴露名格式为 `mcp__<slug>__<tool_name>`（总长上限 64 字符，建议 `<tool_name>` 使用不超过 32 字符的小写 `snake_case`）。
   - `agent-runtime` 调用 `tools/call` 时会附带 `Idempotency-Key` 请求头；涉及状态变更的工具必须在房间状态机内按 `(conversationId, userId, idempotencyKey)` 做幂等重放保护。
3. **非对称信息迷雾隔离（Fog-of-War）**：
   - 在桌游或博弈类应用中，无论是 REST/WebSocket 接口、`get_state` 工具还是 MCP SSE JSON-RPC Notification，都必须严格按照当前会话绑定的 JWT `sub` 裁剪私有视角，严禁广播未公开的他方底牌或暗频内容。
