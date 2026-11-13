// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter, useLocation } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type {
  Agent,
  Bootstrap,
  Conversation,
  MarketplaceApp,
  Member,
  User,
  Workspace,
} from "@teamie/api-client";
import type { Connection } from "./chat";
import { Shell } from "./shell";
import { chatStore } from "./store";
import { resolveCurrentWorkspace } from "./shell/current-workspace";

const owner: User = {
  id: "owner",
  kind: "human",
  platformRole: "user",
  displayName: "创建者",
  email: "owner@example.test",
  createdAt: "2026-10-16T00:00:00Z",
};
const team: Workspace = { id: "team", name: "团队", createdBy: owner.id, createdAt: owner.createdAt };
const beta: Workspace = { id: "beta", name: "北极星", createdBy: owner.id, createdAt: owner.createdAt };
const conv = (id: string, workspaceId: string, extra: Partial<Conversation> = {}): Conversation => ({
  id,
  workspaceId,
  type: "channel",
  name: id,
  memberIds: [],
  lastSeq: 0,
  readSeq: 0,
  updatedAt: owner.createdAt,
  ...extra,
});
const enabledAgent: Agent = {
  id: "agent-on",
  principalId: "agent-on",
  name: "值班助手",
  ownerId: owner.id,
  ownerName: owner.displayName,
  description: "",
  workspaceIds: [team.id],
  enabled: true,
  createdAt: owner.createdAt,
  updatedAt: owner.createdAt,
};
const disabledAgent: Agent = { ...enabledAgent, id: "agent-off", principalId: "agent-off", name: "休眠助手", enabled: false };
const members: Member[] = [
  { userId: owner.id, displayName: owner.displayName, kind: "human", role: "owner" },
  { userId: "human", displayName: "设计伙伴", kind: "human", role: "member" },
  { userId: enabledAgent.principalId, displayName: enabledAgent.name, kind: "agent", role: "member" },
  { userId: disabledAgent.principalId, displayName: disabledAgent.name, kind: "agent", role: "member" },
  { userId: "agent-hidden", displayName: "隐身助手", kind: "agent", role: "member" },
];
const installedApp: MarketplaceApp = {
  id: "app-1",
  slug: "board-room",
  name: "协作棋盘",
  description: "多人与 Agent 同台互动",
  webUrl: "https://board.example.test/ui",
  mcpUrl: "https://board.example.test/mcp",
  enabled: true,
  createdAt: owner.createdAt,
  updatedAt: owner.createdAt,
};
const catalogApp: MarketplaceApp = { ...installedApp, id: "app-2", slug: "poll", name: "投票" };

