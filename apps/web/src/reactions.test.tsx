// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type {
  Bootstrap,
  Conversation,
  Member,
  Message,
  User,
  Workspace,
} from "@teamie/api-client";
import { ConversationView } from "./messages";
import { chatStore, serializeSnapshot } from "./store";
import { presenceStore, receiptStore } from "./engagement-store";
import {
  resetNotificationSession,
  setActiveNotificationContext,
} from "./notifications";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

const selfUser: User = {
  id: "u-self",
  kind: "human",
  platformRole: "user",
  displayName: "林舟",
  email: "self@example.test",
  createdAt: "2026-10-20T00:00:00Z",
};

const peerAlice: Member = {
  userId: "u-alice",
  displayName: "陈晨",
  kind: "human",
  role: "member",
};

const peerBob: Member = {
  userId: "u-bob",
  displayName: "周牧",
  kind: "human",
  role: "member",
};

const agentBot: Member = {
  userId: "u-bot",
  displayName: "代码助手",
  kind: "agent",
  role: "member",
  ownerName: "林舟",
};

const members: Member[] = [
  {
    userId: selfUser.id,
    displayName: selfUser.displayName,
    kind: "human",
    role: "owner",
  },
  peerAlice,
  peerBob,
  agentBot,
];

const workspace: Workspace = {
  id: "ws-1",
  name: "核心研发",
  createdBy: selfUser.id,
  createdAt: selfUser.createdAt,
};

let bootstrap: Bootstrap;
const noop = () => {};

vi.mock("./auth", () => ({
  useAuth: () => ({
    user: selfUser,
    loading: false,
    logout: vi.fn(),
    updateUser: vi.fn(),
  }),
}));

vi.mock("./chat", async (original) => ({
  ...(await original<typeof import("./chat")>()),
  useChat: () => ({
    data: bootstrap,
    loading: false,
    error: "",
    connection: "connected",
    syncError: "",
    refresh: vi.fn(async () => {}),
    sync: vi.fn(),
    reconnect: vi.fn(),
    sendTyping: vi.fn(),
    typing: {},
  }),
}));

function makeConversation(
  id: string,
  type: Conversation["type"],
  extra: Partial<Conversation> = {},
): Conversation {
  return {
    id,
    workspaceId: workspace.id,
    type,
    name: type === "direct" ? "" : `会话-${id}`,
    memberIds:
      type === "direct"
        ? [selfUser.id, peerAlice.userId]
        : [selfUser.id, peerAlice.userId, peerBob.userId],
    lastSeq: 2,
    readSeq: 2,
    updatedAt: "2026-10-20T01:00:00Z",
    ...extra,
  };
}

function makeMessage(
  conversationId: string,
  seq: number,
  senderId: string,
  extra: Partial<Message> = {},
): Message {
  const sender =
    members.find((m) => m.userId === senderId) || members[0];
  return {
    id: `${conversationId}-m${seq}`,
    conversationId,
    senderId,
    senderName: sender.displayName,
    senderKind: sender.kind,
    clientMessageId: `${conversationId}-c${seq}`,
    seq,
    body: `消息内容 ${seq}`,
    createdAt: new Date(Date.parse(selfUser.createdAt) + seq * 60_000).toISOString(),
    reactionsVersion: "0",
    ...extra,
  };
}

async function flush() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

