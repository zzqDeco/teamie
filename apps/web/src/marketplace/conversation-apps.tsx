import { useEffect, useMemo, useRef } from "react";
import type { RefObject } from "react";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Blocks, LoaderCircle, Plus, RefreshCw, X } from "lucide-react";
import { api, ApiError } from "@teamie/api-client";
import type {
  Conversation,
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
import {
  buildAppInstanceKey,
  ConversationAppTabsProvider,
  MAX_LIVE_APP_FRAMES,
  useConversationAppTabsController,
  useConversationOpenAppCount,
} from "./app-tabs-state";
import type {
  ConversationAppTabsController,
  OpenedAppTab,
} from "./app-tabs-state";
import { ConversationAppFrame } from "./conversation-app-frame";

export {
  buildAppInstanceKey,
  clampAppPaneWidth,
  clearPersistedAppPaneWidth,
  computeMaxAppPaneWidth,
  ConversationAppTabsProvider,
  DEFAULT_APP_PANE_WIDTH,
  MAX_APP_PANE_WIDTH_CEILING,
  MAX_LIVE_APP_FRAMES,
  MIN_APP_PANE_WIDTH,
  readPersistedAppPaneWidth,
  useConversationAppTabsController,
  useConversationOpenAppCount,
  writePersistedAppPaneWidth,
};
export type { ConversationAppTabsController, OpenedAppTab };

export function ConversationAppsPane(props: {
  paneRef?: RefObject<HTMLElement | null>;
  visible?: boolean;
  tabsController?: ConversationAppTabsController;
  maxLiveFrames?: number;
  overlayMembers: boolean;
  mobile: boolean;
  sidebar: boolean;
  workspace?: Workspace;
  conversation?: Conversation;
  user: User;
  owner: boolean;
  onClose: () => void;
  onOpenWorkspaceApps: () => void;
}) {
  const {
    paneRef,
    visible = true,
    tabsController,
    maxLiveFrames = MAX_LIVE_APP_FRAMES,
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

  const queryClient = useQueryClient();
  const queryMarketplaceEnabled = useMarketplaceWriteEnabled(user.id);
  const internalController = useConversationAppTabsController({
    userId: user.id,
    marketplaceEnabled: queryMarketplaceEnabled,
    maxLiveFrames,
  });
  const controller = tabsController ?? internalController;
  const marketplaceEnabled = tabsController
    ? controller.marketplaceEnabled && queryMarketplaceEnabled
    : queryMarketplaceEnabled;

  useEffect(() => {
    if (!marketplaceEnabled) {
      controller.clearAllTabs();
    }
  }, [controller, marketplaceEnabled]);

  const {
    effectiveWidth,
    maxWidth,
    isDragging,
    desktopWidthStyle,
    handleProps,
  } = useConversationAppPaneResize({
    userId: user.id,
    overlayMembers: !visible || overlayMembers,
  });

  const activeGroupId =
    visible && conversation?.type === "group" ? conversation.id : "";
  const prevActiveGroupIdRef = useRef("");
  useEffect(() => {
    const prev = prevActiveGroupIdRef.current;
    prevActiveGroupIdRef.current = activeGroupId;
    if (!activeGroupId || activeGroupId === prev) return;
    const state = queryClient.getQueryState(["conversationApps", activeGroupId]);
    if (state && state.dataUpdatedAt > 0 && state.fetchStatus !== "fetching") {
      void queryClient.invalidateQueries({
        queryKey: ["conversationApps", activeGroupId],
      });
    }
  }, [activeGroupId, queryClient]);
  const trackedConversationIds = useMemo(() => {
    const ids = new Set<string>();
    if (activeGroupId) ids.add(activeGroupId);
    if (marketplaceEnabled) {
      for (const id of controller.openConversationIds) ids.add(id);
    }
    return [...ids];
  }, [activeGroupId, controller.openConversationIds, marketplaceEnabled]);

  const appQueries = useQueries({
    queries: trackedConversationIds.map((id) => ({
      queryKey: ["conversationApps", id],
      queryFn: () => api.conversationApps(id),
    })),
  });

  useEffect(() => {
    if (!marketplaceEnabled) return;
    trackedConversationIds.forEach((convId, index) => {
      const q = appQueries[index];
      if (!q) return;
      if (q.data?.apps) {
        controller.syncConversationApps(convId, q.data.apps);
      } else if (
        q.isError &&
        q.error instanceof ApiError &&
        (q.error.status === 403 || q.error.status === 404)
      ) {
        controller.purgeConversation(convId);
      }
    });
  }, [appQueries, controller, marketplaceEnabled, trackedConversationIds]);

  const activeQueryIndex = activeGroupId
    ? trackedConversationIds.indexOf(activeGroupId)
    : -1;
  const activeAppsQuery =
    activeQueryIndex >= 0 ? appQueries[activeQueryIndex] : undefined;
  const apps = useMemo(
    () => activeAppsQuery?.data?.apps || [],
    [activeAppsQuery?.data],
  );

  const activeSlice = controller.getSlice(activeGroupId);
  useEffect(() => {
    if (visible && activeGroupId && marketplaceEnabled) {
      controller.touchForegroundConversation(activeGroupId);
    }
  }, [
    activeGroupId,
    activeSlice.activeTabAppId,
    activeSlice.launcherOpen,
    controller,
    marketplaceEnabled,
    visible,
  ]);

  const liveTabs = marketplaceEnabled ? controller.liveTabs : [];
  if (!visible && liveTabs.length === 0) {
    return null;
  }

  const hasForegroundSurface = Boolean(
    visible && workspace && conversation && conversation.type === "group",
  );
  const activeOpenTabs = activeSlice.tabs;
  const isLauncherView =
    activeOpenTabs.length === 0 || activeSlice.launcherOpen;
  const foregroundActiveTab = !isLauncherView
    ? activeOpenTabs.find((t) => t.appId === activeSlice.activeTabAppId) ?? null
    : null;
  const showActiveLiveStage = Boolean(
    hasForegroundSurface && marketplaceEnabled && foregroundActiveTab?.live,
  );

  return (
    <>
      {visible && overlayMembers && !sidebar && (
        <button
          type="button"
          className="members-shade"
          aria-label="关闭小组应用面板"
          onClick={onClose}
        />
      )}
      <aside
        className={`members-pane mp-apps-pane${isDragging ? " mp-apps-pane-dragging" : ""}`}
        ref={visible ? paneRef : undefined}
        style={visible ? desktopWidthStyle : undefined}
        role={visible && overlayMembers ? "dialog" : undefined}
        aria-modal={visible && overlayMembers ? true : undefined}
        aria-label="小组应用"
        tabIndex={-1}
        hidden={!visible}
        inert={!visible || (mobile && sidebar)}
      >
        {visible && !overlayMembers && (
          <div
            className="mp-resize-handle"
            role="separator"
            aria-orientation="vertical"
            aria-label="调节小组应用面板宽度"
            aria-valuenow={effectiveWidth}
            aria-valuemin={MIN_APP_PANE_WIDTH}
            aria-valuemax={maxWidth}
            tabIndex={0}
            {...handleProps}
          />
        )}
        {visible && overlayMembers && !sidebar && (
          <OverlayFocusBoundary edge="start" />
        )}
        {hasForegroundSurface && workspace && conversation && (
          <ConversationAppsForeground
            apps={apps}
            appsQuery={activeAppsQuery}
            workspace={workspace}
            conversation={conversation}
            owner={owner}
            marketplaceEnabled={marketplaceEnabled}
            openTabs={activeOpenTabs}
            activeTabAppId={activeSlice.activeTabAppId}
            launcherOpen={isLauncherView}
            controller={controller}
            onClose={onClose}
            onOpenWorkspaceApps={onOpenWorkspaceApps}
          />
        )}
        <div
          className="mp-frames-stage"
          hidden={!showActiveLiveStage}
          inert={!showActiveLiveStage}
        >
          {liveTabs.map((tab) => {
            const isForegroundTab =
              showActiveLiveStage &&
              conversation?.id === tab.conversationId &&
              foregroundActiveTab?.appId === tab.appId;
            return (
              <div
                key={tab.instanceKey}
                id={`mp-pane-panel-${tab.conversationId}-${tab.appId}`}
                role="tabpanel"
                aria-labelledby={
                  isForegroundTab
                    ? `mp-pane-tab-${tab.conversationId}-${tab.appId}`
                    : undefined
                }
                className="mp-pane-tabpanel mp-frame-slot"
                hidden={!isForegroundTab}
                inert={!isForegroundTab}
              >
                <ConversationAppFrame
                  app={tab.app}
                  conversationId={tab.conversationId}
                  workspaceId={tab.workspaceId}
                  user={user}
                />
              </div>
            );
          })}
        </div>
        {visible && overlayMembers && !sidebar && (
          <OverlayFocusBoundary edge="end" />
        )}
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
  const fromBootstrap =
    bootstrapQuery.data?.capabilities?.["im.marketplace.enabled"];
  if (typeof fromBootstrap === "boolean") return fromBootstrap;
  return true;
}

function resolveTabKeyIndex(
  key: string,
  currentIndex: number,
  total: number,
): number {
  if (total <= 0) return -1;
  if (key === "ArrowRight") return (currentIndex + 1) % total;
  if (key === "ArrowLeft") return (currentIndex - 1 + total) % total;
  if (key === "Home") return 0;
  if (key === "End") return total - 1;
  return -1;
}

function ConversationAppsForeground({
  apps,
  appsQuery,
  workspace,
  conversation,
  owner,
  marketplaceEnabled,
  openTabs,
  activeTabAppId,
  launcherOpen,
  controller,
  onClose,
  onOpenWorkspaceApps,
}: {
  apps: MarketplaceApp[];
  appsQuery?: {
    isLoading: boolean;
    isError: boolean;
    error: unknown;
    refetch: () => Promise<unknown>;
  };
  workspace: Workspace;
  conversation: Conversation;
  owner: boolean;
  marketplaceEnabled: boolean;
  openTabs: OpenedAppTab[];
  activeTabAppId: string | null;
  launcherOpen: boolean;
  controller: ConversationAppTabsController;
  onClose: () => void;
  onOpenWorkspaceApps: () => void;
}) {
  const tabRefs = useRef<(HTMLButtonElement | null)[]>([]);

  return (
    <>
      <header>
        <span>
          小组应用 <b>{apps.length}</b>
        </span>
        <div className="mp-header-actions">
          {owner && marketplaceEnabled && (
            <IconButton
              label="管理工作空间应用"
              onClick={onOpenWorkspaceApps}
            >
              <Plus size={ICON.md} />
            </IconButton>
          )}
          <IconButton label="关闭小组应用面板" onClick={onClose}>
            <X size={ICON.md} />
          </IconButton>
        </div>
      </header>

      {!marketplaceEnabled ? (
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
            <ul
              className="mp-readonly-list"
              aria-label="已安装小组应用只读列表"
            >
              {apps.map((app) => (
                <li key={app.id} className="mp-readonly-card">
                  <div className="mp-frame-meta">
                    <b>{app.name}</b>
                    <BadgePill>{app.slug}</BadgePill>
                    {app.servingDraft && (
                      <BadgePill className="mp-badge-draft">测试草稿</BadgePill>
                    )}
                  </div>
                  {app.description && (
                    <p className="field-help">{app.description}</p>
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>
      ) : (
        <>
          {openTabs.length > 0 && (
            <div className="mp-browser-tabbar">
              <div
                className="mp-pane-tabs"
                role="tablist"
                aria-label="切换小组应用"
              >
                {openTabs.map((tab, idx) => {
                  const selected =
                    !launcherOpen && activeTabAppId === tab.appId;
                  const tabFocusable = selected || (launcherOpen && idx === 0);
                  return (
                    <div
                      key={tab.appId}
                      className={`mp-tab-item${selected ? " active" : ""}${!tab.live ? " hibernated" : ""}`}
                      role="presentation"
                    >
                      <button
                        ref={(el) => {
                          tabRefs.current[idx] = el;
                        }}
                        id={`mp-pane-tab-${conversation.id}-${tab.appId}`}
                        type="button"
                        role="tab"
                        aria-selected={selected}
                        aria-controls={`mp-pane-panel-${conversation.id}-${tab.appId}`}
                        tabIndex={tabFocusable ? 0 : -1}
                        className="mp-tab-trigger"
                        title={
                          tab.live
                            ? tab.app.name
                            : `${tab.app.name}（已休眠，点击唤醒）`
                        }
                        onClick={() =>
                          controller.activateTab(conversation.id, tab.appId)
                        }
                        onKeyDown={(event) => {
                          const nextIdx = resolveTabKeyIndex(
                            event.key,
                            idx,
                            openTabs.length,
                          );
                          if (nextIdx !== -1) {
                            event.preventDefault();
                            const nextTab = openTabs[nextIdx];
                            if (nextTab) {
                              controller.activateTab(
                                conversation.id,
                                nextTab.appId,
                              );
                              tabRefs.current[nextIdx]?.focus();
                            }
                          }
                        }}
                      >
                        <span className="mp-tab-title">{tab.app.name}</span>
                      </button>
                      <button
                        type="button"
                        className="mp-tab-close"
                        aria-label={`关闭标签页：${tab.app.name}`}
                        title={`关闭标签页：${tab.app.name}`}
                        tabIndex={selected ? 0 : -1}
                        onClick={() =>
                          controller.closeTab(conversation.id, tab.appId)
                        }
                      >
                        <X size={ICON.xs} aria-hidden="true" />
                      </button>
                    </div>
                  );
                })}
              </div>
              <IconButton
                label="打开新应用"
                className={`mp-tab-add${launcherOpen ? " active" : ""}`}
                aria-pressed={launcherOpen}
                onClick={() => controller.openLauncher(conversation.id)}
              >
                <Plus size={ICON.sm} />
              </IconButton>
            </div>
          )}

          {launcherOpen && (
            <ConversationAppsLauncher
              apps={apps}
              appsQuery={appsQuery}
              workspace={workspace}
              conversation={conversation}
              owner={owner}
              openTabs={openTabs}
              controller={controller}
              onOpenWorkspaceApps={onOpenWorkspaceApps}
            />
          )}
        </>
      )}
    </>
  );
}

function ConversationAppsLauncher({
  apps,
  appsQuery,
  workspace,
  conversation,
  owner,
  openTabs,
  controller,
  onOpenWorkspaceApps,
}: {
  apps: MarketplaceApp[];
  appsQuery?: {
    isLoading: boolean;
    isError: boolean;
    error: unknown;
    refetch: () => Promise<unknown>;
  };
  workspace: Workspace;
  conversation: Conversation;
  owner: boolean;
  openTabs: OpenedAppTab[];
  controller: ConversationAppTabsController;
  onOpenWorkspaceApps: () => void;
}) {
  if (appsQuery?.isLoading && apps.length === 0) {
    return (
      <div className="member-loading">
        <LoaderCircle size={ICON.sm} className="spin" />
        加载小组应用…
      </div>
    );
  }

  if (appsQuery?.isError && apps.length === 0) {
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
            管理工作空间应用
          </Button>
        )}
      </div>
    );
  }

  return (
    <div className="mp-launcher">
      <p className="mp-launcher-hint field-help">
        点击应用在标签页中启动，切换小组或收起面板后继续后台保活。
      </p>
      <ul className="mp-launcher-list" aria-label="已安装小组应用列表">
        {apps.map((app) => {
          const openedTab = openTabs.find((t) => t.appId === app.id);
          return (
            <li key={app.id} className="mp-launcher-item">
              <button
                type="button"
                className="mp-launcher-card"
                onClick={() => controller.openOrActivateTab(conversation, app)}
              >
                <span className="mp-frame-meta">
                  <b>{app.name}</b>
                  <BadgePill>{app.slug}</BadgePill>
                  {app.servingDraft && (
                    <BadgePill className="mp-badge-draft">测试草稿</BadgePill>
                  )}
                  {openedTab && (
                    <BadgePill className="mp-badge-running">
                      {openedTab.live ? "已打开" : "已休眠"}
                    </BadgePill>
                  )}
                </span>
                {app.description && (
                  <span className="mp-launcher-desc field-help">
                    {app.description}
                  </span>
                )}
              </button>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
