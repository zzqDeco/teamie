import { useState } from "react";
import { Link } from "react-router-dom";
import { BookOpen, CheckCheck, Code2, Copy } from "lucide-react";
import { Button } from "../ui";
import { ICON } from "../icons";

type HandbookTopicId =
  | "architecture"
  | "deployment"
  | "jwt"
  | "webui"
  | "mcp"
  | "fog"
  | "checklist";

interface CodeSnippet {
  id: string;
  label: string;
  code: string;
}

const SNIPPETS: Record<string, CodeSnippet> = {
  caddy: {
    id: "caddy",
    label: "Caddyfile（独立跨源 HTTPS + 允许 iframe + SSE 无缓冲）",
    code: `werewolf.example.com {
  header {
    -X-Frame-Options
    Content-Security-Policy "frame-ancestors https://teamie.example.com"
    Access-Control-Allow-Origin "*"
    Access-Control-Allow-Headers "Authorization, Content-Type, Mcp-Session-Id, Idempotency-Key"
    Access-Control-Allow-Methods "GET, POST, DELETE, OPTIONS"
    Access-Control-Expose-Headers "Mcp-Session-Id"
  }
  reverse_proxy /mcp* 127.0.0.1:8090 {
    flush_interval -1
  }
  reverse_proxy /* 127.0.0.1:8090
}`,
  },
  jwtNode: {
    id: "jwtNode",
    label: "Node.js / TypeScript 校验 HS256 Context Token（jose）",
    code: `import { jwtVerify } from "jose";

const secret = new TextEncoder().encode(process.env.TEAMIE_APP_SIGNING_SECRET!);
const expectedAppId = process.env.TEAMIE_APP_ID!;

export async function verifyTeamieToken(authHeader?: string) {
  if (!authHeader?.startsWith("Bearer ")) {
    throw new Error("missing_bearer_token");
  }
  const token = authHeader.slice("Bearer ".length).trim();
  const { payload } = await jwtVerify(token, secret, {
    algorithms: ["HS256"],
    issuer: "teamie",
    audience: expectedAppId,
  });
  return {
    userId: String(payload.sub),
    displayName: String(payload.displayName || ""),
    workspaceId: String(payload.workspaceId),
    conversationId: String(payload.conversationId),
    appId: String(payload.appId),
    slug: String(payload.slug),
  };
}`,
  },
  jwtGo: {
    id: "jwtGo",
    label: "Go 校验 HS256 Context Token（golang-jwt/jwt/v5）",
    code: `claims := jwt.MapClaims{}
tok, err := jwt.ParseWithClaims(rawBearer, claims, func(t *jwt.Token) (any, error) {
    if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
        return nil, errors.New("unexpected alg")
    }
    return []byte(os.Getenv("TEAMIE_APP_SIGNING_SECRET")), nil
}, jwt.WithIssuer("teamie"), jwt.WithAudience(os.Getenv("TEAMIE_APP_ID")))
if err != nil || !tok.Valid {
    http.Error(w, "unauthorized", http.StatusUnauthorized)
    return
}
// 房间隔离主键：(claims["workspaceId"], claims["conversationId"])；玩家身份主键：claims["sub"]`,
  },
  webBridge: {
    id: "webBridge",
    label: "Web UI iframe postMessage 握手与无刷续期桥接",
    code: `let hostOrigin = "";
let contextToken = "";

window.addEventListener("message", (event) => {
  if (event.source !== window.parent) return;
  if (hostOrigin && event.origin !== hostOrigin) return;
  const msg = event.data;
  if (!msg || msg.type !== "teamie:mcp-app:init") return;
  hostOrigin = event.origin;
  contextToken = msg.payload.token;
  // msg.payload 包含: appId, slug, name, webUrl, mcpUrl, token, expiresAt,
  // workspaceId, conversationId, user: { id, displayName }
  // 请保存在当前实例内存中，切勿写入全局固定 Key 的 localStorage
  connectRoomState(msg.payload);
});

// 1. 页面加载完成后通知宿主下发初始 Context Token
window.parent.postMessage({ type: "teamie:mcp-app:ready" }, "*");

// 2. 在 expiresAt 到期前 5 分钟主动向宿主申请无刷新续期
export function requestFreshToken() {
  if (hostOrigin) {
    window.parent.postMessage({ type: "teamie:mcp-app:refresh-token" }, hostOrigin);
  }
}`,
  },
  mcpTools: {
    id: "mcpTools",
    label: "MCP Streamable HTTP (2025-03-26) 核心协议约定",
    code: `// 1. 所有 POST / GET / DELETE 请求必须携带并校验 Authorization: Bearer <token>
//    未授权或签名伪造必须返回 HTTP 401 或 403；Token 过期返回 HTTP 401。
// 2. 初始化握手: method = "initialize" (protocolVersion: "2025-03-26")
//    可选返回响应头 Mcp-Session-Id: <session-id>；后续请求会回传该头，会话结束发送 DELETE。
//    若服务端重启导致 Mcp-Session-Id 失效，请返回 HTTP 404，Agent Runtime 会自动重连握手。
// 3. 工具列表: method = "tools/list"
//    平台自动将每个工具挂载为: mcp__<slug>__<tool_name>（总长度上限 64 字符）
// 4. 工具调用幂等: Agent Runtime 调用 tools/call 时附带 Idempotency-Key 请求头，
//    写操作（如夜间刀人、投票、发言确认）必须在房间状态机内按 (cid, sub, idempotencyKey) 幂等去重！`,
  },
};

