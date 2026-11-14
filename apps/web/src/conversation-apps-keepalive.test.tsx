// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter, useLocation } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type {
  Bootstrap,
  Conversation,
  ConversationAppSessionResp,
  MarketplaceApp,
  Member,
  User,
  Workspace,
} from "@teamie/api-client";
import {
  buildAppInstanceKey,
  ConversationAppsPane,
  MAX_LIVE_APP_FRAMES,
  writePersistedAppPaneWidth,
} from "./marketplace";
import { Shell } from "./shell";
import { chatStore } from "./store";

const userAlice: User = {
  id: "u-alice",
  kind: "human",
  platformRole: "user",
  displayName: "Alice",
  email: "alice@example.test",
  createdAt: "2026-11-14T00:00:00Z",
};

const userBob: User = {
  id: "u-bob",
  kind: "human",
  platformRole: "user",
  displayName: "Bob",
  email: "bob@example.test",
  createdAt: "2026-11-14T00:00:00Z",
};

const workspace: Workspace = {
  id: "team",
  name: "产品研发空间",
  createdBy: userAlice.id,
  createdAt: userAlice.createdAt,
};

const members: Member[] = [
  {
    userId: userAlice.id,
    displayName: userAlice.displayName,
    kind: "human",
    role: "owner",
  },
  {
    userId: userBob.id,
    displayName: userBob.displayName,
    kind: "human",
    role: "member",
  },
];

const channelGeneral: Conversation = {
  id: "general",
  workspaceId: workspace.id,
  type: "channel",
  name: "general",
  memberIds: [],
  lastSeq: 0,
  readSeq: 0,
  updatedAt: userAlice.createdAt,
};

const groupA: Conversation = {
  ...channelGeneral,
  id: "group-a",
  type: "group",
  name: "架构设计小组",
  memberIds: [userAlice.id, userBob.id],
  canManageMembers: true,
};

const groupB: Conversation = {
  ...channelGeneral,
  id: "group-b",
  type: "group",
  name: "发布协同小组",
  memberIds: [userAlice.id, userBob.id],
  canManageMembers: true,
};

const appBoard: MarketplaceApp = {
  id: "app-board",
  slug: "board",
  name: "协作白板",
  description: "多人实时画布",
  webUrl: "https://board.example.test/ui",
  mcpUrl: "https://board.example.test/mcp",
  enabled: true,
  createdAt: userAlice.createdAt,
  updatedAt: userAlice.createdAt,
};

const appPoll: MarketplaceApp = {
  id: "app-poll",
  slug: "poll",
  name: "快速投票",
  description: "实时统计意见",
  webUrl: "https://poll.example.test/ui",
  mcpUrl: "https://poll.example.test/mcp",
  enabled: true,
  createdAt: userAlice.createdAt,
  updatedAt: userAlice.createdAt,
};

const appNotes: MarketplaceApp = {
  id: "app-notes",
  slug: "notes",
  name: "会议纪要",
  description: "结构化行动项",
  webUrl: "https://notes.example.test/ui",
  mcpUrl: "https://notes.example.test/mcp",
  enabled: true,
  createdAt: userAlice.createdAt,
  updatedAt: userAlice.createdAt,
};

let activeUser: User = userAlice;
let bootstrap: Bootstrap;
let overlayViewport = false;
let mobileViewport = false;
const logoutMock = vi.fn(async () => {
  api.csrfToken = "";
});

vi.mock("./auth", () => ({
  useAuth: () => ({
    user: activeUser,
    loading: false,
    logout: logoutMock,
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
    reconnect: vi.fn(),
    sendTyping: vi.fn(),
  }),
}));

function LocationProbe() {
  const loc = useLocation();
  return <output data-location>{`${loc.pathname}${loc.search}`}</output>;
}

let container: HTMLDivElement;
let root: Root;
let client: QueryClient;
let appsByConversation: Record<string, MarketplaceApp[]>;

async function flush() {
  for (let i = 0; i < 4; i++) {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
  }
  await act(async () => {
    await vi.dynamicImportSettled();
  });
}

let mediaChangeListeners: Array<() => void> = [];

