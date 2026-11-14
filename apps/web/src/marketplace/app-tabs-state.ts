import { createContext, createElement, useCallback, useContext, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import type { Conversation, MarketplaceApp, Workspace } from "@teamie/api-client";

export const MAX_LIVE_APP_FRAMES = 8;

export type OpenedAppTab = {
  tabKey: string;
  instanceKey: string;
  userId: string;
  workspaceId: string;
  conversationId: string;
  appId: string;
  app: MarketplaceApp;
  live: boolean;
  openedOrder: number;
  lastActiveAt: number;
};

export type ConversationAppTabsSlice = {
  tabs: OpenedAppTab[];
  activeTabAppId: string | null;
  launcherOpen: boolean;
};

type InternalTabsState = {
  ownerUserId: string;
  clock: number;
  nextOrder: number;
  byConversation: Record<string, ConversationAppTabsSlice>;
  paneOpenByConversation: Record<string, boolean>;
};

const EMPTY_SLICE: ConversationAppTabsSlice = {
  tabs: [],
  activeTabAppId: null,
  launcherOpen: true,
};

export function buildAppInstanceKey(
  userId: string,
  workspaceId: string,
  conversationId: string,
  app: Pick<MarketplaceApp, "id" | "webUrl" | "mcpUrl">,
): string {
  return JSON.stringify([
    userId,
    workspaceId,
    conversationId,
    app.id,
    app.webUrl,
    app.mcpUrl,
  ]);
}

function createInitialState(ownerUserId = ""): InternalTabsState {
  return {
    ownerUserId,
    clock: 0,
    nextOrder: 0,
    byConversation: {},
    paneOpenByConversation: {},
  };
}

function enforceLiveFrameLimit(
  byConversation: Record<string, ConversationAppTabsSlice>,
  maxLiveFrames: number,
  protectedTabKey?: string,
): Record<string, ConversationAppTabsSlice> {
  const limit = Math.max(1, maxLiveFrames);
  const allLive: OpenedAppTab[] = [];
  for (const slice of Object.values(byConversation)) {
    for (const tab of slice.tabs) {
      if (tab.live) allLive.push(tab);
    }
  }
  if (allLive.length <= limit) return byConversation;

  const evictCount = allLive.length - limit;
  const candidates = allLive
    .filter((tab) => tab.tabKey !== protectedTabKey)
    .sort((a, b) => a.lastActiveAt - b.lastActiveAt || a.openedOrder - b.openedOrder);
  const evictKeys = new Set(candidates.slice(0, evictCount).map((t) => t.tabKey));
  if (evictKeys.size === 0) return byConversation;

  const next: Record<string, ConversationAppTabsSlice> = {};
  for (const [convId, slice] of Object.entries(byConversation)) {
    if (!slice.tabs.some((t) => evictKeys.has(t.tabKey))) {
      next[convId] = slice;
      continue;
    }
    next[convId] = {
      ...slice,
      tabs: slice.tabs.map((t) => (evictKeys.has(t.tabKey) ? { ...t, live: false } : t)),
    };
  }
  return next;
}

export type ConversationAppTabsController = {
  maxLiveFrames: number;
  marketplaceEnabled: boolean;
  getSlice: (conversationId?: string) => ConversationAppTabsSlice;
  getOpenCount: (conversationId?: string) => number;
  isPaneOpen: (conversationId?: string) => boolean;
  setPaneOpen: (conversationId: string, open: boolean) => void;
  togglePaneOpen: (conversationId: string) => void;
  closeAllOpenPanes: () => void;
  openOrActivateTab: (conversation: Conversation, app: MarketplaceApp) => void;
  activateTab: (conversationId: string, appId: string) => void;
  touchForegroundConversation: (conversationId: string) => void;
  openLauncher: (conversationId: string) => void;
  closeTab: (conversationId: string, appId: string) => void;
  syncConversationApps: (conversationId: string, availableApps: MarketplaceApp[]) => void;
  purgeConversation: (conversationId: string) => void;
  clearAllTabs: () => void;
  resetAll: () => void;
  liveTabs: OpenedAppTab[];
  openConversationIds: string[];
};

export function useConversationAppTabsController({
  userId = "",
  marketplaceEnabled = true,
  conversations,
  hasAuthoritativeConversations = false,
  workspaces,
  maxLiveFrames = MAX_LIVE_APP_FRAMES,
}: {
  userId?: string;
  marketplaceEnabled?: boolean;
  conversations?: Record<string, Conversation>;
  hasAuthoritativeConversations?: boolean;
  workspaces?: Workspace[];
  maxLiveFrames?: number;
}): ConversationAppTabsController {
  const [state, setState] = useState<InternalTabsState>(() => createInitialState(userId));

  useEffect(() => {
    setState((prev) => (prev.ownerUserId === userId ? prev : createInitialState(userId)));
  }, [userId]);

  useEffect(() => {
    if (marketplaceEnabled) return;
    setState((prev) =>
      Object.keys(prev.byConversation).length === 0
        ? prev
        : { ...prev, byConversation: {} },
    );
  }, [marketplaceEnabled]);

  const allowedGroupIds = useMemo(() => {
    if (!hasAuthoritativeConversations || !conversations) return null;
    const wsIds = workspaces ? new Set(workspaces.map((w) => w.id)) : null;
    const allowed = new Set<string>();
    for (const conv of Object.values(conversations)) {
      if (conv.type !== "group") continue;
      if (wsIds && !wsIds.has(conv.workspaceId)) continue;
      allowed.add(conv.id);
    }
    return allowed;
  }, [conversations, hasAuthoritativeConversations, workspaces]);

  useEffect(() => {
    if (!allowedGroupIds) return;
    setState((prev) => {
      const staleTabs = Object.keys(prev.byConversation).some((id) => !allowedGroupIds.has(id));
      const stalePanes = Object.keys(prev.paneOpenByConversation).some(
        (id) => !allowedGroupIds.has(id),
      );
      if (!staleTabs && !stalePanes) return prev;
      const nextByConv: Record<string, ConversationAppTabsSlice> = {};
      for (const [id, slice] of Object.entries(prev.byConversation)) {
        if (allowedGroupIds.has(id)) nextByConv[id] = slice;
      }
      const nextPaneOpen: Record<string, boolean> = {};
      for (const [id, open] of Object.entries(prev.paneOpenByConversation)) {
        if (open && allowedGroupIds.has(id)) nextPaneOpen[id] = true;
      }
      return {
        ...prev,
        byConversation: nextByConv,
        paneOpenByConversation: nextPaneOpen,
      };
    });
  }, [allowedGroupIds]);

  const setPaneOpen = useCallback((conversationId: string, open: boolean) => {
    if (!conversationId) return;
    setState((prev) => {
      const current = !!prev.paneOpenByConversation[conversationId];
      if (current === open) return prev;
      const nextMap = { ...prev.paneOpenByConversation };
      if (open) nextMap[conversationId] = true;
      else delete nextMap[conversationId];
      return { ...prev, paneOpenByConversation: nextMap };
    });
  }, []);

  const togglePaneOpen = useCallback((conversationId: string) => {
    if (!conversationId) return;
    setState((prev) => {
      const nextMap = { ...prev.paneOpenByConversation };
      if (nextMap[conversationId]) delete nextMap[conversationId];
      else nextMap[conversationId] = true;
      return { ...prev, paneOpenByConversation: nextMap };
    });
  }, []);

  const closeAllOpenPanes = useCallback(() => {
    setState((prev) =>
      Object.keys(prev.paneOpenByConversation).length === 0
        ? prev
        : { ...prev, paneOpenByConversation: {} },
    );
  }, []);

  const openOrActivateTab = useCallback(
    (conversation: Conversation, app: MarketplaceApp) => {
      if (!userId || !marketplaceEnabled || conversation.type !== "group") return;
      setState((prev) => {
        const base = prev.ownerUserId === userId ? prev : createInitialState(userId);
        const slice = base.byConversation[conversation.id] || EMPTY_SLICE;
        const tabKey = `${conversation.id}::${app.id}`;
        const instanceKey = buildAppInstanceKey(
          userId,
          conversation.workspaceId,
          conversation.id,
          app,
        );
        const clock = base.clock + 1;
        let nextOrder = base.nextOrder;
        const existing = slice.tabs.find((t) => t.appId === app.id);
        const nextTabs = existing
          ? slice.tabs.map((t) =>
              t.appId === app.id
                ? {
                    ...t,
                    app,
                    instanceKey,
                    live: true,
                    lastActiveAt: clock,
                  }
                : t,
            )
          : [
              ...slice.tabs,
              {
                tabKey,
                instanceKey,
                userId,
                workspaceId: conversation.workspaceId,
                conversationId: conversation.id,
                appId: app.id,
                app,
                live: true,
                openedOrder: ++nextOrder,
                lastActiveAt: clock,
              },
            ];
        const withActivated = {
          ...base.byConversation,
          [conversation.id]: {
            tabs: nextTabs,
            activeTabAppId: app.id,
            launcherOpen: false,
          },
        };
        return {
          ...base,
          clock,
          nextOrder,
          byConversation: enforceLiveFrameLimit(withActivated, maxLiveFrames, tabKey),
        };
      });
    },
    [marketplaceEnabled, maxLiveFrames, userId],
  );

  const activateTab = useCallback(
    (conversationId: string, appId: string) => {
      if (!userId || !marketplaceEnabled) return;
      setState((prev) => {
        if (prev.ownerUserId !== userId) return prev;
        const slice = prev.byConversation[conversationId];
        const target = slice?.tabs.find((t) => t.appId === appId);
        if (!slice || !target) return prev;
        if (
          slice.activeTabAppId === appId &&
          !slice.launcherOpen &&
          target.live &&
          target.lastActiveAt === prev.clock
        ) {
          return prev;
        }
        const clock = prev.clock + 1;
        const withActivated = {
          ...prev.byConversation,
          [conversationId]: {
            tabs: slice.tabs.map((t) =>
              t.appId === appId ? { ...t, live: true, lastActiveAt: clock } : t,
            ),
            activeTabAppId: appId,
            launcherOpen: false,
          },
        };
        return {
          ...prev,
          clock,
          byConversation: enforceLiveFrameLimit(withActivated, maxLiveFrames, target.tabKey),
        };
      });
    },
    [marketplaceEnabled, maxLiveFrames, userId],
  );

  const touchForegroundConversation = useCallback(
    (conversationId: string) => {
      if (!userId || !marketplaceEnabled || !conversationId) return;
      setState((prev) => {
        if (prev.ownerUserId !== userId) return prev;
        const slice = prev.byConversation[conversationId];
        if (!slice || slice.launcherOpen || !slice.activeTabAppId) return prev;
        const target = slice.tabs.find((t) => t.appId === slice.activeTabAppId);
        if (!target) return prev;
        if (target.live && target.lastActiveAt === prev.clock) return prev;
        const clock = prev.clock + 1;
        const withTouched = {
          ...prev.byConversation,
          [conversationId]: {
            ...slice,
            tabs: slice.tabs.map((t) =>
              t.appId === target.appId ? { ...t, live: true, lastActiveAt: clock } : t,
            ),
          },
        };
        return {
          ...prev,
          clock,
          byConversation: enforceLiveFrameLimit(withTouched, maxLiveFrames, target.tabKey),
        };
      });
    },
    [marketplaceEnabled, maxLiveFrames, userId],
  );

  const openLauncher = useCallback((conversationId: string) => {
    setState((prev) => {
      const slice = prev.byConversation[conversationId];
      if (!slice || slice.launcherOpen) return prev;
      return {
        ...prev,
        byConversation: {
          ...prev.byConversation,
          [conversationId]: { ...slice, launcherOpen: true },
        },
      };
    });
  }, []);

  const closeTab = useCallback(
    (conversationId: string, appId: string) => {
      setState((prev) => {
        const slice = prev.byConversation[conversationId];
        if (!slice) return prev;
        const idx = slice.tabs.findIndex((t) => t.appId === appId);
        if (idx === -1) return prev;
        const remaining = slice.tabs.filter((t) => t.appId !== appId);
        if (remaining.length === 0) {
          const nextByConv = { ...prev.byConversation };
          delete nextByConv[conversationId];
          return { ...prev, byConversation: nextByConv };
        }
        const wasActive = slice.activeTabAppId === appId;
        const fallback = wasActive
          ? remaining[Math.min(idx, remaining.length - 1)]!
          : remaining.find((t) => t.appId === slice.activeTabAppId) || remaining[0]!;
        let clock = prev.clock;
        const shouldWake = wasActive && !slice.launcherOpen;
        if (shouldWake) clock += 1;
        const updatedTabs = shouldWake
          ? remaining.map((t) =>
              t.appId === fallback.appId ? { ...t, live: true, lastActiveAt: clock } : t,
            )
          : remaining;
        const nextByConv = {
          ...prev.byConversation,
          [conversationId]: {
            tabs: updatedTabs,
            activeTabAppId: fallback.appId,
            launcherOpen: slice.launcherOpen,
          },
        };
        return {
          ...prev,
          clock,
          byConversation: shouldWake
            ? enforceLiveFrameLimit(nextByConv, maxLiveFrames, fallback.tabKey)
            : nextByConv,
        };
      });
    },
    [maxLiveFrames],
  );

  const syncConversationApps = useCallback(
    (conversationId: string, availableApps: MarketplaceApp[]) => {
      setState((prev) => {
        const slice = prev.byConversation[conversationId];
        if (!slice || slice.tabs.length === 0) return prev;
        const byId = new Map(availableApps.map((a) => [a.id, a]));
        let changed = false;
        const surviving: OpenedAppTab[] = [];
        for (const tab of slice.tabs) {
          const latest = byId.get(tab.appId);
          if (
            !latest ||
            latest.enabled === false ||
            latest.webUrl !== tab.app.webUrl ||
            latest.mcpUrl !== tab.app.mcpUrl
          ) {
            changed = true;
            continue;
          }
          if (
            latest.name !== tab.app.name ||
            latest.slug !== tab.app.slug ||
            latest.description !== tab.app.description ||
            latest.servingDraft !== tab.app.servingDraft
          ) {
            changed = true;
            surviving.push({ ...tab, app: latest });
          } else {
            surviving.push(tab);
          }
        }
        if (!changed) return prev;
        if (surviving.length === 0) {
          const nextByConv = { ...prev.byConversation };
          delete nextByConv[conversationId];
          return { ...prev, byConversation: nextByConv };
        }
        const activeStillPresent = surviving.some((t) => t.appId === slice.activeTabAppId);
        const nextActive = activeStillPresent
          ? slice.activeTabAppId
          : surviving[surviving.length - 1]!.appId;
        return {
          ...prev,
          byConversation: {
            ...prev.byConversation,
            [conversationId]: {
              tabs: surviving,
              activeTabAppId: nextActive,
              launcherOpen: slice.launcherOpen,
            },
          },
        };
      });
    },
    [],
  );

  const purgeConversation = useCallback((conversationId: string) => {
    if (!conversationId) return;
    setState((prev) => {
      if (
        !prev.byConversation[conversationId] &&
        !prev.paneOpenByConversation[conversationId]
      ) {
        return prev;
      }
      const nextByConv = { ...prev.byConversation };
      delete nextByConv[conversationId];
      const nextPanes = { ...prev.paneOpenByConversation };
      delete nextPanes[conversationId];
      return {
        ...prev,
        byConversation: nextByConv,
        paneOpenByConversation: nextPanes,
      };
    });
  }, []);

  const clearAllTabs = useCallback(() => {
    setState((prev) =>
      Object.keys(prev.byConversation).length === 0
        ? prev
        : { ...prev, byConversation: {} },
    );
  }, []);

  const resetAll = useCallback(() => {
    setState(createInitialState(userId));
  }, [userId]);

  const effectiveByConversation = useMemo(() => {
    if (!userId || !marketplaceEnabled || state.ownerUserId !== userId) {
      return {} as Record<string, ConversationAppTabsSlice>;
    }
    if (!allowedGroupIds) return state.byConversation;
    const filtered: Record<string, ConversationAppTabsSlice> = {};
    for (const [convId, slice] of Object.entries(state.byConversation)) {
      if (allowedGroupIds.has(convId)) filtered[convId] = slice;
    }
    return filtered;
  }, [allowedGroupIds, marketplaceEnabled, state.byConversation, state.ownerUserId, userId]);

  // Preserve monotonic `openedOrder` so React never reorders sibling DOM frame slots
  // (moving an iframe's ancestor via `insertBefore` would reload the iframe).
  const liveTabs = useMemo(() => {
    const collected: OpenedAppTab[] = [];
    for (const slice of Object.values(effectiveByConversation)) {
      for (const tab of slice.tabs) {
        if (tab.live) collected.push(tab);
      }
    }
    return collected.sort((a, b) => a.openedOrder - b.openedOrder);
  }, [effectiveByConversation]);

  const openConversationIds = useMemo(
    () => Object.keys(effectiveByConversation).filter((id) => effectiveByConversation[id]!.tabs.length > 0),
    [effectiveByConversation],
  );

  const getSlice = useCallback(
    (conversationId?: string): ConversationAppTabsSlice =>
      (conversationId && effectiveByConversation[conversationId]) || EMPTY_SLICE,
    [effectiveByConversation],
  );

  const getOpenCount = useCallback(
    (conversationId?: string): number =>
      conversationId ? effectiveByConversation[conversationId]?.tabs.length || 0 : 0,
    [effectiveByConversation],
  );

  const isPaneOpen = useCallback(
    (conversationId?: string): boolean => {
      if (!userId || state.ownerUserId !== userId || !conversationId) return false;
      if (allowedGroupIds && !allowedGroupIds.has(conversationId)) return false;
      return !!state.paneOpenByConversation[conversationId];
    },
    [allowedGroupIds, state.ownerUserId, state.paneOpenByConversation, userId],
  );

  return useMemo(
    () => ({
      maxLiveFrames,
      marketplaceEnabled,
      getSlice,
      getOpenCount,
      isPaneOpen,
      setPaneOpen,
      togglePaneOpen,
      closeAllOpenPanes,
      openOrActivateTab,
      activateTab,
      touchForegroundConversation,
      openLauncher,
      closeTab,
      syncConversationApps,
      purgeConversation,
      clearAllTabs,
      resetAll,
      liveTabs,
      openConversationIds,
    }),
    [
      activateTab,
      clearAllTabs,
      closeAllOpenPanes,
      closeTab,
      getOpenCount,
      getSlice,
      isPaneOpen,
      liveTabs,
      marketplaceEnabled,
      maxLiveFrames,
      openConversationIds,
      openLauncher,
      openOrActivateTab,
      purgeConversation,
      resetAll,
      setPaneOpen,
      syncConversationApps,
      togglePaneOpen,
      touchForegroundConversation,
    ],
  );
}

const ConversationAppTabsContext = createContext<ConversationAppTabsController | null>(null);

export function ConversationAppTabsProvider({
  value,
  children,
}: {
  value: ConversationAppTabsController;
  children: ReactNode;
}) {
  return createElement(ConversationAppTabsContext.Provider, { value }, children);
}

export function useConversationOpenAppCount(conversationId?: string): number {
  const controller = useContext(ConversationAppTabsContext);
  return controller ? controller.getOpenCount(conversationId) : 0;
}
