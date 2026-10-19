// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter, useLocation } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@teamie/api-client";
import type {
  AttentionItem,
  AttentionPage,
  Bootstrap,
  Conversation,
  Message,
  User,
} from "@teamie/api-client";
import { Shell } from "./shell";
import { chatStore } from "./store";

let user: User;
let bootstrap: Bootstrap;
let rows: AttentionItem[];
let readSeq: number;
const sendTyping = vi.fn();
vi.mock("./auth", () => ({ useAuth: () => ({ user, logout: vi.fn() }) }));
vi.mock("./chat", async (original) => ({
  ...(await original<typeof import("./chat")>()),
  useChat: () => ({
    data: bootstrap,
    loading: false,
    error: "",
    connection: "connected",
    syncError: "",
    sync: vi.fn(),
    refresh: vi.fn(),
    sendTyping,
    typing: {},
  }),
}));
const conversation: Conversation = {
  id: "channel",
  workspaceId: "workspace",
  type: "channel",
  name: "产品协作",
  memberIds: ["human", "agent", "other"],
  lastSeq: 100,
  readSeq: 0,
  updatedAt: "2026-10-17T00:00:00Z",
};
const message = (seq: number, extra: Partial<Message> = {}): Message => ({
  id: `message-${seq}`,
  clientMessageId: `client-${seq}`,
  conversationId: "channel",
  senderId: "agent",
  senderName: "小助手",
  senderKind: "agent",
  seq,
  body: "@林舟 交付文件已准备好 <img src=x>",
  mentions: [{ userId: "human", start: 0, end: 3 }],
  createdAt: conversation.updatedAt,
  ...extra,
});
const item = (
  seq: number,
  extra: Partial<AttentionItem> = {},
): AttentionItem => ({
  message: message(seq),
  conversation: {
    id: conversation.id,
    workspaceId: conversation.workspaceId,
    name: conversation.name,
    type: conversation.type,
  },
  workspaceName: "Teamie 实验室",
  reasons: ["mention"],
  isRead: false,
  ...extra,
});
let host: HTMLDivElement, root: Root, client: QueryClient;
function RouteState() {
  const location = useLocation();
  return <output data-location>{location.pathname + location.search}</output>;
}
function App({ path = "/app/attention" }: { path?: string }) {
  return (
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Shell />
        <RouteState />
      </MemoryRouter>
    </QueryClientProvider>
  );
}
async function settle(action?: () => void) {
  await act(async () => {
    action?.();
    await new Promise((resolve) => setTimeout(resolve, 8));
  });
}
async function mount(path?: string) {
  await settle(() => root.render(<App path={path} />));
  await settle();
}
async function click(selector: string) {
  await settle(() => {
    const element = host.querySelector<HTMLButtonElement>(selector)!;
    element.focus();
    element.click();
  });
}
const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
};
beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
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
  user = {
    id: "human",
    kind: "human",
    platformRole: "user",
    displayName: "林舟",
    email: "member@example.test",
    createdAt: conversation.updatedAt,
  };
  bootstrap = {
    user,
    workspaces: [
      {
        id: "workspace",
        name: "Teamie 实验室",
        createdBy: user.id,
        createdAt: user.createdAt,
      },
    ],
    conversations: [conversation],
    capabilities: {},
    flagsRevision: "fixture",
    syncCursor: "0",
  };
  readSeq = 0;
  rows = [
    item(4, {
      message: message(4, { replyToId: "original" }),
      reasons: ["mention", "reply"],
    }),
    item(3, {
      message: message(3, {
        senderId: "other",
        senderKind: "human",
        senderName: "伙伴",
        body: "回复你的问题",
        mentions: [],
        replyToId: "original",
      }),
      reasons: ["reply"],
    }),
    item(2),
  ];
  chatStore.begin(user.id);
  chatStore.replaceConversations([conversation]);
  vi.spyOn(api, "members").mockResolvedValue({ members: [] });
  vi.spyOn(api, "messages").mockResolvedValue({
    messages: [
      message(100, { body: "更新但不应被顺带标为已读", mentions: [] }),
    ],
    hasMore: true,
  });
  vi.spyOn(api, "read").mockImplementation(async (_id, seq) => {
    readSeq = Math.max(readSeq, seq);
    return { readSeq };
  });
  vi.spyOn(api, "attention").mockImplementation(async (options) => {
    const unreadCount = rows.filter((row) => row.message.seq > readSeq).length;
    const visible = rows
      .map((row) => ({ ...row, isRead: row.message.seq <= readSeq }))
      .filter((row) => !options?.unreadOnly || !row.isRead);
    return {
      items: options?.limit === 100 ? visible.slice(0, 100) : visible,
      unreadCount,
    };
  });
  client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => {
    root.unmount();
    client.clear();
  });
  host.remove();
  chatStore.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("mentions and replies inbox", () => {
  it("shows one rail entry, public human/Agent messages and deduplicated reasons without marking the list read", async () => {
    await mount();
    expect(host.querySelectorAll('a[aria-label="提及与回复"]')).toHaveLength(1);
    expect(host.querySelector('[aria-label="3 条未读提醒"]')).not.toBeNull();
    expect(host.querySelectorAll(".attention-item")).toHaveLength(3);
    expect(host.textContent).toContain("提及并回复了你");
    expect(host.textContent).toContain("回复了你");
    expect(host.textContent).toContain("Agent");
    expect(host.querySelector("img")).toBeNull();
    expect(host.textContent).toContain("<img src=x>");
    expect(api.read).not.toHaveBeenCalled();
    expect(chatStore.getSnapshot().messages.channel).toBeUndefined();
  });
  it("pages independently and filters unread without sending a read acknowledgement", async () => {
    vi.mocked(api.attention).mockImplementation(async (options) =>
      options?.limit === 100
        ? { items: [], unreadCount: 3 }
        : options?.cursor
          ? { items: [item(1)], unreadCount: 3 }
          : { items: [rows[0]], cursor: "older", unreadCount: 3 },
    );
    await mount();
    await click(".attention-more");
    await settle();
    expect(api.attention).toHaveBeenCalledWith(
      { cursor: "older", unreadOnly: false, limit: 50 },
      expect.any(AbortSignal),
    );
    expect(host.querySelectorAll(".attention-item")).toHaveLength(2);
    expect(api.read).not.toHaveBeenCalled();
    await click(".attention-filters button:last-child");
    await settle();
    expect(api.attention).toHaveBeenCalledWith(
      { cursor: undefined, unreadOnly: true, limit: 50 },
      expect.any(AbortSignal),
    );
    expect(api.read).not.toHaveBeenCalled();
  });
  it("loads the source before acknowledging only its sequence, focuses it and preserves newer unread messages", async () => {
    const target = rows[0].message;
    const first = deferred<{ message: Message }>();
    vi.spyOn(api, "message").mockReturnValue(first.promise);
    const around = vi
      .spyOn(api, "messagesAround")
      .mockResolvedValue({ messages: [target], hasMore: false });
    await mount();
    await click(".attention-item");
    expect(host.querySelector("[data-location]")?.textContent).toContain(
      "?message=message-4",
    );
    expect(api.messages).not.toHaveBeenCalled();
    expect(api.read).not.toHaveBeenCalled();
    await settle(() => first.resolve({ message: target }));
    await settle();
    expect(around).toHaveBeenCalledWith("channel", 4);
    expect(api.read).toHaveBeenCalledWith("channel", 4);
    expect(document.activeElement?.id).toBe("message-message-4");
    expect(chatStore.getSnapshot().conversations.channel.readSeq).toBe(4);
    expect(chatStore.getSnapshot().conversations.channel.lastSeq).toBe(100);
    expect(chatStore.getSnapshot().messages.channel).toBeUndefined();
  });
  it("never acknowledges inaccessible or retracted source content", async () => {
    const single = vi
      .spyOn(api, "message")
      .mockRejectedValueOnce(new ApiError(404, "not_found", "不可访问"));
    vi.spyOn(api, "messagesAround").mockResolvedValue({
      messages: [],
      hasMore: false,
    });
    await mount();
    await click(".attention-item");
    expect(host.textContent).toContain("原消息已不可访问");
    expect(api.read).not.toHaveBeenCalled();
    single.mockResolvedValue({
      message: {
        ...rows[0].message,
        body: "",
        retractedAt: "2026-10-17T00:01:00Z",
      },
    });
    await click('a[aria-label="提及与回复"]');
    await settle();
    await click(".attention-item");
    expect(api.read).not.toHaveBeenCalled();
  });
  it("waits until the source is visible before acknowledging it", async () => {
    const visibility = vi
      .spyOn(document, "visibilityState", "get")
      .mockReturnValue("hidden");
    const target = rows[0].message;
    vi.spyOn(api, "message").mockResolvedValue({ message: target });
    vi.spyOn(api, "messagesAround").mockResolvedValue({
      messages: [target],
      hasMore: false,
    });
    await mount("/app/w/workspace/c/channel?message=message-4");
    await settle();
    expect(api.read).not.toHaveBeenCalled();
    await settle(() => {
      visibility.mockReturnValue("visible");
      document.dispatchEvent(new Event("visibilitychange"));
    });
    expect(api.read).toHaveBeenCalledWith("channel", 4);
  });
  it("updates reasons and removes edited, retracted or revoked content from local sync immediately", async () => {
    await mount();
    await settle(() =>
      chatStore.merge([
        {
          ...rows[0].message,
          body: "只回复",
          mentions: [],
          editedAt: "2026-10-17T00:01:00Z",
        },
      ]),
    );
    expect(host.textContent).not.toContain("提及并回复了你");
    await settle(() =>
      chatStore.merge([
        message(1, {
          id: "original",
          senderId: user.id,
          retractedAt: "2026-10-17T00:02:00Z",
          body: "",
          mentions: [],
        }),
      ]),
    );
    expect(host.querySelectorAll(".attention-item")).toHaveLength(1);
    await settle(() =>
      chatStore.merge([
        {
          ...rows[2].message,
          body: "",
          mentions: [],
          retractedAt: "2026-10-17T00:03:00Z",
        },
      ]),
    );
    expect(host.querySelectorAll(".attention-item")).toHaveLength(0);
    await settle(() => chatStore.replaceConversations([]));
    expect(host.querySelectorAll(".attention-item")).toHaveLength(0);
  });
  it("discards a late previous-account response and cancels the old request", async () => {
    const pending = deferred<AttentionPage>();
    let signal: AbortSignal | undefined;
    vi.mocked(api.attention).mockImplementation((options, abort) => {
      if (options?.limit === 100)
        return Promise.resolve({ items: [], unreadCount: 0 });
      signal = abort;
      return pending.promise;
    });
    await mount();
    const previousSignal = signal!;
    vi.mocked(api.attention).mockResolvedValue({ items: [], unreadCount: 0 });
    await settle(() => {
      user = { ...user, id: "new-account" };
      bootstrap = { ...bootstrap, user };
      chatStore.begin(user.id);
      chatStore.replaceConversations([conversation]);
      root.render(<App />);
    });
    await settle(() =>
      pending.resolve({
        items: [
          item(3, {
            message: message(3, {
              body: "旧账号私密回复",
              mentions: [],
              replyToId: "private-source",
            }),
            reasons: ["reply"],
          }),
        ],
        unreadCount: 1,
      }),
    );
    expect(previousSignal.aborted).toBe(true);
    expect(host.textContent).not.toContain("旧账号私密回复");
  });
  it("keeps revoked conversations hidden when an old page arrives after the ACL snapshot changes", async () => {
    const pending = deferred<AttentionPage>();
    vi.mocked(api.attention).mockImplementation((options) =>
      options?.limit === 100
        ? Promise.resolve({ items: [], unreadCount: 0 })
        : pending.promise,
    );
    await mount();
    vi.mocked(api.attention).mockResolvedValue({ items: [], unreadCount: 0 });
    await settle(() => chatStore.replaceConversations([]));
    await settle(() => pending.resolve({ items: rows, unreadCount: 3 }));
    expect(host.querySelectorAll(".attention-item")).toHaveLength(0);
    expect(host.querySelector(".rail-attention-count")).toBeNull();
  });
  it("does not acknowledge a source whose response arrives after leaving its conversation", async () => {
    const pending = deferred<{ message: Message }>();
    vi.spyOn(api, "message").mockReturnValue(pending.promise);
    const around = vi.spyOn(api, "messagesAround");
    await mount();
    await click(".attention-item");
    await click('a[aria-label="提及与回复"]');
    await settle();
    await settle(() => pending.resolve({ message: rows[0].message }));
    expect(api.read).not.toHaveBeenCalled();
    expect(around).not.toHaveBeenCalled();
    expect(host.querySelector(".attention-view")).not.toBeNull();
  });
});
