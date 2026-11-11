// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter, useLocation } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type { AttentionPage, Bootstrap, Conversation, Member, User, Workspace } from "@teamie/api-client";
import { Shell } from "./shell";
import { chatStore } from "./store";
import { matchRank, rankItems } from "./shell/quick-switcher-items";
import type { QuickItem } from "./shell/quick-switcher-items";
import { loadPinyin } from "./shell/pinyin-match";

const owner: User = {
  id: "owner",
  kind: "human",
  platformRole: "user",
  displayName: "创建者",
  email: "owner@example.test",
  createdAt: "2026-10-16T00:00:00Z",
};
const team: Workspace = { id: "team", name: "团队", createdBy: owner.id, createdAt: owner.createdAt };
const conv = (id: string, extra: Partial<Conversation> = {}): Conversation => ({
  id,
  workspaceId: team.id,
  type: "channel",
  name: id,
  memberIds: [],
  lastSeq: 0,
  readSeq: 0,
  updatedAt: owner.createdAt,
  ...extra,
});
const members: Member[] = [
  { userId: owner.id, displayName: owner.displayName, kind: "human", role: "owner" },
  { userId: "human", displayName: "设计伙伴", kind: "human", role: "member" },
];

let activeUser: User;
let bootstrap: Bootstrap;
vi.mock("./auth", () => ({
  useAuth: () => ({ user: activeUser, loading: false, logout: vi.fn(), updateUser: vi.fn() }),
}));
vi.mock("./chat", async (original) => ({
  ...(await original<typeof import("./chat")>()),
  useChat: () => ({
    data: bootstrap,
    loading: false,
    error: "",
    connection: "connected",
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
async function mount(path: string) {
  vi.stubGlobal("matchMedia", vi.fn((query: string) => ({
    matches: query.includes("1250"),
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
function find<T extends HTMLElement>(selector: string): T {
  const node = container.querySelector<T>(selector);
  if (!node) throw new Error(`Missing control: ${selector}`);
  return node;
}
const location = () => find("[data-location]").textContent;
async function openSwitcher() {
  await settle(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: "k", ctrlKey: true, bubbles: true })));
}
async function typeQuery(value: string) {
  const input = document.querySelector<HTMLInputElement>('input[aria-label="快速跳转搜索"]')!;
  await settle(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  return input;
}
const optionLabels = () =>
  [...document.querySelectorAll('[role="listbox"] [role="option"] b')].map((b) => b.textContent);
const switcherOption = (label: string) =>
  [...document.querySelectorAll<HTMLElement>('[role="option"]')].find((o) => o.querySelector("b")?.textContent === label);
const altKey = (key: string, shiftKey = false) =>
  settle(() => window.dispatchEvent(new KeyboardEvent("keydown", { key, altKey: true, shiftKey, bubbles: true })));

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  Object.defineProperty(HTMLElement.prototype, "scrollTo", { configurable: true, value: vi.fn() });
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: vi.fn() });
  localStorage.clear();
  activeUser = { ...owner };
  bootstrap = {
    user: activeUser,
    workspaces: [team],
    conversations: [conv("general")],
    capabilities: { "im.dm.create": true, "im.group.create": true },
    flagsRevision: "unread-test",
    syncCursor: "0",
  };
  chatStore.begin(owner.id);
  chatStore.replaceConversations(bootstrap.conversations);
  vi.spyOn(api, "members").mockResolvedValue({ members });
  vi.spyOn(api, "workspaceAgents").mockResolvedValue({ agents: [] });
  vi.spyOn(api, "messages").mockResolvedValue({ messages: [], hasMore: false });
  vi.spyOn(api, "attention").mockResolvedValue({ items: [], unreadCount: 0 });
  vi.spyOn(api, "announcements").mockResolvedValue({ items: [], readSeq: 0, latestSeq: 0, hasMore: false });
  vi.spyOn(api, "read").mockResolvedValue({ readSeq: 0 });
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

describe("quick switcher ranking", () => {
  const item = (label: string): QuickItem => ({ id: label, label, subtitle: "团队", path: `/${label}`, kind: "channel" });

  it("ranks label prefixes before substrings and matches pinyin and initials", async () => {
    const toPinyin = await loadPinyin();
    expect(toPinyin).not.toBeNull();
    const items = [item("运营日报"), item("日报汇总"), item("design-日报")];
    expect(rankItems(items, "日报").map((i) => i.label)).toEqual(["日报汇总", "design-日报", "运营日报"]);
    expect(matchRank(item("日报汇总"), "rbhz", toPinyin)).toBe(2);
    expect(matchRank(item("日报汇总"), "ribao", toPinyin)).toBe(2);
    expect(matchRank(item("运营日报"), "ribao", toPinyin)).toBe(4);
    expect(matchRank(item("日报汇总"), "rbhz")).toBe(-1);
    expect(matchRank(item("日报汇总"), "团队")).toBe(5);
  });
});

describe("quick switcher", () => {
  it("groups the empty state into at most five recent, channel, direct, workspace and page entries", async () => {
    const channels = Array.from({ length: 7 }, (_, i) => conv(`c${i}`, { name: `频道${i}` }));
    const dm = conv("dm", { type: "direct", name: "", memberIds: [owner.id, "human"] });
    chatStore.replaceConversations([...channels, dm]);
    await mount("/app/w/team/c/c0");
    await openSwitcher();
    const groups = [...document.querySelectorAll(".quick-switcher-list > .quick-switcher-group")];
    expect(groups.map((g) => g.querySelector(".quick-switcher-group-header")?.textContent)).toEqual([
      "最近", "频道", "私信与小组", "工作空间", "页面",
    ]);
    for (const group of groups) {
      const list = group.querySelector('[role="group"]')!;
      expect(list.getAttribute("aria-labelledby")).toBe(group.querySelector(".quick-switcher-group-header")!.id);
      expect(list.querySelectorAll('[role="option"]').length).toBeLessThanOrEqual(5);
    }
    expect(groups[0].textContent).toContain("频道0");
    expect(groups[1].textContent).not.toContain("频道0");
    expect(document.querySelectorAll('[role="option"] button')).toHaveLength(0);
    const workspaceIcon = switcherOption(team.name)!.querySelector(".quick-switcher-icon.workspace svg")!;
    expect(workspaceIcon.getAttribute("class")).not.toContain("lucide-users");
  });

  it("selects options by clicking the option itself and lists page entries, admin pages only for admins", async () => {
    await mount("/app/w/team/c/general");
    await openSwitcher();
    for (const label of ["成员目录", "应用", "收藏", "开发者平台", "键盘快捷键"]) {
      await typeQuery(label);
      expect(optionLabels()[0], label).toBe(label);
    }
    await typeQuery("用户管理");
    expect(optionLabels()).not.toContain("用户管理");
    await typeQuery("应用");
    await settle(() => switcherOption("应用")!.click());
    expect(location()).toBe("/app/w/team/apps");
    await openSwitcher();
    await typeQuery("键盘快捷键");
    const input = document.querySelector<HTMLInputElement>('input[aria-label="快速跳转搜索"]')!;
    await settle(() => input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    expect(document.querySelector(".modal h2")?.textContent).toBe("键盘快捷键");
  });

  it("offers administrator pages to human platform administrators", async () => {
    activeUser = { ...owner, platformRole: "admin" };
    await mount("/app/w/team/c/general");
    await openSwitcher();
    await typeQuery("用户管理");
    expect(optionLabels()[0]).toBe("用户管理");
    await typeQuery("模型网关");
    expect(optionLabels()[0]).toBe("模型网关");
  });

  it("matches Chinese conversation names by pinyin initials once the library loads", async () => {
    chatStore.replaceConversations([conv("general"), conv("daily", { name: "日报汇总" })]);
    await mount("/app/w/team/c/general");
    await openSwitcher();
    await typeQuery("rbhz");
    await vi.waitFor(() => expect(optionLabels()).toContain("日报汇总"));
  });
});

describe("unified unread indicators", () => {
  it("uses the shared summary in the sidebar and quick switcher, including muted and manual unread", async () => {
    const muted = conv("muted", { lastSeq: 5, notificationLevel: "none" });
    const busy = conv("busy", { lastSeq: 4 });
    const dm = conv("dm", { type: "direct", name: "", memberIds: [owner.id, "human"], lastSeq: 3, updatedAt: "2026-10-17T00:00:00Z" });
    const marked = conv("marked", { lastSeq: 2, readSeq: 2 });
    chatStore.replaceConversations([conv("general"), muted, busy, dm, marked]);
    chatStore.setManualUnread("marked", 2);
    await mount("/app/w/team/c/general");
    const row = (id: string) => find(`.navigation-sidebar a[href="/app/w/team/c/${id}"]`);
    expect(row("muted").querySelector(".unread-badge")).toBeNull();
    expect(row("busy").querySelector(".unread-badge")).toBeNull();
    expect(row("busy").classList.contains("has-unread")).toBe(true);
    expect(row("dm").querySelector(".unread-badge")?.textContent).toBe("3");
    expect(row("marked").classList.contains("has-unread")).toBe(true);
    await openSwitcher();
    await typeQuery("muted");
    expect(switcherOption("muted")!.querySelector(".unread-badge, .unread-dot")).toBeNull();
    await typeQuery("busy");
    expect(switcherOption("busy")!.querySelector(".unread-badge")).toBeNull();
    expect(switcherOption("busy")!.querySelector(".unread-dot")).not.toBeNull();
    await typeQuery("marked");
    expect(switcherOption("marked")!.querySelector(".unread-dot, .unread-badge")).not.toBeNull();
    await typeQuery("设计伙伴");
    expect(switcherOption("设计伙伴")!.querySelector(".unread-badge")?.textContent).toBe("3");
  });

  it("makes Alt+Shift+↓ follow summary.hasUnread", async () => {
    const quiet = conv("a-quiet", { lastSeq: 4, notificationLevel: "mentions" });
    const muted = conv("b-muted", { lastSeq: 4, notificationLevel: "none" });
    const marked = conv("c-marked", { lastSeq: 1, readSeq: 1 });
    chatStore.replaceConversations([conv("general"), quiet, muted, marked]);
    chatStore.setManualUnread("c-marked", 1);
    await mount("/app/w/team/c/general");
    await altKey("ArrowDown", true);
    expect(location()).toBe("/app/w/team/c/c-marked");
  });

  it("keeps the rail attention badge while a read refreshes the count", async () => {
    chatStore.replaceConversations([conv("general"), conv("news", { lastSeq: 3 })]);
    let resolveRefresh!: (page: AttentionPage) => void;
    const attention = vi.mocked(api.attention)
      .mockResolvedValueOnce({ items: [], unreadCount: 3 })
      .mockImplementation(() => new Promise((resolve) => { resolveRefresh = resolve; }));
    await mount("/app/w/team/c/general");
    expect(find(".rail-attention-count").textContent).toBe("3");
    const calls = attention.mock.calls.length;
    await settle(() => chatStore.markRead("news", 3));
    expect(attention.mock.calls.length).toBeGreaterThan(calls);
    expect(find(".rail-attention-count").textContent).toBe("3");
    await settle(() => resolveRefresh({ items: [], unreadCount: 1 }));
    expect(find(".rail-attention-count").textContent).toBe("1");
  });
});

describe("sidebar sections", () => {
  it("points to unread conversations scrolled below the list and scrolls to the next one", async () => {
    chatStore.replaceConversations([
      conv("general"),
      conv("near", { lastSeq: 1 }),
      conv("far-a", { lastSeq: 1 }),
      conv("far-b", { lastSeq: 1 }),
    ]);
    Object.defineProperty(HTMLElement.prototype, "clientHeight", {
      configurable: true,
      get(this: HTMLElement) { return this.classList.contains("sidebar-scroll") ? 100 : 0; },
    });
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
      const far = this.getAttribute("href")?.includes("far-");
      const top = this.classList.contains("sidebar-scroll") ? 0 : far ? 240 : 20;
      return { top, bottom: this.classList.contains("sidebar-scroll") ? 100 : top + 30 } as DOMRect;
    });
    try {
      await mount("/app/w/team/c/general");
      const indicator = find<HTMLButtonElement>(".sidebar-unread-below");
      expect(indicator.textContent).toContain("下方有 2 个未读");
      await settle(() => indicator.click());
      expect(HTMLElement.prototype.scrollIntoView).toHaveBeenCalled();
      expect(document.activeElement?.getAttribute("href")).toBe("/app/w/team/c/far-a");
    } finally {
      Reflect.deleteProperty(HTMLElement.prototype, "clientHeight");
    }
  });

  it("persists collapsed sections per user", async () => {
    await mount("/app/w/team/members");
    const toggle = () => find<HTMLButtonElement>('.nav-section-toggle[aria-label^="私信与小组"]');
    await settle(() => toggle().click());
    expect(toggle().getAttribute("aria-expanded")).toBe("false");
    await settle(() => root.unmount());
    root = createRoot(container);
    await mount("/app/w/team/members");
    expect(toggle().getAttribute("aria-expanded")).toBe("false");
    expect(JSON.parse(localStorage.getItem(`teamie.sidebarSections.${owner.id}`) || "{}")).toEqual({ direct: true });
  });
});
