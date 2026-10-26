// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type {
  Agent,
  Bootstrap,
  Conversation,
  Member,
  Message,
  User,
  Workspace,
} from "@teamie/api-client";
import { Shell, parseInviteCode } from "./shell";
import { chatStore } from "./store";

const owner: User = {
  id: "owner",
  kind: "human",
  platformRole: "user",
  displayName: "创建者",
  email: "owner@example.test",
  createdAt: "2026-10-16T00:00:00Z",
};
const regularMember: User = {
  ...owner,
  id: "member",
  displayName: "普通成员",
  email: "member@example.test",
};
const workspace: Workspace = {
  id: "team",
  name: "产品研发",
  createdBy: owner.id,
  createdAt: owner.createdAt,
};
const secondWorkspace: Workspace = {
  id: "design",
  name: "设计中心",
  createdBy: owner.id,
  createdAt: owner.createdAt,
};
const agent: Agent = {
  id: "agent",
  principalId: "agent",
  name: "产品伙伴",
  ownerId: owner.id,
  ownerName: owner.displayName,
  description: "整理想法",
  workspaceIds: [workspace.id],
  enabled: false,
  createdAt: owner.createdAt,
  updatedAt: owner.createdAt,
};
const persistedMembers: Member[] = [
  {
    userId: owner.id,
    displayName: owner.displayName,
    kind: "human",
    role: "owner",
  },
  {
    userId: regularMember.id,
    displayName: regularMember.displayName,
    kind: "human",
    role: "member",
  },
];
const channel: Conversation = {
  id: "general",
  workspaceId: workspace.id,
  type: "channel",
  name: "general",
  memberIds: [owner.id, regularMember.id],
  lastSeq: 0,
  readSeq: 0,
  updatedAt: "2026-10-16T00:00:00Z",
};
const dmOne: Conversation = {
  id: "dm-1",
  workspaceId: workspace.id,
  type: "direct",
  name: "私聊甲",
  memberIds: [owner.id, regularMember.id],
  lastSeq: 0,
  readSeq: 0,
  updatedAt: "2026-10-16T01:00:00Z",
};
const dmTwo: Conversation = {
  id: "dm-2",
  workspaceId: workspace.id,
  type: "direct",
  name: "私聊乙",
  memberIds: [owner.id, regularMember.id],
  lastSeq: 0,
  readSeq: 0,
  updatedAt: "2026-10-16T02:00:00Z",
};
let activeUser: User;
let bootstrap: Bootstrap;
const refresh = vi.fn(async () => {});
const sync = vi.fn();
const sendTyping = vi.fn();
const emptyTyping = {};

vi.mock("./auth", () => ({
  useAuth: () => ({
    user: activeUser,
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
    refresh,
    sync,
    sendTyping,
    typing: emptyTyping,
  }),
}));

let container: HTMLDivElement;
let root: Root;

function LocationProbe() {
  const location = useLocation();
  return <output data-testid="path">{location.pathname}</output>;
}

async function flush() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

async function mount(initialEntry: string) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={[initialEntry]}>
          <Routes>
            <Route path="/join/:code" element={<LocationProbe />} />
            <Route
              path="*"
              element={
                <>
                  <Shell />
                  <LocationProbe />
                </>
              }
            />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );
  });
  await flush();
}

beforeEach(() => {
  (
    globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
  ).IS_REACT_ACT_ENVIRONMENT = true;
  Object.defineProperty(window, "matchMedia", {
    writable: true,
    value: (query: string) => ({
      matches: false,
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
    }),
  });
  Object.defineProperty(globalThis, "ResizeObserver", {
    writable: true,
    value: class {
      observe() {}
      disconnect() {}
    },
  });
  Element.prototype.scrollIntoView = vi.fn();
  Element.prototype.scrollTo = vi.fn();
  sessionStorage.clear();
  localStorage.clear();
  activeUser = owner;
  bootstrap = {
    user: activeUser,
    workspaces: [workspace, secondWorkspace],
    conversations: [channel, dmOne, dmTwo],
    capabilities: {
      "agents.enabled": true,
      "agents.discover": true,
      "agents.create": true,
      "agents.update": true,
      "agents.delete": true,
      "im.dm.create": true,
      "im.group.create": true,
      "im.channel.create": true,
      "im.message.edit": true,
      "im.message.retract": true,
    },
    flagsRevision: "r1",
    syncCursor: "0",
  };
  chatStore.begin(owner.id);
  chatStore.replaceConversations(bootstrap.conversations);
  vi.spyOn(api, "members").mockResolvedValue({ members: persistedMembers });
  vi.spyOn(api, "workspaceAgents").mockResolvedValue({ agents: [] });
  vi.spyOn(api, "attention").mockResolvedValue({
    items: [],
    unreadCount: 0,
  });
  vi.spyOn(api, "messages").mockResolvedValue({ messages: [], hasMore: false });
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  chatStore.clear();
  vi.restoreAllMocks();
});

