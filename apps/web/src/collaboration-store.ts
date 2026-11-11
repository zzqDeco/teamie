import type { Conversation, Message, Pin, SavedItem } from "@teamie/api-client";
import type { NotificationProjection } from "./notification-model";
import type { ReactionStates } from "./reactions";

export type LocalMessage = Message & {
  status?: "sending" | "failed";
  error?: string;
  // A failure the same idempotent retry cannot fix (conflict, lost access,
  // expired file, disabled feature). Only transient failures auto-retry.
  permanent?: boolean;
};

export type PinnedMessage = Pin & { messageId?: string; conversationId?: string };
export type SavedMessage = SavedItem & { messageId?: string; conversationId?: string };

export type ChatSnapshot = {
  userId: string;
  cursor: string;
  messages: Record<string, LocalMessage[]>;
  conversations: Record<string, Conversation>;
  notificationProjection?: NotificationProjection;
  reactionStates?: ReactionStates;
  pinsByConversation: Record<string, PinnedMessage[]>;
  pinsVersionByConversation?: Record<string, string>;
  pinsInitializedByConversation?: Record<string, boolean>;
  pinsNeedsRefreshByConversation?: Record<string, boolean>;
  pinsPendingDeltaByConversation?: Record<string, PinUpdatedEvent[]>;
  savedByConversation: Record<string, string[]>;
  savedMessages: SavedMessage[];
  savedInitialized?: boolean;
  savedDeltaSeq?: number;
  savedDeltaLog?: Array<{ seq: number; event: SavedUpdatedEvent }>;
  readReceipts: Record<string, Record<string, number>>;
  manualUnreadByConversation: Record<string, number>;
  revision: number;
};

export type ChatState = ChatSnapshot;

export type PinUpdatedEvent = {
  conversationId: string; messageId: string; action?: "pinned" | "unpinned"; pinned?: boolean;
  pin?: PinnedMessage; message?: Message; pinnedBy?: string; pinnedByName?: string; pinnedAt?: string; pinsVersion?: string;
};
export type SavedUpdatedEvent = {
  conversationId: string; messageId: string; action?: "saved" | "unsaved"; saved?: boolean;
  savedAt?: string; item?: SavedMessage; message?: Message;
};
export type ReadUpdatedEvent = {
  conversationId: string; userId?: string; readSeq?: number; manualUnreadSeq?: number; unreadCount?: number;
};

const EMPTY_PINS: Record<string, PinnedMessage[]> = Object.freeze({});
const EMPTY_SAVED_BY_CONV: Record<string, string[]> = Object.freeze({});
const EMPTY_SAVED_LIST: SavedMessage[] = Object.freeze([]) as unknown as SavedMessage[];
const EMPTY_RECEIPTS: Record<string, Record<string, number>> = Object.freeze({});
const EMPTY_MANUAL_UNREAD: Record<string, number> = Object.freeze({});
const snapshotProto = {
  pinsByConversation: EMPTY_PINS, savedByConversation: EMPTY_SAVED_BY_CONV,
  savedMessages: EMPTY_SAVED_LIST, readReceipts: EMPTY_RECEIPTS, manualUnreadByConversation: EMPTY_MANUAL_UNREAD,
};

export function withSnapshotDefaults(snapshot: ChatSnapshot): ChatSnapshot {
  return Object.assign(Object.create(snapshotProto), snapshot);
}

export function pinMessageId(pin: PinnedMessage): string {
  return pin.message?.id || pin.messageId || "";
}

function reconcileSnapshotMessage(primary?: Message, cached?: Message): Message | undefined {
  if (!primary) return cached;
  if (!cached) return primary;
  if (cached.retractedAt) return cached;
  if (primary.retractedAt) return primary;
  if ((cached.editedAt || "") > (primary.editedAt || "")) return { ...primary, ...cached };
  if ((primary.editedAt || "") > (cached.editedAt || "")) return primary;
  if (!primary.body && cached.body) return { ...primary, ...cached };
  return primary;
}

