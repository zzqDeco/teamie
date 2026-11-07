// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type { Announcement, Bootstrap, User } from "@teamie/api-client";
import { Shell } from "./shell";
import { chatStore } from "./store";

const brandNewUser: User = {
  id: "new-user-1",
  kind: "human",
  platformRole: "user",
  displayName: "新同学",
  email: "newbie@example.test",
  createdAt: "2026-11-02T00:00:00Z",
};

const adminUser: User = {
  id: "admin-1",
  kind: "human",
  platformRole: "admin",
  displayName: "平台管理员",
  email: "admin@example.test",
  createdAt: "2026-10-01T00:00:00Z",
};

let activeUser: User = brandNewUser;
let bootstrap: Bootstrap;

vi.mock("./auth", () => ({
  useAuth: () => ({
    user: activeUser,
    loading: false,
    error: "",
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
    refresh: vi.fn(async () => {}),
    sync: vi.fn(),
    sendTyping: vi.fn(),
    typing: {},
  }),
}));

const oldAnnouncement: Announcement = {
  id: "11111111-1111-4111-8111-111111111111",
  seq: 1,
  title: "Teamie v0.4.5 发布",
  body: "支持统一 Agent 在场人格提示词。",
  authorId: adminUser.id,
  authorName: adminUser.displayName,
  createdAt: "2026-10-28T10:00:00Z",
};

const latestAnnouncement: Announcement = {
  id: "22222222-2222-4222-8222-222222222222",
  seq: 2,
  title: "Teamie v0.4.6 平台更新",
  body: "新增 **平台公告弹窗** 与模型网关自动错峰。",
  authorId: adminUser.id,
  authorName: adminUser.displayName,
  createdAt: "2026-11-02T10:00:00Z",
};

let container: HTMLDivElement;
let root: Root;
let client: QueryClient;

