import { describe, expect, it } from "vitest";
import { ChatStore, reconcileMessages } from "./store";
import type { LocalMessage, StorageLike } from "./store";
import type { Conversation } from "@teamie/api-client";
const message = (changes: Partial<LocalMessage> = {}): LocalMessage => ({
  id: "m1",
  conversationId: "c1",
  senderId: "u1",
  senderName: "伙伴",
  senderKind: "human",
  clientMessageId: "request-1",
  seq: 1,
  body: "hello",
  createdAt: "2026-10-16T01:00:00Z",
  ...changes,
});
const conversation: Conversation = {
  id: "c1",
  workspaceId: "w1",
  type: "channel",
  name: "general",
  memberIds: [],
  lastSeq: 1,
  readSeq: 0,
  updatedAt: "2026-10-16T01:00:00Z",
};
const storage = (): StorageLike => {
  const map = new Map<string, string>();
  return {
    getItem: (key) => map.get(key) || null,
    setItem: (key, value) => {
      map.set(key, value);
    },
    removeItem: (key) => {
      map.delete(key);
    },
  };
};
describe("durable message reconciliation", () => {
  it("deduplicates optimistic send, HTTP acknowledgement, and duplicate sync delivery using sender + client id", () => {
    const pending = message({
      id: "local:request-1",
      status: "sending",
      seq: Number.MAX_SAFE_INTEGER,
    });
    const result = reconcileMessages([pending], [message(), message()]);
    expect(result).toHaveLength(1);
    expect(result[0].id).toBe("m1");
    expect(result[0].status).toBeUndefined();
  });
  it("does not merge another sender using an identical client id", () => {
    expect(
      reconcileMessages(
        [message()],
        [message({ id: "m2", senderId: "u2", seq: 2 })],
      ),
    ).toHaveLength(2);
  });
  it("preserves immutable sequence order after edit and rejects stale page data after a retraction", () => {
    const retracted = message({
      body: "",
      retractedAt: "2026-10-16T03:00:00Z",
    });
    const result = reconcileMessages(
      [message({ id: "m2", clientMessageId: "request-2", seq: 2 }), retracted],
      [
        message(),
        message({ body: "edited", editedAt: "2026-10-16T02:00:00Z" }),
      ],
    );
    expect(result.map((m) => m.seq)).toEqual([1, 2]);
    expect(result[0].body).toBe("");
    expect(result[0].retractedAt).toBeTruthy();
  });
  it("never adopts the bootstrap cursor; persists applied entities with their cursor and resumes on reload", () => {
    const disk = storage();
    const store = new ChatStore(disk);
    store.begin("u1");
    store.replaceConversations([conversation]);
    expect(store.getSnapshot().cursor).toBe("0");
    store.apply({
      events: [
        {
          id: "5",
          type: "message.created",
          message: message(),
          createdAt: message().createdAt,
        },
      ],
      cursor: "7",
      hasMore: false,
    });
    const restored = new ChatStore(disk);
    restored.begin("u1");
    expect(restored.getSnapshot().cursor).toBe("7");
    expect(restored.getSnapshot().messages.c1).toHaveLength(1);
  });
  it("cannot replay an older sync event over a newer edit or move a read receipt backwards", () => {
    const store = new ChatStore();
    store.begin("u1");
    store.replaceConversations([conversation]);
    store.apply({
      events: [
        {
          id: "6",
          type: "message.updated",
          message: message({ body: "new", editedAt: "2026-10-16T02:00:00Z" }),
          createdAt: message().createdAt,
        },
      ],
      cursor: "6",
      hasMore: false,
    });
    store.apply({
      events: [
        {
          id: "5",
          type: "message.created",
          message: message(),
          createdAt: message().createdAt,
        },
      ],
      cursor: "6",
      hasMore: false,
    });
    store.markRead("c1", 1);
    store.markRead("c1", 0);
    expect(store.getSnapshot().messages.c1[0].body).toBe("new");
    expect(store.getSnapshot().conversations.c1.readSeq).toBe(1);
  });
  it("clears data and invalidates pending account work on logout, account switch, and membership revocation", () => {
    const disk = storage();
    const store = new ChatStore(disk);
    store.begin("u1");
    store.merge([message()]);
    const generation = store.generation;
    store.clear();
    expect(store.getSnapshot().messages).toEqual({});
    expect(store.generation).toBeGreaterThan(generation);
    store.begin("u1");
    expect(store.getSnapshot().messages).toEqual({});
    store.merge([message()]);
    store.begin("u2");
    expect(store.getSnapshot().userId).toBe("u2");
    expect(store.getSnapshot().messages).toEqual({});
    store.merge([message()]);
    store.revoke();
    expect(store.getSnapshot().messages).toEqual({});
    expect(store.getSnapshot().cursor).toBe("0");
  });
  it("removes inaccessible conversation data while retaining persisted content when write capabilities change", () => {
    const store = new ChatStore();
    store.begin("u1");
    store.replaceConversations([conversation]);
    store.merge([message()]);
    store.replaceConversations([conversation]);
    expect(store.getSnapshot().messages.c1).toHaveLength(1);
    store.replaceConversations([]);
    expect(store.getSnapshot().messages).toEqual({});
  });
});