export function normalizePin(pin: PinnedMessage, conversationId?: string, fallbackMessage?: Message): PinnedMessage {
  const msgId = pin.message?.id || pin.messageId || fallbackMessage?.id || "";
  const convId = pin.message?.conversationId || pin.conversationId || conversationId || fallbackMessage?.conversationId || "";
  const message: Message = reconcileSnapshotMessage(pin.message, fallbackMessage) || {
    id: msgId,
    clientMessageId: msgId,
    conversationId: convId,
    senderId: pin.pinnedBy || "",
    senderName: pin.pinnedByName || "",
    senderKind: "human",
    seq: 0,
    body: "",
    createdAt: pin.pinnedAt || new Date(0).toISOString(),
  };
  return { ...pin, message, messageId: msgId, conversationId: convId };
}

export function savedItemMessageId(item: SavedMessage): string {
  return item.message?.id || item.messageId || "";
}

export function savedItemConversationId(item: SavedMessage): string {
  return item.conversation?.id || item.message?.conversationId || item.conversationId || "";
}

export function normalizeSavedMessage(
  item: SavedMessage,
  fallbackConversation?: Conversation,
  fallbackMessage?: Message,
): SavedMessage {
  const msgId = item.message?.id || item.messageId || fallbackMessage?.id || "";
  const convId =
    item.conversation?.id || item.message?.conversationId || item.conversationId || fallbackConversation?.id || fallbackMessage?.conversationId || "";
  const message: Message = reconcileSnapshotMessage(item.message, fallbackMessage) || {
    id: msgId,
    clientMessageId: msgId,
    conversationId: convId,
    senderId: "",
    senderName: "",
    senderKind: "human",
    seq: 0,
    body: "",
    createdAt: item.savedAt || new Date(0).toISOString(),
  };
  const conversation = item.conversation || {
    id: convId,
    workspaceId: fallbackConversation?.workspaceId || "",
    name: fallbackConversation?.name || "",
    type: fallbackConversation?.type || "channel",
  };
  return { ...item, message, conversation, workspaceName: item.workspaceName || "", messageId: msgId, conversationId: convId };
}

function validVersion(version?: string): version is string {
  return Boolean(version && /^\d+$/.test(version));
}

function applyPinDeltaToList(
  state: ChatSnapshot,
  conversationId: string,
  current: PinnedMessage[],
  event: PinUpdatedEvent,
): PinnedMessage[] {
  const { messageId } = event;
  const isPinned = event.action ? event.action === "pinned" : Boolean(event.pinned ?? event.pin);
  if (!isPinned) return current.filter((p) => pinMessageId(p) !== messageId);
  const cachedMsg =
    event.message || event.pin?.message || state.messages[conversationId]?.find((m) => m.id === messageId);
  const entry = normalizePin(
    event.pin || {
      messageId,
      conversationId,
      message: cachedMsg!,
      pinnedBy: event.pinnedBy || "",
      pinnedByName: event.pinnedByName || "",
      pinnedAt: event.pinnedAt || new Date().toISOString(),
    },
    conversationId,
    cachedMsg,
  );
  const filtered = current.filter((p) => pinMessageId(p) !== messageId);
  return [entry, ...filtered]
    .sort((a, b) => (b.pinnedAt || "").localeCompare(a.pinnedAt || ""))
    .slice(0, 50);
}

