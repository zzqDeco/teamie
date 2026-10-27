import { useCallback, useEffect, useSyncExternalStore } from "react";
import { api } from "@teamie/api-client";
import type {
  Conversation,
  Member,
  PresenceItem,
  PresenceState,
  ReadReceipts,
} from "@teamie/api-client";

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
};

let receiptState: ReceiptStoreState = {
  byConversation: {},
  invalidations: {},
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
  reset() {
    if (
      Object.keys(receiptState.byConversation).length > 0 ||
      Object.keys(receiptState.invalidations).length > 0
    ) {
      setReceipts({ byConversation: {}, invalidations: {} });
    }
  },
};

export function useConversationReceiptsSync(
  conversationId: string,
  enabled: boolean,
  hasOwnMessages: boolean,
) {
  const invalidation = useSyncExternalStore(
    receiptStore.subscribe,
    useCallback(
      () => receiptStore.getSnapshot().invalidations[conversationId] || 0,
      [conversationId],
    ),
  );

  useEffect(() => {
    if (!enabled || !conversationId || !hasOwnMessages) return;
    const controller = new AbortController();
    void api
      .readReceipts(conversationId, controller.signal)
      .then((res) => {
        if (!controller.signal.aborted) {
          receiptStore.set(conversationId, res);
        }
      })
      .catch(() => {
        /* Read receipts are supplementary; ignore transient errors. */
      });
    return () => controller.abort();
  }, [conversationId, enabled, hasOwnMessages, invalidation]);
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
  senderReadSeq,
  conversationType,
  members,
}: {
  receipts?: ReadReceipts;
  seq: number;
  senderId: string;
  senderReadSeq: number;
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

  // channel: members array is omitted by contract; compute from readSeqCounts
  let totalAtOrAbove = 0;
  for (const bucket of receipts.readSeqCounts || []) {
    if (bucket.readSeq >= seq) {
      totalAtOrAbove += bucket.count;
    }
  }
  const selfCount = senderReadSeq >= seq ? 1 : 0;
  const readCount = Math.max(0, totalAtOrAbove - selfCount);
  const eligibleOthers = Math.max(0, (receipts.eligibleCount || 1) - 1);
  return {
    isRead: readCount > 0,
    readCount,
    unreadCount: Math.max(0, eligibleOthers - readCount),
    label: readCount > 0 ? `${readCount} 人已读` : "未读",
    readMembers: [],
    unreadMembers: [],
  };
}
