import type {
  AttentionUnreadDTO,
  Conversation,
  Message,
  NotificationLevel,
  Reaction,
  SyncPage,
} from "@teamie/api-client";
import {
  materializeReactionState, messageReactionState, projectReactionList,
  reconcileReactionState, updateReactionStates, validReactionVersion,
} from "./reactions";
import type { ReactionStates } from "./reactions";


export { effectiveNotificationLevel, summarizeConversationUnread } from "./notification-model";
import { notificationInputsChanged } from "./notification-model";
import type { NotificationProjection } from "./notification-model";

export type LocalMessage = Message & {
  status?: "sending" | "failed";
  error?: string;
  // A failure the same idempotent retry cannot fix (conflict, lost access,
  // expired file, disabled feature). Only transient failures auto-retry.
  permanent?: boolean;
};
export type ChatSnapshot = {
  userId: string;
  cursor: string;
  messages: Record<string, LocalMessage[]>;
  conversations: Record<string, Conversation>;
  notificationProjection?: NotificationProjection;
  reactionStates?: ReactionStates;
  revision: number;
};
export type StorageLike = Pick<Storage, "getItem" | "setItem" | "removeItem">;
const empty = (userId = ""): ChatSnapshot => ({
  userId,
  cursor: "0",
  messages: {},
  conversations: {},
  revision: 0,
});
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
const MAX_PERSISTED_MESSAGES = 200;
const clientKey = (m: Message) => `${m.senderId}\0${m.clientMessageId}`;
function trimPersistedMessages(messages: LocalMessage[]): LocalMessage[] {
  const confirmed = messages.filter((m) => !m.status);
  if (confirmed.length <= MAX_PERSISTED_MESSAGES) return messages;
  const local = messages.filter((m) => !!m.status);
  return [...confirmed.slice(-MAX_PERSISTED_MESSAGES), ...local];
}
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
// Serialized shards are cached by array/object identity: commits replace only
// the conversations they change, so unchanged shards are never re-stringified.
// The assembled value is byte-compatible with the previous single-blob format.
const persistedMessagesJson = new WeakMap<LocalMessage[], string>();
const persistedConversationJson = new WeakMap<Conversation, string>();
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
export function serializeSnapshot(snapshot: ChatSnapshot): string {
  const states: ReactionStates = {};
  for (const [conversationId, messages] of Object.entries(snapshot.messages)) {
    const kept = new Set(trimPersistedMessages(messages).map((message) => message.id));
    const reactions = Object.fromEntries(
      Object.entries(snapshot.reactionStates?.[conversationId] || {}).filter(([id]) => kept.has(id)),
    );
    if (Object.keys(reactions).length) states[conversationId] = reactions;
  }
  const reactionsJson = Object.keys(states).length ? `,"reactionStates":${JSON.stringify(states)}` : "";
  return (
    `{"userId":${JSON.stringify(snapshot.userId)}` +
    `,"cursor":${JSON.stringify(snapshot.cursor)}` +
    `,"messages":${recordJson(snapshot.messages, messagesJson)}` +
    `,"conversations":${recordJson(snapshot.conversations, conversationJson)}` +
    `,"revision":${JSON.stringify(snapshot.revision)}` +
    (snapshot.notificationProjection ? `,"notificationProjection":${JSON.stringify(snapshot.notificationProjection)}` : "") +
    reactionsJson + "}"
  );
}
export class ChatStore {
  private snapshot: ChatSnapshot = empty();
  private listeners = new Set<() => void>();
  private storage?: StorageLike;
  private schedule?: PersistScheduler;
  private cancelPersist?: () => void;
  private reactionAccessEpochs = new Map<string, number>();
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
    this.snapshot = { ...next, revision: this.snapshot.revision + 1 };
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
    this.commit(next);
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
    this.commit({
      ...this.snapshot,
      reactionStates: this.snapshot.reactionStates
        ? Object.fromEntries(Object.entries(this.snapshot.reactionStates).filter(([id]) => allowed.has(id)))
        : undefined,
      messages: Object.fromEntries(
        Object.entries(this.snapshot.messages).filter(([id]) =>
          allowed.has(id),
        ),
      ),
      conversations: Object.fromEntries(
        conversations.map((c) => {
          const old = this.snapshot.conversations[c.id];
          return [
            c.id,
            {
              ...c,
              lastSeq: Math.max(c.lastSeq, old?.lastSeq ?? 0),
              readSeq: Math.max(c.readSeq, old?.readSeq ?? 0),
              updatedAt: newerTimestamp(old?.updatedAt, c.updatedAt),
              notificationLevel:
                c.notificationLevel ?? old?.notificationLevel ?? "all",
            },
          ];
        }),
      ),
    });
    return true;
  }
  addConversation(conversation: Conversation) {
    this.conversationVersion += 1;
    this.commit({
      ...this.snapshot,
      conversations: {
        ...this.snapshot.conversations,
        [conversation.id]: conversation,
      },
    });
  }
  markRead(id: string, seq: number) {
    const conversation = this.snapshot.conversations[id];
    if (conversation && seq > conversation.readSeq)
      this.addConversation({ ...conversation, readSeq: seq });
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
    if (page.events.some((event) => event.conversation || event.type === "membership.changed"))
      this.conversationVersion += 1;
    // Entity changes and cursor are persisted together, after every event is reconciled.
    const next = {
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
      }
      if (event.conversation) {
        const old = next.conversations[event.conversation.id];
        next.conversations[event.conversation.id] = {
          ...event.conversation,
          readSeq: Math.max(old?.readSeq || 0, event.conversation.readSeq),
          lastSeq: Math.max(old?.lastSeq || 0, event.conversation.lastSeq),
          updatedAt: newerTimestamp(
            old?.updatedAt,
            event.conversation.updatedAt,
          ),
        };
      }
      if (
        event.type === "read.updated" &&
        event.conversationId &&
        next.conversations[event.conversationId]
      ) {
        const c = next.conversations[event.conversationId];
        next.conversations[event.conversationId] = {
          ...c,
          readSeq: Math.max(c.readSeq, event.readSeq || 0),
        };
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