export function setConversationPinsState(
  state: ChatSnapshot,
  conversationId: string,
  pins: PinnedMessage[],
  pinsVersion?: string,
): ChatSnapshot {
  const existingVersion = state.pinsVersionByConversation?.[conversationId];
  const initialized = Boolean(state.pinsInitializedByConversation?.[conversationId]);
  const isOlderVersion =
    validVersion(existingVersion) && validVersion(pinsVersion) && BigInt(pinsVersion) < BigInt(existingVersion);
  if (isOlderVersion && initialized) return state;
  let normalized = pins
    .map((p) =>
      normalizePin(
        p,
        conversationId,
        state.messages[conversationId]?.find((m) => m.id === pinMessageId(p)),
      ),
    )
    .filter((p) => !p.message?.retractedAt)
    .slice(0, 50);
  let resolvedVersion = pinsVersion || existingVersion;
  let needsRefresh = false;
  if (isOlderVersion && !initialized) {
    const deltas = (state.pinsPendingDeltaByConversation?.[conversationId] || []).filter(
      (d) => !validVersion(d.pinsVersion) || !validVersion(pinsVersion) || BigInt(d.pinsVersion) > BigInt(pinsVersion),
    );
    let cursorVer = validVersion(pinsVersion) ? BigInt(pinsVersion) : null;
    for (const delta of deltas) {
      if (cursorVer !== null && validVersion(delta.pinsVersion)) {
        if (BigInt(delta.pinsVersion) > cursorVer + 1n) needsRefresh = true;
        cursorVer = BigInt(delta.pinsVersion);
      }
      const isDeltaPinned = delta.action ? delta.action === "pinned" : Boolean(delta.pinned ?? delta.pin);
      const hasDeltaMsg = Boolean(
        delta.message ||
          delta.pin?.message ||
          normalized.some((p) => pinMessageId(p) === delta.messageId) ||
          state.messages[conversationId]?.some((m) => m.id === delta.messageId),
      );
      if (isDeltaPinned && !hasDeltaMsg) needsRefresh = true;
      normalized = applyPinDeltaToList(state, conversationId, normalized, delta);
    }
    resolvedVersion = existingVersion;
  }
  const nextPending = { ...state.pinsPendingDeltaByConversation };
  delete nextPending[conversationId];
  return {
    ...state,
    pinsByConversation: { ...state.pinsByConversation, [conversationId]: normalized },
    ...(resolvedVersion
      ? { pinsVersionByConversation: { ...state.pinsVersionByConversation, [conversationId]: resolvedVersion } }
      : {}),
    pinsInitializedByConversation: { ...state.pinsInitializedByConversation, [conversationId]: !needsRefresh },
    pinsNeedsRefreshByConversation: { ...state.pinsNeedsRefreshByConversation, [conversationId]: needsRefresh },
    pinsPendingDeltaByConversation: nextPending,
  };
}

export function applyPinUpdated(state: ChatSnapshot, event: PinUpdatedEvent): ChatSnapshot {
  const { conversationId, messageId } = event;
  if (!conversationId || !messageId) return state;
  const existingVersion = state.pinsVersionByConversation?.[conversationId];
  if (
    validVersion(existingVersion) &&
    validVersion(event.pinsVersion) &&
    BigInt(event.pinsVersion) <= BigInt(existingVersion)
  ) {
    return state;
  }
  const initialized = Boolean(state.pinsInitializedByConversation?.[conversationId]);
  const hasVersionGap =
    validVersion(existingVersion) &&
    validVersion(event.pinsVersion) &&
    BigInt(event.pinsVersion) > BigInt(existingVersion) + 1n;
  const isPinned = event.action ? event.action === "pinned" : Boolean(event.pinned ?? event.pin);
  const current = state.pinsByConversation?.[conversationId] || [];
  const hasCachedMessage = Boolean(
    event.message ||
      event.pin?.message ||
      current.some((p) => pinMessageId(p) === messageId && Boolean(p.message?.body || p.message?.attachments?.length || p.message?.forwarded)) ||
      state.messages[conversationId]?.some((m) => m.id === messageId),
  );
  const needsRefresh =
    Boolean(state.pinsNeedsRefreshByConversation?.[conversationId]) ||
    (!initialized && Boolean(event.pinsVersion)) ||
    hasVersionGap ||
    (isPinned && !hasCachedMessage);
  const nextPins = applyPinDeltaToList(state, conversationId, current, event);
  const prevDeltas = state.pinsPendingDeltaByConversation?.[conversationId] || [];
  return {
    ...state,
    pinsByConversation: { ...state.pinsByConversation, [conversationId]: nextPins },
    ...(event.pinsVersion
      ? { pinsVersionByConversation: { ...state.pinsVersionByConversation, [conversationId]: event.pinsVersion } }
      : {}),
    ...(needsRefresh
      ? { pinsNeedsRefreshByConversation: { ...state.pinsNeedsRefreshByConversation, [conversationId]: true } }
      : {}),
    ...(!initialized
      ? { pinsPendingDeltaByConversation: { ...state.pinsPendingDeltaByConversation, [conversationId]: [...prevDeltas, event] } }
      : {}),
  };
}

