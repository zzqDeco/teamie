import { useEffect, useState } from "react";
import { chatStore } from "./store";
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

const fallbackPreferencesByUser = new Map<string, NotificationPreferences>();
const listeners = new Set<(prefs: NotificationPreferences) => void>();
const activeNotifications = new Set<{ close?: () => void }>();
let notifiedUserId: string | null = null;
const notifiedMessageIds = new Set<string>();
let registeredConversationId: string | null | undefined = undefined;
let registeredNavigate: ((to: string) => void) | null = null;

function resolveUserId(userId?: string | null): string | null {
  if (userId !== undefined) return userId || null;
  return notifiedUserId || chatStore.getSnapshot().userId || null;
}

export function notificationStorageKey(userId?: string | null): string | null {
  const resolved = resolveUserId(userId);
  return resolved ? `${NOTIFICATION_STORAGE_KEY}.${resolved}` : null;
}

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

export function getNotificationPreferences(
  userId?: string | null,
): NotificationPreferences {
  const resolved = resolveUserId(userId);
  if (!resolved) return { ...DEFAULT_PREFERENCES };
  const fallback = fallbackPreferencesByUser.get(resolved);
  if (fallback) return { ...fallback };
  const key = notificationStorageKey(resolved);
  if (!key) return { ...DEFAULT_PREFERENCES };
  try {
    const raw = window.localStorage?.getItem(key);
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
  if (!userId) {
    fallbackPreferencesByUser.clear();
  }
  if (notifiedUserId !== userId) {
    notifiedUserId = userId;
    notifiedMessageIds.clear();
    const next = getNotificationPreferences(userId);
    for (const listener of listeners) listener(next);
  }
}

export function setNotificationPreferences(
  patch: Partial<NotificationPreferences>,
  userId?: string | null,
): NotificationPreferences {
  const resolved = resolveUserId(userId);
  if (!resolved) {
    closeActiveNotifications();
    return { ...DEFAULT_PREFERENCES };
  }
  const current = getNotificationPreferences(resolved);
  const next: NotificationPreferences = {
    enabled: patch.enabled ?? current.enabled,
    showPreview: patch.showPreview ?? current.showPreview,
  };
  const key = notificationStorageKey(resolved)!;
  try {
    window.localStorage?.setItem(key, JSON.stringify(next));
    fallbackPreferencesByUser.delete(resolved);
  } catch {
    fallbackPreferencesByUser.set(resolved, next);
  }
  if (!next.enabled) {
    closeActiveNotifications();
  }
  for (const listener of listeners) listener(next);
  return next;
}

export async function enableBrowserNotifications(
  userId?: string | null,
): Promise<{
  granted: boolean;
  permission: BrowserNotificationPermission;
}> {
  const permission = getNotificationPermission();
  if (permission === "unsupported") {
    setNotificationPreferences({ enabled: false }, userId);
    return { granted: false, permission: "unsupported" };
  }
  if (permission === "denied") {
    setNotificationPreferences({ enabled: false }, userId);
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
      setNotificationPreferences({ enabled: true }, userId);
      return { granted: true, permission: "granted" };
    }
    setNotificationPreferences({ enabled: false }, userId);
    return { granted: false, permission: requested };
  }
  setNotificationPreferences({ enabled: true }, userId);
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
    closeActiveNotifications();
    notifiedUserId = currentUserId;
    notifiedMessageIds.clear();
  }
  const prefs = getNotificationPreferences(currentUserId);
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

export function useNotificationPreferences(userId?: string | null) {
  const [preferences, setPreferencesState] = useState<NotificationPreferences>(
    () => getNotificationPreferences(userId),
  );
  const [permission, setPermission] = useState<BrowserNotificationPermission>(
    () => getNotificationPermission(),
  );

  useEffect(() => {
    const sync = (
      next: NotificationPreferences = getNotificationPreferences(userId),
    ) => {
      setPreferencesState(next);
      setPermission(getNotificationPermission());
    };
    listeners.add(sync);

    const onStorage = (event: StorageEvent) => {
      const key = notificationStorageKey(userId);
      if (!event.key || event.key === key) {
        const resolved = resolveUserId(userId);
        if (resolved) {
          fallbackPreferencesByUser.delete(resolved);
        }
        sync(getNotificationPreferences(userId));
      }
    };
    const onVisibilityOrFocus = () => {
      setPermission(getNotificationPermission());
    };

    window.addEventListener("storage", onStorage);
    window.addEventListener("focus", onVisibilityOrFocus);
    document.addEventListener("visibilitychange", onVisibilityOrFocus);
    sync(getNotificationPreferences(userId));

    return () => {
      listeners.delete(sync);
      window.removeEventListener("storage", onStorage);
      window.removeEventListener("focus", onVisibilityOrFocus);
      document.removeEventListener("visibilitychange", onVisibilityOrFocus);
    };
  }, [userId]);

  return {
    preferences,
    permission,
    setPreferences: (patch: Partial<NotificationPreferences>) =>
      setNotificationPreferences(patch, userId),
    enableNotifications: async () => {
      const result = await enableBrowserNotifications(userId);
      setPermission(result.permission);
      return result;
    },
    disableNotifications: () =>
      setNotificationPreferences({ enabled: false }, userId),
  };
}

