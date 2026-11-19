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

  it("pages forward from a non-unread located jump, ignores isolated cached rows via extendHistoryBoundary, and seamlessly merges into the latest store window", async () => {
    const target = history[124];
    history[249] = { ...history[249], replyToId: target.id };
    chatStore.merge([history[0]]);
    vi.spyOn(api, "message").mockResolvedValue({ message: target });
    const around = vi.spyOn(api, "messagesAround")
      .mockResolvedValueOnce({ messages: history.slice(99, 149), hasMore: false })
      .mockResolvedValueOnce({ messages: history.slice(124, 174), hasMore: false })
      .mockResolvedValueOnce({ messages: history.slice(149, 200), hasMore: false });
    await mount();
    expect(chatStore.getSnapshot().messages.history.slice(0, 2).map((m) => m.seq)).toEqual([1, 201]);

    await click(".message-reply");
    expect(host.querySelector(".located-banner")).not.toBeNull();
    expect(host.querySelector(".load-later")).not.toBeNull();
    expect(chatStore.getSnapshot().messages.history.some((m) => m.seq === 125)).toBe(false);

    // First forward page reaches 174 (< 201 - 1): must remain isolated from chatStore despite cached seq 1.
    await click(".load-later");
    expect(around).toHaveBeenLastCalledWith(conversation.id, 150);
    expect(host.querySelector(".located-banner")).not.toBeNull();
    expect(host.querySelector(".load-later")).not.toBeNull();
    expect(chatStore.getSnapshot().messages.history.some((m) => m.seq === 174)).toBe(false);

    // Second forward page reaches 200 (adjacent to contiguous store window 201..250): merges seamlessly.
    const scroll = host.querySelector<HTMLElement>(".message-scroll")!;
    scroll.scrollTop = 320;
    vi.mocked(HTMLElement.prototype.scrollTo).mockClear();
    vi.mocked(HTMLElement.prototype.scrollIntoView).mockClear();
    await click(".load-later");
    expect(around).toHaveBeenLastCalledWith(conversation.id, 175);
    expect(host.querySelector(".located-banner")).toBeNull();
    expect(host.querySelector(".load-later")).toBeNull();
    expect(scroll.scrollTop).toBe(320);
    expect(HTMLElement.prototype.scrollTo).not.toHaveBeenCalled();
    expect(HTMLElement.prototype.scrollIntoView).not.toHaveBeenCalled();
    const storeSeqs = chatStore.getSnapshot().messages.history.map((m) => m.seq);
    expect(storeSeqs).toEqual([1, ...Array.from({ length: 151 }, (_, i) => 100 + i)]);
    expect(host.querySelectorAll(".message")).toHaveLength(152);

    // Older paging after merge uses contiguous boundary 100 rather than isolated seq 1.
    await click(".load-earlier");
    expect(api.messages).toHaveBeenLastCalledWith(conversation.id, 100);
  });

  it("retries older() instead of fetchLatest() when loading older history fails", async () => {
    const target = history[124];
    history[249] = { ...history[249], replyToId: target.id };
    vi.spyOn(api, "message").mockResolvedValue({ message: target });
    vi.spyOn(api, "messagesAround").mockResolvedValue({ messages: history.slice(99, 149), hasMore: false });
    await mount();
    await click(".message-reply");
    vi.mocked(api.messages).mockRejectedValueOnce(new Error("timeout"));
    await click(".load-earlier");
    expect(host.querySelector(".message-error")).not.toBeNull();
    expect(host.querySelector(".located-banner")).not.toBeNull();

    const callsBeforeRetry = vi.mocked(api.messages).mock.calls.length;
    await click(".message-error button");
    expect(vi.mocked(api.messages).mock.calls).toHaveLength(callsBeforeRetry + 1);
    expect(api.messages).toHaveBeenLastCalledWith(conversation.id, 100);
    expect(host.querySelector(".located-banner")).not.toBeNull();
    expect(host.querySelectorAll(".message")).toHaveLength(100);
    expect(host.querySelector(".message-error")).toBeNull();
  });

  it("auto-loads newer pages via bottom sentinel only after located scroll settles and user scrolls down, with a failure circuit breaker", async () => {
    type IO = { callback: IntersectionObserverCallback; targets: Element[]; disconnected: boolean };
    const observers: IO[] = [];
    vi.stubGlobal("IntersectionObserver", class {
      io: IO;
      constructor(callback: IntersectionObserverCallback) {
        this.io = { callback, targets: [], disconnected: false };
        observers.push(this.io);
      }
      observe(target: Element) { this.io.targets.push(target); }
      unobserve() {}
      disconnect() { this.io.disconnected = true; }
    });
    const bottomObservers = () =>
      observers.filter(
        (o) => !o.disconnected && o.targets.some((t) => t.getAttribute("data-history-sentinel") === "bottom"),
      );

    const target = history[124];
    history[249] = { ...history[249], replyToId: target.id };
    vi.spyOn(api, "message").mockResolvedValue({ message: target });
    const around = vi.spyOn(api, "messagesAround")
      .mockResolvedValueOnce({ messages: history.slice(99, 149), hasMore: false })
      .mockResolvedValueOnce({ messages: history.slice(124, 174), hasMore: false })
      .mockRejectedValueOnce(new Error("network glitch"))
      .mockResolvedValueOnce({ messages: history.slice(149, 199), hasMore: false });

    await mount();
    await click(".message-reply");
    // Must not arm or fire before the user actively scrolls downward.
    expect(around).toHaveBeenCalledTimes(1);
    expect(bottomObservers()).toHaveLength(0);

    const scroll = host.querySelector<HTMLElement>(".message-scroll")!;
    await act(async () => { scroll.dispatchEvent(new Event("scroll")); });
    expect(bottomObservers()).toHaveLength(0);

    scroll.scrollTop = 260;
    await act(async () => { scroll.dispatchEvent(new Event("scroll")); });
    const [firstBottomIo] = bottomObservers();
    expect(firstBottomIo).toBeDefined();

    // Concurrent intersections deduplicate via newerBusyRef.
    const visible = [{ isIntersecting: true, target: firstBottomIo.targets[0] }] as unknown as IntersectionObserverEntry[];
    await act(async () => {
      firstBottomIo.callback(visible, {} as IntersectionObserver);
      firstBottomIo.callback(visible, {} as IntersectionObserver);
    });
    expect(around).toHaveBeenCalledTimes(2);
    expect(around).toHaveBeenLastCalledWith(conversation.id, 150);
    expect(host.querySelectorAll(".message")).toHaveLength(75);

    // Re-armed observer trips circuit breaker on failure instead of looping.
    const [secondBottomIo] = bottomObservers();
    expect(secondBottomIo).toBeDefined();
    expect(secondBottomIo).not.toBe(firstBottomIo);
    await act(async () => secondBottomIo.callback(visible, {} as IntersectionObserver));
    expect(around).toHaveBeenCalledTimes(3);
    expect(bottomObservers()).toHaveLength(0);
    expect(host.querySelector(".message-error")).not.toBeNull();

    // Retrying via error banner calls newer() (not fetchLatest) and re-arms the sentinel.
    const messagesCalls = vi.mocked(api.messages).mock.calls.length;
    await click(".message-error button");
    expect(vi.mocked(api.messages).mock.calls).toHaveLength(messagesCalls);
    expect(around).toHaveBeenCalledTimes(4);
    expect(around).toHaveBeenLastCalledWith(conversation.id, 175);
    expect(host.querySelectorAll(".message")).toHaveLength(100);
    expect(bottomObservers()).toHaveLength(1);
  });

  it("treats zero-progress messagesAround responses as transient errors without merging a gap", async () => {
    const target = history[124];
    history[249] = { ...history[249], replyToId: target.id };
    vi.spyOn(api, "message").mockResolvedValue({ message: target });
    const around = vi.spyOn(api, "messagesAround")
      .mockResolvedValueOnce({ messages: history.slice(99, 149), hasMore: false })
      .mockResolvedValueOnce({ messages: history.slice(99, 149), hasMore: false })
      .mockResolvedValueOnce({ messages: history.slice(124, 174), hasMore: false });
    await mount();
    await click(".message-reply");

    await click(".load-later");
    expect(around).toHaveBeenLastCalledWith(conversation.id, 150);
    expect(host.textContent).toContain("后续消息暂未加载，请重试");
    expect(host.querySelector(".located-banner")).not.toBeNull();
    expect(chatStore.getSnapshot().messages.history.some((m) => m.seq === 149)).toBe(false);

    await click(".load-later");
    expect(host.textContent).not.toContain("后续消息暂未加载，请重试");
    expect(host.querySelectorAll(".message")).toHaveLength(75);
  });

  it("preserves the visible message viewport offset when seamless catch-up prepends older cached rows", async () => {
    const target = history[124];
    history[249] = { ...history[249], replyToId: target.id };
    vi.spyOn(api, "message").mockResolvedValue({ message: target });
    vi.spyOn(api, "messagesAround")
      .mockResolvedValueOnce({ messages: history.slice(99, 149), hasMore: false })
      .mockResolvedValueOnce({ messages: history.slice(149, 200), hasMore: false });

    await mount();
    // Retain an isolated cached older row (seq 1) in the live store before jumping to seq 125.
    await act(async () => {
      chatStore.merge([{ ...history[0], body: "older cached row", editedAt: "2026-10-16T05:00:00Z" }]);
    });

    await click(".message-reply");
    expect(host.querySelector(".located-banner")).not.toBeNull();

    const scroll = host.querySelector<HTMLDivElement>(".message-scroll")!;
    scroll.scrollTop = 320;
    const scrollToSpy = vi.spyOn(scroll, "scrollTo");
    scrollToSpy.mockClear();
    let mergedAboveShift = 0;
    vi.spyOn(scroll, "getBoundingClientRect").mockImplementation(
      () => ({ top: 100, bottom: 700, left: 0, right: 800, width: 800, height: 600, x: 0, y: 100, toJSON: () => ({}) }),
    );
    const originalRect = Element.prototype.getBoundingClientRect;
    const rectSpy = vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
      if (this === scroll) {
        return { top: 100, bottom: 700, left: 0, right: 800, width: 800, height: 600, x: 0, y: 100, toJSON: () => ({}) };
      }
      const seqAttr = this.getAttribute("data-seq");
      if (seqAttr) {
        const seq = Number(seqAttr);
        const top = seq === 1 ? 40 : 120 + (seq - 100) * 40 + mergedAboveShift;
        return { top, bottom: top + 36, left: 0, right: 800, width: 800, height: 36, x: 0, y: top, toJSON: () => ({}) };
      }
      return originalRect.call(this);
    });

    try {
      // Simulate cached seq 1 pushing seq 100 down by 80px once the live store list commits.
      mergedAboveShift = 0;
      const loadLaterPromise = act(async () => {
        const button = host.querySelector<HTMLButtonElement>(".load-later")!;
        // First capture runs synchronously before chatStore.merge inside newer(), then DOM commits with seq 1 above.
        const origMerge = chatStore.merge.bind(chatStore);
        const mergeSpy = vi.spyOn(chatStore, "merge").mockImplementation((msgs) => {
          mergedAboveShift = 80;
          return origMerge(msgs);
        });
        button.click();
        await Promise.resolve();
        await Promise.resolve();
        mergeSpy.mockRestore();
      });
      await loadLaterPromise;
      expect(host.querySelector(".located-banner")).toBeNull();
      expect(scrollToSpy).not.toHaveBeenCalled();
      expect(scroll.scrollTop).toBe(400);
    } finally {
      scrollToSpy.mockRestore();
      rectSpy.mockRestore();
    }
  });
});
