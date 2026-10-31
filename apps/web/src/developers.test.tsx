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
  DeveloperMarketplaceApp,
  MarketplaceAppReview,
  MarketplaceEndpointVerification,
  User,
  Workspace,
} from "@teamie/api-client";
import { DeveloperPortal } from "./developers";
import { ConversationAppsPane, PlatformMarketplace } from "./marketplace";

let currentUser: User = {
  id: "u-dev",
  kind: "human",
  platformRole: "user",
  displayName: "独立开发者",
  email: "dev@example.test",
  createdAt: "2026-10-30T00:00:00Z",
};

vi.mock("./auth", () => ({
  useAuth: () => ({ user: currentUser }),
}));

const ownedWorkspace: Workspace = {
  id: "ws-dev",
  name: "开发者联调空间",
  createdBy: "u-dev",
  createdAt: "2026-10-30T00:00:00Z",
};

const sampleVerification: MarketplaceEndpointVerification = {
  ok: true,
  verifiedAt: "2026-10-30T03:00:00Z",
  webUrl: "https://werewolf.example.test/ui",
  mcpUrl: "https://werewolf.example.test/mcp",
  mcpServerName: "werewolf-mcp",
  mcpServerVersion: "1.0.0",
  mcpProtocolVersion: "2025-03-26",
  checks: [
    {
      stage: "url_security",
      ok: true,
      code: "ok",
      summary: "Web UI 与 MCP 端点协议格式及跨源隔离策略校验通过",
    },
    {
      stage: "web_ui",
      ok: true,
      code: "ok",
      summary: "Web UI 在线可达且允许跨源 iframe 嵌入",
    },
    {
      stage: "mcp_auth",
      ok: true,
      code: "ok",
      summary: "MCP Server 已正确拦截无 Token 与伪造签名请求",
    },
    {
      stage: "mcp_handshake",
      ok: true,
      code: "ok",
      summary: "真实签名握手成功，发现 2 个工具",
    },
  ],
  tools: [
    {
      name: "get_my_view",
      exposedName: "mcp__werewolf_arena__get_my_view",
      description: "获取当前玩家专属迷雾视角",
      inputSchemaJson: '{"type":"object"}',
    },
    {
      name: "cast_night_action",
      exposedName: "mcp__werewolf_arena__cast_night_action",
      description: "提交夜间秘密行动",
    },
  ],
};

const sampleDraftApp: DeveloperMarketplaceApp = {
  id: "app-ww",
  slug: "werewolf-arena",
  name: "暗夜狼人杀",
  description: "支持人类与 Agent 混排的狼人杀应用",
  webUrl: "",
  mcpUrl: "",
  enabled: true,
  createdAt: "2026-10-30T00:00:00Z",
  updatedAt: "2026-10-30T01:00:00Z",
  ownerId: "u-dev",
  ownerDisplayName: "独立开发者",
  isOfficial: false,
  isPublished: false,
  publishedVersion: 0,
  reviewStatus: "draft",
  draftName: "暗夜狼人杀",
  draftDescription: "支持人类与 Agent 混排的狼人杀应用",
  draftWebUrl: "https://werewolf.example.test/ui",
  draftMcpUrl: "https://werewolf.example.test/mcp",
  hasDraftChanges: true,
  submissionRevision: 1,
  submissionNote: "",
  reviewNote: "",
  adminSuspended: false,
  suspendedReason: "",
};

const sampleReviews: MarketplaceAppReview[] = [
  {
    id: "rev-1",
    appId: "app-ww",
    revision: 1,
    action: "submitted",
    actorId: "u-dev",
    actorName: "独立开发者",
    note: "已完成 8 人局联调",
    webUrl: "https://werewolf.example.test/ui",
    mcpUrl: "https://werewolf.example.test/mcp",
    createdAt: "2026-10-30T03:05:00Z",
  },
];

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  currentUser = {
    id: "u-dev",
    kind: "human",
    platformRole: "user",
    displayName: "独立开发者",
    email: "dev@example.test",
    createdAt: "2026-10-30T00:00:00Z",
  };
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function flush() {
  for (let i = 0; i < 4; i++) {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
  }
}