describe("versioned reaction state across message windows", () => {
  let host: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
    presenceStore.reset();
    receiptStore.reset();
    resetNotificationSession(null);
    setActiveNotificationContext(undefined, null);
    vi.stubGlobal("matchMedia", () => ({
      matches: false,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe() {}
        disconnect() {}
      },
    );
    Object.defineProperty(HTMLElement.prototype, "scrollTo", {
      configurable: true,
      value: vi.fn(),
    });
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
      configurable: true,
      value: vi.fn(),
    });

    const conv = makeConversation("c-group", "group");
    bootstrap = {
      user: selfUser,
      workspaces: [workspace],
      conversations: [conv],
      capabilities: {
        "im.message.mentions": true,
        "im.reactions.create": true,
        "im.read_receipts.view": true,
        "im.presence.publish": true,
        "im.notifications.configure": true,
        "im.dm.create": true,
        "im.group.create": true,
      },
      flagsRevision: "engage-1",
      syncCursor: "10",
    };
    chatStore.begin(selfUser.id);
    chatStore.replaceConversations(bootstrap.conversations);
    vi.spyOn(api, "read").mockImplementation(async (_id, seq) => ({
      readSeq: seq,
    }));
    vi.spyOn(api, "readReceipts").mockResolvedValue({
      conversationId: "c-group",
      eligibleCount: 3,
      members: [
        { userId: selfUser.id, readSeq: 2 },
        { userId: peerAlice.userId, readSeq: 2 },
        { userId: peerBob.userId, readSeq: 1 },
      ],
      readSeqCounts: [
        { readSeq: 2, count: 2 },
        { readSeq: 1, count: 1 },
      ],
    });
    vi.spyOn(api, "workspacePresence").mockResolvedValue({
      items: [
        {
          userId: peerAlice.userId,
          state: "online",
        },
        {
          userId: peerBob.userId,
          state: "away",
        },
      ],
    });
    vi.spyOn(api, "members").mockResolvedValue({ members });

    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    host.remove();
    chatStore.clear();
    presenceStore.reset();
    receiptStore.reset();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("own reaction remains removable while creation is disabled", async () => {
    const conv = makeConversation("c-group", "group");
    bootstrap.capabilities["im.reactions.create"] = false;
    const msg = makeMessage(conv.id, 1, peerAlice.userId, {
      reactions: [{ emoji: "👍", count: 1, userIds: [selfUser.id] }],
      reactionsVersion: "1",
    });
    vi.spyOn(api, "messages").mockResolvedValue({ messages: [msg], hasMore: false });
    const remove = vi.spyOn(api, "deleteReaction").mockResolvedValue({ reactions: [], reactionsVersion: "2" });
    await act(async () => root.render(<MemoryRouter><ConversationView conversation={conv} members={members} onMembers={noop} membersOpen={false} onMenu={noop} /></MemoryRouter>));
    await flush();
    const own = host.querySelector<HTMLButtonElement>(".reaction-chip.active")!;
    expect(own).toBeTruthy();
    await act(async () => own.click());
    await flush();
    expect(remove).toHaveBeenCalledWith(conv.id, msg.id, "👍");
  });

  it("updates successful reaction on located historical message", async () => {
    const conv = makeConversation("c-group", "group", { readSeq: 100, lastSeq: 100 });
    bootstrap.conversations = [conv];
    chatStore.replaceConversations([conv]);
    const old = makeMessage(conv.id, 1, peerAlice.userId);
    const tail = makeMessage(conv.id, 100, peerBob.userId);
    chatStore.merge([tail]);
    vi.spyOn(api, "message").mockResolvedValue({ message: old });
    vi.spyOn(api, "messagesAround").mockResolvedValue({ messages: [old], hasMore: false });
    const add = vi.spyOn(api, "putReaction").mockResolvedValue({ reactions: [{ emoji: "👍", count: 1, userIds: [selfUser.id] }], reactionsVersion: "1" });
    const remove = vi.spyOn(api, "deleteReaction").mockResolvedValue({ reactions: [], reactionsVersion: "2" });
    await act(async () => root.render(<MemoryRouter><ConversationView conversation={conv} members={members} onMembers={noop} membersOpen={false} onMenu={noop} initialMessageId={old.id} /></MemoryRouter>));
    await flush();
    const picker = host.querySelector<HTMLButtonElement>('button[aria-label="添加表情回应"]')!;
    expect(picker).toBeTruthy();
    await act(async () => picker.click());
    await flush();
    const emoji = document.querySelector<HTMLButtonElement>(".reaction-picker-emoji")!;
    await act(async () => emoji.click());
    await flush();
    expect(add).toHaveBeenCalledWith(conv.id, old.id, "👍");
    expect(host.querySelector(".reaction-chip.active")?.textContent ?? "").toContain("👍1");
    expect(chatStore.getSnapshot().messages[conv.id].map((m) => m.seq)).toEqual([100]);
    expect(chatStore.getSnapshot().conversations[conv.id].readSeq).toBe(100);
    await act(async () => host.querySelector<HTMLButtonElement>(".reaction-chip.active")!.click());
    await flush();
    expect(remove).toHaveBeenCalledWith(conv.id, old.id, "👍");
    expect(host.querySelector(".reaction-chip")).toBeNull();
    await act(async () => chatStore.apply({ cursor: "1", hasMore: false, events: [{ id: "1", type: "reaction.updated", conversationId: conv.id, messageId: old.id, reactions: [{ emoji: "🎉", count: 1, userIds: [peerBob.userId] }], reactionsVersion: "3", createdAt: "2026-10-27T01:00:00Z" }] }));
    expect(host.querySelector(".reaction-chip")?.textContent ?? "").toContain("🎉1");
    expect(host.querySelector(".reaction-chip.active")).toBeNull();
    expect(chatStore.getSnapshot().messages[conv.id].map((m) => m.seq)).toEqual([100]);
    expect(chatStore.getSnapshot().conversations[conv.id].readSeq).toBe(100);
  });

  it("a late history GET does not restore deleted reactions in the current DOM", async () => {
    const conv = makeConversation("c-group", "group", { readSeq: 1, lastSeq: 1 });
    bootstrap.conversations = [conv];
    chatStore.replaceConversations([conv]);
    const stale = makeMessage(conv.id, 1, peerAlice.userId, {
      reactions: [{ emoji: "👍", count: 1, userIds: [selfUser.id] }],
      reactionsVersion: "1",
    });
    chatStore.merge([stale]);
    let finish!: (page: { messages: Message[]; hasMore: boolean }) => void;
    vi.spyOn(api, "messages").mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    await act(async () => root.render(<MemoryRouter><ConversationView conversation={conv} members={members} onMembers={noop} membersOpen={false} onMenu={noop} /></MemoryRouter>));
    await flush();
    await act(async () => chatStore.apply({ cursor: "11", hasMore: false, events: [{ id: "11", type: "reaction.updated", conversationId: conv.id, messageId: stale.id, reactions: [], reactionsVersion: "2", createdAt: "2026-10-27T01:00:00Z" }] }));
    expect(host.querySelector(".reaction-chip")).toBeNull();
    await act(async () => finish({ messages: [stale], hasMore: false }));
    await flush();
    expect(host.querySelector(".reaction-chip")).toBeNull();
  });

  it("a late PUT response cannot restore a newer durable deletion in the DOM", async () => {
    const conv = makeConversation("c-group", "group", { readSeq: 100, lastSeq: 100 });
    bootstrap.conversations = [conv];
    chatStore.replaceConversations([conv]);
    const old = makeMessage(conv.id, 1, peerAlice.userId);
    const tail = makeMessage(conv.id, 100, peerBob.userId);
    chatStore.merge([tail]);
    vi.spyOn(api, "message").mockResolvedValue({ message: old });
    vi.spyOn(api, "messagesAround").mockResolvedValue({ messages: [old], hasMore: false });
    let finish!: (response: { reactions: NonNullable<Message["reactions"]>; reactionsVersion: string }) => void;
    const add = vi.spyOn(api, "putReaction").mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    await act(async () => root.render(<MemoryRouter><ConversationView conversation={conv} members={members} onMembers={noop} membersOpen={false} onMenu={noop} initialMessageId={old.id} /></MemoryRouter>));
    await flush();
    await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="添加表情回应"]')!.click());
    await flush();
    await act(async () => document.querySelector<HTMLButtonElement>(".reaction-picker-emoji")!.click());
    expect(add).toHaveBeenCalledWith(conv.id, old.id, "👍");
    await act(async () => chatStore.apply({ cursor: "12", hasMore: false, events: [{ id: "12", type: "reaction.updated", conversationId: conv.id, messageId: old.id, reactions: [], reactionsVersion: "2", createdAt: "2026-10-27T01:00:00Z" }] }));
    expect(host.querySelector(".reaction-chip")).toBeNull();
    await act(async () => finish({ reactions: [{ emoji: "👍", count: 1, userIds: [selfUser.id] }], reactionsVersion: "1" }));
    await flush();
    expect(host.querySelector(".reaction-chip")).toBeNull();
  });

  it("a newly loaded located snapshot can clear an older overlay", async () => {
    const conv = makeConversation("c-group", "group", { readSeq: 100, lastSeq: 100 });
    bootstrap.conversations = [conv];
    chatStore.replaceConversations([conv]);
    const old = makeMessage(conv.id, 1, peerAlice.userId, { reactions: [], reactionsVersion: "2" });
    const tail = makeMessage(conv.id, 100, peerBob.userId);
    chatStore.merge([tail]);
    chatStore.setReactions(conv.id, old.id, [{ emoji: "👍", count: 1, userIds: [selfUser.id] }], "1");
    vi.spyOn(api, "message").mockResolvedValue({ message: old });
    vi.spyOn(api, "messagesAround").mockResolvedValue({ messages: [old], hasMore: false });
    await act(async () => root.render(<MemoryRouter><ConversationView conversation={conv} members={members} onMembers={noop} membersOpen={false} onMenu={noop} initialMessageId={old.id} /></MemoryRouter>));
    await flush();
    expect(host.querySelector(".reaction-chip")).toBeNull();
    expect(chatStore.getSnapshot().messages[conv.id].map((m) => m.seq)).toEqual([100]);
    expect(chatStore.getSnapshot().conversations[conv.id].readSeq).toBe(100);
  });

  it("a deleted reaction stays deleted after persisting and reopening a late history snapshot", async () => {
    const conv = makeConversation("c-group", "group", { readSeq: 1, lastSeq: 1 });
    bootstrap.conversations = [conv];
    chatStore.replaceConversations([conv]);
    const stale = makeMessage(conv.id, 1, peerAlice.userId, {
      reactions: [{ emoji: "👍", count: 1, userIds: [selfUser.id] }],
      reactionsVersion: "1",
    });
    chatStore.merge([stale]);
    let finish!: (page: { messages: Message[]; hasMore: boolean }) => void;
    const history = vi.spyOn(api, "messages").mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    await act(async () => root.render(<MemoryRouter><ConversationView conversation={conv} members={members} onMembers={noop} membersOpen={false} onMenu={noop} /></MemoryRouter>));
    await flush();
    await act(async () => chatStore.apply({ cursor: "13", hasMore: false, events: [{ id: "13", type: "reaction.updated", conversationId: conv.id, messageId: stale.id, reactions: [], reactionsVersion: "2", createdAt: "2026-10-27T01:00:00Z" }] }));
    await act(async () => finish({ messages: [stale], hasMore: false }));
    await flush();
    expect(host.querySelector(".reaction-chip")).toBeNull();
    const persisted = serializeSnapshot(chatStore.getSnapshot());
    await act(async () => root.unmount());
    chatStore.clear();
    localStorage.setItem(`teamie.sync.v1.${selfUser.id}`, persisted);
    chatStore.begin(selfUser.id);
    expect(chatStore.getSnapshot().cursor).toBe("13");
    history.mockRejectedValue(new Error("offline"));
    root = createRoot(host);
    await act(async () => root.render(<MemoryRouter><ConversationView conversation={conv} members={members} onMembers={noop} membersOpen={false} onMenu={noop} /></MemoryRouter>));
    await flush();
    expect(host.querySelector(".reaction-chip")).toBeNull();
  });
});