function stubViewport(opts: { overlay: boolean; mobile?: boolean; width?: number }) {
  overlayViewport = opts.overlay;
  mobileViewport = opts.mobile ?? false;
  vi.stubGlobal("innerWidth", opts.width ?? (opts.overlay ? 1024 : 1440));
  vi.stubGlobal(
    "matchMedia",
    vi.fn((query: string) => ({
      get matches() {
        return (
          (query.includes("780") && mobileViewport) ||
          (query.includes("1250") && overlayViewport)
        );
      },
      addEventListener: (_type: string, cb: () => void) => {
        mediaChangeListeners.push(cb);
      },
      removeEventListener: (_type: string, cb: () => void) => {
        mediaChangeListeners = mediaChangeListeners.filter((fn) => fn !== cb);
      },
    })),
  );
  for (const cb of [...mediaChangeListeners]) cb();
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
  mediaChangeListeners = [];
  api.csrfToken = "csrf-alice";
  activeUser = { ...userAlice };
  stubViewport({ overlay: false, mobile: false, width: 1440 });

  bootstrap = {
    user: activeUser,
    workspaces: [workspace],
    conversations: [channelGeneral, groupA, groupB],
    capabilities: {
      "im.marketplace.enabled": true,
      "im.dm.create": true,
      "im.group.create": true,
    },
    flagsRevision: "rev-1",
    syncCursor: "0",
  };
  chatStore.begin(activeUser.id);
  chatStore.replaceConversations(bootstrap.conversations);

  appsByConversation = {
    [groupA.id]: [appBoard, appPoll, appNotes],
    [groupB.id]: [appBoard, appPoll],
  };

  vi.spyOn(api, "members").mockResolvedValue({ members });
  vi.spyOn(api, "messages").mockResolvedValue({ messages: [], hasMore: false });
  vi.spyOn(api, "attention").mockResolvedValue({ items: [], unreadCount: 0 });
  vi.spyOn(api, "announcements").mockResolvedValue({
    items: [],
    readSeq: 0,
    latestSeq: 0,
    hasMore: false,
  });
  vi.spyOn(api, "read").mockResolvedValue({ readSeq: 0 });
  vi.spyOn(api, "conversationApps").mockImplementation(async (convId) => ({
    apps: appsByConversation[convId] || [],
  }));
  let seq = 0;
  vi.spyOn(api, "createConversationAppSession").mockImplementation(
    async (convId, appId): Promise<ConversationAppSessionResp> => {
      const list = appsByConversation[convId] || [appBoard, appPoll, appNotes];
      const found = list.find((a) => a.id === appId) || appBoard;
      seq += 1;
      return {
        app: found,
        token: `tok-${convId}-${appId}-${seq}`,
        expiresAt: "2099-01-01T00:00:00Z",
      };
    },
  );

  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  client.clear();
  chatStore.clear();
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function find<T extends HTMLElement>(selector: string): T {
  const el = container.querySelector<T>(selector);
  if (!el) throw new Error(`Missing element: ${selector}`);
  return el;
}

async function click(el: HTMLElement) {
  await act(async () => {
    el.focus();
    el.click();
  });
  await flush();
}

async function renderShell(initialPath = "/app/w/team/c/group-a") {
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={[initialPath]}>
          <Shell />
          <LocationProbe />
        </MemoryRouter>
      </QueryClientProvider>,
    );
  });
  await flush();
}

async function rerenderShell() {
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={["/app/w/team/c/group-a"]}>
          <Shell />
          <LocationProbe />
        </MemoryRouter>
      </QueryClientProvider>,
    );
  });
  await flush();
}

function getSlotFrame(convId: string, appId: string) {
  return container.querySelector<HTMLIFrameElement>(
    `#mp-pane-panel-${convId}-${appId} iframe.mp-iframe`,
  );
}

async function clickLauncherApp(name: string) {
  const card = [...container.querySelectorAll<HTMLButtonElement>(".mp-launcher-card")].find(
    (btn) => btn.querySelector("b")?.textContent === name,
  );
  if (!card) throw new Error(`Missing launcher card: ${name}`);
  await click(card);
}

