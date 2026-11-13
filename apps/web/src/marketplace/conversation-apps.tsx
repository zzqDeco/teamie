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
import {
  clampAppPaneWidth,
  clearPersistedAppPaneWidth,
  computeMaxAppPaneWidth,
  DEFAULT_APP_PANE_WIDTH,
  MAX_APP_PANE_WIDTH_CEILING,
  MIN_APP_PANE_WIDTH,
  readPersistedAppPaneWidth,
  useConversationAppPaneResize,
  writePersistedAppPaneWidth,
} from "./app-pane-resize";

export {
  clampAppPaneWidth,
  clearPersistedAppPaneWidth,
  computeMaxAppPaneWidth,
  DEFAULT_APP_PANE_WIDTH,
  MAX_APP_PANE_WIDTH_CEILING,
  MIN_APP_PANE_WIDTH,
  readPersistedAppPaneWidth,
  writePersistedAppPaneWidth,
};

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
  const {
    effectiveWidth,
    maxWidth,
    isDragging,
    desktopWidthStyle,
    handleProps,
  } = useConversationAppPaneResize({
    userId: user.id,
    overlayMembers,
  });

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
        className={`members-pane mp-apps-pane${isDragging ? " mp-apps-pane-dragging" : ""}`}
        ref={paneRef}
        style={desktopWidthStyle}
        role={overlayMembers ? "dialog" : undefined}
        aria-modal={overlayMembers ? true : undefined}
        aria-label="群聊应用"
        tabIndex={-1}
        inert={mobile && sidebar}
      >
        {!overlayMembers && (
          <div
            className="mp-resize-handle"
            role="separator"
            aria-orientation="vertical"
            aria-label="调节群聊应用面板宽度"
            aria-valuenow={effectiveWidth}
            aria-valuemin={MIN_APP_PANE_WIDTH}
            aria-valuemax={maxWidth}
            tabIndex={0}
            {...handleProps}
          />
        )}
        {overlayMembers && !sidebar && <OverlayFocusBoundary edge="start" />}
        <header>
          <span>
            群聊应用 <b>{apps.length}</b>
          </span>
          <div className="mp-header-actions">
            <ConversationAppsManageAction
              userId={user.id}
              owner={owner}
              onOpenWorkspaceApps={onOpenWorkspaceApps}
            />
            <IconButton label="关闭群聊应用面板" onClick={onClose}>
              <X size={ICON.md} />
            </IconButton>
          </div>
        </header>

        <ConversationAppsBody
          appsQuery={appsQuery}
          apps={apps}
          activeApp={activeApp}
          workspace={workspace}
          conversation={conversation}
          user={user}
          owner={owner}
          onSelectApp={setSelectedAppId}
          onOpenWorkspaceApps={onOpenWorkspaceApps}
        />
        {overlayMembers && !sidebar && <OverlayFocusBoundary edge="end" />}
      </aside>
    </>
  );
}

function useMarketplaceWriteEnabled(userId: string): boolean {
  const capQuery = useQuery<boolean>({
    queryKey: ["capability", "im.marketplace.enabled", userId],
    queryFn: () => true,
    enabled: false,
  });
  const bootstrapQuery = useQuery<{ capabilities?: Record<string, boolean> }>({
    queryKey: ["bootstrap", userId],
    queryFn: () => api.bootstrap(),
    enabled: false,
  });
  if (typeof capQuery.data === "boolean") return capQuery.data;
  const fromBootstrap = bootstrapQuery.data?.capabilities?.["im.marketplace.enabled"];
  if (typeof fromBootstrap === "boolean") return fromBootstrap;
  return true;
}

function ConversationAppsManageAction({
  userId,
  owner,
  onOpenWorkspaceApps,
}: {
  userId: string;
  owner: boolean;
  onOpenWorkspaceApps: () => void;
}) {
  const marketplaceEnabled = useMarketplaceWriteEnabled(userId);
  if (!owner || !marketplaceEnabled) return null;
  return (
    <IconButton label="管理工作区应用" onClick={onOpenWorkspaceApps}>
      <Plus size={ICON.md} />
    </IconButton>
  );
}

function resolveTabKeyIndex(key: string, currentIndex: number, total: number): number {
  if (total <= 0) return -1;
  if (key === "ArrowRight") return (currentIndex + 1) % total;
  if (key === "ArrowLeft") return (currentIndex - 1 + total) % total;
  if (key === "Home") return 0;
  if (key === "End") return total - 1;
  return -1;
}

