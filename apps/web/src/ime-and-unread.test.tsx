// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type { Bootstrap, Conversation, Member, Message, User } from "@teamie/api-client";
import { ConversationView } from "./messages";
import { chatStore } from "./store";

const user: User = {
  id: "human", kind: "human", platformRole: "user", displayName: "Human",
  email: "unread@example.invalid", createdAt: "2026-10-17T00:00:00Z",
};
const members: Member[] = [
  { userId: "peer", displayName: "Peer", kind: "human", role: "member" },
];
function conversationWith(readSeq: number, lastSeq: number): Conversation {
  return {
    id: "room", workspaceId: "team", type: "group", name: "Room",
    memberIds: [user.id, "peer"], lastSeq, readSeq, updatedAt: user.createdAt,
  };
}
function message(seq: number, senderId = "peer", body = `message ${seq}`): Message {
  return {
    id: `m${seq}`, conversationId: "room", senderId,
    senderName: senderId === user.id ? user.displayName : "Peer",
    senderKind: "human", clientMessageId: `c${seq}`, seq, body,
    createdAt: new Date(Date.parse(user.createdAt) + seq * 1000).toISOString(),
  };
}

let bootstrap: Bootstrap;
let page: Message[];
const sync = vi.fn();
const sendTyping = vi.fn();
vi.mock("./auth", () => ({ useAuth: () => ({ user }) }));
vi.mock("./chat", async (original) => ({
  ...(await original<typeof import("./chat")>()),
  useChat: () => ({ data: bootstrap, syncError: "", sync, sendTyping, typing: {} }),
}));

