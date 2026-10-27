import { useCallback, useEffect, useRef, useSyncExternalStore } from "react";
import { api, ApiError } from "@teamie/api-client";
import type {
  Conversation,
  Member,
  PresenceItem,
  PresenceState,
  ReadReceipts,
} from "@teamie/api-client";
import { chatStore } from "./store";

export const COMMON_REACTION_EMOJIS = [
  "👍",
  "🎉",
  "❤️",
  "🔥",
  "👀",
  "😂",
  "🚀",
  "👏",
] as const;

type PresenceMap = Record<string, PresenceState>;

let presenceState: PresenceMap = {};
const presenceListeners = new Set<() => void>();

function setPresence(next: PresenceMap) {
  if (next === presenceState) return;
  presenceState = next;
  presenceListeners.forEach((listener) => listener());
}

export const presenceStore = {
  getSnapshot: () => presenceState,
  subscribe(listener: () => void) {
    presenceListeners.add(listener);
    return () => {
      presenceListeners.delete(listener);
    };
  },
  hint(userId: string, state: PresenceState) {
    if (!userId || (state !== "online" && state !== "away" && state !== "offline"))
      return;
    if ((presenceState[userId] || "offline") === state) return;
    const next = { ...presenceState };
    if (state === "offline") {
      delete next[userId];
    } else {
      next[userId] = state;
    }
    setPresence(next);
  },
  applyWorkspaceSnapshot(memberUserIds: string[], items: PresenceItem[]) {
    const next = { ...presenceState };
    const activeByUser = new Map<string, "online" | "away">();
    for (const item of items) {
      if (item.state === "online" || item.state === "away") {
        activeByUser.set(item.userId, item.state);
      }
    }
    for (const uid of memberUserIds) {
      const st = activeByUser.get(uid);
      if (st) next[uid] = st;
      else delete next[uid];
    }
    for (const [uid, st] of activeByUser) {
      next[uid] = st;
    }
    setPresence(next);
  },
  reset() {
    if (Object.keys(presenceState).length > 0) setPresence({});
  },
};

export function formatPresenceLabel(state: PresenceState): string {
  return state === "online" ? "在线" : state === "away" ? "离开" : "离线";
}

export function presenceClassName(
  state: PresenceState,
): "presence-online" | "presence-away" | "presence-offline" {
  return state === "online"
    ? "presence-online"
    : state === "away"
      ? "presence-away"
      : "presence-offline";
}

export function useUserPresence(
  userId: string | undefined,
  enabled: boolean,
): PresenceState {
  return useSyncExternalStore(
    presenceStore.subscribe,
    useCallback(() => {
      if (!enabled || !userId) return "offline";
      return presenceStore.getSnapshot()[userId] || "offline";
    }, [enabled, userId]),
  );
}

const EMPTY_PRESENCE: PresenceMap = {};

export function usePresenceMap(enabled: boolean): PresenceMap {
  return useSyncExternalStore(
    presenceStore.subscribe,
    useCallback(
      () => (enabled ? presenceStore.getSnapshot() : EMPTY_PRESENCE),
      [enabled],
    ),
  );
}

type ReceiptStoreState = {
  byConversation: Record<string, ReadReceipts>;
  invalidations: Record<string, number>;
  globalInvalidation: number;
  sessionEpoch: number;
};

let receiptState: ReceiptStoreState = {
  byConversation: {},
  invalidations: {},
  globalInvalidation: 0,
  sessionEpoch: 0,
};
const receiptListeners = new Set<() => void>();

function setReceipts(next: ReceiptStoreState) {
  if (next === receiptState) return;
  receiptState = next;
  receiptListeners.forEach((listener) => listener());
}

export const receiptStore = {
  getSnapshot: () => receiptState,
  subscribe(listener: () => void) {
    receiptListeners.add(listener);
    return () => {
      receiptListeners.delete(listener);
    };
  },
  set(conversationId: string, receipts: ReadReceipts) {
    setReceipts({
      ...receiptState,
      byConversation: {
        ...receiptState.byConversation,
        [conversationId]: receipts,
      },
    });
  },
  invalidate(conversationId: string) {
    if (!conversationId) return;
    setReceipts({
      ...receiptState,
      invalidations: {
        ...receiptState.invalidations,
        [conversationId]: (receiptState.invalidations[conversationId] || 0) + 1,
      },
    });
  },
  invalidateAll(clear = false) {
    setReceipts({
      ...receiptState,
      ...(clear ? { byConversation: {}, sessionEpoch: receiptState.sessionEpoch + 1 } : {}),
      globalInvalidation: receiptState.globalInvalidation + 1,
    });
  },
  remove(conversationId: string) {
    const next = { ...receiptState.byConversation };
    delete next[conversationId];
    setReceipts({ ...receiptState, byConversation: next });
  },
  reset() {
    setReceipts({ byConversation: {}, invalidations: {}, globalInvalidation: receiptState.globalInvalidation + 1, sessionEpoch: receiptState.sessionEpoch + 1 });
  },
};