export function setConversationSavedState(
  state: ChatSnapshot,
  conversationId: string,
  messageIds: string[],
  sinceDeltaSeq = 0,
): ChatSnapshot {
  const retracted = new Set(
    (state.messages[conversationId] || []).filter((m) => Boolean(m.retractedAt)).map((m) => m.id),
  );
  let ids = Array.from(new Set(messageIds)).filter((id) => !retracted.has(id));
  if (state.savedDeltaLog?.length) {
    for (const { seq, event } of state.savedDeltaLog) {
      if (seq <= sinceDeltaSeq) continue;
      const cid = event.conversationId || event.item?.conversation?.id || event.message?.conversationId || "";
      const isSaved = event.action ? event.action === "saved" : Boolean(event.saved ?? event.item);
      if (isSaved && cid === conversationId && !ids.includes(event.messageId)) ids = [...ids, event.messageId];
      else if (!isSaved && (!cid || cid === conversationId)) ids = ids.filter((id) => id !== event.messageId);
    }
  }
  return { ...state, savedByConversation: { ...state.savedByConversation, [conversationId]: ids } };
}

export function setSavedMessagesState(
  state: ChatSnapshot,
  items: SavedMessage[],
  sinceDeltaSeq?: number,
): ChatSnapshot {
  const hasConvs = Object.keys(state.conversations).length > 0;
  const normalized = items
    .map((item) => {
      const convId = savedItemConversationId(item);
      const msgId = savedItemMessageId(item);
      return normalizeSavedMessage(
        item,
        state.conversations[convId],
        state.messages[convId]?.find((m) => m.id === msgId),
      );
    })
    .filter(
      (item) =>
        !item.message?.retractedAt &&
        (!hasConvs || Boolean(state.conversations[savedItemConversationId(item)])),
    );
  const byConv: Record<string, string[]> = { ...state.savedByConversation };
  for (const item of normalized) {
    const convId = savedItemConversationId(item);
    const msgId = savedItemMessageId(item);
    if (!convId || !msgId) continue;
    const list = byConv[convId] || [];
    if (!list.includes(msgId)) byConv[convId] = [...list, msgId];
  }
  let next: ChatSnapshot = {
    ...state,
    savedMessages: normalized,
    savedByConversation: byConv,
    savedInitialized: true,
  };
  const cutoff = sinceDeltaSeq ?? (state.savedInitialized ? state.savedDeltaSeq ?? 0 : 0);
  if (state.savedDeltaLog?.length) {
    for (const entry of state.savedDeltaLog) {
      if (entry.seq > cutoff) next = applySavedDeltaInternal(next, entry.event);
    }
  }
  return next;
}

function applySavedDeltaInternal(state: ChatSnapshot, event: SavedUpdatedEvent): ChatSnapshot {
  const { conversationId, messageId } = event;
  if (!messageId) return state;
  const isSaved = event.action ? event.action === "saved" : Boolean(event.saved ?? event.item);
  const convId =
    conversationId || event.item?.conversation?.id || event.message?.conversationId || "";
  const currentIds = convId ? state.savedByConversation?.[convId] || [] : [];
  const currentList = state.savedMessages || [];

  if (isSaved) {
    const hasConvs = Object.keys(state.conversations).length > 0;
    if (hasConvs && convId && !state.conversations[convId]) return state;
    const nextIds = convId && !currentIds.includes(messageId) ? [...currentIds, messageId] : currentIds;
    const cachedMsg =
      event.message ||
      event.item?.message ||
      (convId ? state.messages[convId]?.find((m) => m.id === messageId) : undefined);
    let nextList = currentList;
    if (event.item || cachedMsg) {
      const entry = normalizeSavedMessage(
        event.item || {
          messageId,
          conversationId: convId,
          message: cachedMsg!,
          conversation: {
            id: convId,
            workspaceId: state.conversations[convId]?.workspaceId || "",
            name: state.conversations[convId]?.name || "",
            type: state.conversations[convId]?.type || "channel",
          },
          workspaceName: "",
          savedAt: event.savedAt || new Date().toISOString(),
        },
        state.conversations[convId],
        cachedMsg,
      );
      nextList = [entry, ...currentList.filter((i) => savedItemMessageId(i) !== messageId)];
    }
    return {
      ...state,
      ...(convId ? { savedByConversation: { ...state.savedByConversation, [convId]: nextIds } } : {}),
      savedMessages: nextList,
    };
  }

  const nextByConv = { ...state.savedByConversation };
  if (convId && nextByConv[convId]) {
    nextByConv[convId] = nextByConv[convId].filter((id) => id !== messageId);
  } else {
    for (const [cid, ids] of Object.entries(nextByConv)) {
      if (ids.includes(messageId)) nextByConv[cid] = ids.filter((id) => id !== messageId);
    }
  }
  return {
    ...state,
    savedByConversation: nextByConv,
    savedMessages: currentList.filter((i) => savedItemMessageId(i) !== messageId),
  };
}

