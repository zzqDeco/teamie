import type { Conversation, Message, SyncPage } from "@teamie/api-client";

export type LocalMessage = Message & {
  status?: "sending" | "failed";
  error?: string;
};
export type ChatSnapshot = {
  userId: string;
  cursor: string;
  messages: Record<string, LocalMessage[]>;
  conversations: Record<string, Conversation>;
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
const version = (m: Message) =>
  timestampNanoseconds(m.retractedAt || m.editedAt || m.createdAt);
export function reconcileMessages(
  existing: LocalMessage[],
  incoming: LocalMessage[],
): LocalMessage[] {
  const output = [...existing];
  for (const incomingMessage of incoming) {
    const message = incomingMessage.retractedAt
      ? { ...incomingMessage, body: "", attachments: undefined }
      : incomingMessage;
    const index = output.findIndex(
      (m) =>
        m.id === message.id ||
        (m.senderId === message.senderId &&
          m.clientMessageId === message.clientMessageId),
    );
    if (index === -1) output.push(message);
    else {
      const old = output[index];
      if (old.retractedAt && !message.retractedAt) continue;
      if (!old.status && message.status) continue;
      if (!old.status && !message.status && version(old) > version(message))
        continue;
      output[index] = message.retractedAt ? { ...message, body: "" } : message;
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
export class ChatStore {
  private snapshot: ChatSnapshot = empty();
  private listeners = new Set<() => void>();
  private storage?: StorageLike;
  generation = 0;
  conversationVersion = 0;
  constructor(storage?: StorageLike) {
    this.storage = storage;
  }
  getSnapshot = () => this.snapshot;
  subscribe = (fn: () => void) => {
    this.listeners.add(fn);
    return () => {
      this.listeners.delete(fn);
    };
  };
  private commit(next: ChatSnapshot) {
    this.snapshot = { ...next, revision: this.snapshot.revision + 1 };
    if (next.userId) {
      try {
        this.storage?.setItem(
          storageKey(next.userId),
          JSON.stringify(this.snapshot),
        );
      } catch {
        /* Sync remains correct in memory if storage is full or unavailable. */
      }
    }
    this.listeners.forEach((fn) => fn());
  }
  begin(userId: string) {
    if (this.snapshot.userId === userId) return;
    this.clear();
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
                      error: "上次发送未确认，请重试",
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
    this.commit(restored);
  }
  clear() {
    this.generation += 1;
    this.conversationVersion += 1;
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
    this.generation += 1;
    this.conversationVersion += 1;
    this.commit(empty(this.snapshot.userId));
  }
  merge(messages: LocalMessage[]) {
    const next = {
      ...this.snapshot,
      messages: { ...this.snapshot.messages },
      conversations: { ...this.snapshot.conversations },
    };
    for (const m of messages) {
      next.messages[m.conversationId] = reconcileMessages(
        next.messages[m.conversationId] || [],
        [m],
      );
      const conversation = next.conversations[m.conversationId];
      if (conversation && !m.status)
        next.conversations[m.conversationId] = {
          ...conversation,
          lastSeq: Math.max(conversation.lastSeq, m.seq),
        };
    }
    this.commit(next);
  }
  replaceConversations(
    conversations: Conversation[],
    observed?: { generation: number; conversationVersion: number },
  ) {
    if (observed && (observed.generation !== this.generation || observed.conversationVersion !== this.conversationVersion))
      return false;
    this.conversationVersion += 1;
    const allowed = new Set(conversations.map((c) => c.id));
    this.commit({
      ...this.snapshot,
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
    };
    for (const event of page.events) {
      if (olderOrEqual(event.id, this.snapshot.cursor)) continue;
      if (event.message) {
        const m = event.message;
        next.messages[m.conversationId] = reconcileMessages(
          next.messages[m.conversationId] || [],
          [m],
        );
        const conversation = next.conversations[m.conversationId];
        if (conversation)
          next.conversations[m.conversationId] = {
            ...conversation,
            lastSeq: Math.max(conversation.lastSeq, m.seq),
          };
      }
      if (event.conversation) {
        const old = next.conversations[event.conversation.id];
        next.conversations[event.conversation.id] = {
          ...event.conversation,
          readSeq: Math.max(old?.readSeq || 0, event.conversation.readSeq),
          lastSeq: Math.max(old?.lastSeq || 0, event.conversation.lastSeq),
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
export const chatStore = new ChatStore(storage);