let host: HTMLDivElement, root: Root;
const noop = () => {};
const composer = () => host.querySelector<HTMLTextAreaElement>('textarea[aria-label="消息内容"]')!;
async function show(conversation: Conversation, extra: Partial<Parameters<typeof ConversationView>[0]> = {}) {
  await act(async () => {
    root.render(<ConversationView
      key="view" conversation={conversation} members={members}
      onMembers={noop} membersOpen={false} onMenu={noop} {...extra}
    />);
  });
  await act(async () => { await Promise.resolve(); });
}
async function type(target: HTMLTextAreaElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(target, value);
    target.setSelectionRange(value.length, value.length);
    target.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function key(target: HTMLElement, init: KeyboardEventInit & { keyCode?: number }) {
  const event = new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
  if (init.keyCode !== undefined) Object.defineProperty(event, "keyCode", { value: init.keyCode });
  await act(async () => { target.dispatchEvent(event); });
}
async function elapse(ms: number) {
  await act(async () => { await vi.advanceTimersByTimeAsync(ms); });
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date(user.createdAt));
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  vi.stubGlobal("matchMedia", () => ({ matches: false }));
  Object.defineProperty(HTMLElement.prototype, "scrollTo", { configurable: true, value: vi.fn() });
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: vi.fn() });
  bootstrap = {
    user, workspaces: [], conversations: [conversationWith(0, 0)],
    capabilities: { "im.message.mentions": true, "im.message.edit": true },
    flagsRevision: "unread", syncCursor: "0",
  };
  localStorage.clear();
  chatStore.begin(user.id);
  chatStore.replaceConversations(bootstrap.conversations);
  page = [];
  vi.spyOn(api, "messages").mockImplementation(async () => ({ messages: page, hasMore: false }));
  vi.spyOn(api, "read").mockImplementation(async (_id, seq) => ({ readSeq: seq }));
  vi.spyOn(api, "send").mockImplementation(async (id, draft) => ({
    message: { ...message(99, user.id), ...draft, id: "sent", conversationId: id },
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  chatStore.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  delete (HTMLElement.prototype as Partial<HTMLElement>).scrollTo;
  delete (HTMLElement.prototype as Partial<HTMLElement>).scrollIntoView;
});

describe("IME-safe composer keys", () => {
  it("never sends or picks a mention for the Enter that confirms a candidate", async () => {
    const conversation = conversationWith(0, 0);
    await show(conversation);
    await type(composer(), "你好");
    await key(composer(), { key: "Enter", keyCode: 229 });
    await key(composer(), { key: "Enter", isComposing: true });
    expect(api.send).not.toHaveBeenCalled();
    expect(composer().value).toBe("你好");

    await type(composer(), "@Pe");
    expect(host.querySelector('[role="option"]')).not.toBeNull();
    await key(composer(), { key: "Enter", keyCode: 229 });
    expect(composer().value).toBe("@Pe");

    await type(composer(), "你好");
    await key(composer(), { key: "Enter" });
    expect(api.send).toHaveBeenCalledTimes(1);
  });
});

describe("unread entry", () => {
  function layout(rowHeight: number, viewport: number) {
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
      const el = this as HTMLElement;
      if (el.classList.contains("message-scroll"))
        return { top: 0, bottom: viewport, left: 0, right: 0, width: 0, height: viewport, x: 0, y: 0, toJSON: noop } as DOMRect;
      const seq = Number(el.dataset?.seq || 0);
      const bottom = seq * rowHeight;
      return { top: bottom - rowHeight, bottom, left: 0, right: 0, width: 0, height: rowHeight, x: 0, y: 0, toJSON: noop } as DOMRect;
    });
  }

  it("keeps the divider and acknowledges only rows that were actually visible", async () => {
    const conversation = conversationWith(3, 6);
    bootstrap.conversations = [conversation];
    chatStore.replaceConversations([conversation]);
    page = [1, 2, 3, 4, 5, 6].map((seq) => message(seq));
    layout(70, 300);
    await show(conversation);
    await elapse(700);
    expect(api.read).toHaveBeenCalledWith("room", 4);
    expect(api.read).not.toHaveBeenCalledWith("room", 6);
    const divider = host.querySelector(".unread-divider");
    expect(divider?.textContent).toContain("以下是新消息");
    expect(divider?.nextElementSibling?.id).toBe("message-m4");

    // The acknowledged cursor reaches the view without remounting it.
    await show({ ...conversation, readSeq: 4 });
    expect(host.querySelector(".unread-divider")?.nextElementSibling?.id).toBe("message-m4");
  });

  it("never acknowledges across unloaded history and reads inside the unread jump context", async () => {
    const conversation = conversationWith(2, 120);
    bootstrap.conversations = [conversation];
    chatStore.replaceConversations([conversation]);
    page = Array.from({ length: 50 }, (_, i) => message(71 + i));
    const around = vi.spyOn(api, "messagesAround").mockResolvedValue({
      messages: [1, 2, 3, 4, 5].map((seq) => message(seq)), hasMore: false,
    });
    await show(conversation);
    await elapse(700);
    // Rows 71..120 are visible, but 3..70 were never loaded or shown.
    expect(api.read).not.toHaveBeenCalled();
    const banner = host.querySelector(".unread-jump-banner");
    expect(banner?.textContent).toContain("99+");
    await act(async () => { banner!.querySelector("button")!.click(); });
    await act(async () => { await Promise.resolve(); });
    expect(around).toHaveBeenCalledWith("room", 3);
    expect(host.querySelector(".located-banner")?.textContent).toContain("消息上下文");
    expect(host.querySelector(".message.located")?.id).toBe("message-m3");
    await elapse(700);
    expect(api.read).toHaveBeenCalledWith("room", 5);
  });

  function visibleOnly(seqs: () => Set<number>) {
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
      const el = this as HTMLElement;
      const rect = (top: number, bottom: number) =>
        ({ top, bottom, left: 0, right: 0, width: 0, height: bottom - top, x: 0, y: top, toJSON: noop }) as DOMRect;
      if (el.classList.contains("message-scroll")) return rect(0, 600);
      const seq = Number(el.dataset?.seq || 0);
      return seqs().has(seq) ? rect(10, 50) : rect(900, 940);
    });
  }

  it("never counts rows of a replaced window that were not on screen", async () => {
    const conversation = conversationWith(2, 120);
    bootstrap.conversations = [conversation];
    chatStore.replaceConversations([conversation]);
    page = Array.from({ length: 50 }, (_, i) => message(71 + i));
    let visible = new Set(page.map((m) => m.seq));
    visibleOnly(() => visible);
    vi.spyOn(api, "messagesAround").mockResolvedValue({
      messages: Array.from({ length: 27 }, (_, i) => message(i + 1)), hasMore: false,
    });
    await show(conversation);
    await elapse(700);
    expect(api.read).not.toHaveBeenCalled();
    visible = new Set([1, 2, 3, 4, 5, 6]);
    await act(async () => { host.querySelector<HTMLButtonElement>(".unread-jump-banner button")!.click(); });
    await elapse(700);
    expect(api.read).toHaveBeenCalledWith("room", 6);
    expect(api.read).not.toHaveBeenCalledWith("room", 27);
  });

  it("keeps a never-read paginated conversation unread until the user chooses to mark it read", async () => {
    const conversation = conversationWith(0, 120);
    bootstrap.conversations = [conversation];
    chatStore.replaceConversations([conversation]);
    page = Array.from({ length: 50 }, (_, i) => message(71 + i));
    await show(conversation);
    await elapse(700);
    expect(api.read).not.toHaveBeenCalled();
    const actions = host.querySelectorAll<HTMLButtonElement>(".unread-jump-banner button");
    expect(actions[1]?.textContent).toBe("全部标为已读");
    await act(async () => { actions[1].click(); });
    expect(api.read).toHaveBeenCalledWith("room", 120);
  });

  it("still detects the unread gap when an isolated old row is cached", async () => {
    const conversation = conversationWith(5, 120);
    bootstrap.conversations = [conversation];
    chatStore.replaceConversations([conversation]);
    chatStore.merge([{ ...message(1), editedAt: "2026-10-17T00:05:00Z" }]);
    page = Array.from({ length: 50 }, (_, i) => message(71 + i));
    await show(conversation);
    expect(host.querySelector(".unread-jump-banner")).not.toBeNull();
  });

  it("freezes the entry from fresh bootstrap cursors rather than a stale cached snapshot", async () => {
    const cached = conversationWith(6, 6);
    bootstrap.conversations = [conversationWith(6, 10)];
    chatStore.replaceConversations([cached]);
    page = Array.from({ length: 10 }, (_, i) => message(i + 1));
    layout(70, 300);
    await show(cached);
    expect(host.querySelector(".unread-divider")?.nextElementSibling?.id).toBe("message-m7");
  });

  it("clears the message deep link when returning to the latest messages", async () => {
    const conversation = conversationWith(0, 3);
    page = [1, 2, 3].map((seq) => message(seq));
    vi.spyOn(api, "message").mockResolvedValue({ message: message(2) });
    vi.spyOn(api, "messagesAround").mockResolvedValue({ messages: page, hasMore: false });
    const clear = vi.fn();
    await show(conversation, { initialMessageId: "m2", onClearMessageTarget: clear });
    await act(async () => {
      host.querySelector<HTMLButtonElement>(".located-banner button")!.click();
    });
    expect(clear).toHaveBeenCalledTimes(1);
  });
});

