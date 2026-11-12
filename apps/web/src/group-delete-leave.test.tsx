// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter, useLocation } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@teamie/api-client";
import type { Bootstrap, Conversation, Member, User, Workspace } from "@teamie/api-client";
import { Shell } from "./shell";
import { readPersistedShellNav, writePersistedShellNav } from "./shell/shell-hooks";
import { chatStore } from "./store";

const owner: User = {
  id: "owner",
  kind: "human",
  platformRole: "user",
  displayName: "空间创建者",
  email: "owner@example.test",
  createdAt: "2026-11-01T00:00:00Z",
};

const creatorMember: User = {
  id: "creator",
  kind: "human",
  platformRole: "user",
  displayName: "小组创建者",
  email: "creator@example.test",
  createdAt: "2026-11-01T00:00:00Z",
};

const ordinaryMember: User = {
  id: "member",
  kind: "human",
  platformRole: "user",
  displayName: "普通成员",
  email: "member@example.test",
  createdAt: "2026-11-01T00:00:00Z",
};

const workspace: Workspace = {
  id: "team",
  name: "研发空间",
  createdBy: owner.id,
  createdAt: owner.createdAt,
};

const persistedMembers: Member[] = [
  { userId: owner.id, displayName: owner.displayName, kind: "human", role: "owner" },
  { userId: creatorMember.id, displayName: creatorMember.displayName, kind: "human", role: "member" },
  { userId: ordinaryMember.id, displayName: ordinaryMember.displayName, kind: "human", role: "member" },
];

const channel: Conversation = {
  id: "general",
  workspaceId: workspace.id,
  type: "channel",
  name: "general",
  memberIds: [],
  lastSeq: 0,
  readSeq: 0,
  updatedAt: owner.createdAt,
};

const baseGroup: Conversation = {
  id: "group-1",
  workspaceId: workspace.id,
  type: "group",
  name: "冲刺讨论组",
  createdBy: creatorMember.id,
  memberIds: [owner.id, creatorMember.id, ordinaryMember.id],
  canManageMembers: true,
  lastSeq: 2,
  readSeq: 2,
  updatedAt: owner.createdAt,
};

let activeUser: User;
let bootstrap: Bootstrap;
const refresh = vi.fn(async () => {});
const sync = vi.fn();

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
    sendTyping: vi.fn(),
    typing: {},
  }),
}));

let container: HTMLDivElement;
let root: Root;
let client: QueryClient;

function Location() {
  return <output data-location>{useLocation().pathname}</output>;
}

async function settle(action?: () => void) {
  await act(async () => {
    action?.();
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  await act(async () => {
    await vi.dynamicImportSettled();
  });
}

async function mount(path = "/app/w/team/c/group-1") {
  vi.stubGlobal(
    "matchMedia",
    vi.fn((query: string) => ({
      matches: query.includes("1250"),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    })),
  );
  await settle(() =>
    root.render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={[path]}>
          <Shell />
          <Location />
        </MemoryRouter>
      </QueryClientProvider>,
    ),
  );
  await settle();
}

function find<T extends HTMLElement>(selector: string): T {
  const node = container.querySelector<T>(selector);
  if (!node) throw new Error(`Missing control: ${selector}`);
  return node;
}

function buttonByText(text: string, scope: ParentNode = document.body): HTMLButtonElement {
  const found = [...scope.querySelectorAll<HTMLButtonElement>("button")].find(
    (b) => b.textContent?.trim() === text,
  );
  if (!found) throw new Error(`Missing button: ${text}`);
  return found;
}

async function clickNode(node: HTMLElement) {
  await settle(() => {
    node.focus();
    node.click();
  });
}

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
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
  localStorage.clear();
  refresh.mockClear();
  sync.mockClear();
  activeUser = { ...owner };
  bootstrap = {
    user: activeUser,
    workspaces: [workspace],
    conversations: [channel, baseGroup],
    capabilities: {
      "im.dm.create": true,
      "im.group.create": true,
    },
    flagsRevision: "group-delete-leave-test",
    syncCursor: "0",
  };
  chatStore.begin(activeUser.id);
  chatStore.replaceConversations(bootstrap.conversations);
  vi.spyOn(api, "members").mockResolvedValue({ members: persistedMembers });
  vi.spyOn(api, "workspaceAgents").mockResolvedValue({ agents: [] });
  vi.spyOn(api, "messages").mockResolvedValue({ messages: [], hasMore: false });
  vi.spyOn(api, "attention").mockResolvedValue({ items: [], unreadCount: 0 });
  vi.spyOn(api, "announcements").mockResolvedValue({
    items: [],
    readSeq: 0,
    latestSeq: 0,
    hasMore: false,
  });
  vi.spyOn(api, "read").mockResolvedValue({ readSeq: 2 });
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
});

afterEach(async () => {
  await settle(() => root.unmount());
  client.clear();
  container.remove();
  chatStore.clear();
  localStorage.clear();
  vi.restoreAllMocks();
  delete (HTMLElement.prototype as Partial<HTMLElement>).scrollTo;
  vi.unstubAllGlobals();
});

