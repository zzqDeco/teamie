// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type { Agent, ModelCatalog, ModelGateway, User, Workspace } from "@teamie/api-client";
import { AgentProfileDialog, WorkspaceChips } from "./agents/agent-profile-dialog";
import { ProfileSettings } from "./dialogs";
import { ModelGatewaySettings } from "./model-gateway";

let currentUser: User = {
  id: "u-admin",
  kind: "human",
  platformRole: "admin",
  displayName: "管理员",
  email: "admin@example.test",
  createdAt: "2026-10-30T00:00:00Z",
};
const updateUserSpy = vi.fn();
let logoutImpl: () => Promise<void> = async () => {};

vi.mock("./auth", () => ({
  useAuth: () => ({
    user: currentUser,
    updateUser: updateUserSpy,
    logout: () => logoutImpl(),
  }),
}));

vi.mock("./chat", () => ({
  useChat: () => ({
    data: {
      capabilities: { "agents.enabled": true },
    },
  }),
}));

const sampleWorkspace: Workspace = {
  id: "ws-1",
  name: "研发空间",
  createdBy: "u-admin",
  createdAt: "2026-10-30T00:00:00Z",
};

const sampleAgent: Agent = {
  id: "ag-1",
  principalId: "ag-1",
  ownerId: "u-admin",
  ownerName: "管理员",
  name: "架构助手",
  description: "协助审查契约",
  modelId: "m-saved",
  workspaceIds: ["ws-1"],
  enabled: true,
  createdAt: "2026-10-30T00:00:00Z",
  updatedAt: "2026-10-30T00:00:00Z",
};

const baseGateway: ModelGateway = {
  routing: { defaultModelId: "m-disabled", effectiveModelId: "m-active" },
  status: "ready",
  webSearch: {
    provider: "tavily",
    hasApiKey: true,
    enabled: true,
    configured: true,
  },
  config: {
    baseUrl: "https://llm.example.test/v1",
    model: "gpt-active",
    hasApiKey: true,
    enabled: true,
    configured: true,
    contextWindowTokens: 64000,
    updatedAt: "2026-10-30T00:00:00Z",
  },
  providers: [
    {
      id: "p-1",
      displayName: "主服务商",
      baseUrl: "https://llm.example.test/v1",
      hasApiKey: true,
      enabled: true,
      configured: true,
      modelCount: 2,
      updatedAt: "2026-10-30T00:00:00Z",
    },
  ],
  models: [
    {
      id: "m-active",
      providerId: "p-1",
      providerDisplayName: "主服务商",
      providerEnabled: true,
      displayName: "主力模型 Pro",
      description: "",
      baseUrl: "https://llm.example.test/v1",
      model: "gpt-active",
      contextWindowTokens: 64000,
      hasApiKey: true,
      enabled: true,
      effectiveEnabled: true,
      configured: true,
      isDefault: false,
      updatedAt: "2026-10-30T00:00:00Z",
    },
    {
      id: "m-disabled",
      providerId: "p-1",
      providerDisplayName: "主服务商",
      providerEnabled: true,
      displayName: "旧默认模型",
      description: "",
      baseUrl: "https://llm.example.test/v1",
      model: "gpt-old",
      contextWindowTokens: 32000,
      hasApiKey: true,
      enabled: false,
      effectiveEnabled: false,
      configured: true,
      isDefault: true,
      updatedAt: "2026-10-30T00:00:00Z",
    },
  ],
};

let host: HTMLDivElement;
let root: Root;

async function flush() {
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });
}

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  currentUser = {
    id: "u-admin",
    kind: "human",
    platformRole: "admin",
    displayName: "管理员",
    email: "admin@example.test",
    createdAt: "2026-10-30T00:00:00Z",
  };
  logoutImpl = async () => {};
  updateUserSpy.mockReset();
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

