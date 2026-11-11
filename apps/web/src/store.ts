import type {
  AttentionUnreadDTO, Conversation, Message, NotificationLevel, Reaction, SyncEvent, SyncPage,
} from "@teamie/api-client";
import {
  applyPinUpdated, applyReadUpdated, applySavedUpdated, pinMessageId,
  pruneCollaborationForConversations, pruneRetractedCollaborationState,
  reconcileConversationsSnapshot, savedItemConversationId, savedItemMessageId, serializeSnapshot,
  setConversationPinsState, setConversationSavedState,
  setManualUnreadState, setSavedMessagesState, withSnapshotDefaults,
} from "./collaboration-store";
import type {
  ChatSnapshot, ChatState, LocalMessage, PendingManualUnreadWrite, PinnedMessage,
  PinUpdatedEvent, ReadUpdatedEvent, SavedMessage, SavedUpdatedEvent,
} from "./collaboration-store";
import {
  materializeReactionState, messageReactionState, projectReactionList,
  reconcileReactionState, updateReactionStates, validReactionVersion,
} from "./reactions";

export {
  applyPinUpdated, applyReadUpdated, applySavedUpdated, pinMessageId,
  pruneCollaborationForConversations, savedItemConversationId, savedItemMessageId,
  serializeSnapshot, setConversationPinsState, setConversationSavedState,
  setManualUnreadState, setSavedMessagesState,
};
export type {
  ChatSnapshot, ChatState, LocalMessage, PinnedMessage,
  PinUpdatedEvent, ReadUpdatedEvent, SavedMessage, SavedUpdatedEvent,
};
export { effectiveNotificationLevel, isUserMentioned, ordinaryUnreadCount, summarizeConversationUnread } from "./notification-model";
import { notificationInputsChanged } from "./notification-model";

export type StorageLike = Pick<Storage, "getItem" | "setItem" | "removeItem">;
const empty = (userId = ""): ChatSnapshot =>
  withSnapshotDefaults({ userId, cursor: "0", messages: {}, conversations: {}, revision: 0 } as ChatSnapshot);
const storageKey = (userId: string) => `teamie.sync.v1.${userId}`;
function timestampNanoseconds(value: string): bigint {
  const match =
    /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/.exec(
      value,
    );
  if (!match) throw new Error("Invalid RFC3339 message timestamp");
  // Parse only the whole second: Date.parse alone truncates sub-millisecond edits.
  const wholeMilliseconds = Date.parse(`${match[1]}${match[3]}`);
  if (!Number.isFinite(wholeMilliseconds))
    throw new Error("Invalid RFC3339 message timestamp");
  return (
    BigInt(wholeMilliseconds) * 1_000_000n +
    BigInt((match[2] || "").padEnd(9, "0"))
  );
}
export function compareTimestampsDesc(a: string, b: string): number {
  try {
    const diff = timestampNanoseconds(b) - timestampNanoseconds(a);
    return diff > 0n ? 1 : diff < 0n ? -1 : 0;
  } catch {
    return b.localeCompare(a);
  }
}
const version = (m: Message) =>
  timestampNanoseconds(m.retractedAt || m.editedAt || m.createdAt);