describe("Browser-style group app tabs & cross-group zero-reload keep-alive (Issue #150)", () => {
  it("opens launcher with zero sessions, supports multi-tab + / × & keyboard navigation without reloading frames", async () => {
    expect(MAX_LIVE_APP_FRAMES).toBe(8);
    expect(buildAppInstanceKey("u1", "ws1", "c1", appBoard)).toBe(
      JSON.stringify(["u1", "ws1", "c1", appBoard.id, appBoard.webUrl, appBoard.mcpUrl]),
    );

    await renderShell("/app/w/team/c/group-a");
    await click(find('header.conversation-header [aria-label="小组应用"]'));

    // Default launcher: zero sessions, zero iframes, no tablist yet
    expect(find('[aria-label="已安装小组应用列表"]')).not.toBeNull();
    expect(container.querySelector('[role="tablist"]')).toBeNull();
    expect(container.querySelectorAll("iframe.mp-iframe").length).toBe(0);
    expect(api.createConversationAppSession).toHaveBeenCalledTimes(0);

    // Click App 1 -> opens first tab and mounts iframe
    await clickLauncherApp("协作白板");
    expect(api.createConversationAppSession).toHaveBeenCalledTimes(1);
    const frameBoard = getSlotFrame("group-a", appBoard.id)!;
    expect(frameBoard).not.toBeNull();
    expect(container.querySelector(".mp-launcher")).toBeNull();

    // Click + ("打开新应用") -> shows launcher while keeping frameBoard mounted & hidden
    await click(find('[aria-label="打开新应用"]'));
    expect(find('[aria-label="已安装小组应用列表"]').textContent).toContain("已打开");
    expect(frameBoard.isConnected).toBe(true);
    expect(find(".mp-frames-stage").hidden).toBe(true);

    // Click App 2 -> opens second tab alongside App 1 without reordering DOM slots
    await clickLauncherApp("快速投票");
    expect(api.createConversationAppSession).toHaveBeenCalledTimes(2);
    const framePoll = getSlotFrame("group-a", appPoll.id)!;
    expect(framePoll).not.toBeNull();
    expect(getSlotFrame("group-a", appBoard.id)).toBe(frameBoard);
    expect(frameBoard.closest<HTMLElement>(".mp-frame-slot")!.hidden).toBe(true);
    expect(frameBoard.closest<HTMLElement>(".mp-frame-slot")!.hasAttribute("inert")).toBe(true);
    expect(framePoll.closest<HTMLElement>(".mp-frame-slot")!.hidden).toBe(false);
    expect(framePoll.closest<HTMLElement>(".mp-frame-slot")!.hasAttribute("inert")).toBe(false);

    // Keyboard roving focus (Home / End / ArrowLeft / ArrowRight) preserves exact iframe references
    const tabPoll = find<HTMLButtonElement>(`#mp-pane-tab-group-a-${appPoll.id}`);
    await act(async () => {
      tabPoll.dispatchEvent(new KeyboardEvent("keydown", { key: "Home", bubbles: true }));
    });
    await flush();
    const tabBoard = find<HTMLButtonElement>(`#mp-pane-tab-group-a-${appBoard.id}`);
    expect(tabBoard.getAttribute("aria-selected")).toBe("true");
    expect(frameBoard.closest<HTMLElement>(".mp-frame-slot")!.hidden).toBe(false);
    expect(framePoll.closest<HTMLElement>(".mp-frame-slot")!.hidden).toBe(true);

    await act(async () => {
      tabBoard.dispatchEvent(new KeyboardEvent("keydown", { key: "End", bubbles: true }));
    });
    await flush();
    expect(tabPoll.getAttribute("aria-selected")).toBe("true");
    expect(getSlotFrame("group-a", appBoard.id)).toBe(frameBoard);
    expect(getSlotFrame("group-a", appPoll.id)).toBe(framePoll);
    expect(api.createConversationAppSession).toHaveBeenCalledTimes(2);

    // Verify monotonic DOM slot order never changed upon tab switches
    const slotIds = [...container.querySelectorAll(".mp-frame-slot")].map((el) => el.id);
    expect(slotIds).toEqual([
      `mp-pane-panel-group-a-${appBoard.id}`,
      `mp-pane-panel-group-a-${appPoll.id}`,
    ]);

    // Close App 1 tab via × -> destroys only frameBoard, keeps framePoll intact
    await click(find(`[aria-label="关闭标签页：${appBoard.name}"]`));
    expect(frameBoard.isConnected).toBe(false);
    expect(getSlotFrame("group-a", appBoard.id)).toBeNull();
    expect(getSlotFrame("group-a", appPoll.id)).toBe(framePoll);

    // Close last tab -> destroys framePoll and returns to launcher
    await click(find(`[aria-label="关闭标签页：${appPoll.name}"]`));
    expect(framePoll.isConnected).toBe(false);
    expect(container.querySelectorAll("iframe.mp-iframe").length).toBe(0);
    expect(find('[aria-label="已安装小组应用列表"]')).not.toBeNull();
  });

  it("keeps exact iframe DOM instances alive across groups, channels, settings, members pane, and desktop vs narrow viewports", async () => {
    writePersistedAppPaneWidth(userAlice.id, 512);
    await renderShell("/app/w/team/c/group-a");

    // Open pane in group-a and launch Collaborative Whiteboard
    const headerAppsBtnA = find<HTMLButtonElement>(
      'header.conversation-header [aria-label="小组应用"]',
    );
    expect(headerAppsBtnA.querySelector(".mp-running-badge")).toBeNull();
    await click(headerAppsBtnA);
    expect(find('aside[aria-label="小组应用"]').style.getPropertyValue("--mp-pane-width")).toBe(
      "512px",
    );

    await clickLauncherApp("协作白板");
    const frameGroupA = getSlotFrame("group-a", appBoard.id)!;
    expect(frameGroupA).not.toBeNull();
    const postSpyA = vi.fn();
    Object.defineProperty(frameGroupA, "contentWindow", {
      value: { postMessage: postSpyA },
      configurable: true,
    });
    expect(
      find('header.conversation-header [aria-label="小组应用"] .mp-running-badge')
        .textContent,
    ).toBe("1");

    // Navigate to #general (channel) -> pane hides in place, iframe stays connected!
    await click(find('.navigation-sidebar a[href="/app/w/team/c/general"]'));
    const hiddenAside = find('aside[aria-label="小组应用"]');
    expect(hiddenAside.hidden).toBe(true);
    expect(hiddenAside.hasAttribute("inert")).toBe(true);
    expect(frameGroupA.isConnected).toBe(true);

    // Navigate to group-b -> desktop remembers group-b has NOT opened its pane yet
    await click(find('.navigation-sidebar a[href="/app/w/team/c/group-b"]'));
    expect(find('aside[aria-label="小组应用"]').hidden).toBe(true);
    expect(
      container.querySelector(
        'header.conversation-header [aria-label="小组应用"] .mp-running-badge',
      ),
    ).toBeNull();

    // Open group-b pane and launch the SAME app ("协作白板") -> second isolated iframe!
    await click(find('header.conversation-header [aria-label="小组应用"]'));
    await clickLauncherApp("协作白板");
    const frameGroupB = getSlotFrame("group-b", appBoard.id)!;
    expect(frameGroupB).not.toBeNull();
    expect(frameGroupB).not.toBe(frameGroupA);
    expect(frameGroupA.isConnected).toBe(true);
    const postSpyB = vi.fn();
    Object.defineProperty(frameGroupB, "contentWindow", {
      value: { postMessage: postSpyB },
      configurable: true,
    });

    // Background token refresh from group-a's iframe delivers strictly to group-a's frame
    postSpyA.mockClear();
    postSpyB.mockClear();
    await act(async () => {
      window.dispatchEvent(
        new MessageEvent("message", {
          origin: "https://board.example.test",
          source: frameGroupA.contentWindow,
          data: { type: "teamie:mcp-app:refresh-token" },
        }),
      );
    });
    await flush();
    expect(postSpyA).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "teamie:mcp-app:init",
        payload: expect.objectContaining({ conversationId: "group-a" }),
      }),
      "https://board.example.test",
    );
    expect(postSpyB).not.toHaveBeenCalled();

    // Collapse group-b's pane, open ShellMembersPane, then visit Workspace Apps page
    await click(find('aside [aria-label="关闭小组应用面板"]'));
    expect(find('aside[aria-label="小组应用"]').hidden).toBe(true);
    await click(find('header.conversation-header [aria-label="查看会话成员"]'));
    expect(find('aside[aria-label="对话成员"]')).not.toBeNull();
    await click(find('.navigation-sidebar a[href="/app/w/team/apps"]'));
    expect(frameGroupA.isConnected).toBe(true);
    expect(frameGroupB.isConnected).toBe(true);

    // Click workspace rail logo (/app redirect) -> preserves frame host through <Navigate> redirect!
    const sessionsBeforeRedirect = vi.mocked(api.createConversationAppSession).mock.calls.length;
    await click(find('a[href="/app"]'));
    expect(frameGroupA.isConnected).toBe(true);
    expect(frameGroupB.isConnected).toBe(true);
    expect(vi.mocked(api.createConversationAppSession).mock.calls.length).toBe(
      sessionsBeforeRedirect,
    );

    // Close members pane and switch back to group-a -> desktop remembers group-a pane was open!
    await click(find('.navigation-sidebar a[href="/app/w/team/c/group-a"]'));
    await click(find('header.conversation-header [aria-label="查看会话成员"]'));
    expect(find('aside[aria-label="小组应用"]').hidden).toBe(false);
    expect(getSlotFrame("group-a", appBoard.id)).toBe(frameGroupA);
    expect(getSlotFrame("group-b", appBoard.id)).toBe(frameGroupB);

    // Switch to narrow/overlay viewport (<= 1250px): route transition closes overlay drawer while keeping frames alive
    await act(async () => {
      stubViewport({ overlay: true, mobile: false, width: 1024 });
      window.dispatchEvent(new Event("resize"));
    });
    await flush();
    expect(find("main.app-main").hasAttribute("inert")).toBe(true);
    const sessionsBeforeNarrowSwitch = vi.mocked(api.createConversationAppSession).mock.calls.length;
    await click(find('.navigation-sidebar a[href="/app/w/team/c/group-b"]'));
    expect(find('aside[aria-label="小组应用"]').hidden).toBe(true);
    expect(find("main.app-main").hasAttribute("inert")).toBe(false);
    expect(frameGroupA.isConnected).toBe(true);
    expect(frameGroupB.isConnected).toBe(true);

    // Reopen drawer in group-b -> instant zero-reload reveal with 0 new session calls
    await click(find('header.conversation-header [aria-label="小组应用"]'));
    expect(find('aside[aria-label="小组应用"]').hidden).toBe(false);
    expect(getSlotFrame("group-b", appBoard.id)).toBe(frameGroupB);
    expect(vi.mocked(api.createConversationAppSession).mock.calls.length).toBe(
      sessionsBeforeNarrowSwitch,
    );
  });

  it("immediately destroys corresponding iframes on conversation loss, app uninstall/endpoint change, capability disable, and account switch", async () => {
    await renderShell("/app/w/team/c/group-a");
    await click(find('header.conversation-header [aria-label="小组应用"]'));
    await clickLauncherApp("协作白板");
    await click(find('[aria-label="打开新应用"]'));
    await clickLauncherApp("快速投票");
    const frameABoard = getSlotFrame("group-a", appBoard.id)!;
    const frameAPoll = getSlotFrame("group-a", appPoll.id)!;

    await click(find('.navigation-sidebar a[href="/app/w/team/c/group-b"]'));
    await click(find('header.conversation-header [aria-label="小组应用"]'));
    await clickLauncherApp("协作白板");
    const frameBBoard = getSlotFrame("group-b", appBoard.id)!;
    expect(frameABoard.isConnected).toBe(true);
    expect(frameAPoll.isConnected).toBe(true);
    expect(frameBBoard.isConnected).toBe(true);

    // 1) Background app endpoint change & uninstall in group-a (while viewing group-b!)
    appsByConversation[groupA.id] = [
      { ...appBoard, webUrl: "https://board-v2.example.test/ui" },
      // appPoll uninstalled!
    ];
    await act(async () => {
      await client.invalidateQueries({ queryKey: ["conversationApps", groupA.id] });
    });
    await flush();
    expect(frameABoard.isConnected).toBe(false);
    expect(frameAPoll.isConnected).toBe(false);
    expect(frameBBoard.isConnected).toBe(true);

    // Re-open updated appBoard in group-a, disable appBoard on server for group-b WITHOUT manual invalidateQueries,
    // and verify returning to group-b's open pane automatically revalidates conversationApps and destroys frameBBoard!
    await click(find('.navigation-sidebar a[href="/app/w/team/c/group-a"]'));
    await clickLauncherApp("协作白板");
    const frameABoardV2 = getSlotFrame("group-a", appBoard.id)!;
    expect(frameABoardV2).not.toBeNull();

    appsByConversation[groupB.id] = [{ ...appBoard, enabled: false }];
    await click(find('.navigation-sidebar a[href="/app/w/team/c/group-b"]'));
    expect(frameBBoard.isConnected).toBe(false);

    // Re-enable in group-b and re-open so we can also test authoritative conversation removal
    appsByConversation[groupB.id] = [appBoard];
    await click(find('aside [aria-label="关闭小组应用面板"]'));
    await click(find('header.conversation-header [aria-label="小组应用"]'));
    await clickLauncherApp("协作白板");
    const frameBBoardV2 = getSlotFrame("group-b", appBoard.id)!;
    expect(frameBBoardV2.isConnected).toBe(true);
    await click(find('.navigation-sidebar a[href="/app/w/team/c/group-a"]'));

    // 2) Losing access to group-b via authoritative conversation sync immediately destroys group-b's iframe
    bootstrap = {
      ...bootstrap,
      conversations: [channelGeneral, groupA],
    };
    await act(async () => {
      chatStore.replaceConversations([channelGeneral, groupA]);
    });
    await rerenderShell();
    expect(frameBBoardV2.isConnected).toBe(false);
    expect(frameABoardV2.isConnected).toBe(true);

    // 3) Capability im.marketplace.enabled turning false immediately destroys all iframes and shows read-only list
    bootstrap = {
      ...bootstrap,
      capabilities: { ...bootstrap.capabilities, "im.marketplace.enabled": false },
    };
    await rerenderShell();
    expect(frameABoardV2.isConnected).toBe(false);
    expect(container.querySelectorAll("iframe.mp-iframe").length).toBe(0);
    expect(find('[aria-label="已安装小组应用只读列表"]')).not.toBeNull();

    // 4) Re-enable capability, open an app, then switch account (Alice -> Bob) -> destroys Alice's frame
    bootstrap = {
      ...bootstrap,
      capabilities: { ...bootstrap.capabilities, "im.marketplace.enabled": true },
    };
    await rerenderShell();
    await clickLauncherApp("协作白板");
    const frameAliceAgain = getSlotFrame("group-a", appBoard.id)!;
    expect(frameAliceAgain.isConnected).toBe(true);

    activeUser = { ...userBob };
    bootstrap = { ...bootstrap, user: activeUser };
    await act(async () => {
      chatStore.begin(activeUser.id);
      chatStore.replaceConversations([channelGeneral, groupA]);
    });
    await rerenderShell();
    expect(frameAliceAgain.isConnected).toBe(false);
    expect(container.querySelectorAll("iframe.mp-iframe").length).toBe(0);
  });

  it("hibernates oldest background frames past maxLiveFrames (LRU) while keeping tabs and waking on click", async () => {
    const noop = () => {};
    await act(async () => {
      root.render(
        <QueryClientProvider client={client}>
          <ConversationAppsPane
            maxLiveFrames={2}
            overlayMembers={false}
            mobile={false}
            sidebar={false}
            workspace={workspace}
            conversation={groupA}
            user={userAlice}
            owner={true}
            onClose={noop}
            onOpenWorkspaceApps={noop}
          />
        </QueryClientProvider>,
      );
    });
    await flush();

    // Open App 1 and App 2 -> both live (2 iframes)
    await clickLauncherApp("协作白板");
    const f1 = getSlotFrame("group-a", appBoard.id)!;
    await click(find('[aria-label="打开新应用"]'));
    await clickLauncherApp("快速投票");
    const f2 = getSlotFrame("group-a", appPoll.id)!;
    expect(f1.isConnected).toBe(true);
    expect(f2.isConnected).toBe(true);
    expect(container.querySelectorAll("iframe.mp-iframe").length).toBe(2);

    // Open App 3 -> exceeds maxLiveFrames=2, hibernates oldest-accessed App 1 while keeping its tab!
    await click(find('[aria-label="打开新应用"]'));
    await clickLauncherApp("会议纪要");
    const f3 = getSlotFrame("group-a", appNotes.id)!;
    expect(f1.isConnected).toBe(false);
    expect(getSlotFrame("group-a", appBoard.id)).toBeNull();
    expect(f2.isConnected).toBe(true);
    expect(f3.isConnected).toBe(true);
    expect(container.querySelectorAll("iframe.mp-iframe").length).toBe(2);
    expect(container.querySelectorAll('[role="tab"]').length).toBe(3);
    expect(
      find(`#mp-pane-tab-group-a-${appBoard.id}`).closest(".mp-tab-item")?.classList.contains("hibernated"),
    ).toBe(true);

    // Launcher reflects "已休眠" for App 1 and "已打开" for App 2 & App 3
    await click(find('[aria-label="打开新应用"]'));
    expect(find('[aria-label="已安装小组应用列表"]').textContent).toContain("已休眠");

    // Clicking App 1's tab wakes App 1 with a fresh session and hibernates App 2 (now oldest)!
    const callsBeforeWake = vi.mocked(api.createConversationAppSession).mock.calls.length;
    await click(find(`#mp-pane-tab-group-a-${appBoard.id}`));
    expect(vi.mocked(api.createConversationAppSession).mock.calls.length).toBe(
      callsBeforeWake + 1,
    );
    expect(getSlotFrame("group-a", appBoard.id)).not.toBeNull();
    expect(f2.isConnected).toBe(false);
    expect(f3.isConnected).toBe(true);
    expect(container.querySelectorAll("iframe.mp-iframe").length).toBe(2);
  });
});
