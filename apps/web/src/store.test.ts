import { afterEach, describe, expect, it, vi } from "vitest";
import {
  ChatStore,
  idlePersistScheduler,
  reconcileMessages,
  serializeSnapshot,
} from "./store";
import type { LocalMessage, PersistScheduler, StorageLike } from "./store";
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
  it.each(["sync", "local"])("rejects an older bootstrap after a %s conversation creation without losing messages or cursor", (kind) => {
    const store = new ChatStore();
    store.begin("u1");
    store.replaceConversations([conversation]);
    const observed = { generation: store.generation, conversationVersion: store.conversationVersion };
    const created = { ...conversation, id: "new-conversation" };
    if (kind === "local") store.addConversation(created);
    store.apply({
      events: [
        ...(kind === "sync" ? [{ id: "7", type: "conversation.created" as const, conversation: created, createdAt: created.updatedAt }] : []),
        { id: "8", type: "message.created" as const, message: message({ conversationId: created.id }), createdAt: created.updatedAt },
      ],
      cursor: "8", hasMore: false,
    });
    expect(store.replaceConversations([conversation], observed)).toBe(false);
    expect(store.getSnapshot().conversations[created.id]).toBeDefined();
    expect(store.getSnapshot().messages[created.id]).toHaveLength(1);
    expect(store.getSnapshot().cursor).toBe("8");
  });
  it("accepts a bootstrap across ordinary message/read arrivals while preserving newer sequence numbers", () => {
    const store = new ChatStore();
    store.begin("u1");
    store.replaceConversations([conversation]);
    const observed = { generation: store.generation, conversationVersion: store.conversationVersion };
    store.apply({ events: [
      { id: "7", type: "message.created", message: message({ seq: 5 }), createdAt: conversation.updatedAt },
      { id: "8", type: "read.updated", conversationId: conversation.id, readSeq: 4, createdAt: conversation.updatedAt },
    ], cursor: "8", hasMore: false });
    expect(store.replaceConversations([conversation], observed)).toBe(true);
    expect(store.getSnapshot().conversations.c1).toMatchObject({ lastSeq: 5, readSeq: 4 });
    expect(store.getSnapshot().messages.c1).toHaveLength(1);
    expect(store.getSnapshot().cursor).toBe("8");
    store.revoke();
    expect(store.replaceConversations([conversation], observed)).toBe(false);
    expect(store.getSnapshot().conversations).toEqual({});
  });
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
  it.each([
    ["2026-10-16T07:00:01.1Z", "2026-10-16T07:00:01.100001Z"],
    ["2026-10-16T07:00:01Z", "2026-10-16T07:00:01.000000001Z"],
    ["2026-10-16T07:00:01.100000001Z", "2026-10-16T07:00:01.100000002Z"],
    ["2026-10-16T07:00:01.999999999Z", "2026-10-16T07:00:02Z"],
    ["2026-10-16T15:00:01.1+08:00", "2026-10-16T07:00:01.100001Z"],
  ])(
    "applies a newer exact timestamp %s -> %s and rejects older history replay",
    (older, newer) => {
      const previous = message({ body: "old", editedAt: older });
      const update = message({ body: "new", editedAt: newer });
      const updated = reconcileMessages([previous], [update]);
      expect(updated[0].body).toBe("new");
      expect(updated[0].editedAt).toBe(newer);
      expect(reconcileMessages(updated, [previous, message()])[0].body).toBe(
        "new",
      );
    },
  );
  it("commits a nanosecond edit with its sync cursor and never resurrects its tombstone", () => {
    const store = new ChatStore();
    store.begin("u1");
    store.merge([message({ body: "old", editedAt: "2026-10-16T07:00:01.1Z" })]);
    store.apply({
      events: [
        {
          id: "7",
          type: "message.updated",
          createdAt: "2026-10-16T07:00:01.100001Z",
          message: message({
            body: "new",
            editedAt: "2026-10-16T07:00:01.100001Z",
          }),
        },
      ],
      cursor: "7",
      hasMore: false,
    });
    expect(store.getSnapshot().cursor).toBe("7");
    expect(store.getSnapshot().messages.c1[0].body).toBe("new");
    store.merge([message({ body: "old", editedAt: "2026-10-16T07:00:01.1Z" })]);
    expect(store.getSnapshot().messages.c1[0].body).toBe("new");
    store.merge([
      message({
        body: "must be omitted",
        retractedAt: "2026-10-16T07:00:01.100002Z",
      }),
    ]);
    store.merge([
      message({ body: "stale history", editedAt: "2026-10-16T07:00:02Z" }),
    ]);
    expect(store.getSnapshot().messages.c1[0].body).toBe("");
    expect(store.getSnapshot().messages.c1[0].retractedAt).toBe(
      "2026-10-16T07:00:01.100002Z",
    );
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
  it("bounds persisted localStorage messages to the latest 200 confirmed messages plus local pending messages while preserving full in-memory history", () => {
    const disk = storage();
    const store = new ChatStore(disk);
    store.begin("u1");
    store.replaceConversations([conversation]);
    const confirmedBatch = Array.from({ length: 250 }, (_, index) => {
      const seq = index + 1;
      return message({
        id: `m-${seq}`,
        clientMessageId: `req-${seq}`,
        seq,
        body: `message-${seq}`,
      });
    });
    const pendingMessage = message({
      id: "local:req-pending",
      clientMessageId: "req-pending",
      seq: Number.MAX_SAFE_INTEGER,
      body: "pending-body",
      status: "sending",
    });
    store.merge([
      ...confirmedBatch,
      pendingMessage,
      message({
        id: "m-250",
        clientMessageId: "req-250",
        seq: 250,
        body: "edited-250",
        editedAt: "2026-10-16T02:00:00Z",
      }),
    ]);
    expect(store.getSnapshot().messages.c1).toHaveLength(251);
    expect(store.getSnapshot().messages.c1[249].body).toBe("edited-250");
    const restored = new ChatStore(disk);
    restored.begin("u1");
    const persistedMessages = restored.getSnapshot().messages.c1;
    expect(persistedMessages).toHaveLength(201);
    expect(persistedMessages[0].seq).toBe(51);
    expect(persistedMessages[199].seq).toBe(250);
    expect(persistedMessages[199].body).toBe("edited-250");
    expect(persistedMessages[200]).toMatchObject({
      clientMessageId: "req-pending",
      status: "failed",
    });
  });
});
describe("deferred snapshot persistence", () => {
  const manualScheduler = () => {
    const queue: Array<() => void> = [];
    const schedule: PersistScheduler = (write) => {
      queue.push(write);
      return () => {
        const index = queue.indexOf(write);
        if (index >= 0) queue.splice(index, 1);
      };
    };
    return { schedule, queue, run: () => queue.splice(0).forEach((write) => write()) };
  };
  const countingStorage = () => {
    const disk = storage();
    const setItem = vi.fn(disk.setItem);
    return { disk: { ...disk, setItem }, setItem };
  };
  afterEach(() => {
    vi.useRealTimers();
  });

  it("coalesces commits into one write that pairs entities with their cursor", () => {
    const { disk, setItem } = countingStorage();
    const persistence = manualScheduler();
    const store = new ChatStore(disk, persistence.schedule);
    store.begin("u1");
    store.replaceConversations([conversation]);
    store.merge([message()]);
    store.apply({
      events: [{ id: "5", type: "message.created", message: message({ id: "m2", clientMessageId: "r2", seq: 2 }), createdAt: message().createdAt }],
      cursor: "5", hasMore: false,
    });
    expect(setItem).not.toHaveBeenCalled();
    expect(persistence.queue).toHaveLength(1);
    persistence.run();
    expect(setItem).toHaveBeenCalledTimes(1);
    const restored = new ChatStore(disk);
    restored.begin("u1");
    expect(restored.getSnapshot().cursor).toBe("5");
    expect(restored.getSnapshot().messages.c1).toHaveLength(2);
  });

  it.each(["logout", "account switch"])("removes the account snapshot immediately and cancels its pending write on %s", (change) => {
    const { disk, setItem } = countingStorage();
    const persistence = manualScheduler();
    const store = new ChatStore(disk, persistence.schedule);
    store.begin("u1");
    store.merge([message()]);
    store.flush();
    expect(disk.getItem("teamie.sync.v1.u1")).toContain("hello");
    store.merge([message({ id: "m2", clientMessageId: "r2", seq: 2, body: "PRIVATE_U1" })]);
    expect(persistence.queue).toHaveLength(1);
    if (change === "logout") store.clear();
    else store.begin("u2");
    expect(disk.getItem("teamie.sync.v1.u1")).toBeNull();
    const writes = setItem.mock.calls.length;
    persistence.run();
    store.flush();
    expect(disk.getItem("teamie.sync.v1.u1")).toBeNull();
    for (const [key, value] of setItem.mock.calls.slice(writes)) {
      expect(key).not.toBe("teamie.sync.v1.u1");
      expect(value).not.toContain("PRIVATE_U1");
    }
  });

  it("defaults to an idle write within about a second, and flush writes immediately", () => {
    vi.useFakeTimers();
    const { disk, setItem } = countingStorage();
    const store = new ChatStore(disk, idlePersistScheduler);
    store.begin("u1");
    store.merge([message()]);
    vi.advanceTimersByTime(999);
    expect(setItem).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(setItem).toHaveBeenCalledTimes(1);
    store.merge([message({ id: "m2", clientMessageId: "r2", seq: 2 })]);
    store.flush();
    expect(setItem).toHaveBeenCalledTimes(2);
    vi.advanceTimersByTime(2000);
    expect(setItem).toHaveBeenCalledTimes(2);
  });

  it("serializes byte-for-byte like the previous whole-snapshot format and re-stringifies only changed shards", () => {
    const persistence = manualScheduler();
    const store = new ChatStore(storage(), persistence.schedule);
    store.begin("u1");
    store.replaceConversations([conversation, { ...conversation, id: "c2" }]);
    store.merge([
      message(),
      message({ id: "m2", conversationId: "c2", clientMessageId: "r2", status: "sending", seq: Number.MAX_SAFE_INTEGER }),
    ]);
    // Below 200 messages per conversation trimming is a no-op, so the previous
    // format is exactly JSON.stringify(snapshot).
    const legacy = (snapshot: ReturnType<typeof store.getSnapshot>) =>
      JSON.stringify(snapshot);
    expect(serializeSnapshot(store.getSnapshot())).toBe(legacy(store.getSnapshot()));
    const stringify = vi.spyOn(JSON, "stringify");
    store.merge([message({ id: "m3", clientMessageId: "r3", seq: 3 })]);
    serializeSnapshot(store.getSnapshot());
    const shardWrites = stringify.mock.calls.filter(([value]) => Array.isArray(value));
    expect(shardWrites).toHaveLength(1);
    expect((shardWrites[0][0] as LocalMessage[]).map((m) => m.id)).toEqual(["m1", "m3"]);
    stringify.mockRestore();
    expect(serializeSnapshot(store.getSnapshot())).toBe(legacy(store.getSnapshot()));
  });
});
