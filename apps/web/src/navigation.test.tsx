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
  User,
  Workspace,
} from "@teamie/api-client";
import { Landing, Login } from "./public";
import { Shell } from "./shell";
import { chatStore } from "./store";

const owner: User = {
  id: "owner",
  kind: "human",
  platformRole: "user",
  displayName: "创建者",
  email: "owner@example.test",
  createdAt: "2026-10-16T00:00:00Z",
};
const workspace: Workspace = {
  id: "team",
  name: "团队名称很长时也仍然是同一个工作空间",
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
  enabled: true,
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
  { userId: "human", displayName: "设计伙伴", kind: "human", role: "member" },
  {
    userId: agent.id,
    displayName: agent.name,
    kind: "agent",
    role: "member",
    ownerName: agent.ownerName,
  },
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
const group: Conversation = {
  ...channel,
  id: "group",
  type: "group",
  name: "可管理的小组",
  memberIds: [owner.id],
  canManageMembers: true,
};
const capabilities = {
  "agents.enabled": true,
  "agents.discover": true,
  "agents.create": true,
  "agents.update": true,
  "agents.delete": true,
  "im.dm.create": true,
  "im.group.create": true,
};
let activeUser: User;
let signedOut = false;
let authLoading = false;
let bootstrap: Bootstrap;
const refresh = vi.fn(async () => {});
const sendTyping = vi.fn();
const sync = vi.fn();
vi.mock("./auth", () => ({
  useAuth: () => ({
    user: signedOut ? null : activeUser,
    loading: authLoading,
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
}
async function mount(path = "/app/w/team/members", mobile = false) {
  vi.stubGlobal(
    "matchMedia",
    vi.fn((query: string) => ({
      matches: mobile || query.includes("1250"),
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
async function click(selector: string) {
  const node = find<HTMLElement>(selector);
  await settle(() => {
    node.focus();
    node.click();
  });
}
async function escape() {
  await settle(() =>
    document.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Escape",
        bubbles: true,
        cancelable: true,
      }),
    ),
  );
}
async function input(selector: string, value: string) {
  const node = find<HTMLInputElement>(selector);
  await settle(() => {
    Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      "value",
    )!.set!.call(node, value);
    node.dispatchEvent(new Event("input", { bubbles: true }));
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
  activeUser = { ...owner };
  signedOut = false;
  authLoading = false;
  bootstrap = {
    user: activeUser,
    workspaces: [workspace],
    conversations: [channel, group],
    capabilities: { ...capabilities },
    flagsRevision: "navigation-test",
    syncCursor: "0",
  };
  chatStore.begin(owner.id);
  chatStore.replaceConversations(bootstrap.conversations);
  vi.spyOn(api, "members").mockResolvedValue({ members: persistedMembers });
  vi.spyOn(api, "workspaceAgents").mockResolvedValue({ agents: [agent] });
  vi.spyOn(api, "agents").mockResolvedValue({ agents: [agent] });
  vi.spyOn(api, "agent").mockResolvedValue({ agent });
  vi.spyOn(api, "registrationInvites").mockResolvedValue({ invites: [] });
  vi.spyOn(api, "modelGateway");
  vi.spyOn(api, "messages").mockResolvedValue({ messages: [], hasMore: false });
  vi.spyOn(api, "attention").mockResolvedValue({ items: [], unreadCount: 0 });
  vi.spyOn(api, "read").mockResolvedValue({ readSeq: 0 });
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
  vi.restoreAllMocks();
  delete (HTMLElement.prototype as Partial<HTMLElement>).scrollTo;
  delete (HTMLElement.prototype as Partial<HTMLElement>).scrollIntoView;
  vi.unstubAllGlobals();
});

describe("member and account navigation", () => {
  it("keeps independent Agent discovery, edits and deletion usable with runtime admission disabled", async () => {
    bootstrap.capabilities["agents.enabled"] = false;
    vi.mocked(api.agent).mockResolvedValue({ agent: { ...agent, enabled: false } });
    await mount();
    expect(api.workspaceAgents).toHaveBeenCalledWith(workspace.id);
    expect(find('[aria-label="团队成员"]').textContent).toContain(agent.name);
    await click('[aria-label="管理 产品伙伴"]');
    await settle();
    expect(find(".agent-detail-actions").textContent).toContain("编辑资料");
    const enable = Array.from(find(".agent-detail-actions").querySelectorAll("button"))
      .find((button) => button.textContent?.trim() === "启用")!;
    expect(enable.disabled).toBe(true);
    expect(find(".agent-delete-section button")).toBeTruthy();
    await click(".agent-detail-actions button:first-child");
    expect(find<HTMLInputElement>('.agent-enabled-switch input').disabled).toBe(true);
    await input('.modal input[placeholder="例如：产品伙伴"]', "新的名字");
    const update = vi.spyOn(api, "updateAgent").mockResolvedValue({ agent: { ...agent, name: "新的名字", enabled: false } });
    await settle(() => find<HTMLFormElement>(".modal form").dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    expect(update).toHaveBeenCalledWith(agent.id, { name: "新的名字" });
  });

  it.each([1, 49])("creates a group with %s selected partners and prevents a 51st total member", async (count) => {
    const members: Member[] = [persistedMembers[0], ...Array.from({ length: 50 }, (_, index) => ({
      userId: `person-${index}`, displayName: `伙伴 ${index}`, kind: "human" as const, role: "member" as const,
    }))];
    vi.mocked(api.members).mockResolvedValue({ members });
    vi.mocked(api.workspaceAgents).mockResolvedValue({ agents: [] });
    const create = vi.spyOn(api, "createConversation").mockResolvedValue({ conversation: group });
    await mount("/app/w/team/c/general");
    await click(".conversation-actions-trigger");
    await click(".conversation-actions-menu button:last-child");
    for (let index = 0; index < count; index++) await click(`.member-option:nth-of-type(${index + 1})`);
    expect(find<HTMLButtonElement>('.modal button[type="submit"]').disabled).toBe(false);
    if (count === 49) {
      expect(find<HTMLButtonElement>('.member-option:nth-of-type(50)').disabled).toBe(true);
      await click('.member-option:nth-of-type(1)');
      expect(find<HTMLButtonElement>('.member-option:nth-of-type(50)').disabled).toBe(false);
      await click('.member-option:nth-of-type(1)');
    }
    await settle(() => find<HTMLFormElement>(".modal form").dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    expect(create).toHaveBeenCalledOnce();
    expect(create.mock.calls[0][1].type).toBe("group");
    expect([...(create.mock.calls[0][1].memberIds || [])].sort()).toEqual(members.slice(1, count + 1).map((member) => member.userId).sort());
  });
  it("places personal navigation in the rail and keeps member/admin management out of the sidebar", async () => {
    activeUser.platformRole = "admin";
    await mount();
    const navigation = find(".app-navigation");
    expect(
      navigation.querySelector('a[href="/app/w/team/members"]'),
    ).toBeNull();
    const rail = find(".workspace-rail");
    expect(find(".rail-agents").nextElementSibling).toBe(find(".account-panel"));
    expect(rail.querySelector('a[href="/app/agents"]')).toBeTruthy();
    expect(rail.querySelector(".account-trigger")).toBeTruthy();
    expect(find(".navigation-sidebar").querySelector(".account-panel")).toBeNull();
    expect(find(".workspace-title").tagName).toBe("DIV");
    expect(navigation.querySelector('a[href^="/app/admin"]')).toBeNull();
    await click(".account-trigger");
    expect(find(".account-menu").textContent).not.toContain("个人 Agent");
    expect(find(".account-menu").textContent).toContain("账号设置");
    expect(find(".account-menu").textContent).not.toContain("平台管理");
    await click('.account-menu a[href="/app/settings"]');
    expect(find('a[href="/app/admin"]').textContent).toContain("平台管理");
    expect(api.modelGateway).not.toHaveBeenCalled();
    expect(api.registrationInvites).not.toHaveBeenCalled();
  });

  it("hides platform management from ordinary accounts, including direct admin routes", async () => {
    await mount("/app/settings");
    expect(container.querySelector('a[href^="/app/admin"]')).toBeNull();
    await settle(() => root.unmount());
    root = createRoot(container);
    await mount("/app/admin/gateway");
    expect(container.textContent).toContain("需要平台管理员权限");
    expect(api.modelGateway).not.toHaveBeenCalled();
  });

  it("dismisses the mobile account menu with Escape/outside click and closes the drawer on navigation", async () => {
    await mount("/app/w/team/members", true);
    await click('[aria-label="打开频道列表"]');
    await click(".account-trigger");
    expect(document.activeElement).toBe(find(".account-menu a"));
    await escape();
    expect(container.querySelector(".account-menu")).toBeNull();
    expect(find(".app-shell").classList.contains("sidebar-open")).toBe(true);
    expect(document.activeElement).toBe(find(".account-trigger"));
    await click(".account-trigger");
    await settle(() =>
      find(".workspace-header").dispatchEvent(
        new MouseEvent("pointerdown", { bubbles: true }),
      ),
    );
    expect(container.querySelector(".account-menu")).toBeNull();
    expect(document.activeElement).toBe(find(".account-trigger"));
    await click(".account-trigger");
    await click('.account-menu a[href="/app/settings"]');
    expect(find("[data-location]").textContent).toBe("/app/settings");
    expect(container.querySelector(".account-menu")).toBeNull();
    expect(find(".app-shell").classList.contains("sidebar-open")).toBe(false);
    expect(document.activeElement).toBe(find('[aria-label="打开频道列表"]'));
    await click('[aria-label="打开频道列表"]');
    expect(container.querySelector(".account-menu")).toBeNull();
  });

  it("keeps the personal Agent rail link usable with write flags disabled and closes mobile navigation", async () => {
    for (const capability of ["agents.enabled", "agents.create", "agents.update", "agents.delete", "agents.discover"]) {
      bootstrap.capabilities[capability] = false;
    }
    await mount("/app/w/team/c/general", true);
    await click('[aria-label="打开对话列表"]');
    await click(".rail-agents");
    await settle();
    expect(find("[data-location]").textContent).toBe("/app/agents");
    expect(find(".rail-agents").getAttribute("aria-current")).toBe("page");
    expect(find(".app-shell").classList.contains("sidebar-open")).toBe(false);
    expect(document.activeElement).toBe(find('[aria-label="打开频道列表"]'));
    expect(find(".agent-card").textContent).toContain(agent.name);
    await click(`a.agent-card[href="/app/agents/${agent.id}"]`);
    await settle();
    expect(find(".rail-agents").getAttribute("aria-current")).toBe("page");
    expect(find(".agent-detail-heading").textContent).toContain(agent.name);
  });

  it.each([
    [true, true, ["发起私聊", "创建小组"]],
    [true, false, ["发起私聊"]],
    [false, true, ["创建小组"]],
  ] as const)(
    "offers only allowed conversation actions (direct=%s, group=%s)",
    async (canDirect, canGroup, labels) => {
      bootstrap.capabilities["im.dm.create"] = canDirect;
      bootstrap.capabilities["im.group.create"] = canGroup;
      await mount("/app/w/team/c/general");
      await click(".conversation-actions-trigger");
      const menu = find(".conversation-actions-menu");
      expect(Array.from(menu.querySelectorAll("button"), (node) => node.textContent)).toEqual(labels);
      expect(document.activeElement).toBe(menu.querySelector("button"));
      expect(find(".conversation-actions-trigger").getAttribute("aria-controls")).toBe(menu.id);
      expect(find(".conversation-actions-trigger").getAttribute("aria-expanded")).toBe("true");
      expect(find('a[href="/app/w/team/c/general"]').getAttribute("aria-current")).toBe("page");
      await escape();
      expect(container.querySelector(".conversation-actions-menu")).toBeNull();
      expect(document.activeElement).toBe(find(".conversation-actions-trigger"));
    },
  );

  it.each([false, true])(
    "dismisses conversation actions on Escape, outside click and focus leaving (mobile=%s)",
    async (mobile) => {
      await mount("/app/w/team/c/general", mobile);
      if (mobile) await click('[aria-label="打开对话列表"]');
      await click(".conversation-actions-trigger");
      await escape();
      expect(container.querySelector(".conversation-actions-menu")).toBeNull();
      expect(find(".app-shell").classList.contains("sidebar-open")).toBe(mobile);
      expect(document.activeElement).toBe(find(".conversation-actions-trigger"));
      await click(".conversation-actions-trigger");
      await settle(() =>
        find(".workspace-title").dispatchEvent(new MouseEvent("pointerdown", { bubbles: true })),
      );
      expect(container.querySelector(".conversation-actions-menu")).toBeNull();
      expect(document.activeElement).toBe(find(".conversation-actions-trigger"));
      await click(".conversation-actions-trigger");
      await settle(() => find(".rail-agents").focus());
      expect(container.querySelector(".conversation-actions-menu")).toBeNull();
      if (mobile) {
        await escape();
        expect(find(".app-shell").classList.contains("sidebar-open")).toBe(false);
        expect(document.activeElement).toBe(find('[aria-label="打开对话列表"]'));
      }
    },
  );

  it.each([
    [false, "direct"],
    [false, "group"],
    [true, "direct"],
    [true, "group"],
  ] as const)(
    "opens the selected dialog and restores focus after closing (mobile=%s, action=%s)",
    async (mobile, kind) => {
      await mount("/app/w/team/c/general", mobile);
      if (mobile) await click('[aria-label="打开对话列表"]');
      await click(".conversation-actions-trigger");
      await click(`.conversation-actions-menu button:${kind === "direct" ? "first" : "last"}-child`);
      expect(container.querySelector(".conversation-actions-menu")).toBeNull();
      expect(find(".app-shell").classList.contains("sidebar-open")).toBe(false);
      const modal = find(".modal");
      expect(modal.querySelector("h2")?.textContent).toBe(kind === "direct" ? "发起私信" : "创建小组");
      expect(modal.contains(document.activeElement)).toBe(true);
      await click('[aria-label="关闭弹窗"]');
      expect(container.querySelector(".modal")).toBeNull();
      expect(document.activeElement).toBe(find(mobile ? '[aria-label="打开对话列表"]' : ".conversation-actions-trigger"));
    },
  );

  it("hides the new-conversation trigger when creation is off while preserving conversations", async () => {
    bootstrap.capabilities["im.dm.create"] = false;
    bootstrap.capabilities["im.group.create"] = false;
    await mount("/app/w/team/c/general");
    expect(container.querySelector(".conversation-actions-trigger")).toBeNull();
    expect(find('a[href="/app/w/team/c/general"]').textContent).toContain("general");
    expect(find('a[href="/app/w/team/c/group"]').textContent).toContain(group.name);
  });

  it("hides workspace conversation actions without a current workspace", async () => {
    await mount("/app/w/private/members");
    expect(container.querySelector(".conversation-actions-trigger")).toBeNull();
    expect(find(".workspace-title").textContent).toBe("我的 Teamie");
  });

  it("shows people and Agents together, searches the creator and preserves owner-only management", async () => {
    const otherAgent = {
      ...agent,
      id: "other-agent",
      principalId: "other-agent",
      name: "其他 Agent",
      ownerId: "other",
      ownerName: "另一位创建者",
    };
    vi.mocked(api.workspaceAgents).mockResolvedValue({
      agents: [agent, otherAgent],
    });
    await mount();
    expect(find('[aria-label="团队成员"]').querySelectorAll("li")).toHaveLength(
      4,
    );
    expect(find('[aria-label="管理 产品伙伴"]')).toBeTruthy();
    expect(
      container.querySelector('[aria-label="管理 其他 Agent"]'),
    ).toBeNull();
    await input('input[type="search"]', "另一位创建者");
    expect(find('[aria-label="团队成员"]').querySelectorAll("li")).toHaveLength(
      1,
    );
    expect(find('[aria-label="团队成员"]').textContent).toContain("其他 Agent");
    await input('input[type="search"]', "不存在的成员");
    expect(container.textContent).toContain("没有找到成员");
    activeUser = { ...owner, id: "platform-admin", platformRole: "admin" };
    await settle(() => root.unmount());
    root = createRoot(container);
    await mount();
    expect(container.querySelector(".member-manage-link")).toBeNull();
  });

  it("keeps persisted Agent members and conversations visible when discovery is disabled, ignoring cached discovery", async () => {
    bootstrap.capabilities["agents.discover"] = false;
    client.setQueryData(["workspaceAgents", owner.id, workspace.id], {
      agents: [
        agent,
        {
          ...agent,
          id: "cached",
          principalId: "cached",
          name: "缓存的新 Agent",
        },
      ],
    });
    chatStore.addConversation({
      ...channel,
      id: "existing-dm",
      name: "",
      type: "direct",
      memberIds: [owner.id, agent.id],
    });
    await mount();
    expect(api.workspaceAgents).not.toHaveBeenCalled();
    expect(find('[aria-label="团队成员"]').textContent).toContain(agent.name);
    expect(find('[aria-label="团队成员"]').textContent).toContain("Agent");
    expect(container.textContent).not.toContain("缓存的新 Agent");
    expect(
      find<HTMLButtonElement>('[aria-label="与 产品伙伴 发起私聊"]').disabled,
    ).toBe(true);
    expect(
      find<HTMLButtonElement>('[aria-label="将 产品伙伴 添加到小组"]').disabled,
    ).toBe(true);
    expect(find('a[href="/app/w/team/c/existing-dm"]').textContent).toContain(
      agent.name,
    );
    expect(
      find<HTMLButtonElement>('[aria-label="与 设计伙伴 发起私聊"]').disabled,
    ).toBe(false);
  });

  it.each([false, true])(
    "uses the public IM direct endpoint for human/Agent participants (Agent=%s)",
    async (isAgent) => {
      const id = isAgent ? agent.id : "human";
      const name = isAgent ? agent.name : "设计伙伴";
      const create = vi.spyOn(api, "createConversation").mockResolvedValue({
        conversation: {
          ...channel,
          id: `dm-${id}`,
          type: "direct",
          memberIds: [owner.id, id],
        },
      });
      await mount();
      await click(`[aria-label="与 ${name} 发起私聊"]`);
      expect(create).toHaveBeenCalledWith(workspace.id, {
        type: "direct",
        memberIds: [id],
      });
      expect(find("[data-location]").textContent).toBe(
        `/app/w/team/c/dm-${id}`,
      );
    },
  );

  it.each([false, true])(
    "adds human/Agent participants only to groups the caller manages (Agent=%s)",
    async (isAgent) => {
      const id = isAgent ? agent.id : "human";
      const name = isAgent ? agent.name : "设计伙伴";
      chatStore.addConversation({
        ...group,
        id: "unmanaged",
        name: "不能管理的小组",
        canManageMembers: false,
      });
      chatStore.addConversation({
        ...group,
        id: "private-team",
        workspaceId: "private",
        name: "其他团队的小组",
      });
      const add = vi.spyOn(api, "addConversationMember").mockResolvedValue({
        conversation: { ...group, memberIds: [owner.id, id] },
      });
      await mount();
      await click(`[aria-label="将 ${name} 添加到小组"]`);
      const modal = find('[role="dialog"]');
      expect(modal.textContent).toContain("可管理的小组");
      expect(modal.textContent).not.toContain("不能管理的小组");
      expect(modal.textContent).not.toContain("其他团队的小组");
      await click(".agent-group-option");
      await click(".modal-actions .button:not(.secondary)");
      expect(add).toHaveBeenCalledWith(group.id, id);
      expect(container.querySelector('[role="dialog"]')).toBeNull();
    },
  );

  it("preselects a discovered Agent in the shared create-group form and honors disabled Agents", async () => {
    const disabled = {
      ...agent,
      id: "disabled",
      principalId: "disabled",
      name: "停用伙伴",
      enabled: false,
    };
    vi.mocked(api.workspaceAgents).mockResolvedValue({
      agents: [agent, disabled],
    });
    const create = vi.spyOn(api, "createConversation").mockResolvedValue({
      conversation: {
        ...group,
        id: "new-group",
        memberIds: [owner.id, agent.id],
      },
    });
    await mount();
    expect(
      find<HTMLButtonElement>('[aria-label="与 停用伙伴 发起私聊"]').disabled,
    ).toBe(true);
    await click('[aria-label="将 产品伙伴 添加到小组"]');
    await click(".create-agent-group-link");
    expect(find(".member-option.selected").textContent).toContain(agent.name);
    await input('input[name="name"]', "新小组");
    await click(".modal-actions .button:not(.secondary)");
    expect(create).toHaveBeenCalledWith(workspace.id, {
      type: "group",
      name: "新小组",
      memberIds: [agent.id],
    });
  });

  it("keeps only basic owner management and does not fetch or subscribe to internal activity", async () => {
    const fetch = vi.fn();
    const events = vi.fn();
    vi.stubGlobal("fetch", fetch);
    vi.stubGlobal("EventSource", events);
    const unsafeAgent = {
      ...agent,
      runtimeState: "running",
      sessionId: "SESSION-SENTINEL",
      lastActiveAt: "ACTIVITY-SENTINEL",
      lastError: "ERROR-SENTINEL",
      revision: 999,
      runs: [{ id: "RUN-ID-SENTINEL", inputCount: 999 }],
      tasks: [
        { id: "TASK-ID-SENTINEL", description: "TASK-DESCRIPTION-SENTINEL" },
      ],
    };
    vi.mocked(api.agent).mockResolvedValue({ agent: unsafeAgent });
    await mount(`/app/agents/${agent.id}`);
    expect(container.textContent).toContain("编辑资料");
    expect(container.textContent).toContain("已启用");
    expect(container.textContent).toContain("团队可见范围");
    expect(container.innerHTML).not.toMatch(
      /SENTINEL|运行与任务|最近活跃|Session|模型|在线/,
    );
    expect(fetch).not.toHaveBeenCalled();
    expect(events).not.toHaveBeenCalled();
  });

  it("shows only configured enablement on personal cards, including unexpected internal response fields", async () => {
    const fetch = vi.fn();
    const events = vi.fn();
    vi.stubGlobal("fetch", fetch);
    vi.stubGlobal("EventSource", events);
    vi.mocked(api.agents).mockResolvedValue({
      agents: [
        {
          ...agent,
          ...{
            sessionId: "SESSION-SENTINEL",
            runtimeState: "running",
            lastError: "ERROR-SENTINEL",
            lastActiveAt: "ACTIVITY-SENTINEL",
            tasks: [{ description: "TASK-DESCRIPTION-SENTINEL" }],
          },
        },
        {
          ...agent,
          id: "disabled",
          principalId: "disabled",
          name: "已停用的伙伴",
          enabled: false,
        },
      ],
    });
    await mount("/app/agents");
    expect(container.querySelectorAll(".agent-card")).toHaveLength(2);
    expect(container.textContent).toContain("已启用");
    expect(container.textContent).toContain("已停用");
    expect(container.innerHTML).not.toMatch(
      /SENTINEL|运行与任务|最近活跃|尚未处理|Session|模型|在线/,
    );
    expect(fetch).not.toHaveBeenCalled();
    expect(events).not.toHaveBeenCalled();
  });

  it("keeps owner enablement, profile edits and deletion usable without activity APIs", async () => {
    const disabled = { ...agent, enabled: false };
    const update = vi
      .spyOn(api, "updateAgent")
      .mockResolvedValue({ agent: disabled });
    const remove = vi.spyOn(api, "deleteAgent").mockResolvedValue(undefined);
    await mount(`/app/agents/${agent.id}`);
    vi.mocked(api.agent).mockResolvedValue({ agent: disabled });
    await click(".agent-detail-actions .button:last-child");
    expect(update).toHaveBeenLastCalledWith(agent.id, { enabled: false });
    expect(find(".agent-detail-labels").textContent).toContain("已停用");
    await click(".agent-detail-actions .button:first-child");
    await input('.modal input[maxlength="80"]', "新的伙伴名称");
    const renamed = { ...disabled, name: "新的伙伴名称" };
    update.mockResolvedValue({ agent: renamed });
    vi.mocked(api.agent).mockResolvedValue({ agent: renamed });
    await click('.modal-actions .button[type="submit"]');
    expect(update).toHaveBeenLastCalledWith(agent.id, { name: "新的伙伴名称" });
    expect(find(".agent-detail-heading h1").textContent).toBe("新的伙伴名称");
    await click(".agent-delete-section .button");
    vi.mocked(api.agents).mockResolvedValue({ agents: [] });
    await click(".modal-actions .button.danger");
    await settle();
    expect(remove).toHaveBeenCalledWith(agent.id);
    expect(find("[data-location]").textContent).toBe("/app/agents");
    expect(container.textContent).toContain("还没有 Agent");
  });

  it("keeps IM Agent identity without showing gateway or runtime configuration hints", async () => {
    const events = vi.fn();
    vi.stubGlobal("EventSource", events);
    vi.mocked(api.workspaceAgents).mockResolvedValue({
      agents: [
        {
          ...agent,
          ...{
            runtimeState: "waiting_configuration",
            sessionId: "SESSION-SENTINEL",
            lastError: "GATEWAY-ERROR-SENTINEL",
          },
        },
      ],
    });
    chatStore.addConversation({
      ...channel,
      id: "agent-dm",
      name: "",
      type: "direct",
      memberIds: [owner.id, agent.id],
    });
    await mount("/app/w/team/c/agent-dm");
    expect(find(".conversation-header").textContent).toContain(agent.name);
    expect(
      find('a[href="/app/w/team/c/agent-dm"] .agent-badge').textContent,
    ).toContain("Agent");
    expect(container.innerHTML).not.toMatch(
      /SENTINEL|等待模型配置|模型网关|Session/,
    );
    expect(events).not.toHaveBeenCalled();
  });

  it("does not open private workspace directories or let a platform admin manage another owner's Agent", async () => {
    activeUser = { ...owner, id: "platform-admin", platformRole: "admin" };
    await mount("/app/w/private/members");
    expect(container.textContent).toContain("无法访问这个工作空间");
    expect(api.members).not.toHaveBeenCalled();
    expect(api.workspaceAgents).not.toHaveBeenCalled();
    await settle(() => root.unmount());
    root = createRoot(container);
    await mount(`/app/agents/${agent.id}`);
    expect(container.textContent).toContain("请使用所有者账户访问");
    expect(container.textContent).not.toContain("编辑资料");
  });

  it("preserves directory content when conversation creation flags are off", async () => {
    bootstrap.capabilities["im.dm.create"] = false;
    bootstrap.capabilities["im.group.create"] = false;
    await mount();
    expect(find('[aria-label="团队成员"]').textContent).toContain("产品伙伴");
    expect(find('[aria-label="团队成员"]').textContent).toContain("设计伙伴");
    expect(
      container.querySelector('[aria-label="与 产品伙伴 发起私聊"]'),
    ).toBeNull();
    expect(
      container.querySelector('[aria-label="将 产品伙伴 添加到小组"]'),
    ).toBeNull();
  });

  it("closes a pending add-to-group dialog when an Agent is no longer a visible member", async () => {
    const add = vi.spyOn(api, "addConversationMember");
    await mount();
    await click('[aria-label="将 产品伙伴 添加到小组"]');
    await click(".agent-group-option");
    await settle(() => {
      client.setQueryData(["members", owner.id, workspace.id], {
        members: persistedMembers.filter((member) => member.kind === "human"),
      });
      client.setQueryData(["workspaceAgents", owner.id, workspace.id], {
        agents: [],
      });
    });
    expect(container.querySelector('[role="dialog"]')).toBeNull();
    expect(add).not.toHaveBeenCalled();
    expect(find('[aria-label="团队成员"]').textContent).not.toContain(
      agent.name,
    );
  });

  it("redirects the old Agent directory to the unified member directory", async () => {
    await mount("/app/w/team/agents");
    expect(find("[data-location]").textContent).toBe("/app/w/team/members");
    expect(find('[aria-label="团队成员"]').textContent).toContain("设计伙伴");
  });

  it("remembers the last visited conversation across management pages and workspace switches", async () => {
    const secondWorkspace: Workspace = {
      id: "design",
      name: "设计空间",
      createdBy: owner.id,
      createdAt: owner.createdAt,
    };
    const emptyWorkspace: Workspace = {
      id: "empty",
      name: "空工作空间",
      createdBy: owner.id,
      createdAt: owner.createdAt,
    };
    const designChannel: Conversation = {
      ...channel,
      id: "design-general",
      workspaceId: secondWorkspace.id,
      name: "design-general",
    };
    bootstrap.workspaces = [workspace, secondWorkspace, emptyWorkspace];
    bootstrap.conversations = [channel, group, designChannel];
    chatStore.replaceConversations(bootstrap.conversations);
    await mount("/app/w/team/c/group");
    await click(".rail-agents");
    expect(find("[data-location]").textContent).toBe("/app/agents");
    expect(find(".settings-header a").getAttribute("href")).toBe(
      "/app/w/team/c/group",
    );
    await click(".settings-header a");
    expect(find("[data-location]").textContent).toBe("/app/w/team/c/group");
    await click(".account-trigger");
    await click('.account-menu a[href="/app/settings"]');
    expect(find("[data-location]").textContent).toBe("/app/settings");
    await click(".settings-header a");
    expect(find("[data-location]").textContent).toBe("/app/w/team/c/group");
    await click(`[aria-label="打开工作空间 ${secondWorkspace.name}"]`);
    expect(find("[data-location]").textContent).toBe(
      "/app/w/design/c/design-general",
    );
    await click(`[aria-label="打开工作空间 ${workspace.name}"]`);
    expect(find("[data-location]").textContent).toBe("/app/w/team/c/group");
    await click(`[aria-label="打开工作空间 ${emptyWorkspace.name}"]`);
    expect(find("[data-location]").textContent).toBe("/app/w/empty/members");
  });

  it("opens the member directory and starts direct messages from the conversation members pane", async () => {
    const disabledAgent: Agent = {
      ...agent,
      id: "disabled-agent",
      principalId: "disabled-agent",
      name: "停用伙伴",
      enabled: false,
    };
    vi.mocked(api.workspaceAgents).mockResolvedValue({
      agents: [agent, disabledAgent],
    });
    const create = vi.spyOn(api, "createConversation").mockResolvedValue({
      conversation: {
        ...channel,
        id: "dm-agent",
        type: "direct",
        memberIds: [owner.id, agent.id],
      },
    });
    await mount("/app/w/team/c/general");
    await click('[aria-label="查看会话成员"]');
    const pane = find(".members-pane");
    expect(pane.querySelector(".members-directory-link")?.getAttribute("href")).toBe(
      "/app/w/team/members",
    );
    expect(
      pane.querySelector('[aria-label="与 停用伙伴 发起私聊"]'),
    ).toBeNull();
    await click('.members-pane [aria-label="与 产品伙伴 发起私聊"]');
    expect(create).toHaveBeenCalledWith(workspace.id, {
      type: "direct",
      memberIds: [agent.id],
    });
    expect(find("[data-location]").textContent).toBe("/app/w/team/c/dm-agent");
    expect(container.querySelector(".members-pane")).toBeNull();
    await click('[aria-label="查看会话成员"]');
    await click(".members-directory-link");
    expect(find("[data-location]").textContent).toBe("/app/w/team/members");
    expect(container.querySelector(".members-pane")).toBeNull();
  });

  it("navigates to the workspace member directory from interactive chips in AgentDetail", async () => {
    await mount(`/app/agents/${agent.id}`);
    const chip = find(
      `.agent-workspace-chips a[aria-label="查看 ${workspace.name} 成员目录"]`,
    );
    expect(chip.getAttribute("href")).toBe(`/app/w/${workspace.id}/members`);
    await click(
      `.agent-workspace-chips a[aria-label="查看 ${workspace.name} 成员目录"]`,
    );
    expect(find("[data-location]").textContent).toBe(
      `/app/w/${workspace.id}/members`,
    );
  });

  it("redirects unknown app routes and bare workspace URLs to a real view", async () => {
    await mount("/app/w/team");
    expect(find("[data-location]").textContent).toBe("/app/w/team/c/general");
    await settle(() => root.unmount());
    root = createRoot(container);
    await mount("/app/does-not-exist");
    expect(find("[data-location]").textContent).toBe("/app/w/team/c/general");
  });

  it("explains an expired session once on the login page", async () => {
    signedOut = true;
    sessionStorage.setItem("teamie.sessionExpired", "1");
    await settle(() =>
      root.render(
        <QueryClientProvider client={client}>
          <MemoryRouter initialEntries={["/login"]}>
            <Login />
          </MemoryRouter>
        </QueryClientProvider>,
      ),
    );
    await settle();
    expect(container.textContent).toContain("登录已失效，请重新登录。");
    expect(sessionStorage.getItem("teamie.sessionExpired")).toBeNull();
  });

  it("clears a message deep link in place without remounting the conversation view", async () => {
    const target = {
      id: "m1", conversationId: channel.id, senderId: "human", senderName: "设计伙伴",
      senderKind: "human" as const, clientMessageId: "c1", seq: 1, body: "旧消息",
      createdAt: owner.createdAt,
    };
    vi.spyOn(api, "message").mockResolvedValue({ message: target });
    vi.spyOn(api, "messagesAround").mockResolvedValue({ messages: [target], hasMore: false });
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: vi.fn() });
    await mount("/app/w/team/c/general?message=m1");
    const composer = find<HTMLTextAreaElement>('textarea[aria-label="消息内容"]');
    expect(find(".located-banner").textContent).toContain("消息上下文");
    await click(".located-banner button");
    expect(container.querySelector(".located-banner")).toBeNull();
    expect(find<HTMLTextAreaElement>('textarea[aria-label="消息内容"]')).toBe(composer);
  });

  it("redirects signed-in users from the landing page directly into /app and keeps rail-home inside /app", async () => {
    const emptyFirst: Workspace = {
      id: "empty-first",
      name: "空置首发空间",
      createdBy: owner.id,
      createdAt: owner.createdAt,
    };
    bootstrap.workspaces = [workspace, emptyFirst];
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
          <MemoryRouter initialEntries={["/"]}>
            <Routes>
              <Route path="/" element={<Landing />} />
              <Route path="/app/*" element={<Shell />} />
            </Routes>
            <Location />
          </MemoryRouter>
        </QueryClientProvider>,
      ),
    );
    await settle();
    expect(find("[data-location]").textContent).toBe("/app/w/team/c/general");
    await click('a[href="/app/w/team/c/group"]');
    expect(find("[data-location]").textContent).toBe("/app/w/team/c/group");
    bootstrap.workspaces = [emptyFirst, workspace];
    await click(".rail-agents");
    expect(find("[data-location]").textContent).toBe("/app/agents");
    expect(find(".rail-home").getAttribute("href")).toBe("/app");
    await click(".rail-home");
    expect(find("[data-location]").textContent).toBe("/app/w/team/c/group");
  });

  it("renders a sticky scroll-locked product story without top anchor links for signed-out visitors", async () => {
    signedOut = true;
    const scrollTo = vi.fn();
    vi.stubGlobal("scrollTo", scrollTo);
    vi.stubGlobal(
      "matchMedia",
      vi.fn((query: string) => ({
        matches: query.includes("prefers-reduced-motion"),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      })),
    );
    await settle(() =>
      root.render(
        <MemoryRouter initialEntries={["/"]}>
          <Landing />
          <Location />
        </MemoryRouter>,
      ),
    );
    await settle();
    expect(find("[data-location]").textContent).toBe("/");
    expect(container.querySelector('.public-header a[href^="#"]')).toBeNull();
    const track = find(".scroll-story-track");
    vi.spyOn(track, "getBoundingClientRect").mockReturnValue({
      top: 0,
      bottom: 2400,
      left: 0,
      right: 1200,
      width: 1200,
      height: 2400,
      x: 0,
      y: 0,
      toJSON: () => ({}),
    });
    expect(track.getAttribute("data-active-scene")).toBe("0");
    expect(find(".story-scene-item.active").textContent).toContain(
      "对的讨论，在对的地方。",
    );
    const controls = find(".story-controls");
    expect(controls.getAttribute("role")).toBe("group");
    const tabs = container.querySelectorAll<HTMLButtonElement>(".story-step-pill");
    expect(tabs).toHaveLength(4);
    expect(tabs[0]?.getAttribute("aria-pressed")).toBe("true");
    expect(tabs[1]?.getAttribute("aria-pressed")).toBe("false");
    const articles =
      container.querySelectorAll<HTMLElement>(".story-scene-item");
    expect(articles[0]?.getAttribute("aria-hidden")).toBe("false");
    expect(articles[1]?.getAttribute("aria-hidden")).toBe("true");
    expect(find(".story-scroll-hint").textContent).toContain(
      "继续滑动切换下一幕",
    );
    await settle(() => tabs[1]?.click());
    expect(track.getAttribute("data-active-scene")).toBe("1");
    expect(articles[0]?.getAttribute("aria-hidden")).toBe("true");
    expect(articles[1]?.getAttribute("aria-hidden")).toBe("false");
    expect(tabs[1]?.getAttribute("aria-pressed")).toBe("true");
    expect(scrollTo).toHaveBeenLastCalledWith(
      expect.objectContaining({ behavior: "auto" }),
    );
    expect(find(".story-scene-item.active").textContent).toContain(
      "每位成员，都有专属伙伴。",
    );
    await settle(() => tabs[3]?.click());
    expect(track.getAttribute("data-active-scene")).toBe("3");
    expect(tabs[3]?.getAttribute("aria-pressed")).toBe("true");
    expect(find(".story-scene-item.active").textContent).toContain(
      "像邀请一位新队友，安静可靠。",
    );
    expect(container.textContent).toContain("下一件好事，一起开始。");
  });

  it("keeps the public landing usable while session discovery is pending, disables scroll auto-switching on mobile, and resets scroll on register navigation", async () => {
    signedOut = true;
    authLoading = true;
    vi.spyOn(api, "registration").mockResolvedValue({
      mode: "invite",
      bootstrapRequired: false,
    });
    const scrollTo = vi.fn();
    vi.stubGlobal("scrollTo", scrollTo);
    vi.stubGlobal("innerWidth", 375);
    vi.stubGlobal("innerHeight", 568);
    vi.stubGlobal(
      "matchMedia",
      vi.fn((query: string) => ({
        matches: query.includes("780px") || query.includes("700px"),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      })),
    );
    await settle(() =>
      root.render(
        <QueryClientProvider client={client}>
          <MemoryRouter initialEntries={["/"]}>
            <Routes>
              <Route path="/" element={<Landing />} />
              <Route path="/login" element={<Login />} />
            </Routes>
            <Location />
          </MemoryRouter>
        </QueryClientProvider>,
      ),
    );
    await settle();
    expect(find("[data-location]").textContent).toBe("/");
    expect(find(".story-scroll-hint").textContent).toContain(
      "点击上方标签切换下一幕",
    );
    expect(find(".landing-register").getAttribute("href")).toBe(
      "/login?mode=register",
    );
    const track = find(".scroll-story-track");
    vi.spyOn(track, "getBoundingClientRect").mockReturnValue({
      top: -180,
      bottom: 720,
      left: 0,
      right: 375,
      width: 375,
      height: 900,
      x: 0,
      y: -180,
      toJSON: () => ({}),
    });
    await settle(() => window.dispatchEvent(new Event("scroll")));
    expect(track.getAttribute("data-active-scene")).toBe("0");
    scrollTo.mockClear();
    const pills =
      container.querySelectorAll<HTMLButtonElement>(".story-step-pill");
    await settle(() => pills[1]?.click());
    expect(track.getAttribute("data-active-scene")).toBe("1");
    expect(scrollTo).not.toHaveBeenCalled();
    document.documentElement.scrollTop = 343;
    document.body.scrollTop = 343;
    await click(".landing-register");
    expect(find("[data-location]").textContent).toBe("/login");
    expect(document.documentElement.scrollTop).toBe(0);
    expect(document.body.scrollTop).toBe(0);
    expect(scrollTo).toHaveBeenCalledWith({
      top: 0,
      left: 0,
      behavior: "auto",
    });
  });
});
