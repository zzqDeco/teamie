import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { FormEvent, RefObject } from "react";
import { Link } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Blocks,
  CheckCheck,
  Copy,
  KeyRound,
  LoaderCircle,
  Plus,
  RefreshCw,
  ShieldCheck,
  Trash2,
  X,
} from "lucide-react";
import { api, ApiError } from "@teamie/api-client";
import type {
  Conversation,
  ConversationAppSessionResp,
  MarketplaceApp,
  User,
  Workspace,
} from "@teamie/api-client";
import { useAuth } from "./auth";
import { errorText } from "./errors";
import { BadgePill, Button, IconButton, Modal, Notice, OverlayFocusBoundary, SwitchField } from "./ui";
import { ICON } from "./icons";

function AdminRequired() {
  return (
    <div className="unavailable-view">
      <span className="large-symbol">
        <ShieldCheck size={ICON.xl} />
      </span>
      <h2>需要平台管理员权限</h2>
      <p>全局应用市场目录由平台管理员维护。</p>
      <Link to="/app" className="button">
        回到我的工作空间
      </Link>
    </div>
  );
}

export function PlatformMarketplace() {
  const { user } = useAuth();
  if (user?.platformRole !== "admin") return <AdminRequired />;
  return <AdminMarketplaceCatalog key={`${user.id}:${user.platformRole}`} user={user} />;
}

