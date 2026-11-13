import { useCallback, useEffect, useRef, useState } from "react";
import { Hash } from "lucide-react";
import type { Conversation, Workspace } from "@teamie/api-client";
import { chatStore, compareTimestampsDesc } from "../store";
import { Button } from "../ui";
import { MobileNavButton } from "./page-header";
import { ICON } from "../icons";
import { pickAdjacentConversation, route } from "../shell-sidebar";

const navStorageKey = (userId: string) => `teamie.nav.${userId}`;

export type PersistedShellNav = {
  lastChatPath: string;
  lastConversationByWorkspace: Record<string, string>;
  recentConversationIds: string[];
};

export function readPersistedShellNav(userId?: string): PersistedShellNav {
  if (!userId) {
    return {
      lastChatPath: "",
      lastConversationByWorkspace: {},
      recentConversationIds: [],
    };
  }
  try {
    const raw = localStorage.getItem(navStorageKey(userId));
    if (!raw) {
      return {
        lastChatPath: "",
        lastConversationByWorkspace: {},
        recentConversationIds: [],
      };
    }
    const parsed = JSON.parse(raw) as Partial<PersistedShellNav>;
    const lastChatPath =
      typeof parsed?.lastChatPath === "string" ? parsed.lastChatPath : "";
    const lastConversationByWorkspace: Record<string, string> = {};
    if (
      parsed?.lastConversationByWorkspace &&
      typeof parsed.lastConversationByWorkspace === "object"
    ) {
      for (const [wsId, convId] of Object.entries(
        parsed.lastConversationByWorkspace,
      )) {
        if (typeof convId === "string" && convId) {
          lastConversationByWorkspace[wsId] = convId;
        }
      }
    }
    const recentConversationIds = Array.isArray(parsed?.recentConversationIds)
      ? parsed.recentConversationIds
          .filter((id): id is string => typeof id === "string" && !!id)
          .slice(0, 20)
      : [];
    return {
      lastChatPath,
      lastConversationByWorkspace,
      recentConversationIds,
    };
  } catch {
    return {
      lastChatPath: "",
      lastConversationByWorkspace: {},
      recentConversationIds: [],
    };
  }
}

export function writePersistedShellNav(userId: string, nav: PersistedShellNav) {
  if (!userId) return;
  try {
    localStorage.setItem(navStorageKey(userId), JSON.stringify(nav));
  } catch {
    /* Ignore storage access errors. */
  }
}

export function clearPersistedShellNav(userId?: string) {
  if (!userId) return;
  try {
    localStorage.removeItem(navStorageKey(userId));
  } catch {
    /* Ignore storage access errors. */
  }
}

