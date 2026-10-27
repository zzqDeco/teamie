import { describe, expect, it } from "vitest";
import type { Conversation, Message, Reaction } from "@teamie/api-client";
import { ChatStore, serializeSnapshot } from "./store";
import { projectMessageReactions } from "./reactions";

const conversation: Conversation = {
  id: "c", workspaceId: "w", name: "C", type: "group", memberIds: ["self", "peer"],
  readSeq: 100, lastSeq: 100, updatedAt: "2026-10-27T00:00:00Z",
};
const reacted: Reaction[] = [{ emoji: "👍", count: 1, userIds: ["self"] }];
const message = (changes: Partial<Message> = {}): Message => ({
  id: "m1", conversationId: "c", senderId: "peer", senderName: "Peer", senderKind: "human",
  clientMessageId: "client1", seq: 1, body: "original", createdAt: "2026-10-27T00:00:00Z",
  reactions: [], reactionsVersion: "0", ...changes,
});
function ready() {
  const store = new ChatStore();
  store.begin("self");
  store.replaceConversations([conversation]);
  return store;
}
function deletion(store: ChatStore, version = "2", cursor = "13") {
  store.apply({ cursor, hasMore: false, events: [{
    id: cursor, type: "reaction.updated", conversationId: "c", messageId: "m1",
    reactions: [], reactionsVersion: version, createdAt: "2026-10-27T00:00:01Z",
  }] });
}

describe("durable reaction reconciliation", () => {
  it("merges message edits and reaction versions independently", () => {
    const store = ready();
    store.merge([message({ reactions: reacted, reactionsVersion: "1" })]);
    deletion(store, "3");
    store.merge([message({ body: "edited", editedAt: "2026-10-27T00:00:02Z", reactions: reacted, reactionsVersion: "2" })]);
    expect(store.getSnapshot().messages.c[0]).toMatchObject({ body: "edited", reactions: [], reactionsVersion: "3" });
    store.merge([message({ reactions: reacted, reactionsVersion: "4" })]);
    expect(store.getSnapshot().messages.c[0]).toMatchObject({ body: "edited", reactions: reacted, reactionsVersion: "4" });
    store.merge([message({ reactions: undefined, reactionsVersion: undefined })]);
    expect(store.getSnapshot().messages.c[0]).toMatchObject({ body: "edited", reactions: reacted, reactionsVersion: "4" });
  });

  it("retains updates received before a context message without adding it to the timeline", () => {
    const store = ready();
    store.merge([message({ id: "tail", clientMessageId: "tail", seq: 100 })]);
    store.setReactions("c", "m1", reacted, "5");
    store.ingestMessageReactions([message({ reactionsVersion: "3" })]);
    expect(projectMessageReactions(message(), store.getSnapshot().reactionStates)).toMatchObject({ reactions: reacted, reactionsVersion: "5" });
    expect(store.getSnapshot().messages.c.map((message) => message.seq)).toEqual([100]);
    expect(store.getSnapshot().conversations.c.readSeq).toBe(100);
    store.ingestMessageReactions([message({ reactionsVersion: "6" })]);
    expect(projectMessageReactions(message(), store.getSnapshot().reactionStates)).toMatchObject({ reactions: [], reactionsVersion: "6" });
  });

  it("ignores stale API results and equal revisions without changing the subscription snapshot", () => {
    const store = ready();
    store.merge([message()]);
    deletion(store);
    const current = store.getSnapshot();
    store.setReactions("c", "m1", reacted, "1");
    store.setReactions("c", "m1", [], "2");
    expect(store.getSnapshot()).toBe(current);
    expect(current.messages.c[0]).toMatchObject({ reactions: [], reactionsVersion: "2" });
  });

  it("orders decimal revisions beyond the JavaScript safe integer boundary", () => {
    const store = ready();
    store.merge([message()]);
    store.setReactions("c", "m1", reacted, "9007199254740993");
    store.setReactions("c", "m1", [], "9007199254740992");
    expect(store.getSnapshot().messages.c[0].reactions).toEqual(reacted);
    store.setReactions("c", "m1", [], "9007199254740994");
    expect(store.getSnapshot().messages.c[0]).toMatchObject({ reactions: [], reactionsVersion: "9007199254740994" });
  });

  it("keeps retraction terminal and invalidates old requests across ACL removal and rejoining", () => {
    const store = ready();
    const scope = store.reactionScope("c");
    store.merge([message({ reactions: reacted, reactionsVersion: "1" })]);
    store.ingestMessageReactions([message({ retractedAt: "2026-10-27T00:00:03Z", reactionsVersion: "2" })]);
    store.setReactions("c", "m1", reacted, "3", scope);
    expect(projectMessageReactions(message(), store.getSnapshot().reactionStates).reactions).toBeUndefined();
    store.replaceConversations([]);
    expect(store.getSnapshot().reactionStates).toEqual({});
    store.replaceConversations([conversation]);
    store.setReactions("c", "m1", reacted, "999", scope);
    expect(store.getSnapshot().reactionStates).toEqual({});
    store.ingestMessageReactions([message()]);
    expect(projectMessageReactions(message(), store.getSnapshot().reactionStates).reactions).toEqual([]);
    store.revoke();
    expect(store.getSnapshot().reactionStates).toBeUndefined();
    store.clear();
    store.begin("other-account");
    store.replaceConversations([conversation]);
    store.setReactions("c", "m1", reacted, "999", scope);
    expect(store.getSnapshot().reactionStates).toBeUndefined();
  });

  it("persists the list, empty-state high-water mark and applied cursor as one snapshot", () => {
    const values = new Map<string, string>();
    const storage = { getItem: (key: string) => values.get(key) || null, setItem: (key: string, value: string) => { values.set(key, value); }, removeItem: (key: string) => { values.delete(key); } };
    const store = new ChatStore(storage);
    store.begin("self");
    store.replaceConversations([conversation]);
    const old = message({ reactions: reacted, reactionsVersion: "1" });
    store.merge([old]);
    deletion(store);
    store.merge([old]);
    const saved = JSON.parse(serializeSnapshot(store.getSnapshot()));
    expect(saved.cursor).toBe("13");
    expect(saved.reactionStates.c.m1).toMatchObject({ reactions: [], reactionsVersion: "2" });
    const restored = new ChatStore(storage);
    restored.begin("self");
    expect(restored.getSnapshot().cursor).toBe("13");
    expect(restored.getSnapshot().messages.c[0]).toMatchObject({ reactions: [], reactionsVersion: "2" });
  });
});