describe("editing shortcuts", () => {
  it("edits the last own message with ArrowUp and skips unchanged saves", async () => {
    const conversation = conversationWith(0, 2);
    page = [message(1), message(2, user.id, "hello")];
    const edit = vi.spyOn(api, "edit").mockImplementation(async (_c, _m, body) => ({
      message: { ...message(2, user.id, body), editedAt: "2026-10-17T00:05:00Z" },
    }));
    await show(conversation);
    await key(composer(), { key: "ArrowUp" });
    const editor = () => host.querySelector<HTMLTextAreaElement>('textarea[aria-label="编辑消息内容"]');
    expect(editor()?.value).toBe("hello");

    await key(editor()!, { key: "Enter" });
    expect(edit).not.toHaveBeenCalled();
    expect(editor()).toBeNull();

    await key(composer(), { key: "ArrowUp" });
    await type(editor()!, "hello again");
    await key(editor()!, { key: "Enter" });
    await act(async () => { await Promise.resolve(); });
    expect(edit).toHaveBeenCalledWith("room", "m2", "hello again", []);
  });

  it("ignores ArrowUp while a reply is selected and sends one PATCH for repeated Enter", async () => {
    const conversation = conversationWith(0, 2);
    page = [message(1), message(2, user.id, "hello")];
    let finish!: () => void;
    const edit = vi.spyOn(api, "edit").mockImplementation(
      (_c, _m, body) =>
        new Promise((resolve) => {
          finish = () => resolve({ message: { ...message(2, user.id, body), editedAt: "2026-10-17T00:05:00Z" } });
        }),
    );
    await show(conversation);
    vi.spyOn(api, "message").mockResolvedValue({ message: message(1) });
    const editor = () => host.querySelector<HTMLTextAreaElement>('textarea[aria-label="编辑消息内容"]');
    await act(async () => {
      host.querySelector<HTMLButtonElement>('[aria-label="回复 Peer"]')!.click();
    });
    await key(composer(), { key: "ArrowUp" });
    expect(editor()).toBeNull();
    await act(async () => {
      host.querySelector<HTMLButtonElement>('[aria-label="取消回复"]')!.click();
    });

    await key(composer(), { key: "ArrowUp" });
    await type(editor()!, "edited once");
    // Returning to the composer and pressing ArrowUp must not reset the draft.
    await key(composer(), { key: "ArrowUp" });
    expect(editor()?.value).toBe("edited once");
    await key(editor()!, { key: "Enter" });
    await key(editor()!, { key: "Enter" });
    await key(editor()!, { key: "Enter" });
    expect(edit).toHaveBeenCalledTimes(1);
    await act(async () => { finish(); await Promise.resolve(); });
    expect(editor()).toBeNull();
  });
});
