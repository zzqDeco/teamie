// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type { Bootstrap, Conversation, Member, Message, User } from "@teamie/api-client";
import { ConversationView, LOCATED_HIGHLIGHT_MS } from "./messages";
import { chatStore } from "./store";
import { toast } from "./toast";
import { LONG_PRESS_MS } from "./chat/long-press";

const user: User = {
  id: "me", kind: "human", platformRole: "user", displayName: "我",
  email: "polish@example.invalid", createdAt: "2026-11-02T01:00:00Z",
};
const conversation: Conversation = {
  id: "room", workspaceId: "team", type: "group", name: "Room",
  memberIds: [user.id, "peer", "bot"], lastSeq: 250, readSeq: 250, updatedAt: user.createdAt,
};
const members: Member[] = [
  { userId: user.id, displayName: "我", kind: "human", role: "member" },
  { userId: "peer", displayName: "同事", kind: "human", role: "member" },
  { userId: "bot", displayName: "助手", kind: "agent", role: "member", ownerName: "老板" },
];
let bootstrap: Bootstrap;
vi.mock("./auth", () => ({ useAuth: () => ({ user }) }));
vi.mock("./chat", async (original) => ({
  ...(await original<typeof import("./chat")>()),
  useChat: () => ({ data: bootstrap, syncError: "", sync: vi.fn(), sendTyping: vi.fn(), typing: {} }),
}));

type IO = { callback: IntersectionObserverCallback; targets: Element[]; disconnected: boolean };
let observers: IO[];
let host: HTMLDivElement, root: Root, history: Message[];
const minute = (n: number) => new Date(Date.parse(user.createdAt) + n * 60_000).toISOString();
const message = (seq: number, extra: Partial<Message> = {}): Message => ({
  id: `m${seq}`, clientMessageId: `c${seq}`, conversationId: conversation.id,
  senderId: "peer", senderName: "同事", senderKind: "human", seq,
  body: `history-${seq}`, createdAt: minute(seq * 10), ...extra,
});
async function mount(props: Partial<Parameters<typeof ConversationView>[0]> = {}) {
  await act(async () => {
    root.render(<ConversationView
      conversation={conversation} members={members} onMembers={() => {}}
      membersOpen={false} onMenu={() => {}} {...props}
    />);
  });
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
}
const scroller = () => host.querySelector<HTMLElement>(".message-scroll")!;
const sentinelObservers = () =>
  observers.filter((o) => !o.disconnected && o.targets.some((t) => t.classList.contains("history-sentinel")));

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  vi.stubGlobal("matchMedia", () => ({ matches: false }));
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => { callback(0); return 0; });
  observers = [];
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
  Object.defineProperty(HTMLElement.prototype, "scrollTo", { configurable: true, value: vi.fn() });
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: vi.fn() });
  bootstrap = {
    user, workspaces: [], conversations: [conversation],
    capabilities: { "im.reactions.create": true, "im.message.edit": true, "im.message.mentions": true },
    flagsRevision: "polish", syncCursor: "0",
  };
  localStorage.clear();
  chatStore.begin(user.id);
  chatStore.replaceConversations([conversation]);
  history = Array.from({ length: 250 }, (_, i) => message(i + 1));
  vi.spyOn(api, "messages").mockImplementation(async (_id, beforeSeq = Infinity) => {
    const earlier = history.filter((m) => m.seq < beforeSeq);
    return { messages: earlier.slice(-50), hasMore: earlier.length > 50 };
  });
  vi.spyOn(api, "read").mockResolvedValue({ readSeq: conversation.readSeq });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  delete (document as { visibilityState?: string }).visibilityState;
  host.remove();
  chatStore.clear();
  localStorage.clear();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("timeline history auto-load", () => {
  it("loads older pages from a top sentinel exactly once per page and keeps the button fallback", async () => {
    await mount();
    expect(host.querySelector(".load-earlier")).not.toBeNull();
    expect(api.messages).toHaveBeenCalledTimes(1);
    const [io] = sentinelObservers();
    expect(io).toBeDefined();
    const visible = [{ isIntersecting: true, target: io.targets[0] }] as unknown as IntersectionObserverEntry[];
    await act(async () => {
      io.callback(visible, {} as IntersectionObserver);
      io.callback(visible, {} as IntersectionObserver);
    });
    expect(api.messages).toHaveBeenCalledTimes(2);
    expect(api.messages).toHaveBeenLastCalledWith(conversation.id, 201);
    expect(host.querySelectorAll("article.message")).toHaveLength(100);
    // Re-armed with a fresh observer for the next page.
    const [next] = sentinelObservers();
    expect(next).not.toBe(io);
    await act(async () => next.callback(visible, {} as IntersectionObserver));
    expect(api.messages).toHaveBeenLastCalledWith(conversation.id, 151);
  });

  it("keeps the reader on the same message when older rows are prepended", async () => {
    await mount();
    const scroll = scroller();
    Object.defineProperty(scroll, "scrollHeight", {
      configurable: true,
      get: () => host.querySelectorAll("article.message").length * 100,
    });
    scroll.scrollTop = 40;
    await act(async () => host.querySelector<HTMLButtonElement>(".load-earlier")!.click());
    expect(host.querySelectorAll("article.message")).toHaveLength(100);
    expect(scroll.scrollTop).toBe(40 + 50 * 100);
  });

  it("pauses sentinel auto-loading after an older-page failure until manual retry succeeds", async () => {
    await mount();
    expect(api.messages).toHaveBeenCalledTimes(1);
    vi.mocked(api.messages).mockRejectedValueOnce(new Error("网络波动"));
    const [io] = sentinelObservers();
    const visible = [{ isIntersecting: true, target: io.targets[0] }] as unknown as IntersectionObserverEntry[];
    await act(async () => io.callback(visible, {} as IntersectionObserver));
    expect(api.messages).toHaveBeenCalledTimes(2);
    expect(sentinelObservers()).toHaveLength(0);
    expect(host.querySelector(".load-earlier")).not.toBeNull();

    await act(async () => host.querySelector<HTMLButtonElement>(".load-earlier")!.click());
    expect(api.messages).toHaveBeenCalledTimes(3);
    expect(host.querySelectorAll("article.message")).toHaveLength(100);
    expect(sentinelObservers()).toHaveLength(1);
  });

  it("re-adjusts scrollTop after prepended tall messages clamp to collapsed height", async () => {
    history = Array.from({ length: 52 }, (_, i) =>
      message(i + 1, i === 0 ? { body: "超长消息段落\n".repeat(120) } : {}),
    );
    Object.defineProperty(HTMLElement.prototype, "scrollHeight", {
      configurable: true,
      get(this: HTMLElement) {
        if (this.classList.contains("message-body")) {
          if (this.textContent?.includes("超长消息段落")) {
            return this.classList.contains("collapsed") ? 600 : 1500;
          }
          return 80;
        }
        if (this.classList.contains("message-scroll")) {
          return [...this.querySelectorAll<HTMLElement>(".message-body")].reduce(
            (sum, body) => sum + body.scrollHeight,
            0,
          );
        }
        return 0;
      },
    });
    try {
      await mount();
      const scroll = scroller();
      expect(scroll.scrollHeight).toBe(50 * 80);
      scroll.scrollTop = 40;
      await act(async () => host.querySelector<HTMLButtonElement>(".load-earlier")!.click());
      expect(host.querySelector("#message-m1 .message-body")?.classList.contains("collapsed")).toBe(true);
      expect(scroll.scrollTop).toBe(40 + 600 + 80);

      scroll.scrollTop = 120;
      await act(async () => {
        host.querySelector<HTMLButtonElement>("#message-m1 .message-expand-toggle")!.click();
      });
      expect(scroll.scrollTop).toBe(120);
    } finally {
      delete (HTMLElement.prototype as { scrollHeight?: number }).scrollHeight;
    }
  });
});