let activeUser: User;
let bootstrap: Bootstrap;
let connection: Connection = "connected";
vi.mock("./auth", () => ({
  useAuth: () => ({ user: activeUser, loading: false, logout: vi.fn(), updateUser: vi.fn() }),
}));
vi.mock("./chat", async (original) => ({
  ...(await original<typeof import("./chat")>()),
  useChat: () => ({
    data: bootstrap,
    loading: false,
    error: "",
    connection,
    syncError: "",
    refresh: vi.fn(async () => {}),
    sync: vi.fn(),
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
  await act(async () => { await vi.dynamicImportSettled(); });
}
async function mount(path: string, mobile = false) {
  vi.stubGlobal("matchMedia", vi.fn((query: string) => ({
    matches: mobile || query.includes("1250"),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  })));
  await settle(() => root.render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Shell />
        <Location />
      </MemoryRouter>
    </QueryClientProvider>,
  ));
  await settle();
}
async function remount(path: string, mobile = false) {
  await settle(() => root.unmount());
  root = createRoot(container);
  await mount(path, mobile);
}
function find<T extends HTMLElement>(selector: string): T {
  const node = container.querySelector<T>(selector);
  if (!node) throw new Error(`Missing control: ${selector}`);
  return node;
}
async function click(selector: string) {
  const node = find(selector);
  await settle(() => { node.focus(); node.click(); });
}
const location = () => find("[data-location]").textContent;
const buttonsByText = (text: string) =>
  [...container.querySelectorAll("button")].filter((b) => b.textContent?.trim() === text);

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  Object.defineProperty(HTMLElement.prototype, "scrollTo", { configurable: true, value: vi.fn() });
  localStorage.clear();
  connection = "connected";
  activeUser = { ...owner };
  bootstrap = {
    user: activeUser,
    workspaces: [team, beta],
    conversations: [conv("general", team.id), conv("beta-general", beta.id), conv("beta-ops", beta.id)],
    capabilities: { "agents.discover": true, "im.dm.create": true, "im.group.create": true, "im.marketplace.enabled": true },
    flagsRevision: "ia-test",
    syncCursor: "0",
  };
  chatStore.begin(owner.id);
  chatStore.replaceConversations(bootstrap.conversations);
  vi.spyOn(api, "members").mockResolvedValue({ members });
  vi.spyOn(api, "workspaceAgents").mockResolvedValue({ agents: [enabledAgent, disabledAgent] });
  vi.spyOn(api, "agents").mockResolvedValue({ agents: [enabledAgent] });
  vi.spyOn(api, "agent").mockResolvedValue({ agent: enabledAgent });
  vi.spyOn(api, "messages").mockResolvedValue({ messages: [], hasMore: false });
  vi.spyOn(api, "attention").mockResolvedValue({ items: [], unreadCount: 0 });
  vi.spyOn(api, "announcements").mockResolvedValue({ items: [], readSeq: 0, latestSeq: 0, hasMore: false });
  vi.spyOn(api, "read").mockResolvedValue({ readSeq: 0 });
  vi.spyOn(api, "workspaceApps").mockResolvedValue({ apps: [installedApp] });
  vi.spyOn(api, "marketplaceCatalog").mockResolvedValue({ apps: [installedApp, catalogApp] });
  vi.spyOn(api, "developerMarketplaceApps").mockResolvedValue({ apps: [] });
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
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

describe("current workspace context", () => {
  it("resolves the route workspace, then the last chat, remembered conversations and the first workspace", () => {
    const workspaces = [team, beta];
    const base = { workspaces, lastChatPath: "", lastConversationByWorkspace: {} };
    expect(resolveCurrentWorkspace({ ...base, routeWorkspaceId: "beta" })).toBe(beta);
    expect(resolveCurrentWorkspace({ ...base, routeWorkspaceId: "gone" })).toBeUndefined();
    expect(resolveCurrentWorkspace({ ...base, lastChatPath: "/app/w/beta/c/beta-ops" })).toBe(beta);
    expect(resolveCurrentWorkspace({ ...base, lastChatPath: "/app/w/left/c/x", lastConversationByWorkspace: { left: "x", beta: "beta-ops" } })).toBe(beta);
    expect(resolveCurrentWorkspace(base)).toBe(team);
  });

  it("keeps the last conversation's workspace for the sidebar, rail, Cmd+K search and Alt+↓ on non-chat pages", async () => {
    await mount("/app/w/beta/c/beta-ops");
    await click('.account-trigger');
    await click('.account-menu a[href="/app/settings"]');
    expect(location()).toBe("/app/settings");
    expect(find(".workspace-title").textContent).toBe(beta.name);
    await click('.rail-attention');
    expect(find(".workspace-button.selected").getAttribute("aria-label")).toContain(beta.name);
    await click('[aria-label="搜索与快速切换"]');
    await click(".quick-switcher-modes button:last-child");
    expect(document.querySelector<HTMLInputElement>(".quick-switcher input")?.placeholder).toContain(beta.name);
    await settle(() => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    await settle(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", altKey: true, bubbles: true })));
    expect(location()).toMatch(/^\/app\/w\/beta\/c\//);
  });
});

describe("page headers and mobile navigation", () => {
  it.each([
    "/app/w/team/c/general",
    "/app/attention",
    "/app/settings",
    "/app/w/team/apps",
    "/app/w/team/c/missing",
    "/app/nope",
  ])("offers one Menu 「打开导航」 control on %s", async (path) => {
    await mount(path, true);
    const buttons = container.querySelectorAll('main [aria-label="打开导航"]');
    expect(buttons).toHaveLength(1);
    expect(buttons[0].querySelector(".lucide-menu")).not.toBeNull();
  });

  it("uses the same control on the welcome view", async () => {
    bootstrap.workspaces = [];
    chatStore.replaceConversations([]);
    await mount("/app", true);
    expect(container.querySelectorAll('main [aria-label="打开导航"]')).toHaveLength(1);
  });

  it("renders one page title and accessible admin tabs only for administrators", async () => {
    activeUser = { ...owner, platformRole: "admin" };
    vi.spyOn(api, "platformUsers").mockResolvedValue({ users: [] });
    await mount("/app/admin/users");
    const headings = container.querySelectorAll("h1");
    expect(headings).toHaveLength(1);
    expect(headings[0].textContent).toBe("用户管理");
    const tabs = find('nav[aria-label="平台管理"]');
    expect(tabs.querySelector('[aria-current="page"]')?.textContent).toContain("用户管理");
    expect(tabs.querySelectorAll("[aria-current]")).toHaveLength(1);
    activeUser = { ...owner };
    await remount("/app/admin/users");
    expect(container.querySelector('nav[aria-label="平台管理"]')).toBeNull();
  });

  it("names Agent detail pages in the document title", async () => {
    await mount(`/app/agents/${enabledAgent.id}`);
    expect(document.title).toBe(`${enabledAgent.name} · 个人 Agent · Teamie`);
    await click('.account-trigger');
    await click('.account-menu a[href="/app/settings"]');
    expect(document.title).toBe("账号设置 · Teamie");
  });
});

describe("workspace apps page", () => {
  it("is reached from the sidebar and shows installed apps read-only to members", async () => {
    activeUser = { ...owner, id: "human", displayName: "设计伙伴" };
    await mount("/app/w/team/c/general");
    await click('.sidebar-page-link[href="/app/w/team/apps"]');
    expect(location()).toBe("/app/w/team/apps");
    expect(find(".sidebar-page-link").getAttribute("aria-current")).toBe("page");
    expect(find('nav.page-tabs a[aria-current="page"]').textContent).toContain("应用");
    expect(document.title).toBe(`${team.name} · 应用 · Teamie`);
    expect(find('[aria-label="已安装的应用"]').textContent).toContain(installedApp.name);
    expect(api.marketplaceCatalog).not.toHaveBeenCalled();
    expect(buttonsByText("安装到工作空间")).toHaveLength(0);
    expect(buttonsByText("卸载应用")).toHaveLength(0);
    expect(container.textContent).toContain("仅工作空间创建者可以安装或卸载应用");
  });

  it("lets the owner install catalog apps and keeps installed apps read-only when the flag is off", async () => {
    await mount("/app/w/team/apps");
    expect(find('[aria-label="工作空间可选应用"]').textContent).toContain(catalogApp.name);
    expect(find('[aria-label="工作空间可选应用"]').textContent).not.toContain(installedApp.name);
    expect(buttonsByText("安装到工作空间")).toHaveLength(1);
    expect(buttonsByText("卸载应用")).toHaveLength(1);
    bootstrap.capabilities["im.marketplace.enabled"] = false;
    await remount("/app/w/team/apps");
    expect(find('[aria-label="已安装的应用"]').textContent).toContain(installedApp.name);
    expect(container.textContent).toContain("应用市场已由平台关闭");
    expect(buttonsByText("安装到工作空间")).toHaveLength(0);
    expect(buttonsByText("卸载应用")).toHaveLength(0);
    expect(find('.sidebar-page-link[href="/app/w/team/apps"]')).toBeTruthy();
  });

  it("replaces the member-directory header modal with page tabs", async () => {
    await mount("/app/w/team/members");
    expect(container.textContent).not.toContain("工作区应用");
    expect(container.textContent).not.toContain("工作空间应用");
    await click('nav.page-tabs a[href="/app/w/team/apps"]');
    expect(location()).toBe("/app/w/team/apps");
    expect(container.querySelector(".modal")).toBeNull();
  });
});

describe("Agent availability", () => {
  it("labels Agents 可用/已停用 from discovery and omits a label when absent", async () => {
    await mount("/app/w/team/members");
    const rows = [...container.querySelectorAll<HTMLElement>(".member-directory-row")];
    const row = (name: string) => rows.find((r) => r.textContent?.includes(name))!;
    expect(row(enabledAgent.name).querySelector(".agent-availability")?.textContent).toBe("可用");
    expect(row(disabledAgent.name).querySelector(".agent-availability")?.textContent).toBe("已停用");
    expect(row("隐身助手").querySelector(".agent-availability")).toBeNull();
    expect(row("设计伙伴").querySelector(".agent-availability")).toBeNull();
    expect(container.textContent).not.toMatch(/在线|运行中|离线/u);
  });

  it("shows availability in the members pane and the Agent DM header", async () => {
    const dm = conv("dm-off", team.id, { type: "direct", name: "", memberIds: [owner.id, disabledAgent.principalId] });
    chatStore.replaceConversations([...bootstrap.conversations, dm]);
    await mount("/app/w/team/c/dm-off");
    expect(find(".conversation-header .agent-availability").textContent).toBe("已停用");
    await click('[aria-label="查看会话成员"]');
    const pane = find(".members-pane");
    expect(pane.querySelector(".agent-availability.disabled")?.textContent).toBe("已停用");
  });

  it("ranks disabled Agents last in @mention options with 「已停用，不会回应」", async () => {
    bootstrap.capabilities["im.message.mentions"] = true;
    const group = conv("crew", team.id, {
      type: "group",
      memberIds: [owner.id, disabledAgent.principalId, enabledAgent.principalId],
    });
    chatStore.replaceConversations([...bootstrap.conversations, group]);
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: vi.fn() });
    await mount("/app/w/team/c/crew");
    const textarea = find<HTMLTextAreaElement>('textarea[aria-label="消息内容"]');
    await settle(() => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(textarea, "@助手");
      textarea.setSelectionRange(3, 3);
      textarea.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const options = [...container.querySelectorAll('[role="listbox"][aria-label="提及成员"] [role="option"]')];
    const names = options.map((o) => o.textContent || "");
    expect(names.at(-1)).toContain(disabledAgent.name);
    expect(names.at(-1)).toContain("已停用，不会回应");
    expect(names[0]).toContain(enabledAgent.name);
  });
});

describe("connection feedback", () => {
  it("overlays the banner, distinguishes first connection and announces through a persistent live region", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      connection = "connecting";
      await mount("/app/w/team/c/general");
      const live = document.body.querySelector(".connection-live-region")!;
      expect(live.getAttribute("role")).toBe("status");
      await act(async () => { await vi.advanceTimersByTimeAsync(3100); });
      expect(find(".connection-banner").textContent).toContain("正在连接");
      expect(find(".connection-banner").textContent).not.toContain("重新连接");
      expect(find(".connection-banner").getAttribute("role")).toBeNull();
      expect(live.textContent).toBe("正在连接…");
      connection = "connected";
      await mount("/app/w/team/c/general");
      expect(container.querySelector(".connection-banner")).toBeNull();
      connection = "reconnecting";
      await mount("/app/w/team/c/general");
      await act(async () => { await vi.advanceTimersByTimeAsync(3100); });
      expect(find(".connection-banner").textContent).toContain("正在重新连接");
      expect(document.body.querySelector(".connection-live-region")).toBe(live);
      expect(find(".account-trigger").querySelector('[role="status"]')).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });
});