describe("PR-3 navigation & notification ergonomics", () => {
  it("renders workspace unread dot and updates document.title for channel vs urgent unreads", async () => {
    chatStore.replaceConversations([
      { ...channel, lastSeq: 3, readSeq: 1 },
      dmOne,
      {
        ...channel,
        id: "design-general",
        workspaceId: secondWorkspace.id,
        lastSeq: 2,
        readSeq: 0,
      },
    ]);
    await mount("/app/w/team/c/general");

    const wsButtons = container.querySelectorAll(".workspace-button");
    expect(wsButtons.length).toBe(2);
    expect(wsButtons[0]?.getAttribute("aria-label")).toBe(
      "打开工作空间 产品研发（有未读消息）",
    );
    expect(wsButtons[1]?.getAttribute("aria-label")).toBe(
      "打开工作空间 设计中心（有未读消息）",
    );
    expect(
      wsButtons[0]?.querySelector('.workspace-unread-dot[aria-hidden="true"]'),
    ).toBeTruthy();
    expect(
      wsButtons[1]?.querySelector('.workspace-unread-dot[aria-hidden="true"]'),
    ).toBeTruthy();
    expect(document.title).toBe("• # general · Teamie");

    act(() => {
      chatStore.replaceConversations([
        { ...channel, lastSeq: 3, readSeq: 1 },
        { ...dmOne, lastSeq: 4, readSeq: 1 },
      ]);
    });
    await flush();
    expect(document.title).toBe("(3) # general · Teamie");

    vi.spyOn(api, "attention").mockResolvedValue({
      items: [
        {
          message: {
            id: "chan-mention",
            clientMessageId: "c-chan",
            conversationId: channel.id,
            body: "@创建者 看看这个",
            mentions: [{ userId: owner.id, start: 0, end: 4 }],
            senderId: regularMember.id,
            senderName: regularMember.displayName,
            senderKind: "human",
            seq: 3,
            createdAt: "2026-10-16T03:00:00Z",
          },
          conversation: {
            id: channel.id,
            workspaceId: workspace.id,
            name: channel.name,
            type: "channel",
          },
          workspaceName: workspace.name,
          reasons: ["mention"],
          isRead: false,
        },
        {
          message: {
            id: "dm-mention",
            clientMessageId: "c-dm",
            conversationId: dmOne.id,
            body: "@创建者 私聊提醒",
            mentions: [{ userId: owner.id, start: 0, end: 4 }],
            senderId: regularMember.id,
            senderName: regularMember.displayName,
            senderKind: "human",
            seq: 4,
            createdAt: "2026-10-16T03:01:00Z",
          },
          conversation: {
            id: dmOne.id,
            workspaceId: workspace.id,
            name: dmOne.name,
            type: "direct",
          },
          workspaceName: workspace.name,
          reasons: ["mention"],
          isRead: false,
        },
      ],
      unreadCount: 2,
      channelUnreadCount: 1,
    });
    act(() => {
      chatStore.replaceConversations([
        { ...channel, lastSeq: 3, readSeq: 1 },
        { ...dmOne, lastSeq: 4, readSeq: 3 },
      ]);
    });
    await flush();
    await flush();
    // 1 unread DM (which is also an attention item) + 1 unread channel mention = (2), not (3)
    expect(document.title).toBe("(2) # general · Teamie");
  });

  it("re-sorts DM list immediately when a newer message arrives via store.apply", async () => {
    await mount("/app/w/team/c/general");
    const getDmLabels = () =>
      Array.from(
        container.querySelectorAll(
          ".nav-section:nth-of-type(2) .conversation-link > span:not(.avatar):not(.unread-badge)",
        ),
      ).map((el) => el.textContent);
    expect(getDmLabels()).toEqual(["私聊乙", "私聊甲"]);

    const incoming: Message = {
      id: "m-new",
      clientMessageId: "c-new",
      conversationId: dmOne.id,
      body: "新消息来了",
      senderId: regularMember.id,
      senderName: regularMember.displayName,
      senderKind: "human",
      seq: 1,
      createdAt: "2026-10-16T05:00:00Z",
    };
    act(() => {
      chatStore.apply({
        events: [
          {
            id: "ev-11",
            type: "message.created",
            workspaceId: workspace.id,
            conversationId: dmOne.id,
            message: incoming,
            createdAt: incoming.createdAt,
          },
        ],
        cursor: "11",
        hasMore: false,
      });
    });
    await flush();
    expect(getDmLabels()).toEqual(["私聊甲", "私聊乙"]);
  });

  it("shows invite button only to workspace owner in empty channel and members directory", async () => {
    await mount("/app/w/team/c/general");
    expect(container.querySelector(".conversation-start")?.textContent).toContain(
      "邀请成员加入",
    );

    await act(async () => root.unmount());
    root = createRoot(container);
    await mount("/app/w/team/members");
    expect(container.querySelector(".members-heading")?.textContent).toContain(
      "邀请成员",
    );

    await act(async () => root.unmount());
    root = createRoot(container);
    activeUser = regularMember;
    chatStore.begin(regularMember.id);
    chatStore.replaceConversations(bootstrap.conversations);
    await mount("/app/w/team/c/general");
    expect(container.querySelector(".conversation-start")?.textContent).toContain(
      "请联系空间创建者邀请",
    );
    expect(container.querySelector(".conversation-start")?.textContent).not.toContain(
      "邀请成员加入",
    );
  });

  it("parses invite links/codes on Welcome screen and navigates to /join/:code", async () => {
    expect(parseInviteCode("https://teamie.example/join/team-code-123")).toBe(
      "team-code-123",
    );
    expect(parseInviteCode("  raw-code-456 ")).toBe("raw-code-456");

    bootstrap = {
      ...bootstrap,
      workspaces: [],
      conversations: [],
    };
    chatStore.replaceConversations([]);
    await mount("/app");

    const input = container.querySelector<HTMLInputElement>(
      ".welcome-join-form input",
    );
    expect(input).toBeTruthy();
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(
        HTMLInputElement.prototype,
        "value",
      )?.set;
      setter?.call(input, "https://teamie.example/join/welcome-789");
      input!.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const form = container.querySelector<HTMLFormElement>(".welcome-join-form");
    await act(async () => {
      form!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    expect(container.querySelector('[data-testid="path"]')?.textContent).toBe(
      "/join/welcome-789",
    );
  });

  it("explains why Agent enable control is disabled when agents.enabled capability is off", async () => {
    bootstrap = {
      ...bootstrap,
      capabilities: {
        ...bootstrap.capabilities,
        "agents.enabled": false,
      },
    };
    vi.spyOn(api, "agent").mockResolvedValue({ agent });
    await mount("/app/agents/agent");

    const reason = container.querySelector("#agent-detail-enable-disabled-reason");
    expect(reason?.textContent).toBe("平台暂未开放启用 Agent");
    const enableBtn = Array.from(container.querySelectorAll("button")).find((b) =>
      b.textContent?.includes("启用"),
    );
    expect(enableBtn?.disabled).toBe(true);
    expect(enableBtn?.getAttribute("aria-describedby")).toBe(
      "agent-detail-enable-disabled-reason",
    );
  });

  it("supports Cmd/Ctrl+K quick switcher across workspaces and Alt+Up/Down conversation switching", async () => {
    const crossDm: Conversation = {
      id: "design-dm",
      workspaceId: secondWorkspace.id,
      type: "direct",
      name: "",
      memberIds: [owner.id, "designer"],
      lastSeq: 0,
      readSeq: 0,
      updatedAt: "2026-10-16T04:00:00Z",
    };
    chatStore.replaceConversations([channel, dmOne, dmTwo, crossDm]);
    let resolveSecondMembers!: (value: { members: Member[] }) => void;
    const secondMembersPromise = new Promise<{ members: Member[] }>((r) => {
      resolveSecondMembers = r;
    });
    vi.spyOn(api, "members").mockImplementation((wsId) =>
      wsId === secondWorkspace.id
        ? secondMembersPromise
        : Promise.resolve({ members: persistedMembers }),
    );
    const scrollIntoView = vi.fn();
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
      configurable: true,
      value: scrollIntoView,
    });
    vi.spyOn(HTMLElement.prototype, "getClientRects").mockReturnValue(
      [{ width: 10, height: 10 }] as unknown as DOMRectList,
    );
    await mount("/app/w/team/c/general");

    await act(async () => {
      window.dispatchEvent(
        new KeyboardEvent("keydown", { key: "k", ctrlKey: true, bubbles: true }),
      );
    });
    const searchInput = document.querySelector<HTMLInputElement>(
      'input[aria-label="快速跳转搜索"]',
    );
    expect(searchInput).toBeTruthy();
    expect(document.activeElement).toBe(searchInput);
    expect(searchInput?.getAttribute("aria-activedescendant")).toBe(
      "quick-switcher-opt-conv:general",
    );

    scrollIntoView.mockClear();
    await act(async () => {
      searchInput!.dispatchEvent(
        new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }),
      );
    });
    expect(scrollIntoView).toHaveBeenCalled();
    expect(searchInput?.getAttribute("aria-activedescendant")).toBe(
      "quick-switcher-opt-conv:dm-2",
    );

    await act(async () => {
      resolveSecondMembers({
        members: [
          ...persistedMembers,
          {
            userId: "designer",
            displayName: "设计师阿青",
            role: "member",
            kind: "human",
          },
        ],
      });
    });
    await flush();
    const options = Array.from(
      document.querySelectorAll(".quick-switcher-option"),
    );
    expect(options.length).toBeGreaterThanOrEqual(5);
    expect(options.some((el) => el.textContent?.includes("设计师阿青"))).toBe(
      true,
    );
    await act(async () => {
      searchInput!.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Enter", bubbles: true }),
      );
    });
    expect(container.querySelector('[data-testid="path"]')?.textContent).toBe(
      "/app/w/team/c/dm-2",
    );

    await act(async () => {
      window.dispatchEvent(
        new KeyboardEvent("keydown", {
          key: "ArrowDown",
          altKey: true,
          bubbles: true,
        }),
      );
    });
    expect(container.querySelector('[data-testid="path"]')?.textContent).toBe(
      "/app/w/team/c/dm-1",
    );

    act(() => {
      chatStore.replaceConversations([
        channel,
        dmOne,
        dmTwo,
        { ...crossDm, lastSeq: 5, readSeq: 2 },
      ]);
    });
    await flush();
    await act(async () => {
      window.dispatchEvent(
        new KeyboardEvent("keydown", {
          key: "ArrowDown",
          altKey: true,
          shiftKey: true,
          bubbles: true,
        }),
      );
    });
    expect(container.querySelector('[data-testid="path"]')?.textContent).toBe(
      "/app/w/design/c/design-dm",
    );
  });

  it("persists composer drafts synchronously in sessionStorage, retains drafts across stale localStorage restore, and clears them on logout", async () => {
    await mount("/app/w/team/c/general");
    const textarea = container.querySelector<HTMLTextAreaElement>(
      'textarea[aria-label="消息内容"]',
    );
    expect(textarea).toBeTruthy();
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(
        HTMLTextAreaElement.prototype,
        "value",
      )?.set;
      setter?.call(textarea, "第一段");
      textarea!.dispatchEvent(new Event("input", { bubbles: true }));
      setter?.call(textarea, "未发送的草稿内容");
      textarea!.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(sessionStorage.getItem("teamie.drafts.v1:owner")).toContain(
      "未发送的草稿内容",
    );

    // Simulate reload where sessionStorage has a draft for a newer conversation (dm-2)
    // not yet present in a stale non-empty localStorage snapshot
    act(() => {
      chatStore.clear();
    });
    sessionStorage.setItem(
      "teamie.drafts.v1:owner",
      JSON.stringify({
        general: { body: "未发送的草稿内容", mentions: [], replyToId: "" },
        "dm-2": { body: "新会话草稿", mentions: [], replyToId: "" },
        "revoked-conv": { body: "已移除会话草稿", mentions: [], replyToId: "" },
      }),
    );
    localStorage.setItem(
      "teamie.sync.v1.owner",
      JSON.stringify({
        userId: owner.id,
        cursor: "1",
        revision: 1,
        messages: {},
        conversations: { general: channel },
      }),
    );
    act(() => {
      chatStore.begin(owner.id);
    });
    expect(Object.keys(chatStore.getSnapshot().conversations)).toEqual([
      "general",
    ]);
    // Before replaceConversations runs, dm-2 draft must NOT be pruned by stale localStorage
    expect(sessionStorage.getItem("teamie.drafts.v1:owner")).toContain(
      "新会话草稿",
    );
    act(() => {
      chatStore.replaceConversations([channel, dmOne, dmTwo]);
    });
    expect(sessionStorage.getItem("teamie.drafts.v1:owner")).toContain(
      "新会话草稿",
    );
    expect(sessionStorage.getItem("teamie.drafts.v1:owner")).not.toContain(
      "已移除会话草稿",
    );

    // Sync cursor recovery (revoke) must preserve drafts for the same user
    act(() => {
      chatStore.revoke();
    });
    expect(sessionStorage.getItem("teamie.drafts.v1:owner")).toContain(
      "新会话草稿",
    );

    act(() => {
      chatStore.clear();
    });
    expect(sessionStorage.getItem("teamie.drafts.v1:owner")).toBeNull();
  });

  it("implements roving tabindex and keyboard navigation on message list", async () => {
    const firstMsg: Message = {
      id: "m-1",
      clientMessageId: "c-1",
      conversationId: channel.id,
      body: "第一条消息",
      senderId: regularMember.id,
      senderName: regularMember.displayName,
      senderKind: "human",
      seq: 1,
      createdAt: "2026-10-16T01:00:00Z",
    };
    const secondMsg: Message = {
      id: "m-2",
      clientMessageId: "c-2",
      conversationId: channel.id,
      body: "第二条消息",
      senderId: owner.id,
      senderName: owner.displayName,
      senderKind: "human",
      seq: 2,
      createdAt: "2026-10-16T01:01:00Z",
    };
    vi.spyOn(api, "messages").mockResolvedValue({
      messages: [firstMsg, secondMsg],
      hasMore: false,
    });
    await mount("/app/w/team/c/general");

    const row1 = container.querySelector<HTMLElement>("#message-m-1")!;
    const row2 = container.querySelector<HTMLElement>("#message-m-2")!;
    expect(row1.tabIndex).toBe(-1);
    expect(row2.tabIndex).toBe(0);
    expect(
      row1.querySelector<HTMLButtonElement>(".message-more-button")?.tabIndex,
    ).toBe(-1);
    expect(
      row2.querySelector<HTMLButtonElement>(".message-more-button")?.tabIndex,
    ).toBe(0);

    await act(async () => {
      row2.focus();
      row2.dispatchEvent(
        new KeyboardEvent("keydown", { key: "ArrowUp", bubbles: true }),
      );
    });
    expect(document.activeElement).toBe(row1);
    expect(row1.tabIndex).toBe(0);
    expect(row2.tabIndex).toBe(-1);

    await act(async () => {
      row1.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Enter", bubbles: true }),
      );
    });
    expect(row1.classList.contains("actions-open")).toBe(true);

    await act(async () => {
      row1.dispatchEvent(
        new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }),
      );
    });
    expect(document.activeElement).toBe(row2);
    expect(row1.classList.contains("actions-open")).toBe(false);

    await act(async () => {
      row2.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
      );
    });
    expect(row2.classList.contains("actions-open")).toBe(false);
    expect(document.activeElement).toBe(
      container.querySelector('textarea[aria-label="消息内容"]'),
    );
  });

  it("sorts fractional-second updatedAt timestamps, uses channelUnreadCount, scopes QuickSwitcher queries, and restores focus to account trigger after closing shortcuts", async () => {
    const dmWholeSecond: Conversation = {
      ...dmOne,
      updatedAt: "2026-10-16T01:00:00Z",
    };
    const dmSubSecond: Conversation = {
      ...dmTwo,
      updatedAt: "2026-10-16T01:00:00.123456Z",
    };
    const secondChannel: Conversation = {
      ...channel,
      id: "design-general",
      workspaceId: secondWorkspace.id,
      name: "design",
    };
    const agentDm: Conversation = {
      id: "dm-agent",
      workspaceId: workspace.id,
      type: "direct",
      name: "",
      memberIds: [owner.id, agent.principalId],
      lastSeq: 0,
      readSeq: 0,
      updatedAt: "2026-10-16T00:30:00Z",
    };
    chatStore.replaceConversations([
      channel,
      dmWholeSecond,
      dmSubSecond,
      agentDm,
      secondChannel,
    ]);
    vi.spyOn(api, "workspaceAgents").mockResolvedValue({ agents: [agent] });
    vi.spyOn(api, "attention").mockResolvedValue({
      items: [
        {
          message: {
            id: "dm-mention",
            clientMessageId: "c-dm",
            conversationId: dmOne.id,
            body: "@创建者 私聊提醒",
            mentions: [{ userId: owner.id, start: 0, end: 4 }],
            senderId: regularMember.id,
            senderName: regularMember.displayName,
            senderKind: "human",
            seq: 4,
            createdAt: "2026-10-16T03:01:00Z",
          },
          conversation: {
            id: dmOne.id,
            workspaceId: workspace.id,
            name: dmOne.name,
            type: "direct",
          },
          workspaceName: workspace.name,
          reasons: ["mention"],
          isRead: false,
        },
      ],
      unreadCount: 60,
      channelUnreadCount: 2,
    });
    const membersSpy = vi
      .spyOn(api, "members")
      .mockResolvedValue({ members: persistedMembers });

    await mount("/app/w/team/c/general");

    // 2 unread channel mentions from channelUnreadCount + 0 unread DMs = (2)
    expect(document.title).toBe("(2) # general · Teamie");

    // dmSubSecond (.123456Z) must sort ahead of dmWholeSecond (Z)
    const dmLabels = Array.from(
      container.querySelectorAll(
        ".nav-section:nth-of-type(2) .conversation-link > span:not(.avatar):not(.unread-badge):not(.agent-badge)",
      ),
    ).map((el) => el.textContent);
    expect(dmLabels).toEqual(["私聊乙", "私聊甲", "产品伙伴"]);

    // Open QuickSwitcher: secondWorkspace has only channels so api.members("design") must not be called,
    // while unnamed Agent DM ("产品伙伴") is preserved in QuickSwitcher options
    await act(async () => {
      window.dispatchEvent(
        new KeyboardEvent("keydown", { key: "k", ctrlKey: true, bubbles: true }),
      );
    });
    await flush();
    expect(
      membersSpy.mock.calls.some((call) => call[0] === secondWorkspace.id),
    ).toBe(false);
    const switcherTexts = Array.from(
      document.querySelectorAll(".quick-switcher-option"),
    ).map((el) => el.textContent);
    expect(switcherTexts.some((t) => t?.includes("产品伙伴"))).toBe(true);

    // Close QuickSwitcher
    await act(async () => {
      document.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
      );
    });
    await flush();

    // Open AccountMenu -> click 键盘快捷键 -> close modal -> focus returns to .account-trigger
    const accountTrigger =
      container.querySelector<HTMLButtonElement>(".account-trigger")!;
    await act(async () => {
      accountTrigger.focus();
      accountTrigger.click();
    });
    const shortcutsItem = Array.from(
      container.querySelectorAll<HTMLButtonElement>(".account-menu button"),
    ).find((btn) => btn.textContent?.includes("键盘快捷键"))!;
    await act(async () => {
      shortcutsItem.focus();
      shortcutsItem.click();
    });
    await flush();
    expect(document.querySelector(".shortcuts-list")).toBeTruthy();

    await act(async () => {
      document.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
      );
    });
    await flush();
    expect(document.activeElement).toBe(accountTrigger);
  });
});