describe("message rows", () => {
  it("highlights rows that mention me or reply to me, shows Agent ownership once per run and full-time tooltips", async () => {
    history = [
      message(1, { senderId: user.id, senderName: "我", body: "我的问题" }),
      message(2, { body: "@我 看一下", mentions: [{ userId: user.id, start: 0, end: 2 }] }),
      message(3, { body: "回复你", replyToId: "m1" }),
      message(4, { body: "@所有人 开会", mentions: [{ kind: "all", start: 0, end: 4, recipientIds: [user.id, "bot"] }] }),
      message(5, { body: "@在线成员 看", mentions: [{ kind: "here", start: 0, end: 5, recipientCount: 2 }] }),
      message(6, { senderId: "bot", senderName: "助手", senderKind: "agent", body: "第一段" }),
      message(7, { senderId: "bot", senderName: "助手", senderKind: "agent", body: "第二段（10 分钟后）" }),
      message(8, { body: "普通消息" }),
    ];
    await mount();
    const row = (seq: number) => host.querySelector(`#message-m${seq}`)!;
    expect(row(1).classList.contains("relevant-to-me")).toBe(false);
    expect(row(2).classList.contains("relevant-to-me")).toBe(true);
    expect(row(3).classList.contains("relevant-to-me")).toBe(true);
    expect(row(4).classList.contains("relevant-to-me")).toBe(true);
    // `here` without an attention projection never addresses me.
    expect(row(5).classList.contains("relevant-to-me")).toBe(false);
    expect(row(8).classList.contains("relevant-to-me")).toBe(false);
    // Both Agent rows are group heads (10 min apart) but ownership shows once.
    expect(row(6).textContent).toContain("由 老板 创建");
    expect(row(7).querySelector(".message-meta")).not.toBeNull();
    expect(row(7).textContent).not.toContain("由 老板 创建");
    expect(row(7).querySelector(".agent-badge")?.getAttribute("title")).toBe("Agent · 由 老板 创建");
    expect(row(6).querySelector(".agent-avatar")?.className).toMatch(/avatar-tone-[1-6]/);
    const time = row(8).querySelector("time")!;
    expect(time.getAttribute("title")).toMatch(/^\d{4}年\d{1,2}月\d{1,2}日星期.+ \d{2}:\d{2}$/);
    // Reply quotes show plain text, not Markdown.
    expect(row(3).querySelector(".message-reply span")?.textContent).toBe("我的问题");
  });

  it("long-press on touch opens an action sheet whose reply action fills the composer", async () => {
    vi.stubGlobal("matchMedia", (query: string) => ({ matches: query.includes("coarse") }));
    history = [message(1, { body: "**加粗** 的 [链接](https://a.example)" })];
    await mount();
    vi.useFakeTimers();
    const article = host.querySelector<HTMLElement>("#message-m1 .message-text")!;
    const down = new MouseEvent("pointerdown", { bubbles: true, clientX: 5, clientY: 5 });
    Object.assign(down, { pointerType: "touch", isPrimary: true });
    article.dispatchEvent(down);
    await act(async () => { vi.advanceTimersByTime(LONG_PRESS_MS + 20); });
    vi.useRealTimers();
    const sheet = document.querySelector(".message-action-sheet")!;
    expect(sheet).not.toBeNull();
    const labels = [...sheet.querySelectorAll(".message-sheet-action")].map((b) => b.textContent);
    expect(labels).toEqual(expect.arrayContaining(["回复", "复制文本", "复制消息链接"]));
    expect(sheet.querySelectorAll(".message-sheet-reactions button").length).toBe(8);
    const reply = [...sheet.querySelectorAll<HTMLButtonElement>(".message-sheet-action")].find((b) => b.textContent === "回复")!;
    await act(async () => reply.click());
    expect(document.querySelector(".message-action-sheet")).toBeNull();
    expect(host.querySelector(".reply-preview span")?.textContent).toBe("加粗 的 链接");
  });
});