function AdminMarketplaceCatalog({ user }: { user: User }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [description, setDescription] = useState("");
  const [webUrl, setWebUrl] = useState("");
  const [mcpUrl, setMcpUrl] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [busy, setBusy] = useState(false);
  const [busyAppId, setBusyAppId] = useState("");
  const [error, setError] = useState("");
  const [issuedSecret, setIssuedSecret] = useState<{
    appName: string;
    secret: string;
    copied: boolean;
  } | null>(null);

  const appsQuery = useQuery({
    queryKey: ["adminMarketplaceApps", user.id],
    queryFn: () => api.adminMarketplaceApps(),
  });

  async function handleCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      const created = await api.createAdminMarketplaceApp({
        name: name.trim(),
        slug: slug.trim().toLowerCase(),
        description: description.trim(),
        webUrl: webUrl.trim(),
        mcpUrl: mcpUrl.trim(),
        enabled,
      });
      setName("");
      setSlug("");
      setDescription("");
      setWebUrl("");
      setMcpUrl("");
      setEnabled(true);
      setIssuedSecret({
        appName: created.app.name,
        secret: created.signingSecret,
        copied: false,
      });
      await queryClient.invalidateQueries({ queryKey: ["adminMarketplaceApps"] });
      await queryClient.invalidateQueries({ queryKey: ["marketplaceCatalog"] });
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  async function runAppMutation(appId: string, fn: () => Promise<void>) {
    if (busyAppId) return;
    setBusyAppId(appId);
    setError("");
    try {
      await fn();
      await queryClient.invalidateQueries({ queryKey: ["adminMarketplaceApps"] });
      await queryClient.invalidateQueries({ queryKey: ["marketplaceCatalog"] });
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusyAppId("");
    }
  }

  async function handleToggleEnabled(app: MarketplaceApp, nextEnabled: boolean) {
    await runAppMutation(app.id, async () => {
      await api.updateAdminMarketplaceApp(app.id, { enabled: nextEnabled });
    });
  }

  async function handleRotateSecret(app: MarketplaceApp) {
    await runAppMutation(app.id, async () => {
      const rotated = await api.rotateAdminMarketplaceAppSecret(app.id);
      setIssuedSecret({ appName: rotated.app.name, secret: rotated.signingSecret, copied: false });
    });
  }

  async function handleDelete(app: MarketplaceApp) {
    await runAppMutation(app.id, async () => {
      await api.deleteAdminMarketplaceApp(app.id);
    });
  }

  async function copySecret() {
    if (!issuedSecret) return;
    try {
      await navigator.clipboard.writeText(issuedSecret.secret);
      setIssuedSecret({ ...issuedSecret, copied: true });
    } catch {
      setError("复制未成功，请手动选中密钥复制。");
    }
  }

  if (
    appsQuery.error instanceof ApiError &&
    appsQuery.error.status === 403 &&
    appsQuery.error.code === "forbidden"
  ) {
    return <AdminRequired />;
  }

  const apps = appsQuery.data?.apps || [];

  return (
    <div className="admin-view">
      <div className="admin-heading">
        <div>
          <h1>
            应用市场
            <span className="platform-role-badge">
              <ShieldCheck size={ICON.xs} />
              平台管理员
            </span>
          </h1>
          <p>管理平台级 Web + MCP 应用目录。工作空间所有者可从目录中选择安装至群聊。</p>
        </div>
        <span className="admin-heading-icon">
          <Blocks size={ICON.xl} />
        </span>
      </div>

      <Notice>{error}</Notice>

      {issuedSecret && (
        <section className="settings-form" aria-label="应用签名密钥">
          <h3>「{issuedSecret.appName}」的 Context Token 签名密钥</h3>
          <p className="field-help">
            此 HMAC-SHA256 密钥仅显示一次，请妥善保存并配置到对应的第三方 MCP Server 用于校验Bearer Context Token。
          </p>
          <div className="copy-input">
            <input
              value={issuedSecret.secret}
              readOnly
              aria-label="应用签名密钥"
              onFocus={(e) => e.target.select()}
            />
            <button type="button" onClick={() => void copySecret()} aria-label="复制签名密钥">
              {issuedSecret.copied ? <CheckCheck size={ICON.md} /> : <Copy size={ICON.md} />}
            </button>
          </div>
        </section>
      )}

      <form className="settings-form" onSubmit={handleCreate}>
        <h3>上架新应用</h3>
        <label>
          应用名称
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="例如：协作桌游面板"
            maxLength={80}
            required
          />
        </label>
        <label>
          唯一标识（Slug）
          <input
            value={slug}
            onChange={(e) => setSlug(e.target.value)}
            placeholder="例如：board-room"
            maxLength={64}
            required
          />
          <small className="field-help">
            仅限小写字母、数字与连字符，Agent 工具将暴露为 mcp__slug__tool。
          </small>
        </label>
        <label>
          应用简介
          <input
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="简要说明该群聊应用的用途与交互方式"
            maxLength={400}
          />
        </label>
        <label>
          Web UI 地址（HTTPS / HTTP）
          <input
            type="url"
            value={webUrl}
            onChange={(e) => setWebUrl(e.target.value)}
            placeholder="https://app.example.com/ui"
            required
          />
        </label>
        <label>
          MCP Server 地址（Streamable HTTP）
          <input
            type="url"
            value={mcpUrl}
            onChange={(e) => setMcpUrl(e.target.value)}
            placeholder="https://app.example.com/mcp"
            required
          />
        </label>
        <SwitchField
          checked={enabled}
          title="立即启用"
          description="启用后，工作空间所有者即可在目录中浏览并安装该应用。"
          onChange={setEnabled}
        />
        <Button
          type="submit"
          busy={busy}
          disabled={!name.trim() || !slug.trim() || !webUrl.trim() || !mcpUrl.trim()}
        >
          <Plus size={ICON.sm} /> 注册应用
        </Button>
      </form>

      <section className="settings-form" aria-label="已注册应用列表">
        <h3>已注册应用（{apps.length}）</h3>
        {appsQuery.isLoading ? (
          <p className="muted">正在加载应用目录…</p>
        ) : apps.length === 0 ? (
          <p className="muted">暂未注册任何应用。</p>
        ) : (
          <ul className="member-directory-list" aria-label="平台应用目录">
            {apps.map((app) => (
              <li key={app.id} className="member-directory-row">
                <div className="member-directory-identity">
                  <div>
                    <b>
                      {app.name}{" "}
                      <BadgePill>{app.slug}</BadgePill>{" "}
                      <BadgePill className={app.enabled ? "" : "member-unavailable"}>
                        {app.enabled ? "已启用" : "已停用"}
                      </BadgePill>
                    </b>
                    {app.description && <p className="field-help">{app.description}</p>}
                    <small className="field-help">
                      Web: {app.webUrl} · MCP: {app.mcpUrl}
                    </small>
                  </div>
                </div>
                <div className="member-directory-actions">
                  <Button
                    type="button"
                    className="secondary"
                    disabled={!!busyAppId}
                    busy={busyAppId === app.id}
                    onClick={() => void handleToggleEnabled(app, !app.enabled)}
                  >
                    {app.enabled ? "停用" : "启用"}
                  </Button>
                  <Button
                    type="button"
                    className="secondary"
                    disabled={!!busyAppId}
                    onClick={() => void handleRotateSecret(app)}
                    aria-label={`轮换 ${app.name} 密钥`}
                  >
                    <KeyRound size={ICON.sm} /> 轮换密钥
                  </Button>
                  <Button
                    type="button"
                    className="secondary danger-text"
                    disabled={!!busyAppId}
                    onClick={() => void handleDelete(app)}
                    aria-label={`删除 ${app.name}`}
                  >
                    <Trash2 size={ICON.sm} /> 删除
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

export function WorkspaceMarketplaceModal({
  workspace,
  isOwner,
  onClose,
}: {
  workspace: Workspace;
  isOwner: boolean;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [busyAppId, setBusyAppId] = useState("");
  const [error, setError] = useState("");

  const catalogQuery = useQuery({
    queryKey: ["marketplaceCatalog"],
    queryFn: () => api.marketplaceCatalog(),
  });

  const installedQuery = useQuery({
    queryKey: ["workspaceApps", workspace.id],
    queryFn: () => api.workspaceApps(workspace.id),
  });

  const installedIds = useMemo(
    () => new Set((installedQuery.data?.apps || []).map((a) => a.id)),
    [installedQuery.data],
  );

  async function toggleInstall(app: MarketplaceApp, installed: boolean) {
    if (!isOwner || busyAppId) return;
    setBusyAppId(app.id);
    setError("");
    try {
      if (installed) {
        await api.uninstallWorkspaceApp(workspace.id, app.id);
      } else {
        await api.installWorkspaceApp(workspace.id, app.id);
      }
      await queryClient.invalidateQueries({ queryKey: ["workspaceApps", workspace.id] });
      await queryClient.invalidateQueries({ queryKey: ["conversationApps"] });
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusyAppId("");
    }
  }

  const catalogApps = catalogQuery.data?.apps || [];

  return (
    <Modal
      title={`「${workspace.name}」工作区应用`}
      subtitle="安装后的应用将在当前工作空间的所有群聊（小组对话）中对人类成员与 Agent 开放。"
      onClose={onClose}
      busy={!!busyAppId}
    >
      <div>
        <Notice>{error}</Notice>
        {catalogQuery.isLoading || installedQuery.isLoading ? (
          <p className="muted">正在加载应用目录…</p>
        ) : catalogApps.length === 0 ? (
          <p className="muted">平台目录中暂无可用应用。</p>
        ) : (
          <ul className="member-directory-list" aria-label="工作空间可选应用">
            {catalogApps.map((app) => {
              const isInstalled = installedIds.has(app.id);
              return (
                <li key={app.id} className="member-directory-row">
                  <div className="member-directory-identity">
                    <div>
                      <b>
                        {app.name} <BadgePill>{app.slug}</BadgePill>
                        {isInstalled && <BadgePill>已安装</BadgePill>}
                      </b>
                      {app.description && <p className="field-help">{app.description}</p>}
                    </div>
                  </div>
                  {isOwner && (
                    <div className="member-directory-actions">
                      <Button
                        type="button"
                        className={isInstalled ? "secondary danger-text" : ""}
                        busy={busyAppId === app.id}
                        disabled={!!busyAppId}
                        onClick={() => void toggleInstall(app, isInstalled)}
                      >
                        {isInstalled ? "卸载应用" : "安装到工作空间"}
                      </Button>
                    </div>
                  )}
                </li>
              );
            })}
          </ul>
        )}
        <div className="modal-actions">
          <Button type="button" variant="secondary" onClick={onClose}>
            关闭
          </Button>
        </div>
      </div>
    </Modal>
  );
}

export function ConversationAppsPane(props: {
  paneRef?: RefObject<HTMLElement | null>;
  overlayMembers: boolean;
  mobile: boolean;
  sidebar: boolean;
  workspace: Workspace;
  conversation: Conversation;
  user: User;
  owner: boolean;
  onClose: () => void;
  onOpenWorkspaceApps: () => void;
}) {
  const {
    paneRef,
    overlayMembers,
    mobile,
    sidebar,
    workspace,
    conversation,
    user,
    owner,
    onClose,
    onOpenWorkspaceApps,
  } = props;
  const [selectedAppId, setSelectedAppId] = useState("");

  const appsQuery = useQuery({
    queryKey: ["conversationApps", conversation.id],
    queryFn: () => api.conversationApps(conversation.id),
    enabled: conversation.type === "group",
  });

  const apps = useMemo(() => appsQuery.data?.apps || [], [appsQuery.data]);
  const activeApp = useMemo(
    () => apps.find((a) => a.id === selectedAppId) || apps[0] || null,
    [apps, selectedAppId],
  );

  useEffect(() => {
    if (activeApp && activeApp.id !== selectedAppId) {
      setSelectedAppId(activeApp.id);
    }
  }, [activeApp, selectedAppId]);

  return (
    <>
      {overlayMembers && !sidebar && (
        <button
          className="members-shade"
          aria-label="关闭群聊应用面板"
          onClick={onClose}
        />
      )}
      <aside
        className="members-pane"
        ref={paneRef}
        role={overlayMembers ? "dialog" : undefined}
        aria-modal={overlayMembers ? true : undefined}
        aria-label="群聊应用"
        tabIndex={-1}
        inert={mobile && sidebar}
      >
        {overlayMembers && !sidebar && <OverlayFocusBoundary edge="start" />}
        <header>
          <span>
            群聊应用 <b>{apps.length}</b>
          </span>
          <div style={{ display: "flex", gap: "4px", alignItems: "center" }}>
            {owner && (
              <IconButton label="管理工作区应用" onClick={onOpenWorkspaceApps}>
                <Plus size={ICON.md} />
              </IconButton>
            )}
            <IconButton label="关闭群聊应用面板" onClick={onClose}>
              <X size={ICON.md} />
            </IconButton>
          </div>
        </header>

        {appsQuery.isLoading ? (
          <div className="member-loading">
            <LoaderCircle size={ICON.sm} className="spin" />
            加载群聊应用…
          </div>
        ) : apps.length === 0 ? (
          <div className="members-description">
            <span className="member-cover">
              <Blocks size={ICON.xl} />
            </span>
            <h3>暂无已安装应用</h3>
            <p className="field-help">「{workspace.name}」尚未安装群聊应用。</p>
            {owner && (
              <Button type="button" onClick={onOpenWorkspaceApps}>
                管理工作区应用
              </Button>
            )}
          </div>
        ) : (
          <div style={{ display: "flex", flexDirection: "column", flex: 1, minHeight: 0 }}>
            {apps.length > 1 && (
              <div className="theme-options" role="tablist" aria-label="切换群聊应用" style={{ padding: "8px 12px" }}>
                {apps.map((app) => (
                  <button
                    key={app.id}
                    type="button"
                    role="tab"
                    aria-selected={activeApp?.id === app.id}
                    className={`theme-option ${activeApp?.id === app.id ? "active" : ""}`}
                    onClick={() => setSelectedAppId(app.id)}
                  >
                    {app.name}
                  </button>
                ))}
              </div>
            )}
            {activeApp && (
              <ConversationAppFrame
                key={JSON.stringify([user.id, conversation.workspaceId, conversation.id, activeApp.id, activeApp.webUrl, activeApp.mcpUrl])}
                app={activeApp}
                conversation={conversation}
                user={user}
              />
            )}
          </div>
        )}
        {overlayMembers && !sidebar && <OverlayFocusBoundary edge="end" />}
      </aside>
    </>
  );
}

// A different user, conversation or app mounts a new instance. Pending requests
// and iframe refs therefore cannot cross the credential's scope, even on one origin.
function ConversationAppFrame({ app, conversation, user }: {
  app: MarketplaceApp;
  conversation: Conversation;
  user: User;
}) {
  const iframeRef = useRef<HTMLIFrameElement | null>(null);
  const requests = useRef({ generation: 0 });
  const [session, setSession] = useState<ConversationAppSessionResp | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const fetchSession = useCallback(async () => {
    const pending = requests.current;
    const generation = ++pending.generation;
    setLoading(true);
    setError("");
    try {
      const next = await api.createConversationAppSession(conversation.id, app.id);
      if (generation !== pending.generation) return;
      if (next.app.id !== app.id || next.app.webUrl !== app.webUrl) {
        throw new Error("应用信息已更新，请重新打开应用。");
      }
      setSession(next);
    } catch (e) {
      if (generation !== pending.generation) return;
      setSession(null);
      setError(errorText(e));
    } finally {
      if (generation === pending.generation) setLoading(false);
    }
  }, [app.id, app.webUrl, conversation.id]);

  useEffect(() => {
    const pending = requests.current;
    void fetchSession();
    return () => { pending.generation++; };
  }, [fetchSession]);

  const postContext = useCallback(() => {
    const win = iframeRef.current?.contentWindow;
    if (!win || !session || session.app.id !== app.id || session.app.webUrl !== app.webUrl) return;
    let origin: string;
    try {
      origin = new URL(app.webUrl).origin;
    } catch {
      return;
    }
    win.postMessage({
      type: "teamie:mcp-app:init",
      payload: {
        appId: session.app.id,
        slug: session.app.slug,
        name: session.app.name,
        webUrl: session.app.webUrl,
        mcpUrl: session.app.mcpUrl,
        token: session.token,
        expiresAt: session.expiresAt,
        workspaceId: conversation.workspaceId,
        conversationId: conversation.id,
        user: { id: user.id, displayName: user.displayName },
      },
    }, origin);
  }, [app.id, app.webUrl, conversation.id, conversation.workspaceId, session, user.displayName, user.id]);

  // A refreshed credential must reach the existing frame without reloading it.
  useEffect(() => { postContext(); }, [postContext]);

  useEffect(() => {
    let origin: string;
    try {
      origin = new URL(app.webUrl).origin;
    } catch {
      return;
    }
    const onMessage = (event: MessageEvent) => {
      if (event.origin !== origin || event.source !== iframeRef.current?.contentWindow) return;
      const type = (event.data as { type?: string } | null)?.type;
      if (type === "teamie:mcp-app:ready") {
        if (session) postContext();
        else void fetchSession();
      } else if (type === "teamie:mcp-app:refresh-token") {
        void fetchSession();
      }
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [app.webUrl, fetchSession, postContext, session]);

  return (
    <>
      <Notice>{error}</Notice>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", padding: "6px 12px", fontSize: "12px" }}>
        <span><b>{app.name}</b> · <small>{app.slug}</small></span>
        <IconButton label="刷新应用会话" disabled={loading} onClick={() => void fetchSession()}>
          <RefreshCw size={ICON.xs} />
        </IconButton>
      </div>
      {session && (
        <iframe
          ref={iframeRef}
          tabIndex={0}
          title={`群聊应用：${app.name}`}
          src={session.app.webUrl}
          sandbox="allow-scripts allow-forms allow-same-origin"
          onLoad={postContext}
          style={{ width: "100%", flex: 1, border: "0", minHeight: "360px" }}
        />
      )}
    </>
  );
}