const chatPathConversationId = (path: string) => /\/c\/([^/?#]+)$/.exec(path)?.[1] || "";

export function pruneConversationIdsFromNav(
  nav: PersistedShellNav,
  isAllowed: (conversationId: string) => boolean,
  fallbackChatPath = "",
): PersistedShellNav | null {
  let changed = false;
  const pathConvId = chatPathConversationId(nav.lastChatPath);
  const nextChatPath = pathConvId && !isAllowed(pathConvId) ? fallbackChatPath : nav.lastChatPath;
  if (nextChatPath !== nav.lastChatPath) changed = true;

  const nextByWorkspace: Record<string, string> = {};
  for (const [wsId, convId] of Object.entries(nav.lastConversationByWorkspace)) {
    if (isAllowed(convId)) nextByWorkspace[wsId] = convId;
    else changed = true;
  }
  const nextRecent = nav.recentConversationIds.filter((id) => isAllowed(id));
  if (nextRecent.length !== nav.recentConversationIds.length) changed = true;

  if (!changed) return null;
  return {
    lastChatPath: nextChatPath,
    lastConversationByWorkspace: nextByWorkspace,
    recentConversationIds: nextRecent,
  };
}

export type ShellPersistedNavController = ReturnType<typeof useShellPersistedNav>;

export function useShellPersistedNav(
  userId?: string,
  onClearPreviousUser?: (previousUserId: string) => void,
) {
  const [initialNav] = useState(() => readPersistedShellNav(userId));
  const navUserIdRef = useRef(userId);
  const onClearPreviousRef = useRef(onClearPreviousUser);
  useEffect(() => {
    onClearPreviousRef.current = onClearPreviousUser;
  });
  const lastConversationByWorkspace = useRef<Record<string, string>>(
    initialNav.lastConversationByWorkspace,
  );
  const [rememberedByWorkspace, setRememberedByWorkspace] = useState(
    initialNav.lastConversationByWorkspace,
  );
  const lastChatRoute = useRef<string>(initialNav.lastChatPath);
  const [lastChatPath, setLastChatPath] = useState(initialNav.lastChatPath);
  const recentConversationsRef = useRef<string[]>(
    initialNav.recentConversationIds,
  );
  const [recentConversationIds, setRecentConversationIds] = useState<string[]>(
    initialNav.recentConversationIds,
  );

  const applyNav = useCallback((next: PersistedShellNav) => {
    lastConversationByWorkspace.current = next.lastConversationByWorkspace;
    lastChatRoute.current = next.lastChatPath;
    recentConversationsRef.current = next.recentConversationIds;
    setRememberedByWorkspace(next.lastConversationByWorkspace);
    setLastChatPath(next.lastChatPath);
    setRecentConversationIds(next.recentConversationIds);
  }, []);

  useEffect(() => {
    if (navUserIdRef.current && navUserIdRef.current !== userId) {
      clearPersistedShellNav(navUserIdRef.current);
      onClearPreviousRef.current?.(navUserIdRef.current);
    }
    navUserIdRef.current = userId;
    applyNav(readPersistedShellNav(userId));
  }, [userId, applyNav]);

  const pruneConversation = useCallback(
    (removedId: string, fallbackChatPath = "") => {
      if (!userId) return;
      const pruned = pruneConversationIdsFromNav(
        {
          lastChatPath: lastChatRoute.current,
          lastConversationByWorkspace: lastConversationByWorkspace.current,
          recentConversationIds: recentConversationsRef.current,
        },
        (id) => id !== removedId,
        fallbackChatPath,
      );
      if (!pruned) return;
      applyNav(pruned);
      writePersistedShellNav(userId, pruned);
    },
    [userId, applyNav],
  );

  const clearForUser = useCallback(
    (targetUserId?: string) => {
      clearPersistedShellNav(targetUserId);
      applyNav({
        lastChatPath: "",
        lastConversationByWorkspace: {},
        recentConversationIds: [],
      });
    },
    [applyNav],
  );

  return {
    lastChatPath,
    rememberedByWorkspace,
    recentConversationIds,
    lastConversationByWorkspace,
    lastChatRoute,
    recentConversationsRef,
    applyNav,
    pruneConversation,
    clearForUser,
  };
}

export function useSyncShellPersistedNav(
  nav: ShellPersistedNavController,
  {
    userId,
    workspace,
    conversation,
    conversations,
  }: {
    userId?: string;
    workspace?: Workspace;
    conversation?: Conversation;
    conversations: Record<string, Conversation>;
  },
) {
  const {
    lastConversationByWorkspace,
    lastChatRoute,
    recentConversationsRef,
    applyNav,
  } = nav;
  useEffect(() => {
    if (
      conversation &&
      workspace &&
      conversation.workspaceId === workspace.id &&
      userId
    ) {
      const nextRoute = route(conversation);
      const nextByWorkspace =
        lastConversationByWorkspace.current[workspace.id] === conversation.id
          ? lastConversationByWorkspace.current
          : {
              ...lastConversationByWorkspace.current,
              [workspace.id]: conversation.id,
            };
      const prevRecent = recentConversationsRef.current;
      const nextRecent =
        prevRecent[0] === conversation.id
          ? prevRecent
          : [
              conversation.id,
              ...prevRecent.filter((id) => id !== conversation.id),
            ].slice(0, 20);
      const nextNav: PersistedShellNav = {
        lastChatPath: nextRoute,
        lastConversationByWorkspace: nextByWorkspace,
        recentConversationIds: nextRecent,
      };
      applyNav(nextNav);
      writePersistedShellNav(userId, nextNav);
    }
  }, [
    conversation,
    workspace,
    userId,
    lastConversationByWorkspace,
    recentConversationsRef,
    applyNav,
  ]);

  useEffect(() => {
    if (!userId || !chatStore.hasAuthoritativeConversations) return;
    const pruned = pruneConversationIdsFromNav(
      {
        lastChatPath: lastChatRoute.current,
        lastConversationByWorkspace: lastConversationByWorkspace.current,
        recentConversationIds: recentConversationsRef.current,
      },
      (id) => Boolean(conversations[id]),
    );
    if (!pruned) return;
    applyNav(pruned);
    writePersistedShellNav(userId, pruned);
  }, [
    userId,
    conversations,
    lastChatRoute,
    lastConversationByWorkspace,
    recentConversationsRef,
    applyNav,
  ]);
}

let trackedNavUserId = chatStore.getSnapshot().userId;
chatStore.subscribe(() => {
  const snap = chatStore.getSnapshot();
  if (snap.userId !== trackedNavUserId) {
    if (!snap.userId && trackedNavUserId) {
      clearPersistedShellNav(trackedNavUserId);
    }
    trackedNavUserId = snap.userId;
  }
});

export function useDocumentTitle(
  pageTitle: string,
  urgentCount: number,
  hasChannelUnread: boolean,
) {
  useEffect(() => {
    const base = pageTitle ? `${pageTitle} · Teamie` : "Teamie";
    const prefix =
      urgentCount > 0
        ? `(${urgentCount > 99 ? "99+" : urgentCount}) `
        : hasChannelUnread
          ? "• "
          : "";
    document.title = `${prefix}${base}`;
  }, [pageTitle, urgentCount, hasChannelUnread]);
  useEffect(
    () => () => {
      document.title = "Teamie";
    },
    [],
  );
}

export function useMediaQuery(query: string) {
  const [matches, setMatches] = useState(
    () => window.matchMedia(query).matches,
  );
  useEffect(() => {
    const media = window.matchMedia(query);
    const change = () => setMatches(media.matches);
    change();
    media.addEventListener("change", change);
    return () => media.removeEventListener("change", change);
  }, [query]);
  return matches;
}

export function UnavailableConversationView({
  hasMatch,
  onOpenSidebar,
  onBack,
}: {
  hasMatch: boolean;
  onOpenSidebar: () => void;
  onBack: () => void;
}) {
  return (
    <div className="unavailable-view">
      <MobileNavButton onClick={onOpenSidebar} />
      <span className="large-symbol">
        <Hash size={ICON.xl} />
      </span>
      <h2>{hasMatch ? "这个对话暂时无法访问" : "选择一个对话"}</h2>
      <p>
        {hasMatch
          ? "你可能已离开工作空间，或尚未加入这个对话。"
          : "打开导航，选择频道或私信。"}
      </p>
      <Button onClick={onBack}>返回对话</Button>
    </div>
  );
}

export function useShellKeyboardShortcuts({
  channels, direct, workspace, conversationsById, workspaces, conversationId,
  hasOverlayModal, quickSwitcherOpen, shortcutsOpen,
  setShortcutsOpen, setQuickSwitcherOpen, navigate, isUnread,
}: {
  channels: Conversation[]; direct: Conversation[]; workspace?: Workspace;
  conversationsById: Record<string, Conversation>; workspaces?: Workspace[];
  conversationId?: string; hasOverlayModal: boolean;
  quickSwitcherOpen: boolean; shortcutsOpen: boolean;
  setShortcutsOpen: (value: boolean | ((open: boolean) => boolean)) => void;
  setQuickSwitcherOpen: (value: boolean | ((open: boolean) => boolean)) => void;
  navigate: (to: string) => void;
  /** Sidebar unread judgement (`summary.hasUnread`) for Alt+Shift+↑/↓. */
  isUnread?: (conversation: Conversation) => boolean;
}) {
  const isUnreadRef = useRef(isUnread);
  useEffect(() => {
    isUnreadRef.current = isUnread;
  });
  useEffect(() => {
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.isComposing || event.keyCode === 229) return;
      const anyModalOpen = !!document.querySelector(".modal-backdrop");
      if (
        (event.metaKey || event.ctrlKey) &&
        !event.altKey &&
        !event.shiftKey &&
        event.key.toLowerCase() === "k"
      ) {
        if (anyModalOpen && !quickSwitcherOpen && !shortcutsOpen) return;
        event.preventDefault();
        setShortcutsOpen(false);
        setQuickSwitcherOpen((open) => !open);
        return;
      }
      const target = event.target as HTMLElement | null;
      const inEditable =
        !!target &&
        (target.tagName === "INPUT" ||
          target.tagName === "TEXTAREA" ||
          target.tagName === "SELECT" ||
          target.isContentEditable);
      const isComposerTextarea =
        target instanceof HTMLTextAreaElement &&
        (!!target.closest(".composer") ||
          target.getAttribute("aria-label") === "消息内容");
      const canNavigateFromComposer =
        isComposerTextarea &&
        target.getAttribute("aria-expanded") !== "true" &&
        (target.value === "" ||
          (!target.value.includes("\n") &&
            target.selectionStart === target.selectionEnd &&
            (target.selectionStart === 0 ||
              target.selectionStart === target.value.length)));
      if ((inEditable && !canNavigateFromComposer) || hasOverlayModal || anyModalOpen)
        return;
      if (
        event.altKey &&
        !event.metaKey &&
        !event.ctrlKey &&
        (event.key === "ArrowUp" || event.key === "ArrowDown")
      ) {
        const direction = event.key === "ArrowDown" ? 1 : -1;
        const ordered = [...channels, ...direct];
        const orderedWorkspaces = [
          ...(workspace ? [workspace] : []),
          ...(workspaces || []).filter((w) => w.id !== workspace?.id),
        ];
        const allOrdered = orderedWorkspaces.flatMap((ws) => {
          if (ws.id === workspace?.id) return ordered;
          const inWs = Object.values(conversationsById).filter((c) => c.workspaceId === ws.id);
          return [
            ...inWs.filter((c) => c.type === "channel").sort((a, b) => a.name.localeCompare(b.name)),
            ...inWs.filter((c) => c.type !== "channel").sort((a, b) => compareTimestampsDesc(a.updatedAt, b.updatedAt)),
          ];
        });
        const pool = event.shiftKey || ordered.length === 0 ? allOrdered : ordered;
        const next = pickAdjacentConversation(
          pool, conversationId, direction, event.shiftKey, isUnreadRef.current,
        );
        if (next) {
          event.preventDefault();
          navigate(route(next));
        }
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [
    channels, direct, workspace, conversationsById, workspaces, conversationId,
    hasOverlayModal, quickSwitcherOpen, shortcutsOpen,
    setShortcutsOpen, setQuickSwitcherOpen, navigate,
  ]);
}
