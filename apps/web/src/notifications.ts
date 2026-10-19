import { useEffect, useState } from "react";
import type { ChatSnapshot } from "./store";

export const NOTIFICATION_STORAGE_KEY = "teamie.notifications";

export type NotificationPreferences = {
  enabled: boolean;
  showPreview: boolean;
};

export type BrowserNotificationPermission =
  | NotificationPermission
  | "unsupported";

const DEFAULT_PREFERENCES: NotificationPreferences = {
  enabled: false,
  showPreview: false,
};

let fallbackPreferences: NotificationPreferences | null = null;
const listeners = new Set<(prefs: NotificationPreferences) => void>();
const activeNotifications = new Set<{ close?: () => void }>();
let notifiedUserId: string | null = null;
const notifiedMessageIds = new Set<string>();
let registeredConversationId: string | null | undefined = undefined;
let registeredNavigate: ((to: string) => void) | null = null;

export function getNotificationPermission(): BrowserNotificationPermission {
  if (
    typeof window === "undefined" ||
    !("Notification" in window) ||
    typeof window.Notification !== "function"
  ) {
    return "unsupported";
  }
  return window.Notification.permission;
}

export function getNotificationPreferences(): NotificationPreferences {
  if (fallbackPreferences) return fallbackPreferences;
  try {
    const raw = window.localStorage?.getItem(NOTIFICATION_STORAGE_KEY);
    if (raw) {
      const parsed = JSON.parse(raw) as Partial<NotificationPreferences>;
      return {
        enabled: Boolean(parsed?.enabled),
        showPreview: Boolean(parsed?.showPreview),
      };
    }
  } catch {
    // Ignore storage access or parse errors in restricted contexts
  }
  return { ...DEFAULT_PREFERENCES };
}

export function closeActiveNotifications(): void {
  for (const item of activeNotifications) {
    try {
      item.close?.();
    } catch {
      // Ignore close errors
    }
  }
  activeNotifications.clear();
}

export function resetNotificationSession(userId: string | null = null): void {
  closeActiveNotifications();
  if (notifiedUserId !== userId) {
    notifiedUserId = userId;
    notifiedMessageIds.clear();
  }
}

export function setNotificationPreferences(
  patch: Partial<NotificationPreferences>,
): NotificationPreferences {
  const current = getNotificationPreferences();
  const next: NotificationPreferences = {
    enabled: patch.enabled ?? current.enabled,
    showPreview: patch.showPreview ?? current.showPreview,
  };
  try {
    window.localStorage?.setItem(
      NOTIFICATION_STORAGE_KEY,
      JSON.stringify(next),
    );
    fallbackPreferences = null;
  } catch {
    fallbackPreferences = next;
  }
  if (!next.enabled) {
    closeActiveNotifications();
  }
  for (const listener of listeners) listener(next);
  return next;
}

export async function enableBrowserNotifications(): Promise<{
  granted: boolean;
  permission: BrowserNotificationPermission;
}> {
  const permission = getNotificationPermission();
  if (permission === "unsupported") {
    setNotificationPreferences({ enabled: false });
    return { granted: false, permission: "unsupported" };
  }
  if (permission === "denied") {
    setNotificationPreferences({ enabled: false });
    return { granted: false, permission: "denied" };
  }
  if (permission === "default") {
    let requested: NotificationPermission = "default";
    try {
      requested = await window.Notification.requestPermission();
    } catch {
      requested = getNotificationPermission() as NotificationPermission;
    }
    if (requested === "granted") {
      setNotificationPreferences({ enabled: true });
      return { granted: true, permission: "granted" };
    }
    setNotificationPreferences({ enabled: false });
    return { granted: false, permission: requested };
  }
  setNotificationPreferences({ enabled: true });
  return { granted: true, permission: "granted" };
}

export function setActiveNotificationContext(
  conversationId: string | null | undefined,
  navigate?: ((to: string) => void) | null,
): void {
  registeredConversationId = conversationId;
  if (navigate !== undefined) {
    registeredNavigate = navigate;
  }
}