export function applySavedUpdated(state: ChatSnapshot, event: SavedUpdatedEvent): ChatSnapshot {
  if (!event.messageId) return state;
  const nextSeq = (state.savedDeltaSeq ?? 0) + 1;
  const nextLog = [...(state.savedDeltaLog || []).slice(-199), { seq: nextSeq, event }];
  return applySavedDeltaInternal(
    { ...state, savedDeltaSeq: nextSeq, savedDeltaLog: nextLog },
    event,
  );
}

export function applyReadUpdated(
  state: ChatSnapshot,
  event: ReadUpdatedEvent,
  currentUserId = state.userId,
): ChatSnapshot {
  const { conversationId } = event;
  if (!conversationId) return state;
  const isSelf = !event.userId || event.userId === currentUserId;
  const userKey = event.userId || currentUserId;
  let nextReceipts = state.readReceipts;
  if (userKey && typeof event.readSeq === "number") {
    const convReceipts = state.readReceipts?.[conversationId] || {};
    const nextSeq = Math.max(convReceipts[userKey] ?? 0, event.readSeq);
    nextReceipts = { ...state.readReceipts, [conversationId]: { ...convReceipts, [userKey]: nextSeq } };
  }
  const conv = state.conversations[conversationId];
  if (!conv || !isSelf) {
    return nextReceipts !== state.readReceipts ? { ...state, readReceipts: nextReceipts } : state;
  }

  const prevReadSeq = conv.readSeq;
  const nextReadSeq = typeof event.readSeq === "number" ? Math.max(prevReadSeq, event.readSeq) : prevReadSeq;

  let nextManualSeq: number | undefined = state.manualUnreadByConversation?.[conversationId] ?? conv.manualUnreadSeq;
  const hadManualMap = Object.hasOwn(state, "manualUnreadByConversation");
  const nextManualMap = { ...state.manualUnreadByConversation };
  let manualChanged = false;

  if (typeof event.manualUnreadSeq === "number") {
    manualChanged = true;
    if (event.manualUnreadSeq > 0) {
      nextManualSeq = event.manualUnreadSeq;
      nextManualMap[conversationId] = event.manualUnreadSeq;
    } else {
      nextManualSeq = undefined;
      delete nextManualMap[conversationId];
    }
  } else if (typeof event.readSeq === "number" && nextManualSeq && event.readSeq >= nextManualSeq) {
    manualChanged = true;
    nextManualSeq = undefined;
    delete nextManualMap[conversationId];
  }

  const computedUnreadCount =
    typeof event.unreadCount === "number"
      ? event.unreadCount
      : Math.max(
          0,
          conv.lastSeq - nextReadSeq,
          nextManualSeq && nextManualSeq > 0 ? conv.lastSeq - nextManualSeq + 1 : 0,
        );
  const nextConv: Conversation & { unreadCount?: number } = {
    ...conv,
    readSeq: nextReadSeq,
    ...(nextManualSeq !== undefined ? { manualUnreadSeq: nextManualSeq } : {}),
    unreadCount: computedUnreadCount,
  };
  if (nextManualSeq === undefined) delete nextConv.manualUnreadSeq;

  return {
    ...state,
    conversations: { ...state.conversations, [conversationId]: nextConv },
    ...(nextReceipts !== state.readReceipts && Boolean(event.userId)
      ? { readReceipts: nextReceipts }
      : {}),
    ...(manualChanged || hadManualMap ? { manualUnreadByConversation: nextManualMap } : {}),
  };
}

