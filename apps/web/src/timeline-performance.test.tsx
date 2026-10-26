// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import katex from "katex";
import { api } from "@teamie/api-client";
import type { Bootstrap, Conversation, Member, Message, User } from "@teamie/api-client";
import { ConversationView } from "./messages";
import { MessageBody } from "./markdown";
import { ConversationHeader } from "./message-item";
import { Avatar } from "./ui";
import { chatStore } from "./store";
import { typingStore } from "./typing-store";
import { readComposerDraft, restoreComposerDraft } from "./composer-drafts";

const user: User = {
  id: "human", kind: "human", platformRole: "user", displayName: "Human",
  email: "perf@example.invalid", createdAt: "2026-10-17T00:00:00Z",
};
const members: Member[] = [
  { userId: "peer", displayName: "Peer", kind: "human", role: "member" },
];
function conversationWith(readSeq: number, lastSeq: number, id = "room"): Conversation {
  return {
    id, workspaceId: "team", type: "group", name: id,
    memberIds: [user.id, "peer"], lastSeq, readSeq, updatedAt: user.createdAt,
  };
}
// Ten minutes apart so every row renders its own avatar (no compact grouping).
function message(seq: number, senderId = "peer"): Message {
  return {
    id: `m${seq}`, conversationId: "room", senderId,
    senderName: senderId === user.id ? user.displayName : "Peer",
    senderKind: "human", clientMessageId: `c${seq}`, seq, body: `message ${seq}`,
    createdAt: new Date(Date.parse(user.createdAt) + seq * 600_000).toISOString(),
  };
}

let bootstrap: Bootstrap;
let page: Message[];
vi.mock("./auth", () => ({ useAuth: () => ({ user }) }));
vi.mock("./chat", async (original) => ({
  ...(await original<typeof import("./chat")>()),
  useChat: () => ({ data: bootstrap, syncError: "", sync: vi.fn(), sendTyping: vi.fn() }),
}));
// Render probes: the header renders once per ConversationView render and each
// non-compact row renders one Avatar per MessageItem render.
vi.mock("./ui", async (original) => {
  const actual = await original<typeof import("./ui")>();
  return { ...actual, Avatar: vi.fn(actual.Avatar) };
});
vi.mock("./message-item", async (original) => {
  const actual = await original<typeof import("./message-item")>();
  return { ...actual, ConversationHeader: vi.fn(actual.ConversationHeader) };
});

type ObserverStub = {
  callback: (entries: Array<Partial<IntersectionObserverEntry>>) => void;
  targets: Set<Element>;
  disconnected: boolean;
};
let resizeObservers: ObserverStub[];
let intersectionObservers: ObserverStub[];
function observerClass(registry: () => ObserverStub[]) {
  return class {
    stub: ObserverStub;
    constructor(callback: ObserverStub["callback"]) {
      this.stub = { callback, targets: new Set(), disconnected: false };
      registry().push(this.stub);
    }
    observe(target: Element) { this.stub.targets.add(target); }
    unobserve(target: Element) { this.stub.targets.delete(target); }
    disconnect() { this.stub.disconnected = true; this.stub.targets.clear(); }
  };
}