function getCurrentConversationId(): string | null {
  if (registeredConversationId !== undefined) {
    return registeredConversationId;
  }
  if (typeof window !== "undefined") {
    const match = window.location.pathname.match(/\/app\/w\/[^/]+\/c\/([^/?#]+)/);
    if (match?.[1]) {
      try {
        return decodeURIComponent(match[1]);
      } catch {
        return match[1];
      }
    }
  }
  return null;
}

function isTabInBackground(): boolean {
  if (typeof document === "undefined") return false;
  return document.visibilityState === "hidden" || document.hidden === true;
}

export function notifySyncedMessages(
  candidates: ReadonlyArray<{ conversationId: string; messageId: string }>,
  snapshot: ChatSnapshot,
  currentUserId: string,
): void {
  if (!candidates.length || snapshot.userId !== currentUserId) return;
  if (notifiedUserId !== currentUserId) {
    resetNotificationSession(currentUserId);
  }
  const prefs = getNotificationPreferences();
  if (!prefs.enabled || getNotificationPermission() !== "granted") return;

  for (const { conversationId, messageId } of candidates) {
    if (notifiedMessageIds.has(messageId)) continue;
    const conversation = snapshot.conversations[conversationId];
    if (!conversation) continue;
    const cached = snapshot.messages[conversationId] || [];
    const message = cached.find((m) => m.id === messageId);
    if (
      !message ||
      message.status ||
      message.retractedAt ||
      message.senderId === currentUserId ||
      message.seq <= conversation.readSeq
    ) {
      continue;
    }

    const isMention = Boolean(
      message.mentions?.some((mention) => mention.userId === currentUserId),
    );
    const isDirect = conversation.type === "direct";
    let isReplyToMe = false;
    if (message.replyToId) {
      const parent = cached.find((m) => m.id === message.replyToId);
      if (parent && !parent.retractedAt && parent.senderId === currentUserId) {
        isReplyToMe = true;
      }
    }
    if (!isMention && !isDirect && !isReplyToMe) continue;

    if (!isTabInBackground() && getCurrentConversationId() === conversation.id) {
      continue;
    }

    notifiedMessageIds.add(messageId);

    const title =
      conversation.type === "direct"
        ? message.senderName
        : `${message.senderName} · ${conversation.name || "小组对话"}`;
    const reasonText =
      isMention && isReplyToMe
        ? "提及并回复了你"
        : isMention
          ? "在消息中提及了你"
          : isReplyToMe
            ? "回复了你的消息"
            : "发来一条新私信";
    const previewText = message.body
      ? message.body.length > 120
        ? `${message.body.slice(0, 120)}…`
        : message.body
      : message.attachments?.length
        ? `[${message.attachments.length} 个文件]`
        : reasonText;
    const body = prefs.showPreview ? previewText : reasonText;
    const targetUrl = `/app/w/${encodeURIComponent(conversation.workspaceId)}/c/${encodeURIComponent(conversation.id)}?message=${encodeURIComponent(message.id)}`;

    try {
      const instance = new window.Notification(title, {
        body,
        tag: `teamie-message-${message.id}`,
      });
      activeNotifications.add(instance);
      instance.onclose = () => {
        activeNotifications.delete(instance);
      };
      instance.onclick = (event) => {
        event?.preventDefault?.();
        try {
          window.focus?.();
        } catch {
          // Ignore focus errors
        }
        try {
          instance.close?.();
        } catch {
          // Ignore close errors
        }
        activeNotifications.delete(instance);
        if (registeredNavigate) {
          registeredNavigate(targetUrl);
        } else if (typeof window !== "undefined") {
          window.location.assign(targetUrl);
        }
      };
    } catch {
      // Ignore Notification constructor errors in restricted environments
    }
  }
}

export function useNotificationPreferences() {
  const [preferences, setPreferencesState] = useState<NotificationPreferences>(
    () => getNotificationPreferences(),
  );
  const [permission, setPermission] = useState<BrowserNotificationPermission>(
    () => getNotificationPermission(),
  );

  useEffect(() => {
    const sync = (next: NotificationPreferences = getNotificationPreferences()) => {
      setPreferencesState(next);
      setPermission(getNotificationPermission());
    };
    listeners.add(sync);

    const onStorage = (event: StorageEvent) => {
      if (!event.key || event.key === NOTIFICATION_STORAGE_KEY) {
        fallbackPreferences = null;
        sync();
      }
    };
    const onVisibilityOrFocus = () => {
      setPermission(getNotificationPermission());
    };

    window.addEventListener("storage", onStorage);
    window.addEventListener("focus", onVisibilityOrFocus);
    document.addEventListener("visibilitychange", onVisibilityOrFocus);
    sync();

    return () => {
      listeners.delete(sync);
      window.removeEventListener("storage", onStorage);
      window.removeEventListener("focus", onVisibilityOrFocus);
      document.removeEventListener("visibilitychange", onVisibilityOrFocus);
    };
  }, []);

  return {
    preferences,
    permission,
    setPreferences: setNotificationPreferences,
    enableNotifications: async () => {
      const result = await enableBrowserNotifications();
      setPermission(result.permission);
      return result;
    },
    disableNotifications: () => setNotificationPreferences({ enabled: false }),
  };
}