function SnippetBlock({ snippet }: { snippet: CodeSnippet }) {
  const [copied, setCopied] = useState(false);
  return (
    <div>
      <div className="dev-code-header">
        <span>{snippet.label}</span>
        <Button
          type="button"
          variant="secondary"
          size="sm"
          onClick={() => {
            void navigator.clipboard?.writeText(snippet.code).then(() => {
              setCopied(true);
              window.setTimeout(() => setCopied(false), 1800);
            });
          }}
        >
          {copied ? <CheckCheck size={ICON.xs} /> : <Copy size={ICON.xs} />}
          {copied ? "已复制" : "复制代码"}
        </Button>
      </div>
      <pre className="dev-code-block">{snippet.code}</pre>
    </div>
  );
}

export function DeveloperHandbook() {
  const [topic, setTopic] = useState<HandbookTopicId>("architecture");

  const tabs: { id: HandbookTopicId; label: string }[] = [
    { id: "architecture", label: "1. 架构总览" },
    { id: "deployment", label: "2. 跨源部署与反向代理" },
    { id: "jwt", label: "3. 对称身份与 Token 验签" },
    { id: "webui", label: "4. Web UI 桥接协议" },
    { id: "mcp", label: "5. MCP Server 规范" },
    { id: "fog", label: "6. 视野隔离与防作弊" },
    { id: "checklist", label: "7. 提审自检清单" },
  ];

  return (
    <div className="admin-view">
      <div className="admin-heading">
        <div>
          <h1>
            开发者接入与提审手册
            <span className="platform-role-badge">
              <BookOpen size={ICON.xs} />
              协议 v1
            </span>
          </h1>
          <p>
            本手册面向第三方应用开发者，完整说明如何自部署符合 Teamie 平台协议的外部 Web UI 与 Streamable HTTP MCP Server，并通过四阶段拨测上架应用市场。
          </p>
        </div>
        <span className="admin-heading-icon">
          <Code2 size={ICON.xl} />
        </span>
      </div>

      <div className="dev-handbook-nav" role="tablist" aria-label="手册章节切换">
        {tabs.map((item) => (
          <button
            key={item.id}
            type="button"
            role="tab"
            aria-selected={topic === item.id}
            className={`theme-option ${topic === item.id ? "active" : ""}`}
            onClick={() => setTopic(item.id)}
          >
            {item.label}
          </button>
        ))}
      </div>

      {topic === "architecture" && (
        <section className="dev-handbook-section" aria-label="架构总览">
          <h3>人类 Web UI + Agent MCP 对称双通道架构</h3>
          <p>
            在 Teamie 群聊（小组对话）中，每个已安装的外部市场应用由你自行部署的<strong>同一后端状态机</strong>同时服务两类参与者：
          </p>
          <ul className="dev-handbook-list">
            <li>
              <strong>人类成员通道（Web UI）：</strong>群聊右侧抽屉以跨源沙箱{" "}
              <code>iframe</code> 加载你的 <code>webUrl</code>，并通过{" "}
              <code>postMessage</code> 注入短期签名的 <code>Context Token</code>。
            </li>
            <li>
              <strong>Agent 成员通道（MCP Server）：</strong>群聊内的 Eino Agent 自动以{" "}
              <code>Authorization: Bearer &lt;Context Token&gt;</code> 连接你的{" "}
              <code>mcpUrl</code>（MCP Streamable HTTP <code>2025-03-26</code>），发现并调用{" "}
              <code>mcp__&lt;slug&gt;__&lt;tool&gt;</code> 工具。
            </li>
            <li>
              <strong>房间与会话天然对齐：</strong>两端收到的 JWT 均包含相同的{" "}
              <code>workspaceId</code> 与 <code>conversationId</code>，你的服务以{" "}
              <code>(workspaceId, conversationId)</code> 作为房间隔离主键，无需额外建房配对码。
            </li>
          </ul>
        </section>
      )}

      {topic === "deployment" && (
        <section className="dev-handbook-section" aria-label="跨源部署与反向代理">
          <h3>独立跨源域名、HTTPS 与反向代理要求</h3>
          <ul className="dev-handbook-list">
            <li>
              <strong>强制跨源隔离：</strong><code>webUrl</code> 与 <code>mcpUrl</code>{" "}
              必须部署在你自己的独立域名上，严禁与 Teamie 主站同源，严禁指向内网/回环地址（如{" "}
              <code>127.0.0.1</code>、<code>10.x</code>、<code>192.168.x</code>），线上生产环境强制要求{" "}
              <code>https://</code>。
            </li>
            <li>
              <strong>允许 iframe 嵌入：</strong><code>webUrl</code> 响应头不得返回{" "}
              <code>X-Frame-Options: DENY</code> 或 <code>SAMEORIGIN</code>；若配置{" "}
              <code>Content-Security-Policy: frame-ancestors</code>，必须放行 Teamie 主站域名。
            </li>
            <li>
              <strong>关闭 SSE 代理缓冲：</strong>若 MCP Server 使用{" "}
              <code>text/event-stream</code> 推送通知或响应，Nginx 需设置{" "}
              <code>proxy_buffering off;</code>，Caddy 需设置 <code>flush_interval -1</code>。
            </li>
          </ul>
          <SnippetBlock snippet={SNIPPETS.caddy} />
        </section>
      )}

      {topic === "jwt" && (
        <section className="dev-handbook-section" aria-label="对称身份与 Token 验签">
          <h3>HS256 Context Token 载荷与强制验签规范</h3>
          <p>
            你在「我的应用」创建草稿后，会获得应用专属的 <code>App ID</code> 与{" "}
            <code>Signing Secret</code>（前缀 <code>tmsec_</code>）。所有发往你后端 API 与{" "}
            <code>mcpUrl</code> 的请求都必须严格校验 <code>HS256</code> 签名、
            <code>iss === &quot;teamie&quot;</code>、<code>aud === App ID</code> 以及{" "}
            <code>exp</code> 有效期（默认 1 小时）：
          </p>
          <ul className="dev-handbook-list">
            <li>
              <code>sub</code> / <code>userId</code>：当前调用者的唯一主体 ID（严禁信任前端或工具参数自报的 userId！）。
            </li>
            <li>
              <strong>对称人机平权：</strong>载荷故意不包含人类与 Agent 区分字段，服务端状态机对所有{" "}
              <code>sub</code> 完全一视同仁。
            </li>
            <li>
              <code>displayName</code>：当前调用者在 Teamie 中的显示名称。
            </li>
            <li>
              <code>workspaceId</code> / <code>conversationId</code>：所属工作空间 ID 与当前群聊会话 ID。
            </li>
          </ul>
          <SnippetBlock snippet={SNIPPETS.jwtNode} />
          <SnippetBlock snippet={SNIPPETS.jwtGo} />
        </section>
      )}

      {topic === "webui" && (
        <section className="dev-handbook-section" aria-label="Web UI 桥接协议">
          <h3>Web UI iframe 握手与无刷新凭证续期</h3>
          <p>
            群聊右侧面板挂载你的 <code>webUrl</code> 后，遵循以下三类{" "}
            <code>window.postMessage</code> 标准事件：
          </p>
          <ul className="dev-handbook-list">
            <li>
              <code>teamie:mcp-app:ready</code>（子页面 → 宿主）：前端框架挂载完毕后立即发送，请求宿主推送上下文。
            </li>
            <li>
              <code>teamie:mcp-app:init</code>（宿主 → 子页面）：携带最新{" "}
              <code>token</code>、<code>expiresAt</code>、<code>workspaceId</code>、<code>conversationId</code> 与当前{" "}
              <code>user</code> 信息。
            </li>
            <li>
              <code>teamie:mcp-app:refresh-token</code>（子页面 → 宿主）：在{" "}
              <code>expiresAt</code> 过期前主动触发，宿主会静默签发新 Token 并再次通过{" "}
              <code>teamie:mcp-app:init</code> 下发，全程不刷新 iframe。
            </li>
          </ul>
          <SnippetBlock snippet={SNIPPETS.webBridge} />
        </section>
      )}

      {topic === "mcp" && (
        <section className="dev-handbook-section" aria-label="MCP Server 规范">
          <h3>MCP Streamable HTTP (2025-03-26) 工具与通知</h3>
          <ul className="dev-handbook-list">
            <li>
              <strong>传输协议：</strong>单端点 HTTP <code>POST</code> / <code>GET</code> /{" "}
              <code>DELETE</code>，支持直接返回 <code>application/json</code> 或{" "}
              <code>text/event-stream</code>（SSE）。
            </li>
            <li>
              <strong>工具命名与精简 Schema：</strong>至少暴露 1 个工具，挂载后全名{" "}
              <code>mcp__&lt;slug&gt;__&lt;tool_name&gt;</code> 总长不得超过 64 字符（建议{" "}
              <code>&lt;tool_name&gt;</code> 使用不超过 32 字符的小写动词短语，如{" "}
              <code>get_my_view</code>、<code>cast_night_action</code>、<code>submit_vote</code>），并在{" "}
              <code>description</code> 中明确告知大模型何时调用与参数约束。
            </li>
            <li>
              <strong>会话失效恢复与实时唤醒通知：</strong>若服务端重启导致{" "}
              <code>Mcp-Session-Id</code> 失效，请返回 HTTP <code>404</code>（Token 过期返回{" "}
              <code>401</code>），Agent Runtime 会自动重新握手并刷新凭证；当房间状态变化（如全员入座、天黑请闭眼、投票结果出炉）时，可通过 MCP SSE 流向在线会话推送任意 JSON-RPC Notification（如{" "}
              <code>notifications/message</code>），Agent Runtime 会将其作为持久输入（<code>TEAMIE_APP_NOTIFICATION</code>）唤醒对应 Agent 执行下一回合。
            </li>
          </ul>
          <SnippetBlock snippet={SNIPPETS.mcpTools} />
        </section>
      )}

      {topic === "fog" && (
        <section className="dev-handbook-section" aria-label="视野隔离与防作弊">
          <h3>多玩家/多 Agent 博弈的迷雾隔离（Fog-of-War）</h3>
          <p>
            以「狼人杀」等非对称信息应用为例，群聊公屏是所有人类与 Agent 共享的公开讨论区，而<strong>底牌、夜间行动与阵营私密信息必须完全托管在你的外部应用状态机中</strong>：
          </p>
          <ul className="dev-handbook-list">
            <li>
              <strong>严格按 Token <code>sub</code> 裁剪返回值：</strong>无论是人类打开 Web UI 查询桌面状态，还是 Agent 调用{" "}
              <code>get_my_view</code> 工具，服务端都只能根据已验签 JWT 的{" "}
              <code>sub</code> 返回该玩家有权知晓的身份、队友与夜间结果，绝不返回上帝视角全量状态。
            </li>
            <li>
              <strong>阶段锁与动作幂等：</strong>发言讨论走 Teamie 群聊消息；夜间行动、警长竞选投票、白天放逐投票全部走你的 Web UI 按钮或 MCP 工具，并在服务端校验当前阶段合法性。
            </li>
          </ul>
        </section>
      )}

      {topic === "checklist" && (
        <section className="dev-handbook-section" aria-label="提审自检清单">
          <h3>提交平台审核前的 6 项必过自检</h3>
          <ul className="dev-handbook-list">
            <li>
              <strong>1. 独立跨源 HTTPS：</strong><code>webUrl</code> 与 <code>mcpUrl</code>{" "}
              均使用公网可访问的独立 HTTPS 域名，无自签名证书报错。
            </li>
            <li>
              <strong>2. iframe 嵌入就绪：</strong>未设置阻断嵌入的{" "}
              <code>X-Frame-Options</code> 或过窄的 <code>frame-ancestors</code>。
            </li>
            <li>
              <strong>3. 未授权请求严格返回 HTTP 401/403：</strong>不带{" "}
              <code>Authorization</code> 头访问 <code>mcpUrl</code> 时必须返回 HTTP{" "}
              <code>401</code> 或 <code>403</code> 状态码（切勿返回 HTTP <code>200</code> 包装错误）。
            </li>
            <li>
              <strong>4. 伪造签名严格返回 HTTP 401/403：</strong>使用错误密钥签名的 JWT 访问{" "}
              <code>mcpUrl</code> 时必须返回 HTTP <code>401</code> 或 <code>403</code> 状态码，绝不裸解析 Base64 Payload。
            </li>
            <li>
              <strong>5. 合法 Token 正常暴露工具：</strong>使用真实{" "}
              <code>Signing Secret</code> 签发的探测 Token（<code>sub = 00000000-0000-4000-8000-000000000001</code>）完成{" "}
              <code>initialize</code> 与 <code>tools/list</code>，且至少返回 1 个有效工具且无破坏状态副作用。
            </li>
            <li>
              <strong>6. 自有空间实测闭环：</strong>已在你创建的工作空间群聊内完成人类 Web UI + Agent 同台联调，确认完结后前往{" "}
              <Link to="/app/developers">「我的应用」</Link> 点击提交审核。
            </li>
          </ul>
        </section>
      )}
    </div>
  );
}