describe("PR-4 management primitives, gateway UX, and Agent catalog states", () => {
  it("decouples ProfileSettings save busy state from sign-out button", async () => {
    let resolveSave!: (res: { user: User }) => void;
    vi.spyOn(api, "profile").mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveSave = resolve;
        }),
    );

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => {
      root.render(
        <QueryClientProvider client={qc}>
          <MemoryRouter initialEntries={["/app/settings"]}>
            <ProfileSettings />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });

    const input = host.querySelector<HTMLInputElement>("form.settings-form input")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(
        input,
        "新昵称",
      );
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });

    const form = host.querySelector<HTMLFormElement>("form.settings-form")!;
    await act(async () => {
      form.requestSubmit();
    });

    const saveBtn = [...host.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("保存更改"),
    )!;
    const logoutBtn = [...host.querySelectorAll("button")].find((b) =>
      b.textContent?.includes("退出登录"),
    )!;

    expect(saveBtn.querySelector(".spin")).not.toBeNull();
    expect(logoutBtn.querySelector(".spin")).toBeNull();

    await act(async () => {
      resolveSave({ user: { ...currentUser, displayName: "新昵称" } });
    });
    await flush();
    expect(saveBtn.querySelector(".spin")).toBeNull();
  });

  it("deduplicates gateway errors, surfaces effective fallback routing, and warns before setting a disabled default", async () => {
    vi.spyOn(api, "modelGateway").mockResolvedValue(baseGateway);
    const updateRoutingSpy = vi
      .spyOn(api, "saveModelRouting")
      .mockRejectedValueOnce(new Error("routing_failed"))
      .mockResolvedValue({
        ...baseGateway,
        routing: { defaultModelId: "m-disabled", effectiveModelId: "m-active" },
      });

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => {
      root.render(
        <QueryClientProvider client={qc}>
          <MemoryRouter>
            <ModelGatewaySettings />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    await flush();

    // Fallback routing banner surfaces both configured default and effective fallback model
    const banner = host.querySelector(".gateway-routing-banner");
    expect(banner?.textContent).toContain("默认路由已触发自动回退");
    expect(banner?.textContent).toContain("主力模型 Pro");
    expect(banner?.textContent).toContain("旧默认模型");

    // Trigger page-level routing error (on active model 设为默认) and ensure it is NOT duplicated inside web search form
    const setActiveDefaultBtn = [...host.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "设为默认",
    )!;
    await act(async () => {
      setActiveDefaultBtn.click();
    });
    await flush();

    const matchingNotices = [...host.querySelectorAll(".notice")].filter((el) =>
      el.textContent?.includes("模型网关请求未完成"),
    );
    expect(matchingNotices).toHaveLength(1);
    const searchForm = host.querySelector("form")!;
    expect(searchForm.textContent).not.toContain("模型网关请求未完成");

    // Switch default to m-active so m-disabled shows 设为默认 -> clicking 设为默认 on disabled model requires confirmation
    vi.spyOn(api, "modelGateway").mockResolvedValue({
      ...baseGateway,
      routing: { defaultModelId: "m-active", effectiveModelId: "m-active" },
      models: baseGateway.models!.map((m) => ({
        ...m,
        isDefault: m.id === "m-active",
      })),
    });
    await qc.invalidateQueries({ queryKey: ["modelGateway"] });
    await flush();

    updateRoutingSpy.mockClear();
    const setDefaultBtn = [...host.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "设为默认",
    )!;
    await act(async () => {
      setDefaultBtn.click();
    });
    expect(updateRoutingSpy).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("仍然设为默认");

    const confirmBtn = [...document.body.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "仍然设为默认",
    )!;
    await act(async () => {
      confirmBtn.click();
    });
    await flush();
    expect(updateRoutingSpy).toHaveBeenCalledWith({
      defaultModelId: "m-disabled",
    });
  });

  it("distinguishes loading, error, and empty model catalog in AgentProfileDialog without false offline labels", async () => {
    // 1. Loading state: never falsely labels saved modelId as 已下线
    let resolveCatalog!: (data: ModelCatalog) => void;
    const catalogSpy = vi.spyOn(api, "modelCatalog").mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveCatalog = resolve;
        }),
    );

    const qc1 = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => {
      root.render(
        <QueryClientProvider client={qc1}>
          <MemoryRouter>
            <AgentProfileDialog
              agent={sampleAgent}
              workspaces={[sampleWorkspace]}
              onClose={() => {}}
            />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    expect(document.body.textContent).toContain("正在加载平台模型目录");
    expect(document.body.textContent).not.toContain("已下线");

    // Resolve with empty catalog for admin -> shows hint + gateway link
    await act(async () => {
      resolveCatalog({ models: [] });
    });
    await flush();
    expect(document.body.textContent).toContain(
      "平台暂未配置可用模型，配置完成前 Agent 暂时无法回复。",
    );
    expect(
      document.body.querySelector('a[href="/app/admin/gateway"]')?.textContent,
    ).toContain("前往模型网关配置");

    // 2. Non-admin user on empty catalog -> hides /app/admin/gateway link
    currentUser = { ...currentUser, id: "u-member", platformRole: "user" };
    catalogSpy.mockResolvedValueOnce({ models: [] });
    const qc2 = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => {
      root.render(
        <QueryClientProvider key="member-empty" client={qc2}>
          <MemoryRouter>
            <AgentProfileDialog
              agent={{ ...sampleAgent, ownerId: "u-member", modelId: undefined }}
              workspaces={[sampleWorkspace]}
              onClose={() => {}}
            />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    await flush();
    expect(document.body.textContent).toContain(
      "平台暂未配置可用模型，配置完成前 Agent 暂时无法回复。",
    );
    expect(document.body.querySelector('a[href="/app/admin/gateway"]')).toBeNull();

    // 3. Error state -> shows retry notice without false 已下线 label
    catalogSpy.mockRejectedValueOnce(new Error("目录服务暂不可达"));
    const qc3 = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => {
      root.render(
        <QueryClientProvider key="member-error" client={qc3}>
          <MemoryRouter>
            <AgentProfileDialog
              agent={{ ...sampleAgent, ownerId: "u-member" }}
              workspaces={[sampleWorkspace]}
              onClose={() => {}}
            />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    await flush();
    expect(document.body.textContent).toContain("模型目录暂时无法加载，已保留当前模型选择。");
    expect(document.body.textContent).not.toContain("已下线");

    // 4. Zero-workspace chip uses EyeOff icon
    await act(async () => {
      root.render(
        <MemoryRouter>
          <WorkspaceChips ids={[]} workspaces={[sampleWorkspace]} />
        </MemoryRouter>,
      );
    });
    expect(host.querySelector(".lucide-eye-off")).not.toBeNull();
    expect(host.textContent).toContain("未选择工作空间");
  });
});