export let manualUnreadHoldEpoch = 0;

export function setManualUnreadState(state: ChatSnapshot, conversationId: string, seq: number | undefined): ChatSnapshot {
  const nextManualMap = { ...state.manualUnreadByConversation };
  const conv = state.conversations[conversationId];
  if (seq && seq > 0) {
    manualUnreadHoldEpoch += 1;
    nextManualMap[conversationId] = seq;
    const nextUnreadCount = conv ? Math.max(1, conv.lastSeq - conv.readSeq, conv.lastSeq - seq + 1) : undefined;
    return {
      ...state,
      ...(conv
        ? {
            conversations: {
              ...state.conversations,
              [conversationId]: {
                ...conv,
                manualUnreadSeq: seq,
                ...(nextUnreadCount !== undefined ? { unreadCount: nextUnreadCount } : {}),
              } as Conversation,
            },
          }
        : {}),
      manualUnreadByConversation: nextManualMap,
    };
  }
  delete nextManualMap[conversationId];
  const clearedUnreadCount = conv ? Math.max(0, conv.lastSeq - conv.readSeq) : undefined;
  return {
    ...state,
    ...(conv
      ? {
          conversations: {
            ...state.conversations,
            [conversationId]: { ...conv, manualUnreadSeq: undefined, unreadCount: clearedUnreadCount } as Conversation,
          },
        }
      : {}),
    manualUnreadByConversation: nextManualMap,
  };
}

export function pruneCollaborationForConversations(state: ChatSnapshot, allowed: ReadonlySet<string>): Partial<ChatSnapshot> {
  const filterRec = <T>(rec: Record<string, T> | undefined) =>
    rec ? Object.fromEntries(Object.entries(rec).filter(([id]) => allowed.has(id))) : undefined;
  return {
    ...(Object.hasOwn(state, "pinsByConversation") ? { pinsByConversation: filterRec(state.pinsByConversation) || EMPTY_PINS } : {}),
    ...(Object.hasOwn(state, "pinsVersionByConversation") && state.pinsVersionByConversation ? { pinsVersionByConversation: filterRec(state.pinsVersionByConversation) } : {}),
    ...(Object.hasOwn(state, "pinsInitializedByConversation") && state.pinsInitializedByConversation ? { pinsInitializedByConversation: filterRec(state.pinsInitializedByConversation) } : {}),
    ...(Object.hasOwn(state, "pinsNeedsRefreshByConversation") && state.pinsNeedsRefreshByConversation ? { pinsNeedsRefreshByConversation: filterRec(state.pinsNeedsRefreshByConversation) } : {}),
    ...(Object.hasOwn(state, "pinsPendingDeltaByConversation") && state.pinsPendingDeltaByConversation ? { pinsPendingDeltaByConversation: filterRec(state.pinsPendingDeltaByConversation) } : {}),
    ...(Object.hasOwn(state, "savedByConversation") ? { savedByConversation: filterRec(state.savedByConversation) || EMPTY_SAVED_BY_CONV } : {}),
    ...(Object.hasOwn(state, "savedMessages") ? { savedMessages: (state.savedMessages || []).filter((item) => allowed.has(savedItemConversationId(item))) } : {}),
    ...(Object.hasOwn(state, "readReceipts") ? { readReceipts: filterRec(state.readReceipts) || EMPTY_RECEIPTS } : {}),
    ...(Object.hasOwn(state, "manualUnreadByConversation") ? { manualUnreadByConversation: filterRec(state.manualUnreadByConversation) || EMPTY_MANUAL_UNREAD } : {}),
  };
}