function newerTimestamp(a?: string, b?: string): string {
  if (!a) return b || "";
  if (!b) return a;
  try {
    return timestampNanoseconds(b) > timestampNanoseconds(a) ? b : a;
  } catch {
    return b > a ? b : a;
  }
}
const clientKey = (m: Message) => `${m.senderId}\0${m.clientMessageId}`;
export function reconcileMessages(
  existing: LocalMessage[],
  incoming: LocalMessage[],
): LocalMessage[] {
  const output = [...existing];
  const byId = new Map<string, number>();
  const byClientKey = new Map<string, number>();
  for (let i = 0; i < output.length; i++) {
    const m = output[i];
    if (!byId.has(m.id)) byId.set(m.id, i);
    const key = clientKey(m);
    if (!byClientKey.has(key)) byClientKey.set(key, i);
  }
  for (const incomingMessage of incoming) {
    const message = incomingMessage.retractedAt
      ? {
          ...incomingMessage,
          body: "",
          attachments: undefined,
          mentions: undefined,
          reactions: undefined,
          forwarded: undefined,
        }
      : incomingMessage;
    const key = clientKey(message);
    const byIdIndex = byId.get(message.id);
    const byClientIndex = byClientKey.get(key);
    const index =
      byIdIndex !== undefined && byClientIndex !== undefined
        ? Math.min(byIdIndex, byClientIndex)
        : (byIdIndex ?? byClientIndex ?? -1);
    if (index === -1) {
      const nextIndex = output.length;
      output.push(message);
      byId.set(message.id, nextIndex);
      byClientKey.set(key, nextIndex);
    } else {
      const old = output[index];
      if (old.retractedAt && !message.retractedAt) continue;
      if (!old.status && message.status) continue;
      const reactions = reconcileReactionState(messageReactionState(old), messageReactionState(message));
      if (!old.status && !message.status && version(old) > version(message)) {
        output[index] = materializeReactionState(old, reactions);
        continue;
      }
      output[index] = materializeReactionState(message, reactions);
      if (old.id !== message.id) byId.delete(old.id);
      const oldKey = clientKey(old);
      if (oldKey !== key) byClientKey.delete(oldKey);
      byId.set(message.id, index);
      byClientKey.set(key, index);
    }
  }
  return output.sort((a, b) => {
    if (!!a.status !== !!b.status) return a.status ? 1 : -1;
    return (
      a.seq - b.seq ||
      a.createdAt.localeCompare(b.createdAt) ||
      a.id.localeCompare(b.id)
    );
  });
}
const olderOrEqual = (a: string, b: string) =>
  /^\d+$/.test(a) && /^\d+$/.test(b) ? BigInt(a) <= BigInt(b) : a === b;
/**
 * Schedules a deferred persistence callback and returns its canceller. Without
 * a scheduler the store persists synchronously on every commit.
 */
export type PersistScheduler = (write: () => void) => () => void;
const PERSIST_DELAY_MS = 1000;
type IdleWindow = {
  requestIdleCallback?: (cb: () => void, options?: { timeout: number }) => number;
  cancelIdleCallback?: (handle: number) => void;
};
/** Coalesces writes into an idle period (at most ~1s later). */
export const idlePersistScheduler: PersistScheduler = (write) => {
  const idle = globalThis as IdleWindow;
  if (idle.requestIdleCallback && idle.cancelIdleCallback) {
    const cancelIdle = idle.cancelIdleCallback;
    const handle = idle.requestIdleCallback(write, { timeout: PERSIST_DELAY_MS });
    return () => cancelIdle(handle);
  }
  const handle = setTimeout(write, PERSIST_DELAY_MS);
  return () => clearTimeout(handle);
};
// Pending/failed outbound messages per conversation array, cached by identity.
const outboundByList = new WeakMap<LocalMessage[], LocalMessage[]>();
function outbound(messages: LocalMessage[] | undefined): LocalMessage[] {
  if (!messages) return [];
  let local = outboundByList.get(messages);
  if (!local) {
    local = messages.filter((m) => !!m.status);
    outboundByList.set(messages, local);
  }
  return local;
}
/**
 * True when a commit adds, changes or removes a locally pending/failed
 * outbound message (optimistic send, retry state, discard, confirmation).
 * Only conversations whose message array was replaced are inspected.
 */
