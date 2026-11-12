// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type {
  Conversation,
  ConversationAppSessionResp,
  DeveloperMarketplaceApp,
  MarketplaceApp,
  MarketplaceEndpointVerification,
  User,
  Workspace,
} from "@teamie/api-client";
import { DeveloperPortal } from "./developers";
import { ConversationAppsPane, PlatformMarketplace } from "./marketplace";
import { toast } from "./toast";

let currentUser: User = {
  id: "u-dev",
  kind: "human",
  platformRole: "user",
  displayName: "开发者小赵",
  email: "dev@example.test",
  createdAt: "2026-10-30T00:00:00Z",
};

vi.mock("./auth", () => ({
  useAuth: () => ({ user: currentUser }),
}));

const ownedWorkspace: Workspace = {
  id: "ws-dev",
  name: "联调空间",
  createdBy: "u-dev",
  createdAt: "2026-10-30T00:00:00Z",
};

const sampleGroup: Conversation = {
  id: "conv-group",
  workspaceId: "ws-dev",
  type: "group",
  name: "桌游小组",
  memberIds: ["u-dev"],
  lastSeq: 0,
  readSeq: 0,
  updatedAt: "2026-10-30T00:00:00Z",
};

const sampleVerification: MarketplaceEndpointVerification = {
  ok: true,
  verifiedAt: "2026-10-30T03:00:00Z",
  webUrl: "https://board.example.test/ui",
  mcpUrl: "https://board.example.test/mcp",
  mcpServerName: "board-mcp",
  mcpServerVersion: "1.0.0",
  mcpProtocolVersion: "2025-03-26",
  checks: [
    { stage: "url_security", ok: true, code: "ok", summary: "跨源 HTTPS 通过" },
    { stage: "web_ui", ok: true, code: "ok", summary: "iframe 嵌入通过" },
    { stage: "mcp_auth", ok: true, code: "ok", summary: "401 门禁通过" },
    { stage: "mcp_handshake", ok: true, code: "ok", summary: "发现 1 个工具" },
  ],
  tools: [
    {
      name: "roll_dice",
      exposedName: "mcp__board_game__roll_dice",
      description: "掷骰子",
    },
  ],
};

function makeDevApp(overrides: Partial<DeveloperMarketplaceApp> = {}): DeveloperMarketplaceApp {
  return {
    id: "app-1",
    slug: "board-game",
    name: "协作桌游",
    description: "线上描述",
    webUrl: "https://board.example.test/ui",
    mcpUrl: "https://board.example.test/mcp",
    enabled: true,
    isOfficial: false,
    isPublished: false,
    publishedVersion: 0,
    ownerId: "u-dev",
    ownerDisplayName: "开发者小赵",
    draftName: "协作桌游",
    draftDescription: "草稿描述",
    draftWebUrl: "https://board.example.test/ui",
    draftMcpUrl: "https://board.example.test/mcp",
    hasDraftChanges: true,
    reviewStatus: "draft",
    submissionRevision: 0,
    submissionNote: "",
    reviewNote: "",
    adminSuspended: false,
    suspendedReason: "",
    createdAt: "2026-10-30T01:00:00Z",
    updatedAt: "2026-10-30T01:00:00Z",
    ...overrides,
  };
}

let host: HTMLDivElement;
let root: Root;

async function flush() {
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });
}