export function useConversationReceiptsSync(conversationId: string, enabled: boolean, hasOwnMessages: boolean) {
  const invalidation = useSyncExternalStore(receiptStore.subscribe, useCallback(() => {
    const snap = receiptStore.getSnapshot();
    return `${snap.sessionEpoch}:${snap.globalInvalidation}:${snap.invalidations[conversationId] || 0}`;
  }, [conversationId]));
  const sessionEpoch = receiptStore.getSnapshot().sessionEpoch;
  const identity = useSyncExternalStore(chatStore.subscribe, () => `${chatStore.getSnapshot().userId}:${chatStore.generation}`);
  const runner = useRef<{ refresh: () => void } | undefined>(undefined);

  useEffect(() => {
    if (!enabled || !conversationId || !hasOwnMessages) return;
    let stopped = false, inFlight = false, dirty = false, failures = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let controller: AbortController | undefined;
    const generation = chatStore.generation;
    const refresh = () => {
      dirty = true;
      if (stopped || inFlight) return;
      if (timer) clearTimeout(timer);
      if (!navigator.onLine || document.visibilityState === "hidden") return;
      dirty = false;
      inFlight = true;
      controller = new AbortController();
      void api.readReceipts(conversationId, controller.signal).then((result) => {
        if (!stopped && generation === chatStore.generation && sessionEpoch === receiptStore.getSnapshot().sessionEpoch) {
          failures = 0;
          receiptStore.set(conversationId, result);
        }
      }).catch((error: unknown) => {
        if (stopped || controller?.signal.aborted) return;
        if (error instanceof ApiError && error.status >= 400 && error.status < 500 && error.status !== 429) {
          receiptStore.remove(conversationId);
          dirty = false;
          return;
        }
        timer = setTimeout(refresh, Math.min(30_000, 1000 * 2 ** Math.min(failures++, 5)));
      }).finally(() => {
        inFlight = false;
        if (dirty && !stopped) refresh();
      });
    };
    runner.current = { refresh };
    return () => {
      stopped = true;
      controller?.abort();
      if (timer) clearTimeout(timer);
      runner.current = undefined;
    };
  }, [conversationId, enabled, hasOwnMessages, identity, sessionEpoch]);

  useEffect(() => {
    if (enabled && hasOwnMessages) runner.current?.refresh();
  }, [conversationId, enabled, hasOwnMessages, identity, invalidation]);

  useEffect(() => {
    if (!enabled || !conversationId || !hasOwnMessages) return;
    const refresh = () => { if (document.visibilityState !== "hidden") receiptStore.invalidate(conversationId); };
    window.addEventListener("focus", refresh);
    window.addEventListener("online", refresh);
    document.addEventListener("visibilitychange", refresh);
    return () => {
      window.removeEventListener("focus", refresh);
      window.removeEventListener("online", refresh);
      document.removeEventListener("visibilitychange", refresh);
    };
  }, [conversationId, enabled, hasOwnMessages]);
}

export function useConversationReceipts(
  conversationId: string,
  enabled: boolean,
): ReadReceipts | undefined {
  return useSyncExternalStore(
    receiptStore.subscribe,
    useCallback(
      () =>
        enabled
          ? receiptStore.getSnapshot().byConversation[conversationId]
          : undefined,
      [conversationId, enabled],
    ),
  );
}

export type MessageReadSummary = {
  isRead: boolean;
  readCount: number;
  unreadCount: number;
  label: string;
  readMembers: Member[];
  unreadMembers: Member[];
};

export function computeMessageReadSummary({
  receipts,
  seq,
  senderId,
  conversationType,
  members,
}: {
  receipts?: ReadReceipts;
  seq: number;
  senderId: string;
  senderReadSeq?: number;
  conversationType: Conversation["type"];
  members: Member[];
}): MessageReadSummary | null {
  if (!receipts || seq <= 0) return null;

  if (conversationType === "direct") {
    const otherReceipts = (receipts.members || []).filter(
      (m) => m.userId !== senderId,
    );
    const isRead = otherReceipts.some((m) => m.readSeq >= seq);
    return {
      isRead,
      readCount: isRead ? 1 : 0,
      unreadCount: isRead ? 0 : 1,
      label: isRead ? "已读" : "未读",
      readMembers: [],
      unreadMembers: [],
    };
  }

  if (conversationType === "group") {
    const memberById = new Map<string, Member>();
    for (const m of members) {
      if (!memberById.has(m.userId)) memberById.set(m.userId, m);
    }
    const readMembers: Member[] = [];
    const unreadMembers: Member[] = [];
    for (const entry of receipts.members || []) {
      if (entry.userId === senderId) continue;
      const member = memberById.get(entry.userId) || {
        userId: entry.userId,
        displayName: entry.userId.slice(0, 8),
        role: "member",
        kind: "human",
      };
      if (entry.readSeq >= seq) readMembers.push(member);
      else unreadMembers.push(member);
    }
    const readCount = readMembers.length;
    const unreadCount = unreadMembers.length;
    return {
      isRead: readCount > 0,
      readCount,
      unreadCount,
      label: readCount > 0 ? `${readCount} 人已读` : "未读",
      readMembers,
      unreadMembers,
    };
  }

  // channel: members array is omitted by contract and caller is already excluded by backend
  let totalAtOrAbove = 0;
  for (const bucket of receipts.readSeqCounts || []) {
    if (bucket.readSeq >= seq) {
      totalAtOrAbove += bucket.count;
    }
  }
  const readCount = Math.max(0, totalAtOrAbove);
  const eligibleOthers = Math.max(0, receipts.eligibleCount ?? readCount);
  return {
    isRead: readCount > 0,
    readCount,
    unreadCount: Math.max(0, eligibleOthers - readCount),
    label: readCount > 0 ? `${readCount} 人已读` : "未读",
    readMembers: [],
    unreadMembers: [],
  };
}