describe("located context mode", () => {
  it("fades the located highlight after 2.5s and announces new messages with a return pill", async () => {
    history = Array.from({ length: 30 }, (_, i) => message(i + 1));
    vi.spyOn(api, "message").mockImplementation(async (_c, id) => ({ message: history.find((m) => m.id === id)! }));
    vi.spyOn(api, "messagesAround").mockImplementation(async () => ({ messages: history.slice(0, 20), hasMore: false }));
    chatStore.merge(history.slice(-10));
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    await mount({ initialMessageId: "m10" });
    expect(host.querySelector("#message-m10")?.classList.contains("located")).toBe(true);
    await act(async () => { vi.advanceTimersByTime(LOCATED_HIGHLIGHT_MS - 100); });
    expect(host.querySelector("#message-m10")?.classList.contains("located")).toBe(true);
    await act(async () => { vi.advanceTimersByTime(200); });
    vi.useRealTimers();
    expect(host.querySelector("#message-m10")?.classList.contains("located")).toBe(false);
    expect(host.querySelector(".located-new-messages")).toBeNull();
    await act(async () => {
      chatStore.merge([message(31, { body: "新来的" }), message(32, { senderId: user.id, senderName: "我" })]);
    });
    const pill = host.querySelector<HTMLButtonElement>(".located-new-messages")!;
    expect(pill.textContent).toBe("1 条新消息 · 返回最新");
    await act(async () => pill.click());
    for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
    expect(host.querySelector(".located-new-messages")).toBeNull();
  });
});

describe("composer guards", () => {
  const setText = async (value: string) => {
    const textarea = host.querySelector<HTMLTextAreaElement>('textarea[aria-label="消息内容"]')!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(textarea, value);
      textarea.setSelectionRange(value.length, value.length);
      textarea.dispatchEvent(new Event("input", { bubbles: true }));
    });
    return textarea;
  };
  it("disables send and shows a red counter beyond 8000 characters", async () => {
    history = [message(1)];
    await mount();
    await setText("字".repeat(7700));
    const send = () => host.querySelector<HTMLButtonElement>('[aria-label="发送消息"]')!;
    expect(host.querySelector(".character-count")?.classList.contains("over-limit")).toBe(false);
    expect(send().disabled).toBe(false);
    const textarea = await setText("字".repeat(8001));
    const counter = host.querySelector(".character-count")!;
    expect(counter.classList.contains("over-limit")).toBe(true);
    expect(counter.textContent).toContain("8001/8000");
    expect(send().disabled).toBe(true);
    const sendSpy = vi.spyOn(api, "send");
    await act(async () => {
      textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    });
    expect(sendSpy).not.toHaveBeenCalled();
  });
  it("explains that pasting files is unavailable when uploads are disabled", async () => {
    history = [message(1)];
    await mount();
    const error = vi.spyOn(toast, "error");
    const textarea = host.querySelector<HTMLTextAreaElement>('textarea[aria-label="消息内容"]')!;
    const paste = new Event("paste", { bubbles: true, cancelable: true });
    Object.assign(paste, { clipboardData: { files: [new File(["x"], "a.png")] } });
    await act(async () => { textarea.dispatchEvent(paste); });
    expect(error).toHaveBeenCalledWith("文件上传已关闭，无法粘贴文件");
    expect(paste.defaultPrevented).toBe(true);
  });
});
