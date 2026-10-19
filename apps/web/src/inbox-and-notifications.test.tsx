// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter, useLocation } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type {
  AttentionItem,
  Bootstrap,
  Conversation,
  Message,
  User,
} from "@teamie/api-client";
import { ProfileSettings } from "./dialogs";
import {
  getNotificationPreferences,
  notificationStorageKey,
  notifySyncedMessages,
  resetNotificationSession,
  setActiveNotificationContext,
  setNotificationPreferences,
} from "./notifications";
import { Shell } from "./shell";
import { chatStore } from "./store";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

let user: User;
let bootstrap: Bootstrap;
const sendTyping = vi.fn();

vi.mock("./auth", () => ({
  useAuth: () => ({
    user,
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
    sync: vi.fn(),
    reconnect: vi.fn(),
    refresh: vi.fn(),
    sendTyping,
    typing: {},
  }),
}));

const makeConv = (id: string, extra: Partial<Conversation> = {}): Conversation => ({
  id,
  workspaceId: "workspace",
  type: "channel",
  name: `频道-${id}`,
  memberIds: ["human", "other"],
  lastSeq: 100,
  readSeq: 0,
  updatedAt: "2026-10-18T10:00:00Z",
  ...extra,
});

const makeMsg = (
  convId: string,
  seq: number,
  extra: Partial<Message> = {},
): Message => ({
  id: `${convId}-msg-${seq}`,
  clientMessageId: `${convId}-client-${seq}`,
  conversationId: convId,
  senderId: "other",
  senderName: "伙伴",
  senderKind: "human",
  seq,
  body: "@林舟 请查看最新进度",
  mentions: [{ userId: "human", start: 0, end: 3 }],
  createdAt: "2026-10-18T10:00:00Z",
  ...extra,
});

const makeAttention = (
  conv: Conversation,
  seq: number,
  extra: Partial<AttentionItem> = {},
): AttentionItem => ({
  message: makeMsg(conv.id, seq),
  conversation: {
    id: conv.id,
    workspaceId: conv.workspaceId,
    name: conv.name,
    type: conv.type,
  },
  workspaceName: "Teamie 实验室",
  reasons: ["mention"],
  isRead: false,
  ...extra,
});

function RouteState() {
  const location = useLocation();
  return <output data-location>{location.pathname + location.search}</output>;
}