function outboundChanged(previous: ChatSnapshot, next: ChatSnapshot) {
  const ids = new Set([
    ...Object.keys(previous.messages),
    ...Object.keys(next.messages),
  ]);
  for (const id of ids) {
    const before = previous.messages[id];
    const after = next.messages[id];
    if (before === after) continue;
    const a = outbound(before);
    const b = outbound(after);
    if (a.length !== b.length || a.some((m, i) => m !== b[i])) return true;
  }
  return false;
}
export class ChatStore {
  private snapshot: ChatSnapshot = empty();
  private listeners = new Set<() => void>();
  private storage?: StorageLike;
  private schedule?: PersistScheduler;
  private cancelPersist?: () => void;
  private reactionAccessEpochs = new Map<string, number>();
  private manualUnreadWrites = new Map<string, PendingManualUnreadWrite>();
  private nextManualWriteToken = 0;
  generation = 0;
  notificationEpoch = 0;
  conversationVersion = 0;
  hasAuthoritativeConversations = false;
  constructor(storage?: StorageLike, schedule?: PersistScheduler) {
    this.storage = storage;
    this.schedule = schedule;
  }
  getSnapshot = () => this.snapshot;
  subscribe = (fn: () => void) => {
    this.listeners.add(fn);
    return () => {
      this.listeners.delete(fn);
    };
  };
  private persist() {
    this.cancelPendingPersist();
    const snapshot = this.snapshot;
    if (!snapshot.userId) return;
    try {
      this.storage?.setItem(
        storageKey(snapshot.userId),
        serializeSnapshot(snapshot),
      );
    } catch {
      /* Sync remains correct in memory if storage is full or unavailable. */
    }
  }
  private cancelPendingPersist() {
    const cancel = this.cancelPersist;
    this.cancelPersist = undefined;
    cancel?.();
  }
  /** Writes a pending deferred snapshot now (e.g. on pagehide). */
  flush = () => {
    if (this.cancelPersist) this.persist();
  };
  private commit(next: ChatSnapshot) {
    const previous = this.snapshot;
    if (notificationInputsChanged(previous, next)) {
      this.notificationEpoch += 1;
      if (next.notificationProjection) {
        next = {
          ...next,
          notificationProjection: {
            stale: true,
            byConversation: Object.fromEntries(Object.entries(next.notificationProjection.byConversation).filter(([id]) => next.conversations[id])),
          },
        };
      }
    }
    this.snapshot = withSnapshotDefaults({ ...next, revision: this.snapshot.revision + 1 });
    if (next.userId && this.storage) {
      // The persisted snapshot always pairs entities with their cursor, so a
      // deferred write can lag but never tear; replay after a crash is
      // idempotent because events at or below the cursor are skipped.
      // Outbound sends bypass the deferral: a crash right after Enter must
      // not lose the queued message or its idempotent clientMessageId.
      if (
        !this.schedule ||
        (previous.userId === next.userId && outboundChanged(previous, next))
      )
        this.persist();
      else if (!this.cancelPersist)
        this.cancelPersist = this.schedule(() => {
          this.cancelPersist = undefined;
          this.persist();
        });
    }
    this.listeners.forEach((fn) => fn());
  }
  begin(userId: string) {
    if (this.snapshot.userId === userId) return;
    if (this.snapshot.userId) this.clear();
    let restored = empty(userId);
    try {
      const raw = this.storage?.getItem(storageKey(userId));
      const data = raw ? (JSON.parse(raw) as ChatSnapshot) : null;
      if (
        data?.userId === userId &&
        typeof data.cursor === "string" &&
        data.messages &&
        data.conversations
      ) {
        restored = {
          ...data,
          messages: Object.fromEntries(
            Object.entries(data.messages).map(([id, messages]) => [
              id,
              messages.map((m) =>
                m.status
                  ? {
                      ...m,
                      status: "failed" as const,
                      error:
                        m.status === "failed" &&
                        typeof m.permanent === "boolean" &&
                        m.error
                          ? m.error
                          : "上次发送未确认，请重试",
                    }
                  : m,
              ),
            ]),
          ),
        };
      }
    } catch {
      /* A corrupt snapshot restarts durable sync from zero. */
    }
    const cachedProjection = restored.notificationProjection;
    if (cachedProjection && cachedProjection.byConversation && typeof cachedProjection.byConversation === "object" &&
      Object.keys(cachedProjection.byConversation).length === Object.keys(restored.conversations).length &&
      Object.entries(cachedProjection.byConversation).every(([id, count]) => restored.conversations[id] && Number.isSafeInteger(count) && count >= 0)) {
      restored.notificationProjection = { ...cachedProjection, stale: true };
    } else {
      delete restored.notificationProjection;
    }
    for (const messages of Object.values(restored.messages)) {
      for (const message of messages)
        restored.reactionStates = updateReactionStates(restored.reactionStates, message.conversationId, message.id, messageReactionState(message));
    }
    restored.messages = Object.fromEntries(Object.entries(restored.messages).map(([id, messages]) =>
      [id, projectReactionList(messages, restored.reactionStates)],
    ));
    this.hasAuthoritativeConversations = false;
    this.conversationVersion =
      Object.keys(restored.conversations).length > 0 ? 1 : 0;
    this.commit(restored);
  }
  clear() {
    this.reactionAccessEpochs.clear();
    this.manualUnreadWrites.clear();
    this.generation += 1;
    this.notificationEpoch += 1;
    this.conversationVersion += 1;
    this.hasAuthoritativeConversations = false;
    // Account isolation: a deferred write of this account must never land
    // after logout or an account switch.
    this.cancelPendingPersist();
    if (this.snapshot.userId) {
      try {
        this.storage?.removeItem(storageKey(this.snapshot.userId));
      } catch {
        /* No storage access. */
      }
    }
    this.snapshot = empty();
    this.listeners.forEach((fn) => fn());
  }
  revoke() {
    this.reactionAccessEpochs.clear();
    this.manualUnreadWrites.clear();
    this.generation += 1;
    this.conversationVersion += 1;
    this.hasAuthoritativeConversations = false;
    this.commit(empty(this.snapshot.userId));
  }
  discardLocal(conversationId: string, clientMessageId: string) {
    const current = this.snapshot.messages[conversationId];
    if (!current) return;
    const kept = current.filter(
      (m) => !(m.status === "failed" && m.clientMessageId === clientMessageId),
    );
    if (kept.length === current.length) return;
    this.commit({
      ...this.snapshot,
      messages: { ...this.snapshot.messages, [conversationId]: kept },
    });
  }
  merge(messages: LocalMessage[]) {
    const next = {
      ...this.snapshot,
      messages: { ...this.snapshot.messages },
      conversations: { ...this.snapshot.conversations },
    };
    const grouped = new Map<string, LocalMessage[]>();
    for (const m of messages) {
      const list = grouped.get(m.conversationId);
      if (list) list.push(m);
      else grouped.set(m.conversationId, [m]);
      const conversation = next.conversations[m.conversationId];
      if (conversation && !m.status)
        next.conversations[m.conversationId] = {
          ...conversation,
          lastSeq: Math.max(conversation.lastSeq, m.seq),
          updatedAt: newerTimestamp(conversation.updatedAt, m.createdAt),
        };
    }
    this.ingestReactionsInto(next, messages);
    for (const [conversationId, batch] of grouped) {
      next.messages[conversationId] = reconcileMessages(
        next.messages[conversationId] || [],
        batch,
      );
      next.messages[conversationId] = projectReactionList(next.messages[conversationId], next.reactionStates);
    }
    this.commit(pruneRetractedCollaborationState(next, messages));
  }
  replaceConversations(
    conversations: Conversation[],
    observed?: { generation: number; conversationVersion: number },
  ) {
    if (observed && (observed.generation !== this.generation || observed.conversationVersion !== this.conversationVersion))
      return false;
    this.hasAuthoritativeConversations = true;
    this.conversationVersion += 1;
    const allowed = new Set(conversations.map((c) => c.id));
    for (const id of Object.keys(this.snapshot.conversations)) {
      if (!allowed.has(id)) this.reactionAccessEpochs.set(id, (this.reactionAccessEpochs.get(id) || 0) + 1);
    }
    const next = reconcileConversationsSnapshot(this.snapshot, conversations, allowed, this.manualUnreadWrites, newerTimestamp);
    this.commit({
      ...next,
      reactionStates: this.snapshot.reactionStates
        ? Object.fromEntries(Object.entries(this.snapshot.reactionStates).filter(([id]) => allowed.has(id)))
        : undefined,
      messages: Object.fromEntries(Object.entries(this.snapshot.messages).filter(([id]) => allowed.has(id))),
    });
    return true;
  }
  addConversation(conversation: Conversation) {
    this.conversationVersion += 1;
    this.commit({
      ...this.snapshot,
      conversations: { ...this.snapshot.conversations, [conversation.id]: conversation },
    });
  }
  markRead(id: string, seq: number, manualUnreadSeq?: number | null) {
    const conversation = this.snapshot.conversations[id] as (Conversation & { unreadCount?: number }) | undefined;
    if (!conversation) return;
    const currentManual = this.snapshot.manualUnreadByConversation[id] ?? conversation.manualUnreadSeq;
    const nextManual = manualUnreadSeq !== undefined
      ? (manualUnreadSeq && manualUnreadSeq > 0 ? manualUnreadSeq : undefined)
      : (currentManual && seq >= currentManual ? undefined : currentManual);
    const manualChanged = nextManual !== currentManual || nextManual !== conversation.manualUnreadSeq;
    const nextReadSeq = Math.max(conversation.readSeq, seq);
    const nextUnreadCount = Math.max(0, conversation.lastSeq - nextReadSeq, nextManual && nextManual > 0 ? conversation.lastSeq - nextManual + 1 : 0);
    if (seq > conversation.readSeq || manualChanged || conversation.unreadCount !== nextUnreadCount) {
      const nextManualMap = { ...this.snapshot.manualUnreadByConversation };
      if (nextManual && nextManual > 0) nextManualMap[id] = nextManual;
      else delete nextManualMap[id];
      const nextConv: Conversation & { unreadCount?: number } = {
        ...conversation,
        readSeq: nextReadSeq,
        ...(nextManual !== undefined ? { manualUnreadSeq: nextManual } : {}),
        unreadCount: nextUnreadCount,
      };
      if (nextManual === undefined) delete nextConv.manualUnreadSeq;
      this.conversationVersion += 1;
      this.commit({
        ...this.snapshot,
        conversations: { ...this.snapshot.conversations, [id]: nextConv as Conversation },
        ...(manualChanged ? { manualUnreadByConversation: nextManualMap } : {}),
      });
    }
  }
  setConversationPins(conversationId: string, pins: PinnedMessage[], pinsVersion?: string) {
    const next = setConversationPinsState(this.snapshot, conversationId, pins, pinsVersion);
    if (next !== this.snapshot) this.commit(next);
  }
  applyPinUpdated(event: PinUpdatedEvent) {
    const next = applyPinUpdated(this.snapshot, event);
    if (next !== this.snapshot) this.commit(next);
  }
  setConversationSaved(conversationId: string, messageIds: string[], sinceDeltaSeq?: number) {
    const next = setConversationSavedState(this.snapshot, conversationId, messageIds, sinceDeltaSeq);
    if (next !== this.snapshot) this.commit(next);
  }
  setSavedMessages(items: SavedMessage[], sinceDeltaSeq?: number) {
    const next = setSavedMessagesState(this.snapshot, items, sinceDeltaSeq);
    if (next !== this.snapshot) this.commit(next);
  }
  applySavedUpdated(event: SavedUpdatedEvent) {
    const next = applySavedUpdated(this.snapshot, event);
    if (next !== this.snapshot) this.commit(next);
  }
  applyReadUpdated(event: ReadUpdatedEvent, currentUserId?: string) {
    const next = applyReadUpdated(this.snapshot, event, currentUserId);
    if (next !== this.snapshot) {
      if (next.conversations !== this.snapshot.conversations || next.manualUnreadByConversation !== this.snapshot.manualUnreadByConversation)
        this.conversationVersion += 1;
      this.commit(next);
    }
  }
  setManualUnread(conversationId: string, seq: number | undefined) {
    const next = setManualUnreadState(this.snapshot, conversationId, seq);
    if (next !== this.snapshot) {
      this.conversationVersion += 1;
      this.commit(next);
    }
  }
  beginManualUnreadWrite(conversationId: string) {
    const gen = this.generation, token = ++this.nextManualWriteToken;
    const prev = this.manualUnreadWrites.get(conversationId);
    this.manualUnreadWrites.set(conversationId, { token, active: (prev?.active || 0) + 1 });
    this.conversationVersion += 1;
    let done = false;
    return {
      isLatest: () => gen === this.generation && this.manualUnreadWrites.get(conversationId)?.token === token,
      rollbackSeq: (fallback: number | undefined) => {
        const cur = this.manualUnreadWrites.get(conversationId);
        return cur?.token === token && cur.hasServerSeq ? cur.serverSeq : fallback;
      },
      end: () => {
        if (done || gen !== this.generation) return;
        done = true;
        const cur = this.manualUnreadWrites.get(conversationId);
        if (cur && cur.active <= 1) this.manualUnreadWrites.delete(conversationId);
        else if (cur) cur.active -= 1;
        this.conversationVersion += 1;
      },
    };
  }
  applyRealtimeEvent(event: SyncEvent) {
    this.apply({
      events: [event],
      cursor: event.id || this.snapshot.cursor,
      hasMore: false,
    });
  }
  reactionScope(conversationId: string) {
    if (!this.snapshot.userId || !this.snapshot.conversations[conversationId]) return undefined;
    return { conversationId, userId: this.snapshot.userId, generation: this.generation, epoch: this.reactionAccessEpochs.get(conversationId) || 0 };
  }
  reactionScopeCurrent(scope: ReturnType<ChatStore["reactionScope"]>) {
    return !!scope && scope.userId === this.snapshot.userId && scope.generation === this.generation &&
      !!this.snapshot.conversations[scope.conversationId] && scope.epoch === (this.reactionAccessEpochs.get(scope.conversationId) || 0);
  }
  private ingestReactionsInto(next: ChatSnapshot, messages: Message[]) {
    for (const message of messages) {
      if (!next.conversations[message.conversationId]) continue;
      next.reactionStates = updateReactionStates(next.reactionStates, message.conversationId, message.id, messageReactionState(message));
    }
  }
  ingestMessageReactions(messages: Message[], scope?: ReturnType<ChatStore["reactionScope"]>) {
    if (!this.snapshot.userId || (scope && !this.reactionScopeCurrent(scope))) return;
    const next = { ...this.snapshot, messages: { ...this.snapshot.messages } };
    this.ingestReactionsInto(next, messages);
    if (next.reactionStates === this.snapshot.reactionStates) return;
    for (const id of new Set(messages.map((message) => message.conversationId))) {
      if (next.messages[id]) next.messages[id] = projectReactionList(next.messages[id], next.reactionStates);
    }
    this.commit(next);
  }
  setReactions(conversationId: string, messageId: string, reactions: Reaction[], reactionsVersion: string, scope?: ReturnType<ChatStore["reactionScope"]>) {
    if (!this.snapshot.userId || !this.snapshot.conversations[conversationId] || !validReactionVersion(reactionsVersion) ||
      (scope && !this.reactionScopeCurrent(scope))) return;
    const reactionStates = updateReactionStates(this.snapshot.reactionStates, conversationId, messageId, { reactions, reactionsVersion });
    if (reactionStates === this.snapshot.reactionStates) return;
    const list = this.snapshot.messages[conversationId];
    this.commit({
      ...this.snapshot,
      reactionStates,
      messages: list ? { ...this.snapshot.messages, [conversationId]: projectReactionList(list, reactionStates) } : this.snapshot.messages,
    });
  }
  setAttentionProjection(items: AttentionUnreadDTO[], observed: { userId: string; generation: number; epoch: number }) {
    if (observed.userId !== this.snapshot.userId || observed.generation !== this.generation || observed.epoch !== this.notificationEpoch) return false;
    const byConversation: Record<string, number> = {};
    for (const item of items) {
      if (!this.snapshot.conversations[item.conversationId] || !Number.isSafeInteger(item.unreadCount) || item.unreadCount < 0 || item.conversationId in byConversation) return false;
      byConversation[item.conversationId] = item.unreadCount;
    }
    if (Object.keys(byConversation).length !== Object.keys(this.snapshot.conversations).length) return false;
    this.commit({ ...this.snapshot, notificationProjection: { byConversation, stale: false } });
    return true;
  }
  setNotificationLevel(conversationId: string, notificationLevel: NotificationLevel) {
    const conversation = this.snapshot.conversations[conversationId];
    if (!conversation || conversation.notificationLevel === notificationLevel)
      return;
    this.addConversation({ ...conversation, notificationLevel });
  }
  apply(page: SyncPage) {
    if (
      page.cursor === this.snapshot.cursor &&
      page.events.every((event) => olderOrEqual(event.id, this.snapshot.cursor))
    )
      return;
    if (page.events.some((event) => !olderOrEqual(event.id, this.snapshot.cursor) &&
      (event.conversation || event.type === "membership.changed" || event.type === "read.updated" || event.type === "preference.updated")))
      this.conversationVersion += 1;
    // Entity changes and cursor are persisted together, after every event is reconciled.
    let next: ChatSnapshot = {
      ...this.snapshot,
      messages: { ...this.snapshot.messages },
      conversations: { ...this.snapshot.conversations },
      reactionStates: this.snapshot.reactionStates,
    };
    for (const event of page.events) {
      if (olderOrEqual(event.id, this.snapshot.cursor)) continue;
      if (event.message) {
        const m = event.message;
        next.messages[m.conversationId] = reconcileMessages(
          next.messages[m.conversationId] || [],
          [m],
        );
        this.ingestReactionsInto(next, [m]);
        next.messages[m.conversationId] = projectReactionList(next.messages[m.conversationId], next.reactionStates);
        const conversation = next.conversations[m.conversationId];
        if (conversation)
          next.conversations[m.conversationId] = {
            ...conversation,
            lastSeq: Math.max(conversation.lastSeq, m.seq),
            updatedAt: newerTimestamp(conversation.updatedAt, m.createdAt),
          };
        next = pruneRetractedCollaborationState(next, [m]);
      }
      if (event.conversation) {
        const old = next.conversations[event.conversation.id];
        const manualSeq = event.conversation.manualUnreadSeq ?? next.manualUnreadByConversation?.[event.conversation.id] ?? old?.manualUnreadSeq;
        const merged: Conversation = {
          ...event.conversation,
          readSeq: Math.max(old?.readSeq || 0, event.conversation.readSeq),
          lastSeq: Math.max(old?.lastSeq || 0, event.conversation.lastSeq),
          updatedAt: newerTimestamp(old?.updatedAt, event.conversation.updatedAt),
          notificationLevel: old?.notificationLevel && (!event.conversation.notificationLevel || event.conversation.notificationLevel === "all")
            ? old.notificationLevel : (event.conversation.notificationLevel ?? "all"),
        };
        if (manualSeq && manualSeq > 0) merged.manualUnreadSeq = manualSeq;
        else delete merged.manualUnreadSeq;
        next.conversations[event.conversation.id] = merged;
      }
      if (event.type === "read.updated" && event.conversationId) {
        const readEvt = event as SyncEvent & { userId?: string; unreadCount?: number };
        next = applyReadUpdated(next, {
          conversationId: event.conversationId, userId: readEvt.userId,
          readSeq: event.readSeq, manualUnreadSeq: event.manualUnreadSeq, unreadCount: readEvt.unreadCount,
        }, next.userId);
      }
      if (event.type === "pin.updated" && event.conversationId && event.messageId) {
        const pinEvt = event as SyncEvent & { action?: "pinned" | "unpinned"; pin?: PinnedMessage };
        next = applyPinUpdated(next, {
          conversationId: event.conversationId, messageId: event.messageId,
          action: pinEvt.action, pinned: event.pinned, pin: pinEvt.pin, pinsVersion: event.pinsVersion,
        });
      }
      if (event.type === "saved.updated" && event.messageId) {
        next = applySavedUpdated(next, {
          conversationId: event.conversationId || "",
          messageId: event.messageId,
          action: (event as SyncEvent & { action?: "saved" | "unsaved" }).action,
          saved: event.saved,
          savedAt: event.savedAt,
        });
      }
      if (
        event.type === "reaction.updated" &&
        event.conversationId &&
        event.messageId
      ) {
        if (next.conversations[event.conversationId] && validReactionVersion(event.reactionsVersion)) {
          next.reactionStates = updateReactionStates(next.reactionStates, event.conversationId, event.messageId, {
            reactions: event.reactions || [], reactionsVersion: event.reactionsVersion,
          });
          if (next.messages[event.conversationId])
            next.messages[event.conversationId] = projectReactionList(next.messages[event.conversationId], next.reactionStates);
        }
      }
      if (
        event.type === "preference.updated" &&
        event.conversationId &&
        event.notificationLevel &&
        next.conversations[event.conversationId]
      ) {
        const c = next.conversations[event.conversationId];
        next.conversations[event.conversationId] = {
          ...c,
          notificationLevel: event.notificationLevel,
        };
      }
    }
    next.cursor = page.cursor;
    this.commit(next);
  }
}
let storage: StorageLike | undefined;
try {
  storage = typeof localStorage !== "undefined" ? localStorage : undefined;
} catch {
  /* Private browsing may disable persistent storage. */
}
export const chatStore = new ChatStore(storage, idlePersistScheduler);
if (typeof window !== "undefined") {
  window.addEventListener("pagehide", chatStore.flush);
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "hidden") chatStore.flush();
  });
}