export type PendingManualUnreadWrite = { token: number; active: number; serverSeq?: number; hasServerSeq?: boolean };
export function reconcileConversationsSnapshot(
  state: ChatSnapshot, conversations: ReadonlyArray<Conversation>, allowed: ReadonlySet<string>,
  pendingWrites: Map<string, PendingManualUnreadWrite>, mergeUpdatedAt: (a?: string, b?: string) => string,
): ChatSnapshot {
  for (const id of pendingWrites.keys()) if (!allowed.has(id)) pendingWrites.delete(id);
  const nextManual: Record<string, number> = {};
  const nextConvs = Object.fromEntries(conversations.map((c) => {
    const old = state.conversations[c.id], pending = pendingWrites.get(c.id);
    const serverSeq = c.manualUnreadSeq && c.manualUnreadSeq > 0 ? c.manualUnreadSeq : undefined;
    if (pending) { pending.hasServerSeq = true; pending.serverSeq = serverSeq; }
    const manualSeq = pending ? (state.manualUnreadByConversation?.[c.id] ?? old?.manualUnreadSeq) : serverSeq;
    if (manualSeq && manualSeq > 0) nextManual[c.id] = manualSeq;
    const merged: Conversation & { unreadCount?: number } = {
      ...c, lastSeq: Math.max(c.lastSeq, old?.lastSeq ?? 0), readSeq: Math.max(c.readSeq, old?.readSeq ?? 0),
      updatedAt: mergeUpdatedAt(old?.updatedAt, c.updatedAt), notificationLevel: c.notificationLevel ?? old?.notificationLevel ?? "all",
    };
    if (manualSeq && manualSeq > 0) merged.manualUnreadSeq = manualSeq; else delete merged.manualUnreadSeq;
    if ("unreadCount" in merged) merged.unreadCount = Math.max(0, merged.lastSeq - merged.readSeq, manualSeq && manualSeq > 0 ? Math.max(1, merged.lastSeq - manualSeq + 1) : 0);
    return [c.id, merged as Conversation];
  }));
  const next: ChatSnapshot = { ...state, ...pruneCollaborationForConversations(state, allowed), conversations: nextConvs };
  if (Object.keys(nextManual).length > 0) next.manualUnreadByConversation = nextManual;
  else delete (next as Partial<ChatSnapshot>).manualUnreadByConversation;
  return next;
}

export function pruneRetractedCollaborationState(state: ChatSnapshot, messages: LocalMessage[]): ChatSnapshot {
  const retracted: LocalMessage[] = [];
  const updatedById = new Map<string, LocalMessage>();
  for (const raw of messages) {
    const m = state.messages[raw.conversationId]?.find((c) => c.id === raw.id) || raw;
    if (m.retractedAt) retracted.push(m);
    else if (!m.status) updatedById.set(m.id, m);
  }
  const retractedIds = new Set(retracted.map((m) => m.id));
  if (retractedIds.size === 0 && updatedById.size === 0) return state;

  let next = state;
  if (retracted.length > 0) {
    let seq = next.savedDeltaSeq ?? 0;
    const extra = retracted.map((m) => ({
      seq: ++seq,
      event: { conversationId: m.conversationId, messageId: m.id, action: "unsaved" as const },
    }));
    next = { ...next, savedDeltaSeq: seq, savedDeltaLog: [...(next.savedDeltaLog || []).slice(-199), ...extra] };
  }
  if (Object.hasOwn(state, "pinsByConversation") && state.pinsByConversation) {
    const nextPins: Record<string, PinnedMessage[]> = {};
    let changed = false;
    for (const [cid, list] of Object.entries(state.pinsByConversation)) {
      const reconciled: PinnedMessage[] = [];
      for (const p of list) {
        const mid = pinMessageId(p);
        if (retractedIds.has(mid)) { changed = true; continue; }
        const fresh = updatedById.get(mid);
        const nextMsg = fresh ? reconcileSnapshotMessage(p.message, fresh) : p.message;
        if (nextMsg && nextMsg !== p.message) { changed = true; reconciled.push({ ...p, message: nextMsg }); }
        else reconciled.push(p);
      }
      nextPins[cid] = reconciled;
    }
    if (changed) next = { ...next, pinsByConversation: nextPins };
  }
  if (retractedIds.size > 0 && Object.hasOwn(state, "savedByConversation") && state.savedByConversation) {
    const nextSaved: Record<string, string[]> = {};
    let changed = false;
    for (const [cid, ids] of Object.entries(state.savedByConversation)) {
      const filtered = ids.filter((id) => !retractedIds.has(id));
      if (filtered.length !== ids.length) changed = true;
      nextSaved[cid] = filtered;
    }
    if (changed) next = { ...next, savedByConversation: nextSaved };
  }
  if (Object.hasOwn(state, "savedMessages") && state.savedMessages?.length) {
    const reconciledSaved: SavedMessage[] = [];
    let changed = false;
    for (const item of state.savedMessages) {
      const mid = savedItemMessageId(item);
      if (retractedIds.has(mid)) { changed = true; continue; }
      const fresh = updatedById.get(mid);
      const nextMsg = fresh ? reconcileSnapshotMessage(item.message, fresh) : item.message;
      if (nextMsg && nextMsg !== item.message) { changed = true; reconciledSaved.push({ ...item, message: nextMsg }); }
      else reconciledSaved.push(item);
    }
    if (changed) next = { ...next, savedMessages: reconciledSaved };
  }
  return next;
}