describe("group dissolution and ordinary member self-leave", () => {
  it("dissolves a group from GroupMembersDialog after confirmation, prunes nav memory, and falls back to #general", async () => {
    const deleteSpy = vi.spyOn(api, "deleteConversation").mockResolvedValue(undefined);
    await mount("/app/w/team/c/group-1");
    expect(readPersistedShellNav(owner.id).lastConversationByWorkspace[workspace.id]).toBe("group-1");

    await clickNode(find('[aria-label="查看会话成员"]'));
    await clickNode(find(".members-pane .members-invite"));
    expect(find(".modal h2").textContent).toBe("小组成员");

    await clickNode(buttonByText("解散小组", find(".modal")));
    expect(find(".modal h2").textContent).toBe("解散小组「冲刺讨论组」？");
    expect(find(".confirm-impact").textContent).toContain("全部消息、上传文件、表情回应与置顶记录将被彻底清除");
    expect(deleteSpy).not.toHaveBeenCalled();

    await clickNode(buttonByText("确认解散", find(".modal")));
    expect(deleteSpy).toHaveBeenCalledWith("group-1");
    expect(find("[data-location]").textContent).toBe("/app/w/team/c/general");
    expect(chatStore.getSnapshot().conversations["group-1"]).toBeUndefined();

    const persisted = readPersistedShellNav(owner.id);
    expect(persisted.recentConversationIds).not.toContain("group-1");
    expect(persisted.lastConversationByWorkspace[workspace.id]).toBe("general");
    expect(refresh).toHaveBeenCalled();
  });

  it("dissolves a group from ShellMembersPane and surfaces API errors inline before retrying", async () => {
    const deleteSpy = vi
      .spyOn(api, "deleteConversation")
      .mockRejectedValueOnce(new ApiError(503, "unavailable", "Service Unavailable"))
      .mockResolvedValueOnce(undefined);

    await mount("/app/w/team/c/group-1");
    await clickNode(find('[aria-label="查看会话成员"]'));
    await clickNode(buttonByText("解散小组", find(".members-pane")));

    expect(find(".modal h2").textContent).toBe("解散小组「冲刺讨论组」？");
    await clickNode(buttonByText("确认解散", find(".modal")));
    expect(find(".modal .notice.error").textContent).toContain("服务暂时不可用");
    expect(find("[data-location]").textContent).toBe("/app/w/team/c/group-1");

    await clickNode(buttonByText("确认解散", find(".modal")));
    expect(deleteSpy).toHaveBeenCalledTimes(2);
    expect(find("[data-location]").textContent).toBe("/app/w/team/c/general");
  });

  it("allows an ordinary member to leave a group without showing management or dissolve actions", async () => {
    activeUser = { ...ordinaryMember };
    const memberGroup: Conversation = {
      ...baseGroup,
      canManageMembers: false,
    };
    bootstrap = {
      ...bootstrap,
      user: activeUser,
      conversations: [channel, memberGroup],
      capabilities: {
        "im.dm.create": true,
        "im.group.create": false,
      },
    };
    chatStore.begin(activeUser.id);
    chatStore.replaceConversations(bootstrap.conversations);

    const leaveSpy = vi.spyOn(api, "removeConversationMember").mockResolvedValue({
      conversation: {
        ...memberGroup,
        memberIds: [owner.id, creatorMember.id],
        canManageMembers: false,
      },
    });

    await mount("/app/w/team/c/group-1");
    await clickNode(find('[aria-label="查看会话成员"]'));
    const pane = find(".members-pane");
    expect(pane.querySelector(".members-invite")).toBeNull();
    expect([...pane.querySelectorAll("button")].some((b) => b.textContent?.trim() === "解散小组")).toBe(false);

    await clickNode(buttonByText("退出小组", pane));
    expect(find(".modal h2").textContent).toBe("退出小组「冲刺讨论组」？");
    await clickNode(buttonByText("确认退出", find(".modal")));

    expect(leaveSpy).toHaveBeenCalledWith("group-1", ordinaryMember.id);
    expect(find("[data-location]").textContent).toBe("/app/w/team/c/general");
    expect(chatStore.getSnapshot().conversations["group-1"]).toBeUndefined();
    expect(readPersistedShellNav(ordinaryMember.id).recentConversationIds).not.toContain("group-1");
  });

  it("warns non-owner creators when leaving and prunes persisted nav when a group disappears via sync", async () => {
    activeUser = { ...creatorMember };
    bootstrap = {
      ...bootstrap,
      user: activeUser,
      conversations: [channel, baseGroup],
    };
    chatStore.begin(activeUser.id);
    chatStore.replaceConversations(bootstrap.conversations);
    writePersistedShellNav(creatorMember.id, {
      lastChatPath: "/app/w/team/c/group-1",
      lastConversationByWorkspace: { [workspace.id]: "group-1" },
      recentConversationIds: ["group-1", "general"],
    });

    await mount("/app/w/team/c/group-1");
    await clickNode(find('[aria-label="查看会话成员"]'));
    await clickNode(buttonByText("退出小组", find(".members-pane")));
    expect(find(".modal").textContent).toContain("你是该小组创建者");
    await clickNode(buttonByText("取消", find(".modal")));

    // Navigate to #general, then simulate another manager dissolving group-1 via authoritative refresh.
    await clickNode(find('.members-pane header [aria-label="关闭成员列表"]'));
    await clickNode(find('.navigation-sidebar a[href="/app/w/team/c/general"]'));
    expect(readPersistedShellNav(creatorMember.id).recentConversationIds).toContain("group-1");

    await settle(() => {
      bootstrap.conversations = [channel];
      chatStore.replaceConversations([channel]);
    });

    const pruned = readPersistedShellNav(creatorMember.id);
    expect(pruned.recentConversationIds).toEqual(["general"]);
    expect(pruned.lastConversationByWorkspace[workspace.id]).toBe("general");
  });
});