async function typeField(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  const proto =
    el instanceof HTMLTextAreaElement
      ? HTMLTextAreaElement.prototype
      : HTMLInputElement.prototype;
  const setter = Object.getOwnPropertyDescriptor(proto, "value")?.set;
  await act(async () => {
    setter?.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  currentUser = {
    id: "u-dev",
    kind: "human",
    platformRole: "user",
    displayName: "开发者小赵",
    email: "dev@example.test",
    createdAt: "2026-10-30T00:00:00Z",
  };
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => {
    root.unmount();
  });
  host.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("PR-4 admin marketplace governance, developer portal UX, and read-preservation", () => {
  it("renders review queue first in PlatformMarketplace with search, 7 status filters, pagination, and toast feedback", async () => {
    currentUser = { ...currentUser, id: "u-admin", platformRole: "admin" };
    const manyApps: DeveloperMarketplaceApp[] = Array.from({ length: 23 }, (_, idx) =>
      makeDevApp({
        id: `app-${idx + 1}`,
        slug: `app-slug-${idx + 1}`,
        name: `应用 ${idx + 1}`,
        draftName: `应用 ${idx + 1}`,
        ownerDisplayName: idx === 0 ? "星辰工作室" : "普通团队",
        isOfficial: idx === 1,
        isPublished: idx > 2,
        publishedVersion: idx > 2 ? 1 : 0,
        reviewStatus:
          idx === 0
            ? "pending_review"
            : idx === 2
              ? "rejected"
              : idx > 2
                ? "approved"
                : "draft",
        adminSuspended: idx === 22,
      }),
    );

    vi.spyOn(api, "adminMarketplaceApps").mockResolvedValue({ apps: manyApps });
    vi.spyOn(api, "updateAdminMarketplaceApp").mockResolvedValue({
      app: { ...manyApps[0]!, enabled: false },
    });
    const toastSuccessSpy = vi.spyOn(toast, "success");

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => {
      root.render(
        <QueryClientProvider client={qc}>
          <MemoryRouter>
            <PlatformMarketplace />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    await flush();

    // Official app creation form is not inline before clicking 上架官方应用
    expect(host.querySelector("form")).toBeNull();

    // All 7 filter pills exist with aria-pressed
    const filterGroup = host.querySelector('[aria-label="筛选应用状态"]')!;
    const filterButtons = [...filterGroup.querySelectorAll("button")];
    expect(filterButtons.map((b) => b.textContent?.replace(/\s*\(\d+\)/, "").trim())).toEqual([
      "全部",
      "待审核",
      "已上架",
      "已驳回",
      "草稿",
      "官方",
      "已封禁",
    ]);
    expect(filterButtons[0]?.getAttribute("aria-pressed")).toBe("true");
    expect(filterButtons[1]?.textContent).toContain("待审核 (1)");

    // Bounded pagination shows 20 rows initially + 加载更多
    expect(host.querySelectorAll(".member-directory-row")).toHaveLength(20);
    const loadMoreBtn = [...host.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("加载更多"),
    )!;
    await act(async () => {
      loadMoreBtn.click();
    });
    expect(host.querySelectorAll(".member-directory-row")).toHaveLength(23);

    // Search filters by publisher / slug / id
    const searchInput = host.querySelector<HTMLInputElement>(
      'input[aria-label="搜索应用目录"]',
    )!;
    await typeField(searchInput, "星辰工作室");
    expect(host.querySelectorAll(".member-directory-row")).toHaveLength(1);

    // Row toggle emits toast.success
    const toggleBtn = [
      ...host.querySelectorAll<HTMLButtonElement>(".member-directory-row button"),
    ].find((b) => b.textContent?.includes("停用"))!;
    await act(async () => {
      toggleBtn.click();
    });
    await flush();
    expect(toastSuccessSpy).toHaveBeenCalledWith(expect.stringContaining("已停用"));
  });

  it("protects dirty draft edits on background refetch, warns on unsaved probe URLs, and decouples enabled toggle", async () => {
    let serverApp = makeDevApp({
      isPublished: true,
      publishedVersion: 2,
      reviewStatus: "draft",
    });
    vi.spyOn(api, "developerMarketplaceApp").mockImplementation(async () => ({
      app: serverApp,
      reviews: [],
    }));
    const updateSpy = vi
      .spyOn(api, "updateDeveloperMarketplaceApp")
      .mockImplementation(async (_id, patch) => {
        serverApp = {
          ...serverApp,
          draftName: patch.name ?? serverApp.draftName,
          draftDescription: patch.description ?? serverApp.draftDescription,
          draftWebUrl: patch.webUrl ?? serverApp.draftWebUrl,
          draftMcpUrl: patch.mcpUrl ?? serverApp.draftMcpUrl,
          enabled: patch.enabled ?? serverApp.enabled,
          updatedAt: "2026-10-30T05:00:00Z",
        };
        return { app: serverApp };
      });
    const verifySpy = vi
      .spyOn(api, "verifyDeveloperMarketplaceApp")
      .mockResolvedValue({ app: serverApp, verification: sampleVerification });

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => {
      root.render(
        <QueryClientProvider client={qc}>
          <MemoryRouter initialEntries={["/app/developers/apps/app-1"]}>
            <DeveloperPortal appId="app-1" workspaces={[ownedWorkspace]} />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    await flush();

    // Edit draft name and Web URL locally
    const nameInput = host.querySelectorAll<HTMLInputElement>("form.settings-form input")[0]!;
    const webInput = host.querySelectorAll<HTMLInputElement>('input[type="url"]')[0]!;
    await typeField(nameInput, "我的未保存本地草稿");
    await typeField(webInput, "https://next.example.test/ui");

    // Probe section warns about unsaved URL edits and offers 保存并检测
    expect(host.textContent).toContain("检测将连接已保存的草稿地址");
    expect(host.textContent).toContain("当前地址修改尚未保存，检测仍会使用上次保存的地址");

    // Simulate background refetch with newer server updatedAt -> preserves local edits!
    serverApp = {
      ...serverApp,
      draftName: "远端新草稿名",
      updatedAt: "2026-10-30T04:00:00Z",
    };
    await act(async () => {
      await qc.invalidateQueries({ queryKey: ["developerMarketplaceApp", "app-1"] });
    });
    await flush();

    expect(nameInput.value).toBe("我的未保存本地草稿");
    expect(host.textContent).toContain("服务端草稿已更新");

    // Clicking 保存并检测 saves draft (without enabled) and runs live verification
    const saveVerifyBtn = [...host.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("保存并检测"),
    )!;
    await act(async () => {
      saveVerifyBtn.click();
    });
    await flush();
    expect(updateSpy).toHaveBeenCalledWith("app-1", {
      name: "我的未保存本地草稿",
      slug: "board-game",
      description: "草稿描述",
      webUrl: "https://next.example.test/ui",
      mcpUrl: "https://board.example.test/mcp",
    });
    expect(verifySpy).toHaveBeenCalledWith("app-1");

    // Disabling a published app requires ConfirmDialog and remains operable during pending_review
    serverApp = { ...serverApp, reviewStatus: "pending_review", updatedAt: "2026-10-30T06:00:00Z" };
    await act(async () => {
      await qc.invalidateQueries({ queryKey: ["developerMarketplaceApp", "app-1"] });
    });
    await flush();

    const switchInput = host.querySelector<HTMLInputElement>(
      'section[aria-label="应用启用状态"] input[type="checkbox"]',
    )!;
    expect(switchInput.disabled).toBe(false);
    await act(async () => {
      switchInput.click();
    });
    expect(document.body.textContent).toContain("确认停用");
    const confirmDisableBtn = [...document.body.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "确认停用",
    )!;
    await act(async () => {
      confirmDisableBtn.click();
    });
    await flush();
    expect(updateSpy).toHaveBeenLastCalledWith("app-1", { enabled: false });
  });

  it("validates contract slug rules, catches clipboard errors, deep-links handbook tabs, and scrubs internal names", async () => {
    vi.spyOn(api, "developerMarketplaceApps").mockResolvedValue({ apps: [] });
    const createSpy = vi.spyOn(api, "createDeveloperMarketplaceApp");

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => {
      root.render(
        <QueryClientProvider client={qc}>
          <MemoryRouter initialEntries={["/app/developers"]}>
            <DeveloperPortal workspaces={[ownedWorkspace]} />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    await flush();

    const inputs = [...host.querySelectorAll<HTMLInputElement>("form.settings-form input")];
    expect(inputs[2]?.maxLength).toBe(500);
    await typeField(inputs[0]!, "测试应用");
    await typeField(inputs[1]!, "-bad-slug");

    const form = host.querySelector<HTMLFormElement>("form.settings-form")!;
    await act(async () => {
      form.requestSubmit();
    });
    expect(createSpy).not.toHaveBeenCalled();
    expect(host.textContent).toContain("以字母或数字开头");

    // Deep-link DeveloperHandbook to ?tab=mcp and navigate via ArrowRight / Home
    await act(async () => {
      root.render(
        <QueryClientProvider key="handbook" client={qc}>
          <MemoryRouter key="handbook" initialEntries={["/app/developers/docs?tab=mcp"]}>
            <DeveloperPortal isDocsPage={true} />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    await flush();

    const activeTab = host.querySelector<HTMLButtonElement>('[role="tab"][aria-selected="true"]')!;
    expect(activeTab.textContent).toContain("MCP Server 规范");
    expect(host.querySelector('[role="tabpanel"]')?.textContent).toContain("2025-03-26");
    expect(host.textContent).not.toMatch(/Eino|TEAMIE_APP_NOTIFICATION|runtime_inputs|Agent Runtime/);

    // Clipboard failure emits toast.error instead of unhandled rejection
    const toastErrSpy = vi.spyOn(toast, "error");
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: vi.fn().mockRejectedValue(new Error("denied")) },
    });
    const copyCodeBtn = [...host.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("复制代码"),
    )!;
    await act(async () => {
      copyCodeBtn.click();
    });
    await flush();
    expect(toastErrSpy).toHaveBeenCalled();

    // ArrowRight moves from tab 5 (mcp) to tab 6 (fog)
    await act(async () => {
      activeTab.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    });
    expect(
      host.querySelector('[role="tab"][aria-selected="true"]')?.textContent,
    ).toContain("视野隔离与防作弊");
  });

  it("preserves read-only ConversationAppsPane when marketplace is disabled and shows frame loading + keyboard tabs when enabled", async () => {
    const installedApps: MarketplaceApp[] = [
      {
        id: "app-a",
        slug: "chess-club",
        name: "国际象棋",
        description: "双人对弈面板",
        webUrl: "https://chess.example.test/ui",
        mcpUrl: "https://chess.example.test/mcp",
        enabled: true,
        createdAt: "",
        updatedAt: "",
      },
      {
        id: "app-b",
        slug: "poll-box",
        name: "群投票",
        description: "匿名投票面板",
        webUrl: "https://poll.example.test/ui",
        mcpUrl: "https://poll.example.test/mcp",
        enabled: true,
        createdAt: "",
        updatedAt: "",
      },
    ];
    vi.spyOn(api, "conversationApps").mockResolvedValue({ apps: installedApps });
    let resolveSession!: (s: ConversationAppSessionResp) => void;
    const sessionSpy = vi.spyOn(api, "createConversationAppSession").mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveSession = resolve;
        }),
    );

    // 1. Disabled capability -> read-only list, no + button, zero session POSTs
    const qcDisabled = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    qcDisabled.setQueryData(["capability", "im.marketplace.enabled", currentUser.id], false);
    await act(async () => {
      root.render(
        <QueryClientProvider client={qcDisabled}>
          <ConversationAppsPane
            overlayMembers={false}
            mobile={false}
            sidebar={false}
            workspace={ownedWorkspace}
            conversation={sampleGroup}
            user={currentUser}
            owner={true}
            onClose={() => {}}
            onOpenWorkspaceApps={() => {}}
          />
        </QueryClientProvider>,
      );
    });
    await flush();

    expect(host.textContent).toContain("平台当前已关闭应用功能，已安装应用仅展示只读信息。");
    expect(host.textContent).toContain("国际象棋");
    expect(host.textContent).toContain("群投票");
    expect(host.querySelector('[aria-label="管理工作空间应用"]')).toBeNull();
    expect(sessionSpy).not.toHaveBeenCalled();

    // 2. Live capability flip to true -> shows 正在启动应用会话… while session is pending + arrow-key tabs
    await act(async () => {
      qcDisabled.setQueryData(
        ["capability", "im.marketplace.enabled", currentUser.id],
        true,
      );
    });
    await flush();

    expect(host.textContent).toContain("正在启动应用会话…");
    expect(host.querySelector('[role="tabpanel"]')).not.toBeNull();

    const firstTab = host.querySelector<HTMLButtonElement>('#mp-pane-tab-app-a')!;
    await act(async () => {
      firstTab.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    });
    expect(host.querySelector('#mp-pane-tab-app-b')?.getAttribute("aria-selected")).toBe("true");

    // Cleanly settle in-flight session
    await act(async () => {
      resolveSession({
        app: installedApps[1]!,
        token: "tok",
        expiresAt: "2026-10-30T05:00:00Z",
      });
    });
  });
});