let host: HTMLDivElement, root: Root;
const noop = () => {};
const composer = () => host.querySelector<HTMLTextAreaElement>('textarea[aria-label="消息内容"]')!;
const scroller = () => host.querySelector<HTMLElement>(".message-scroll")!;
async function show(conversation: Conversation) {
  await act(async () => {
    root.render(<ConversationView
      key={conversation.id} conversation={conversation} members={members}
      onMembers={noop} membersOpen={false} onMenu={noop}
    />);
  });
  await act(async () => { await Promise.resolve(); });
}
async function type(value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(composer(), value);
    composer().setSelectionRange(value.length, value.length);
    composer().dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function elapse(ms: number) {
  await act(async () => { await vi.advanceTimersByTimeAsync(ms); });
}
const viewRenders = () => vi.mocked(ConversationHeader).mock.calls.length;
const rowRenders = () => vi.mocked(Avatar).mock.calls.length;
const draftKey = `teamie.drafts.v1:${user.id}`;

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date(user.createdAt));
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  resizeObservers = [];
  intersectionObservers = [];
  vi.stubGlobal("ResizeObserver", observerClass(() => resizeObservers));
  vi.stubGlobal("matchMedia", () => ({ matches: false }));
  Object.defineProperty(HTMLElement.prototype, "scrollTo", { configurable: true, value: vi.fn() });
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: vi.fn() });
  bootstrap = {
    user, workspaces: [], conversations: [conversationWith(5, 5), conversationWith(0, 0, "other")],
    capabilities: { "im.message.mentions": true }, flagsRevision: "perf", syncCursor: "0",
  };
  localStorage.clear();
  sessionStorage.clear();
  chatStore.begin(user.id);
  chatStore.replaceConversations(bootstrap.conversations);
  page = [1, 2, 3, 4, 5].map((seq) => message(seq));
  vi.spyOn(api, "messages").mockImplementation(async (id) => ({
    messages: id === "room" ? page : [], hasMore: false,
  }));
  vi.spyOn(api, "read").mockImplementation(async (_id, seq) => ({ readSeq: seq }));
  vi.mocked(Avatar).mockClear();
  vi.mocked(ConversationHeader).mockClear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  chatStore.clear();
  typingStore.reset();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  delete (HTMLElement.prototype as Partial<HTMLElement>).scrollTo;
  delete (HTMLElement.prototype as Partial<HTMLElement>).scrollIntoView;
});

describe("timeline render isolation", () => {
  it("re-renders neither the conversation view nor any message row while typing or on typing hints", async () => {
    await show(conversationWith(5, 5));
    expect(host.querySelectorAll("article.message")).toHaveLength(5);
    const views = viewRenders();
    const rows = rowRenders();

    await type("h");
    await type("he");
    await type("hello");
    expect(composer().value).toBe("hello");
    await act(async () => { typingStore.hint("room", "peer", "Peer", true); });
    expect(host.querySelector(".composer-footer span")?.textContent).toBe("Peer 正在输入…");
    await act(async () => { typingStore.hint("room", "peer", "Peer", false); });

    expect(viewRenders()).toBe(views);
    expect(rowRenders()).toBe(rows);
  });

  it("re-renders only the affected row when one row's view state changes", async () => {
    await show(conversationWith(5, 5));
    const rows = rowRenders();
    await act(async () => {
      host.querySelector<HTMLButtonElement>("#message-m3 .message-more-button")!.click();
    });
    expect(host.querySelector("#message-m3")?.classList.contains("actions-open")).toBe(true);
    expect(rowRenders() - rows).toBe(1);
  });
});

describe("debounced composer draft persistence", () => {
  it("persists after typing pauses and flushes on blur and conversation switch", async () => {
    await show(conversationWith(5, 5));
    await type("draft one");
    expect(sessionStorage.getItem(draftKey)).toBeNull();
    await elapse(299);
    expect(sessionStorage.getItem(draftKey)).toBeNull();
    await elapse(1);
    expect(sessionStorage.getItem(draftKey)).toContain("draft one");

    await type("draft two");
    await act(async () => { composer().dispatchEvent(new FocusEvent("focusout", { bubbles: true })); });
    expect(sessionStorage.getItem(draftKey)).toContain("draft two");

    await type("draft three");
    await show(conversationWith(0, 0, "other"));
    expect(sessionStorage.getItem(draftKey)).toContain("draft three");
    await show(conversationWith(5, 5));
    expect(composer().value).toBe("draft three");
  });

  it("shows a restored failed-message draft in the mounted composer without remounting it", async () => {
    await show(conversationWith(5, 5));
    const textarea = composer();
    await type("typed before");
    await act(async () => {
      restoreComposerDraft("room", { body: "@Peer fix", mentions: [{ userId: "peer", start: 0, end: 5 }] });
    });
    expect(composer()).toBe(textarea);
    expect(composer().value).toBe("typed before\n@Peer fix");
    expect(readComposerDraft("room").mentions).toEqual([{ userId: "peer", start: 13, end: 18 }]);
    // Typing continues from the restored text.
    await type("typed before\n@Peer fix now");
    expect(readComposerDraft("room").body).toBe("typed before\n@Peer fix now");
  });

  it.each(["logout", "account switch"])("cancels a pending draft write on %s", async (change) => {
    await show(conversationWith(5, 5));
    await type("private draft");
    await act(async () => {
      if (change === "logout") chatStore.clear();
      else chatStore.begin("another-account");
    });
    await elapse(1000);
    expect(sessionStorage.getItem(draftKey)).toBeNull();
    expect(sessionStorage.getItem("teamie.drafts.v1:another-account")).toBeNull();
    expect(composer().value).toBe("");
  });
});