describe("PR-7 inbox batch mark-all-read and opt-in in-tab notifications", () => {
  let host: HTMLDivElement;
  let root: Root;
  let client: QueryClient;

  beforeEach(() => {
    window.localStorage.clear();
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

    user = {
      id: "human",
      kind: "human",
      platformRole: "user",
      displayName: "林舟",
      email: "member@example.test",
      createdAt: "2026-10-18T10:00:00Z",
    };
    const convs = [
      makeConv("c1"),
      makeConv("c2"),
      makeConv("c3"),
      makeConv("c4"),
      makeConv("c5"),
    ];
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
      conversations: convs,
      capabilities: {},
      flagsRevision: "fixture",
      syncCursor: "1",
    };
    chatStore.begin(user.id);
    chatStore.replaceConversations(convs);
    vi.spyOn(api, "members").mockResolvedValue({ members: [] });
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
    resetNotificationSession(null);
    setActiveNotificationContext(undefined, null);
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  async function settle(action?: () => void) {
    await act(async () => {
      action?.();
      await new Promise((resolve) => setTimeout(resolve, 10));
    });
  }

  it("pages unreadOnly=true (limit=100), marks max seq per conversation with concurrency<=4, and retries partial failures", async () => {
    const convs = Object.values(chatStore.getSnapshot().conversations);
    const staleCachedOnlyItem = makeAttention(convs[0], 50);
    const page1Items = [
      makeAttention(convs[0], 15),
      makeAttention(convs[0], 8),
      makeAttention(convs[1], 22),
      makeAttention(convs[2], 31),
    ];
    const page2Items = [
      makeAttention(convs[3], 40),
      makeAttention(convs[4], 12),
    ];

    vi.spyOn(api, "attention").mockImplementation(async (options) => {
      const snap = chatStore.getSnapshot().conversations;
      const freshAll = [...page1Items, ...page2Items].map((it) => ({
        ...it,
        isRead: snap[it.conversation.id].readSeq >= it.message.seq,
      }));
      const unreadCount = freshAll.filter((it) => !it.isRead).length;
      if (options?.limit === 100 && options?.unreadOnly) {
        if (options.cursor === "page-2") {
          return {
            items: page2Items.filter(
              (it) => snap[it.conversation.id].readSeq < it.message.seq,
            ),
            unreadCount,
          };
        }
        return {
          items: page1Items.filter(
            (it) => snap[it.conversation.id].readSeq < it.message.seq,
          ),
          cursor: "page-2",
          unreadCount,
        };
      }
      return {
        items: [staleCachedOnlyItem, ...freshAll],
        unreadCount: unreadCount + 1,
      };
    });

    let activeReads = 0;
    let maxConcurrentReads = 0;
    let failC3Once = true;

    const readSpy = vi
      .spyOn(api, "read")
      .mockImplementation(async (convId, seq) => {
        activeReads += 1;
        maxConcurrentReads = Math.max(maxConcurrentReads, activeReads);
        await new Promise((resolve) => setTimeout(resolve, 8));
        activeReads -= 1;
        if (convId === "c3" && failC3Once) {
          failC3Once = false;
          throw new Error("网络波动");
        }
        return { readSeq: seq };
      });

    await settle(() =>
      root.render(
        <QueryClientProvider client={client}>
          <MemoryRouter initialEntries={["/app/attention"]}>
            <Shell />
            <RouteState />
          </MemoryRouter>
        </QueryClientProvider>,
      ),
    );
    await settle();

    const markBtn = host.querySelector<HTMLButtonElement>(
      ".attention-mark-all-read",
    )!;
    expect(markBtn).not.toBeNull();
    expect(markBtn.disabled).toBe(false);
    expect(markBtn.textContent).toContain("全部标为已读");

    await settle(() => {
      markBtn.click();
    });
    await settle();
    await settle();

    // Concurrency is bounded to 4 even across 5 conversations
    expect(maxConcurrentReads).toBeLessThanOrEqual(4);
    // c1 had fresh seq 15 and 8 (and stale cached seq 50 omitted from fresh scan) -> only max fresh seq 15 was called
    expect(readSpy).toHaveBeenCalledWith("c1", 15);
    expect(readSpy).not.toHaveBeenCalledWith("c1", 50);
    expect(readSpy).not.toHaveBeenCalledWith("c1", 8);
    expect(chatStore.getSnapshot().conversations.c1.readSeq).toBe(15);
    expect(chatStore.getSnapshot().conversations.c1.lastSeq).toBe(100);

    // c3 failed once -> partial progress 4 / 5 is shown
    expect(markBtn.textContent).toContain("已处理 4 / 5，重试剩余");

    // Click again to retry only the remaining failed conversation (c3)
    readSpy.mockClear();
    await settle(() => {
      markBtn.click();
    });
    await settle();

    expect(readSpy).toHaveBeenCalledTimes(1);
    expect(readSpy).toHaveBeenCalledWith("c3", 31);
    expect(chatStore.getSnapshot().conversations.c3.readSeq).toBe(31);
    expect(markBtn.textContent).toContain("全部标为已读");
  });

  it("requests Notification permission only on user opt-in, defaults preview to off, scopes consent to account, and fires/navigates on eligible synced messages", async () => {
    const createdNotifications: Array<{
      title: string;
      options?: NotificationOptions;
      onclick: ((event?: Event) => void) | null;
      closed: boolean;
    }> = [];
    const requestPermission = vi
      .fn<() => Promise<NotificationPermission>>()
      .mockResolvedValue("granted");

    class MockNotification {
      static permission: NotificationPermission = "default";
      static requestPermission = async () => {
        const res = await requestPermission();
        MockNotification.permission = res;
        return res;
      };
      title: string;
      options?: NotificationOptions;
      onclick: ((event?: Event) => void) | null = null;
      onclose: (() => void) | null = null;
      closed = false;
      constructor(title: string, options?: NotificationOptions) {
        this.title = title;
        this.options = options;
        createdNotifications.push(this);
      }
      close() {
        this.closed = true;
        this.onclose?.();
      }
    }

    vi.stubGlobal("Notification", MockNotification);

    await settle(() =>
      root.render(
        <QueryClientProvider client={client}>
          <MemoryRouter initialEntries={["/app/settings"]}>
            <ProfileSettings />
          </MemoryRouter>
        </QueryClientProvider>,
      ),
    );

    // Permission is never requested on mount
    expect(requestPermission).not.toHaveBeenCalled();
    expect(getNotificationPreferences("human")).toEqual({
      enabled: false,
      showPreview: false,
    });

    const enableInput = host.querySelector<HTMLInputElement>(
      ".notification-enable-switch input",
    )!;
    const previewInput = host.querySelector<HTMLInputElement>(
      ".notification-preview-switch input",
    )!;
    expect(enableInput.checked).toBe(false);
    expect(previewInput.checked).toBe(false);
    expect(previewInput.disabled).toBe(true);

    // Opt in via user click
    await settle(() => {
      enableInput.click();
    });
    expect(requestPermission).toHaveBeenCalledTimes(1);
    expect(getNotificationPreferences("human")).toEqual({
      enabled: true,
      showPreview: false,
    });
    expect(
      JSON.parse(
        window.localStorage.getItem(notificationStorageKey("human")!)!,
      ),
    ).toEqual({
      enabled: true,
      showPreview: false,
    });
    // Another account on the same browser does not inherit human's consent
    expect(getNotificationPreferences("other-user")).toEqual({
      enabled: false,
      showPreview: false,
    });
    expect(enableInput.checked).toBe(true);
    expect(previewInput.disabled).toBe(false);

    // Prepare conversations and messages in chatStore
    const dmConv = makeConv("dm-1", {
      type: "direct",
      name: "",
      memberIds: ["human", "other"],
    });
    const chanConv = makeConv("c1", {
      type: "channel",
      name: "研发讨论",
    });
    chatStore.replaceConversations([dmConv, chanConv]);

    const myMsg = makeMsg("c1", 10, {
      id: "my-msg-10",
      senderId: "human",
      senderName: "林舟",
      body: "我先提交一版草案",
      mentions: [],
    });
    const replyToMe = makeMsg("c1", 11, {
      id: "reply-11",
      senderId: "other",
      senderName: "陈工",
      body: "收到，包含敏感客户报价 ￥999",
      mentions: [],
      replyToId: "my-msg-10",
    });
    const dmMsg = makeMsg("dm-1", 5, {
      id: "dm-msg-5",
      conversationId: "dm-1",
      senderId: "other",
      senderName: "苏晴",
      body: "私聊秘密内容",
      mentions: [],
    });
    chatStore.merge([myMsg, replyToMe, dmMsg]);

    // When user is actively viewing c1 in a visible tab, c1 notification is suppressed,
    // while dm-1 (different conversation) fires with body preview hidden!
    const navigatedTo: string[] = [];
    setActiveNotificationContext("c1", (to) => navigatedTo.push(to));
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "visible",
    });

    notifySyncedMessages(
      [
        { conversationId: "c1", messageId: "reply-11" },
        { conversationId: "dm-1", messageId: "dm-msg-5" },
      ],
      chatStore.getSnapshot(),
      "human",
    );

    expect(createdNotifications).toHaveLength(1);
    expect(createdNotifications[0].title).toBe("苏晴");
    expect(createdNotifications[0].options?.body).toBe("发来一条新私信");
    expect(createdNotifications[0].options?.body).not.toContain("私聊秘密内容");

    // Enable body preview and hide tab -> c1 reply now fires and shows body preview
    await settle(() => {
      setNotificationPreferences({ showPreview: true }, "human");
    });
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "hidden",
    });

    notifySyncedMessages(
      [{ conversationId: "c1", messageId: "reply-11" }],
      chatStore.getSnapshot(),
      "human",
    );

    expect(createdNotifications).toHaveLength(2);
    expect(createdNotifications[1].title).toBe("陈工 · 研发讨论");
    expect(createdNotifications[1].options?.body).toContain(
      "收到，包含敏感客户报价 ￥999",
    );

    // Clicking the notification focuses window and navigates to ?message=
    const focusSpy = vi.spyOn(window, "focus").mockImplementation(() => {});
    createdNotifications[1].onclick?.();
    expect(focusSpy).toHaveBeenCalled();
    expect(createdNotifications[1].closed).toBe(true);
    expect(navigatedTo).toEqual(["/app/w/workspace/c/c1?message=reply-11"]);
  });
});

