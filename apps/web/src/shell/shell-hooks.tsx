import { useEffect, useState } from "react";
import { Hash, Menu } from "lucide-react";
import type { Conversation, Workspace } from "@teamie/api-client";
import { chatStore, compareTimestampsDesc } from "../store";
import { Button, IconButton } from "../ui";
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
      <IconButton
        label="打开频道列表"
        className="mobile-menu"
        onClick={onOpenSidebar}
      >
        <Menu size={ICON.lg} />
      </IconButton>
      <span className="large-symbol">
        <Hash size={ICON.xl} />
      </span>
      <h2>{hasMatch ? "这个对话暂时无法访问" : "选择一个对话"}</h2>
      <p>
        {hasMatch
          ? "你可能已离开工作空间，或尚未加入这个对话。"
          : "打开对话列表，选择频道或私信。"}
      </p>
      <Button onClick={onBack}>返回对话</Button>
    </div>
  );
}

export function useShellKeyboardShortcuts({
  channels, direct, workspace, conversationsById, workspaces, conversationId,
  hasOverlayModal, quickSwitcherOpen, shortcutsOpen,
  setShortcutsOpen, setQuickSwitcherOpen, navigate,
}: {
  channels: Conversation[]; direct: Conversation[]; workspace?: Workspace;
  conversationsById: Record<string, Conversation>; workspaces?: Workspace[];
  conversationId?: string; hasOverlayModal: boolean;
  quickSwitcherOpen: boolean; shortcutsOpen: boolean;
  setShortcutsOpen: (value: boolean | ((open: boolean) => boolean)) => void;
  setQuickSwitcherOpen: (value: boolean | ((open: boolean) => boolean)) => void;
  navigate: (to: string) => void;
}) {
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
        const next = pickAdjacentConversation(pool, conversationId, direction, event.shiftKey);
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
