import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { RefObject } from "react";
import { useQuery } from "@tanstack/react-query";
import { Blocks, LoaderCircle, Plus, RefreshCw, X } from "lucide-react";
import { api } from "@teamie/api-client";
import type {
  Conversation,
  ConversationAppSessionResp,
  MarketplaceApp,
  User,
  Workspace,
} from "@teamie/api-client";
import { errorText } from "../errors";
import { BadgePill, Button, IconButton, Notice, OverlayFocusBoundary } from "../ui";
import { ICON } from "../icons";

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
          <div className="mp-header-actions">
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
          <div className="mp-pane-body">
            {apps.length > 1 && (
              <div
                className="theme-options mp-pane-tabs"
                role="tablist"
                aria-label="切换群聊应用"
              >
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
                key={JSON.stringify([
                  user.id,
                  conversation.workspaceId,
                  conversation.id,
                  activeApp.id,
                  activeApp.webUrl,
                  activeApp.mcpUrl,
                ])}
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
function ConversationAppFrame({
  app,
  conversation,
  user,
}: {
  app: MarketplaceApp;
  conversation: Conversation;
  user: User;
}) {
  const iframeRef = useRef<HTMLIFrameElement | null>(null);
  const requests = useRef({ generation: 0 });
  const [session, setSession] = useState<ConversationAppSessionResp | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const sameOriginBlocked = useMemo(() => {
    try {
      return new URL(app.webUrl).origin === window.location.origin;
    } catch {
      return true;
    }
  }, [app.webUrl]);

  const fetchSession = useCallback(async () => {
    if (sameOriginBlocked) return;
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
  }, [app.id, app.webUrl, conversation.id, sameOriginBlocked]);

  useEffect(() => {
    const pending = requests.current;
    void fetchSession();
    return () => {
      pending.generation++;
    };
  }, [fetchSession]);

  const postContext = useCallback(() => {
    const win = iframeRef.current?.contentWindow;
    if (!win || !session || session.app.id !== app.id || session.app.webUrl !== app.webUrl) {
      return;
    }
    let origin: string;
    try {
      origin = new URL(app.webUrl).origin;
    } catch {
      return;
    }
    if (origin === window.location.origin) return;
    win.postMessage(
      {
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
      },
      origin,
    );
  }, [
    app.id,
    app.webUrl,
    conversation.id,
    conversation.workspaceId,
    session,
    user.displayName,
    user.id,
  ]);

  // A refreshed credential must reach the existing frame without reloading it.
  useEffect(() => {
    postContext();
  }, [postContext]);

  useEffect(() => {
    if (sameOriginBlocked) return;
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
  }, [app.webUrl, fetchSession, postContext, sameOriginBlocked, session]);

  return (
    <>
      <Notice>
        {sameOriginBlocked
          ? "安全限制：群聊应用 Web UI 必须部署在独立跨源域名上，禁止与当前主站同源挂载。"
          : error}
      </Notice>
      <div className="mp-frame-bar">
        <span className="mp-frame-meta">
          <b>{app.name}</b>
          <small>· {app.slug}</small>
          {(app.servingDraft || session?.app.servingDraft) && (
            <BadgePill className="mp-badge-draft">测试草稿</BadgePill>
          )}
        </span>
        <IconButton
          label="刷新应用会话"
          disabled={loading || sameOriginBlocked}
          onClick={() => void fetchSession()}
        >
          <RefreshCw size={ICON.xs} />
        </IconButton>
      </div>
      {!sameOriginBlocked && session && (
        <iframe
          ref={iframeRef}
          tabIndex={0}
          title={`群聊应用：${app.name}`}
          src={session.app.webUrl}
          sandbox="allow-scripts allow-forms allow-same-origin"
          onLoad={postContext}
          className="mp-iframe"
        />
      )}
    </>
  );
}