async function settle(action?: () => void) {
  await act(async () => {
    action?.();
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  await act(async () => { await vi.dynamicImportSettled(); });
}

async function mount(path = "/app") {
  vi.stubGlobal(
    "matchMedia",
    vi.fn(() => ({
      matches: false,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    })),
  );
  await settle(() => {
    root.render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={[path]}>
          <Shell />
        </MemoryRouter>
      </QueryClientProvider>,
    );
  });
  await settle();
}

function findButtonByText(label: string): HTMLButtonElement {
  const buttons = Array.from(document.body.querySelectorAll("button"));
  const found = buttons.find((btn) => btn.textContent?.includes(label));
  if (!found) throw new Error(`Missing button with text: ${label}`);
  return found;
}

async function setInputValue(
  el: HTMLInputElement | HTMLTextAreaElement,
  value: string,
) {
  const proto =
    el instanceof HTMLTextAreaElement
      ? HTMLTextAreaElement.prototype
      : HTMLInputElement.prototype;
  await settle(() => {
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
    el.dispatchEvent(new Event("change", { bubbles: true }));
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
  activeUser = { ...brandNewUser };
  bootstrap = {
    user: activeUser,
    workspaces: [],
    conversations: [],
    capabilities: { "im.announcements.publish": true },
    flagsRevision: "ann-test",
    syncCursor: "0",
  };
  chatStore.begin(activeUser.id);
  chatStore.replaceConversations([]);
  vi.spyOn(api, "attention").mockResolvedValue({ items: [], unreadCount: 0 });
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
});

describe("Platform announcements UX", () => {
  it("pops up the current latest announcement once for a brand-new user and supports history replay", async () => {
    vi.spyOn(api, "announcements").mockImplementation(async (params) => {
      if (params?.beforeSeq === 2) {
        return {
          items: [oldAnnouncement],
          readSeq: 2,
          latestSeq: 2,
          hasMore: false,
        };
      }
      return {
        items: [latestAnnouncement],
        readSeq: 0,
        latestSeq: 2,
        hasMore: true,
      };
    });
    const markReadSpy = vi
      .spyOn(api, "markAnnouncementsRead")
      .mockResolvedValue({ readSeq: 2 });

    await mount("/app");

    // Latest announcement modal pops up automatically on first entry (even with 0 workspaces).
    const dialog = document.body.querySelector('[role="dialog"]');
    expect(dialog).toBeTruthy();
    expect(dialog!.textContent).toContain("Teamie v0.4.6 平台更新");
    expect(dialog!.textContent).toContain("平台公告弹窗");
    expect(dialog!.textContent).not.toContain("Teamie v0.4.5 发布");

    // Acknowledge marks seq=2 read and closes without popping up older announcements.
    const ackBtn = findButtonByText("我知道了");
    await settle(() => ackBtn.click());
    expect(markReadSpy).toHaveBeenCalledWith(2);
    expect(document.body.querySelector('[role="dialog"]')).toBeNull();

    // Clicking the persistent rail button opens history and supports pagination.
    const railBtn = container.querySelector<HTMLButtonElement>(
      ".rail-announcements",
    );
    expect(railBtn).toBeTruthy();
    await settle(() => railBtn!.click());
    await settle();

    const historyDialog = document.body.querySelector('[role="dialog"]');
    expect(historyDialog).toBeTruthy();
    expect(historyDialog!.textContent).toContain("Teamie v0.4.6 平台更新");

    const loadMoreBtn = findButtonByText("加载更早公告");
    await settle(() => loadMoreBtn.click());
    await settle();
    expect(historyDialog!.textContent).toContain("Teamie v0.4.5 发布");
  });

  it("enforces admin role on /app/admin/announcements and supports publishing, previewing, and deleting", async () => {
    vi.spyOn(api, "announcements").mockResolvedValue({
      items: [latestAnnouncement],
      readSeq: 2,
      latestSeq: 2,
      hasMore: false,
    });

    // Non-admin user is denied access.
    await mount("/app/admin/announcements");
    expect(container.textContent).toContain("需要平台管理员权限");

    // Remount as platform admin.
    await settle(() => root.unmount());
    root = createRoot(container);
    client.clear();
    activeUser = { ...adminUser };
    bootstrap = {
      ...bootstrap,
      user: activeUser,
    };
    chatStore.begin(activeUser.id);

    const createSpy = vi
      .spyOn(api, "createAnnouncement")
      .mockResolvedValue({
        announcement: {
          id: "33333333-3333-4333-8333-333333333333",
          seq: 3,
          title: "周末维护通知",
          body: "预计周日凌晨升级网关。",
          authorId: adminUser.id,
          authorName: adminUser.displayName,
          createdAt: "2026-11-03T00:00:00Z",
        },
      });
    const deleteSpy = vi
      .spyOn(api, "deleteAnnouncement")
      .mockResolvedValue(undefined);

    await mount("/app/admin/announcements");

    const titleInput = container.querySelector<HTMLInputElement>(
      "#announcement-title-input",
    )!;
    const bodyInput = container.querySelector<HTMLTextAreaElement>(
      "#announcement-body-input",
    )!;
    await setInputValue(titleInput, "周末维护通知");
    await setInputValue(bodyInput, "预计 **周日凌晨** 升级网关。");

    // Toggle Markdown preview before publishing.
    const previewTab = findButtonByText("预览");
    await settle(() => previewTab.click());
    expect(container.querySelector(".admin-announcement-preview")?.textContent).toContain(
      "预计 周日凌晨 升级网关。",
    );

    const publishBtn = findButtonByText("发布公告");
    await settle(() => publishBtn.click());
    await settle();
    expect(createSpy).toHaveBeenCalledWith({
      title: "周末维护通知",
      body: "预计 **周日凌晨** 升级网关。",
    });
    expect(container.textContent).toContain("公告已发布");

    // Delete existing announcement via confirmation modal.
    const deleteBtn = container.querySelector<HTMLButtonElement>(
      `[aria-label="删除公告：${latestAnnouncement.title}"]`,
    )!;
    await settle(() => deleteBtn.click());
    const confirmDeleteBtn = findButtonByText("确认删除");
    await settle(() => confirmDeleteBtn.click());
    await settle();
    expect(deleteSpy).toHaveBeenCalledWith(latestAnnouncement.id);
  });

  it("preserves read access while disabling writes when im.announcements.publish is false", async () => {
    activeUser = { ...adminUser };
    bootstrap = {
      ...bootstrap,
      user: activeUser,
      capabilities: { "im.announcements.publish": false },
    };
    chatStore.begin(activeUser.id);
    vi.spyOn(api, "announcements").mockResolvedValue({
      items: [latestAnnouncement],
      readSeq: 2,
      latestSeq: 2,
      hasMore: false,
    });

    await mount("/app/admin/announcements");
    expect(container.textContent).toContain("平台公告发布功能当前已关闭");
    expect(container.textContent).toContain(latestAnnouncement.title);
    expect(findButtonByText("发布公告").disabled).toBe(true);
    expect(
      container.querySelector<HTMLButtonElement>(
        `[aria-label="删除公告：${latestAnnouncement.title}"]`,
      )?.disabled,
    ).toBe(true);
  });
});