function ConversationAppsBody({
  appsQuery,
  apps,
  activeApp,
  workspace,
  conversation,
  user,
  owner,
  onSelectApp,
  onOpenWorkspaceApps,
}: {
  appsQuery: {
    isLoading: boolean;
    isError: boolean;
    error: unknown;
    refetch: () => Promise<unknown>;
  };
  apps: MarketplaceApp[];
  activeApp: MarketplaceApp | null;
  workspace: Workspace;
  conversation: Conversation;
  user: User;
  owner: boolean;
  onSelectApp: (id: string) => void;
  onOpenWorkspaceApps: () => void;
}) {
  const marketplaceEnabled = useMarketplaceWriteEnabled(user.id);
  const tabRefs = useRef<(HTMLButtonElement | null)[]>([]);

  if (appsQuery.isLoading) {
    return (
      <div className="member-loading">
        <LoaderCircle size={ICON.sm} className="spin" />
        加载群聊应用…
      </div>
    );
  }

  if (appsQuery.isError && apps.length === 0) {
    return (
      <div className="members-description" role="alert">
        <h3>加载失败</h3>
        <p className="field-help">{errorText(appsQuery.error)}</p>
        <Button
          type="button"
          variant="secondary"
          onClick={() => void appsQuery.refetch()}
        >
          <RefreshCw size={ICON.xs} /> 重新加载
        </Button>
      </div>
    );
  }

  if (!marketplaceEnabled) {
    return (
      <div className="mp-pane-body">
        <Notice kind="info">
          平台当前已关闭应用功能，已安装应用仅展示只读信息。
        </Notice>
        {apps.length === 0 ? (
          <div className="members-description">
            <span className="member-cover">
              <Blocks size={ICON.xl} />
            </span>
            <h3>暂无已安装应用</h3>
            <p className="field-help">「{workspace.name}」尚未安装应用。</p>
          </div>
        ) : (
          <ul className="mp-readonly-list" aria-label="已安装群聊应用只读列表">
            {apps.map((app) => (
              <li key={app.id} className="mp-readonly-card">
                <div className="mp-frame-meta">
                  <b>{app.name}</b>
                  <BadgePill>{app.slug}</BadgePill>
                  {app.servingDraft && (
                    <BadgePill className="mp-badge-draft">测试草稿</BadgePill>
                  )}
                </div>
                {app.description && <p className="field-help">{app.description}</p>}
              </li>
            ))}
          </ul>
        )}
      </div>
    );
  }

  if (apps.length === 0) {
    return (
      <div className="members-description">
        <span className="member-cover">
          <Blocks size={ICON.xl} />
        </span>
        <h3>暂无已安装应用</h3>
        <p className="field-help">
          「{workspace.name}」尚未安装应用。
          {!owner && "如需使用，请联系工作空间创建者安装。"}
        </p>
        {owner && (
          <Button type="button" onClick={onOpenWorkspaceApps}>
            管理工作区应用
          </Button>
        )}
      </div>
    );
  }

  return (
    <div className="mp-pane-body">
      {apps.length > 1 && (
        <div
          className="theme-options mp-pane-tabs"
          role="tablist"
          aria-label="切换群聊应用"
        >
          {apps.map((app, idx) => {
            const selected = activeApp?.id === app.id;
            return (
              <button
                key={app.id}
                ref={(el) => {
                  tabRefs.current[idx] = el;
                }}
                id={`mp-pane-tab-${app.id}`}
                type="button"
                role="tab"
                aria-selected={selected}
                aria-controls={`mp-pane-panel-${app.id}`}
                tabIndex={selected ? 0 : -1}
                className={`theme-option ${selected ? "active" : ""}`}
                onClick={() => onSelectApp(app.id)}
                onKeyDown={(event) => {
                  const nextIdx = resolveTabKeyIndex(event.key, idx, apps.length);
                  if (nextIdx !== -1) {
                    event.preventDefault();
                    const nextApp = apps[nextIdx];
                    if (nextApp) {
                      onSelectApp(nextApp.id);
                      tabRefs.current[nextIdx]?.focus();
                    }
                  }
                }}
              >
                {app.name}
              </button>
            );
          })}
        </div>
      )}
      {activeApp && (
        <div
          id={apps.length > 1 ? `mp-pane-panel-${activeApp.id}` : undefined}
          role={apps.length > 1 ? "tabpanel" : undefined}
          aria-labelledby={apps.length > 1 ? `mp-pane-tab-${activeApp.id}` : undefined}
          className="mp-pane-tabpanel"
        >
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
        </div>
      )}
    </div>
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
      {loading && !session && !sameOriginBlocked && !error && (
        <div className="mp-frame-loading" role="status">
          <LoaderCircle size={ICON.md} className="spin" aria-hidden="true" />
          <span>正在启动应用会话…</span>
        </div>
      )}
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