const MAX_PERSISTED_MESSAGES = 200;
const persistedMessagesJson = new WeakMap<LocalMessage[], string>();
const persistedConversationJson = new WeakMap<Conversation, string>();

export function trimPersistedMessages(messages: LocalMessage[]): LocalMessage[] {
  const confirmed = messages.filter((m) => !m.status);
  if (confirmed.length <= MAX_PERSISTED_MESSAGES) return messages;
  const local = messages.filter((m) => !!m.status);
  return [...confirmed.slice(-MAX_PERSISTED_MESSAGES), ...local];
}

function messagesJson(messages: LocalMessage[]) {
  let json = persistedMessagesJson.get(messages);
  if (json === undefined) {
    json = JSON.stringify(trimPersistedMessages(messages));
    persistedMessagesJson.set(messages, json);
  }
  return json;
}

function conversationJson(conversation: Conversation) {
  let json = persistedConversationJson.get(conversation);
  if (json === undefined) {
    json = JSON.stringify(conversation);
    persistedConversationJson.set(conversation, json);
  }
  return json;
}

function recordJson<T>(record: Record<string, T>, value: (item: T) => string) {
  const parts: string[] = [];
  for (const [id, item] of Object.entries(record))
    if (item !== undefined) parts.push(`${JSON.stringify(id)}:${value(item)}`);
  return `{${parts.join(",")}}`;
}

export function serializeSnapshot(snapshot: ChatSnapshot): string {
  const states: ReactionStates = {};
  for (const [conversationId, messages] of Object.entries(snapshot.messages)) {
    const kept = new Set(
      trimPersistedMessages(messages).map((message) => message.id),
    );
    const reactions = Object.fromEntries(
      Object.entries(snapshot.reactionStates?.[conversationId] || {}).filter(
        ([id]) => kept.has(id),
      ),
    );
    if (Object.keys(reactions).length) states[conversationId] = reactions;
  }
  const reactionsJson = Object.keys(states).length
    ? `,"reactionStates":${JSON.stringify(states)}`
    : "";
  return (
    `{"userId":${JSON.stringify(snapshot.userId)}` +
    `,"cursor":${JSON.stringify(snapshot.cursor)}` +
    `,"messages":${recordJson(snapshot.messages, messagesJson)}` +
    `,"conversations":${recordJson(snapshot.conversations, conversationJson)}` +
    `,"revision":${JSON.stringify(snapshot.revision)}` +
    (snapshot.notificationProjection
      ? `,"notificationProjection":${JSON.stringify(snapshot.notificationProjection)}`
      : "") +
    reactionsJson +
    "}"
  );
}