describe("read dwell with IntersectionObserver", () => {
  it("measures only rows reported near the viewport and keeps contiguous acknowledgement", async () => {
    vi.stubGlobal("IntersectionObserver", observerClass(() => intersectionObservers));
    const conversation = conversationWith(2, 6);
    bootstrap.conversations = [conversation];
    // Restart the account so the read cursor is not raised by the default setup.
    chatStore.clear();
    chatStore.begin(user.id);
    chatStore.replaceConversations([conversation]);
    page = [1, 2, 3, 4, 5, 6].map((seq) => message(seq));
    const measured = new Set<string>();
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
      const rect = (top: number, bottom: number) =>
        ({ top, bottom, left: 0, right: 0, width: 0, height: bottom - top, x: 0, y: top, toJSON: noop }) as DOMRect;
      if (this.classList.contains("message-scroll")) return rect(0, 600);
      if (this.tagName === "ARTICLE") measured.add(this.id);
      return rect(10, 50);
    });
    await show(conversation);
    const io = intersectionObservers.find((o) => !o.disconnected)!;
    expect(io.targets.size).toBe(6);
    await act(async () => {
      io.callback([...io.targets].map((target) => ({
        target,
        isIntersecting: ["message-m3", "message-m4"].includes(target.id),
      })));
    });
    measured.clear();
    await act(async () => { scroller().dispatchEvent(new Event("scroll")); });
    await elapse(700);
    expect([...measured].sort()).toEqual(["message-m3", "message-m4"]);
    expect(api.read).toHaveBeenCalledWith("room", 4);
    expect(api.read).not.toHaveBeenCalledWith("room", 6);
  });
});

describe("MessageBody caching", () => {
  it("reuses parsed output across unrelated re-renders and KaTeX HTML across rows", async () => {
    const render = vi.spyOn(katex, "renderToString");
    const formula = "\\frac{perf}{cache}";
    const body = `Formula $${formula}$`;
    const calls = () => render.mock.calls.filter(([source]) => source === formula).length;
    let bump!: () => void;
    function Parent() {
      const [count, setCount] = useState(0);
      bump = () => setCount((value) => value + 1);
      return <div data-count={count}><MessageBody body={body} userId={user.id} /></div>;
    }
    await act(async () => { root.render(<><Parent /><MessageBody body={`again $${formula}$`} userId={user.id} /></>); });
    // Two rows with the same formula typeset it once (LRU hit).
    expect(calls()).toBe(1);

    // Evict the formula from the 500-entry LRU, then re-render the parent.
    const filler = createRoot(document.createElement("div"));
    await act(async () => {
      filler.render(<MessageBody
        body={Array.from({ length: 501 }, (_, i) => `$x_{${i}}$`).join(" ")}
        userId={user.id}
      />);
    });
    await act(async () => { bump(); });
    expect(host.querySelector("[data-count]")?.getAttribute("data-count")).toBe("1");
    // Same props: the memoized parse is reused, so KaTeX is not called again.
    expect(calls()).toBe(1);

    // A fresh mount must parse again, proving the formula really was evicted.
    await act(async () => { root.render(<MessageBody key="fresh" body={body} userId={user.id} />); });
    expect(calls()).toBe(2);
    await act(async () => filler.unmount());
  });
});
