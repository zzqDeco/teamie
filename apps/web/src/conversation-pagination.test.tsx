// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type { Bootstrap, Conversation, Message, User } from "@teamie/api-client";
import { ConversationView } from "./messages";
import { ChatStore, chatStore } from "./store";

const user: User = {
  id: "human", kind: "human", platformRole: "user", displayName: "Human",
  email: "pagination@example.invalid", createdAt: "2026-10-17T00:00:00Z",
};
const conversation: Conversation = {
  id: "history", workspaceId: "team", type: "group", name: "History",
  memberIds: [user.id, "peer"], lastSeq: 250, readSeq: 250, updatedAt: user.createdAt,
};
const bootstrap: Bootstrap = {
  user, workspaces: [], conversations: [conversation], capabilities: {},
  flagsRevision: "pagination", syncCursor: "0",
};
const sendTyping = vi.fn();
vi.mock("./auth", () => ({ useAuth: () => ({ user }) }));
vi.mock("./chat", async (original) => ({
  ...(await original<typeof import("./chat")>()),
  useChat: () => ({ data: bootstrap, syncError: "", sync: vi.fn(), sendTyping, typing: {} }),
}));

let host: HTMLDivElement, root: Root, history: Message[];
const page = (beforeSeq = Infinity) => {
  const earlier = history.filter((message) => message.seq < beforeSeq);
  return { messages: earlier.slice(-50), hasMore: earlier.length > 50 };
};
async function mount() {
  await act(async () => {
    root.render(<ConversationView
      conversation={conversation} members={[]} onMembers={() => {}}
      membersOpen={false} onMenu={() => {}}
    />);
  });
}
async function click(selector: string) {
  const button = host.querySelector<HTMLButtonElement>(selector);
  expect(button).not.toBeNull();
  await act(async () => button!.click());
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}
function restoreTrimmedHistory() {
  chatStore.clear();
  const previous = new ChatStore(localStorage);
  previous.begin(user.id);
  previous.replaceConversations([conversation]);
  previous.apply({
    events: history.map((message) => ({
      id: String(message.seq), type: "message.created", message,
      createdAt: message.createdAt,
    })),
    cursor: "250", hasMore: false,
  });
  expect(previous.getSnapshot().messages.history).toHaveLength(250);
  chatStore.begin(user.id);
  const restored = chatStore.getSnapshot();
  expect(restored.cursor).toBe("250");
  expect(restored.messages.history).toHaveLength(200);
  expect(restored.messages.history[0].seq).toBe(51);
}

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  vi.stubGlobal("matchMedia", () => ({ matches: false }));
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
    callback(0);
    return 0;
  });
  Object.defineProperty(HTMLElement.prototype, "scrollTo", { configurable: true, value: vi.fn() });
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: vi.fn() });
  localStorage.clear();
  chatStore.begin(user.id);
  chatStore.replaceConversations([conversation]);
  history = Array.from({ length: 250 }, (_, index) => ({
    id: `message-${index + 1}`, clientMessageId: `request-${index + 1}`,
    conversationId: conversation.id, senderId: "peer", senderName: "Peer", senderKind: "human",
    seq: index + 1, body: `history-${index + 1}`, createdAt: user.createdAt,
  }));
  vi.spyOn(api, "messages").mockImplementation(async (_id, beforeSeq) => page(beforeSeq));
  vi.spyOn(api, "read").mockResolvedValue({ readSeq: conversation.readSeq });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  chatStore.clear();
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("conversation history pagination", () => {
  it("adds missing history on the first older action after restoring an ordinary bounded cache", async () => {
    restoreTrimmedHistory();
    await mount();
    expect(host.querySelectorAll(".message")).toHaveLength(200);
    await click(".load-earlier");
    expect(api.messages).toHaveBeenLastCalledWith(conversation.id, 51);
    expect(chatStore.getSnapshot().messages.history.map((message) => message.seq)).toEqual(history.map((message) => message.seq));
    expect(host.querySelectorAll(".message")).toHaveLength(250);
    expect(host.querySelector(".load-earlier")).toBeNull();
  });

  it("does not reuse a history boundary after conversation access is reset", async () => {
    await mount();
    expect(api.messages).toHaveBeenCalledTimes(1);
    await act(async () => chatStore.revoke());
    await click(".load-earlier");
    expect(api.messages).toHaveBeenCalledTimes(1);
    expect(host.querySelectorAll(".message")).toHaveLength(0);
  });

  it.each(["edit", "retraction"])("recovers evicted history after a synced old %s without skipping messages", async (change) => {
    restoreTrimmedHistory();
    const updated = change === "edit"
      ? { ...history[0], body: "CURRENT_EDIT", editedAt: "2026-10-17T00:01:00Z" }
      : { ...history[0], body: "", retractedAt: "2026-10-17T00:01:00Z" };
    chatStore.apply({
      events: [{
        id: "251", type: change === "edit" ? "message.updated" : "message.retracted",
        message: updated, createdAt: "2026-10-17T00:01:00Z",
      }],
      cursor: "251", hasMore: false,
    });
    expect(chatStore.getSnapshot().messages.history.slice(0, 3).map((message) => message.seq)).toEqual([1, 51, 52]);
    await mount();
    expect(host.querySelector(".message-loading")).toBeNull();
    await click(".load-earlier");
    expect(api.messages).toHaveBeenLastCalledWith(conversation.id, 51);
    const recovered = chatStore.getSnapshot();
    expect(recovered.messages.history.map((message) => message.seq)).toEqual(history.map((message) => message.seq));
    expect(recovered.messages.history[0]).toEqual(updated);
    expect(recovered.cursor).toBe("251");
    expect(host.querySelector(".load-earlier")).toBeNull();
    expect(host.querySelectorAll(".message")).toHaveLength(250);
    expect(host.querySelector("#message-message-1 .message-text")?.textContent).toContain(
      change === "edit" ? "CURRENT_EDIT" : "这条消息已撤回",
    );
  });

  it("pages from located history and resets its boundary when a pending older page is canceled by returning latest", async () => {
    const target = history[124];
    history[249] = { ...history[249], replyToId: target.id };
    chatStore.merge([history[0], history[249]]);
    vi.spyOn(api, "message").mockResolvedValue({ message: target });
    vi.spyOn(api, "messagesAround").mockResolvedValue({ messages: history.slice(99, 149), hasMore: false });
    await mount();
    await click(".message-reply");
    await click(".load-earlier");
    expect(api.messages).toHaveBeenLastCalledWith(conversation.id, 100);
    expect(host.querySelector(".message")?.id).toBe("message-message-50");
    expect(host.querySelectorAll(".message")).toHaveLength(100);
    const pending = deferred<ReturnType<typeof page>>();
    vi.mocked(api.messages).mockImplementation(async (_id, beforeSeq) => beforeSeq === 50 ? pending.promise : page(beforeSeq));
    await click(".load-earlier");
    expect(api.messages).toHaveBeenLastCalledWith(conversation.id, 50);
    await click(".located-banner button");
    await act(async () => pending.resolve(page(50)));
    expect(host.querySelector(".located-banner")).toBeNull();
    expect(chatStore.getSnapshot().messages.history.some((message) => message.seq === 2)).toBe(false);
    await click(".load-earlier");
    expect(api.messages).toHaveBeenLastCalledWith(conversation.id, 201);
    expect(chatStore.getSnapshot().messages.history.some((message) => message.seq === 151)).toBe(true);
    expect(chatStore.getSnapshot().messages.history.some((message) => message.seq === 50)).toBe(false);
  });
});