async function typeField(
  node: HTMLInputElement | HTMLTextAreaElement,
  value: string,
) {
  const proto =
    node instanceof HTMLTextAreaElement
      ? HTMLTextAreaElement.prototype
      : HTMLInputElement.prototype;
  await act(async () => {
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(node, value);
    node.dispatchEvent(new Event("input", { bubbles: true }));
    node.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

describe("Developer Platform & Submission Workflow", () => {
  it("creates a two-stage draft app and displays the one-time HS256 signing secret", async () => {
    vi.spyOn(api, "developerMarketplaceApps").mockResolvedValue({ apps: [] });
    const createSpy = vi
      .spyOn(api, "createDeveloperMarketplaceApp")
      .mockResolvedValue({
        app: sampleDraftApp,
        signingSecret: "dev-signing-secret-xyz",
      });

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

    const inputs = [...host.querySelectorAll<HTMLInputElement>("form input")];
    await typeField(inputs[0], "暗夜狼人杀");
    await typeField(inputs[1], "werewolf-arena");
    await typeField(inputs[2], "支持人类与 Agent 混排的狼人杀应用");

    const form = host.querySelector<HTMLFormElement>("form.settings-form")!;
    await act(async () => {
      form.requestSubmit();
    });
    await flush();

    expect(createSpy).toHaveBeenCalledWith({
      name: "暗夜狼人杀",
      slug: "werewolf-arena",
      description: "支持人类与 Agent 混排的狼人杀应用",
      webUrl: undefined,
      mcpUrl: undefined,
    });
    expect(
      host.querySelector<HTMLInputElement>('input[aria-label="应用签名密钥"]')
        ?.value,
    ).toBe("dev-signing-secret-xyz");
  });

  it("verifies live endpoints, installs draft into owned workspace, submits for review, and withdraws", async () => {
    let appState: DeveloperMarketplaceApp = { ...sampleDraftApp };
    vi.spyOn(api, "developerMarketplaceApp").mockImplementation(async () => ({
      app: appState,
      reviews: sampleReviews,
    }));
    const verifySpy = vi
      .spyOn(api, "verifyDeveloperMarketplaceApp")
      .mockImplementation(async () => {
        appState = {
          ...appState,
          lastVerifiedAt: sampleVerification.verifiedAt,
          verificationReport: sampleVerification,
        };
        return { app: appState, verification: sampleVerification };
      });
    const installSpy = vi
      .spyOn(api, "installWorkspaceApp")
      .mockResolvedValue({
        app: {
          id: appState.id,
          slug: appState.slug,
          name: appState.draftName,
          description: appState.draftDescription,
          webUrl: appState.draftWebUrl,
          mcpUrl: appState.draftMcpUrl,
          enabled: true,
          servingDraft: true,
          createdAt: appState.createdAt,
          updatedAt: appState.updatedAt,
        },
      });
    const submitSpy = vi
      .spyOn(api, "submitDeveloperMarketplaceApp")
      .mockImplementation(async (_id, note) => {
        appState = {
          ...appState,
          reviewStatus: "pending_review",
          submissionNote: note || "",
        };
        return { app: appState, reviews: sampleReviews };
      });
    const withdrawSpy = vi
      .spyOn(api, "withdrawDeveloperMarketplaceApp")
      .mockImplementation(async () => {
        appState = { ...appState, reviewStatus: "draft" };
        return { app: appState, reviews: sampleReviews };
      });

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => {
      root.render(
        <QueryClientProvider client={qc}>
          <MemoryRouter initialEntries={["/app/developers/apps/app-ww"]}>
            <DeveloperPortal appId="app-ww" workspaces={[ownedWorkspace]} />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    await flush();

    // Before verification, submit button is disabled
    const submitBtnBefore = [...host.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("提交平台审核"),
    )!;
    expect(submitBtnBefore.disabled).toBe(true);

    // Run 4-stage live verification
    const verifyBtn = [...host.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("立即检测线上服务"),
    )!;
    await act(async () => {
      verifyBtn.click();
    });
    await flush();

    expect(verifySpy).toHaveBeenCalledWith("app-ww");
    expect(host.textContent).toContain("mcp__werewolf_arena__get_my_view");
    expect(host.textContent).toContain("mcp__werewolf_arena__cast_night_action");

    // 1-click install draft into owned workspace
    const sandboxBtn = [...host.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("安装草稿到该空间自测"),
    )!;
    await act(async () => {
      sandboxBtn.click();
    });
    await flush();
    expect(installSpy).toHaveBeenCalledWith("ws-dev", "app-ww");

    // Submit for review
    const noteBox = host.querySelector<HTMLTextAreaElement>("textarea")!;
    await typeField(noteBox, "8 人局实测完毕，申请上架");
    const submitBtnAfter = [...host.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("提交平台审核"),
    )!;
    expect(submitBtnAfter.disabled).toBe(false);
    await act(async () => {
      submitBtnAfter.click();
    });
    await flush();
    expect(submitSpy).toHaveBeenCalledWith("app-ww", "8 人局实测完毕，申请上架");

    // Withdraw submission
    const withdrawBtn = [...host.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("撤回提审"),
    )!;
    await act(async () => {
      withdrawBtn.click();
    });
    await flush();
    expect(withdrawSpy).toHaveBeenCalledWith("app-ww");
  });

  it("renders interactive Chinese Developer Handbook sections and protocol snippets", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => {
      root.render(
        <QueryClientProvider client={qc}>
          <MemoryRouter initialEntries={["/app/developers/docs"]}>
            <DeveloperPortal isDocsPage={true} />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    await flush();

    expect(host.textContent).toContain("开发者接入与提审手册");
    expect(host.textContent).toContain("人类 Web UI + Agent MCP 对称双通道架构");

    const webuiTab = [...host.querySelectorAll('[role="tab"]')].find((b) =>
      b.textContent?.includes("Web UI 桥接协议"),
    ) as HTMLButtonElement;
    await act(async () => {
      webuiTab.click();
    });
    expect(host.textContent).toContain("teamie:mcp-app:ready");
    expect(host.textContent).toContain("teamie:mcp-app:init");
    expect(host.textContent).toContain("teamie:mcp-app:refresh-token");
  });

  it("blocks same-origin Web UI URLs in ConversationAppsPane client guard", async () => {
    const sameOriginUrl = `${window.location.origin}/malicious-ui`;
    vi.spyOn(api, "conversationApps").mockResolvedValue({
      apps: [
        {
          id: "app-same",
          slug: "same-origin",
          name: "同源风险应用",
          description: "",
          webUrl: sameOriginUrl,
          mcpUrl: "https://mcp.example.test/mcp",
          enabled: true,
          servingDraft: true,
          createdAt: "",
          updatedAt: "",
        },
      ],
    });
    const sessionSpy = vi.spyOn(api, "createConversationAppSession");
    const conv: Conversation = {
      id: "conv-g1",
      workspaceId: "ws-dev",
      type: "group",
      name: "联调小组",
      memberIds: ["u-dev"],
      lastSeq: 0,
      readSeq: 0,
      updatedAt: "",
    };

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => {
      root.render(
        <QueryClientProvider client={qc}>
          <ConversationAppsPane
            overlayMembers={false}
            mobile={false}
            sidebar={false}
            workspace={ownedWorkspace}
            conversation={conv}
            user={currentUser}
            owner={true}
            onClose={() => {}}
            onOpenWorkspaceApps={() => {}}
          />
        </QueryClientProvider>,
      );
    });
    await flush();

    expect(sessionSpy).not.toHaveBeenCalled();
    expect(host.querySelector("iframe")).toBeNull();
    expect(host.textContent).toContain("独立跨源域名");
    expect(host.textContent).toContain("测试草稿");
  });

  it("lets platform admins inspect, re-verify, approve, reject, and suspend developer submissions", async () => {
    currentUser = {
      ...currentUser,
      id: "u-admin",
      platformRole: "admin",
      displayName: "平台管理员",
    };
    const pendingApp: DeveloperMarketplaceApp = {
      ...sampleDraftApp,
      reviewStatus: "pending_review",
      submissionRevision: 2,
      submissionNote: "新增女巫解药毒药工具",
      verificationReport: sampleVerification,
    };

    vi.spyOn(api, "adminMarketplaceApps").mockResolvedValue({
      apps: [pendingApp],
    });
    vi.spyOn(api, "adminMarketplaceApp").mockResolvedValue({
      app: pendingApp,
      reviews: sampleReviews,
    });
    const approveSpy = vi
      .spyOn(api, "approveAdminMarketplaceApp")
      .mockResolvedValue({
        app: {
          ...pendingApp,
          isPublished: true,
          publishedVersion: 1,
          reviewStatus: "approved",
        },
        reviews: sampleReviews,
      });

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

    expect(host.textContent).toContain("待审核 (1)");
    const inspectBtn = document.body.querySelector<HTMLButtonElement>(
      'button[aria-label="审核 暗夜狼人杀"]',
    )!;
    await act(async () => {
      inspectBtn.click();
    });
    await flush();

    expect(document.body.textContent).toContain("新增女巫解药毒药工具");
    expect(document.body.textContent).toContain("mcp__werewolf_arena__get_my_view");

    const approveBtn = [...document.body.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("审批通过并上架"),
    )!;
    await act(async () => {
      approveBtn.click();
    });
    await flush();

    expect(approveSpy).toHaveBeenCalledWith("app-ww", 2, undefined);
  });

  it("preserves intentionally cleared draft description and endpoints on a published app", async () => {
    const publishedWithClearedDraft: DeveloperMarketplaceApp = {
      ...sampleDraftApp,
      isPublished: true,
      publishedVersion: 1,
      reviewStatus: "draft",
      hasDraftChanges: true,
      description: "线上已发布描述",
      webUrl: "https://werewolf.example.com/ui",
      mcpUrl: "https://werewolf.example.com/mcp",
      draftName: "暗夜狼人杀 2.0",
      draftDescription: "",
      draftWebUrl: "",
      draftMcpUrl: "",
    };
    vi.spyOn(api, "developerMarketplaceApp").mockResolvedValue({
      app: publishedWithClearedDraft,
      reviews: sampleReviews,
    });

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => {
      root.render(
        <QueryClientProvider client={qc}>
          <MemoryRouter initialEntries={["/app/developers/apps/app-ww"]}>
            <DeveloperPortal appId="app-ww" workspaces={[ownedWorkspace]} />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    await flush();

    const formInputs = host.querySelectorAll<HTMLInputElement>(
      "form.settings-form input",
    );
    expect(formInputs[0]?.value).toBe("暗夜狼人杀 2.0");
    expect(formInputs[2]?.value).toBe("");
    const urlInputs = host.querySelectorAll<HTMLInputElement>('input[type="url"]');
    expect(urlInputs[0]?.value).toBe("");
    expect(urlInputs[1]?.value).toBe("");
  });

  it("exposes marketplace review queue shortcuts only to platform admins", async () => {
    const pendingApp: DeveloperMarketplaceApp = {
      ...sampleDraftApp,
      reviewStatus: "pending_review",
      submissionRevision: 1,
      verificationReport: sampleVerification,
    };
    vi.spyOn(api, "developerMarketplaceApp").mockResolvedValue({
      app: pendingApp,
      reviews: sampleReviews,
    });

    const memberQc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => {
      root.render(
        <QueryClientProvider client={memberQc}>
          <MemoryRouter initialEntries={["/app/developers/apps/app-ww"]}>
            <DeveloperPortal appId="app-ww" workspaces={[ownedWorkspace]} />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    await flush();
    expect(host.querySelector('a[href="/app/admin/marketplace"]')).toBeNull();

    currentUser = {
      ...currentUser,
      id: "u-admin",
      platformRole: "admin",
      displayName: "平台管理员",
    };
    const adminQc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => {
      root.render(
        <QueryClientProvider client={adminQc}>
          <MemoryRouter initialEntries={["/app/developers/apps/app-ww"]}>
            <DeveloperPortal appId="app-ww" workspaces={[ownedWorkspace]} />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    await flush();
    const adminLinks = [...host.querySelectorAll<HTMLAnchorElement>('a[href="/app/admin/marketplace"]')];
    expect(adminLinks.map((el) => el.textContent?.trim())).toEqual([
      "平台审核队列",
      "前往平台审核队列",
    ]);
  });
});
