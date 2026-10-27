// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type { ModelGateway, User } from "@teamie/api-client";
import { WebSearchConfigurationFields } from "./agent-ui";
import type { WebSearchFormValue } from "./agent-ui";
import { ModelGatewaySettings } from "./model-gateway";

const admin: User = {
  id: "admin",
  kind: "human",
  platformRole: "admin",
  displayName: "平台管理员",
  email: "admin@example.test",
  createdAt: "2026-10-16T00:00:00Z",
};
vi.mock("./auth", () => ({ useAuth: () => ({ user: admin }) }));

const initial = {
  baseUrl: "https://model.example.test/v1",
  model: "model-id",
  hasApiKey: true,
  enabled: true,
  contextWindowTokens: 16384,
  webSearch: {
    provider: "tavily",
    hasApiKey: true,
    enabled: true,
    configured: true,
  },
};
const gateway: ModelGateway = {
  routing: { defaultModelId: "default", effectiveModelId: "default" },
  webSearch: initial.webSearch,
  status: "ready",
  config: {
    ...initial,
    configured: true,
    updatedAt: "2026-10-16T00:00:00Z",
  },
};

let host: HTMLDivElement, root: Root;
beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
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

function find<T extends HTMLElement>(selector: string): T {
  const node = document.body.querySelector<T>(selector);
  if (!node) throw new Error(`Missing control: ${selector}`);
  return node;
}
function buttonByText(text: string) {
  const button = [...document.body.querySelectorAll("button")].find(
    (node) => node.textContent?.trim() === text,
  );
  if (!button) throw new Error(`Missing button: ${text}`);
  return button;
}
async function type(selector: string, value: string) {
  const node = find<HTMLInputElement>(selector);
  await act(async () => {
    Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      "value",
    )!.set!.call(node, value);
    node.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function click(node: HTMLElement) {
  await act(async () => {
    node.click();
  });
}
async function submit() {
  await act(async () => {
    find<HTMLFormElement>(".gateway-configuration-form").requestSubmit();
  });
}
async function renderFields(
  onSave: (value: WebSearchFormValue) => Promise<boolean>,
) {
  await act(async () => {
    root.render(
      <WebSearchConfigurationFields
        initial={initial.webSearch}
        onSave={onSave}
      />,
    );
  });
}

describe("independent web search form", () => {
  it("keeps a typed search key after failed save and clears it only after success", async () => {
    const save = vi
      .fn<(value: WebSearchFormValue) => Promise<boolean>>()
      .mockResolvedValueOnce(false)
      .mockResolvedValueOnce(true);
    await renderFields(save);
    await type('[name="gatewayWebSearchApiKey"]', "search-secret");
    await submit();
    expect(save).toHaveBeenLastCalledWith({
      enabled: true,
      apiKey: "search-secret",
    });
    expect(
      find<HTMLInputElement>('[name="gatewayWebSearchApiKey"]').value,
    ).toBe("search-secret");
    await submit();
    expect(
      find<HTMLInputElement>('[name="gatewayWebSearchApiKey"]').value,
    ).toBe("");
    expect(document.querySelector('[name="gatewayBaseUrl"]')).toBeNull();
  });
  it("clears only the search key after explicit confirmation", async () => {
    const save = vi
      .fn<(value: WebSearchFormValue) => Promise<boolean>>()
      .mockResolvedValue(true);
    await renderFields(save);
    await click(find('[name="gatewayClearWebSearchApiKey"]'));
    await submit();
    expect(save).not.toHaveBeenCalled();
    expect(find('[role="dialog"]').textContent).not.toContain("模型");
    await click(buttonByText("确认保存"));
    expect(save).toHaveBeenLastCalledWith({
      enabled: true,
      apiKey: "",
      clearApiKey: true,
    });
  });
});

describe("model gateway settings page", () => {
  async function renderSettings() {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    await act(async () => {
      root.render(
        <QueryClientProvider client={client}>
          <MemoryRouter>
            <ModelGatewaySettings />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
  }

  it("reports test results with success or error styling and separate busy states", async () => {
    vi.spyOn(api, "modelGateway").mockResolvedValue(gateway);
    let finishTest!: (value: {
      ok: boolean;
      model: string;
      latencyMs: number;
    }) => void;
    vi.spyOn(api, "testModelGateway").mockImplementation(
      () =>
        new Promise((resolve) => {
          finishTest = resolve;
        }),
    );
    await renderSettings();
    expect(find(".gateway-saved-state").textContent).toContain(
      "连接尚需测试验证",
    );
    const testButton = buttonByText("测试连接");
    await click(testButton);
    // Only the clicked action shows progress; Save is locked, not spinning.
    expect(testButton.querySelector(".spin")).not.toBeNull();
    const saveButton = buttonByText("保存搜索设置");
    expect(saveButton.disabled).toBe(false);
    expect(saveButton.querySelector(".spin")).toBeNull();
    await act(async () => {
      finishTest({ ok: true, model: "model-id", latencyMs: 120 });
    });
    expect(find(".notice.success").textContent).toContain("连接测试通过");
    expect(find(".gateway-saved-state").textContent).toContain(
      "连接测试已通过",
    );
    expect(buttonByText("保存搜索设置").disabled).toBe(false);

    await click(buttonByText("测试连接"));
    await act(async () => {
      finishTest({ ok: false, model: "model-id", latencyMs: 0 });
    });
    const failure = find(".notice.error");
    expect(failure.getAttribute("role")).toBe("alert");
    expect(failure.textContent).toContain("连接测试未通过");
    expect(find(".gateway-saved-state").textContent).toContain(
      "连接尚需测试验证",
    );
  });

  it("saves only search fields while the configured default is disabled and a fallback is active", async () => {
    const disabledDefault = {
      ...gateway,
      routing: { defaultModelId: "a", effectiveModelId: "b" },
    };
    vi.spyOn(api, "modelGateway").mockResolvedValue(disabledDefault);
    const search = vi
      .spyOn(api, "saveWebSearch")
      .mockResolvedValue(disabledDefault);
    const model = vi.spyOn(api, "updateModelProfile");
    const routing = vi.spyOn(api, "saveModelRouting");
    await renderSettings();
    await type('[name="gatewayWebSearchApiKey"]', "new-search-key");
    await submit();
    expect(search).toHaveBeenCalledWith({
      enabled: true,
      apiKey: "new-search-key",
    });
    expect(model).not.toHaveBeenCalled();
    expect(routing).not.toHaveBeenCalled();
  });

  it("renders multi-model catalog profiles and supports creating, setting default, testing, and deleting models", async () => {
    const multiGateway: ModelGateway = {
      ...gateway,
      defaultModelId: "default",
      models: [
        {
          id: "default",
          displayName: "GPT-4o",
          description: "通用默认模型",
          baseUrl: "https://model.example.test/v1",
          model: "gpt-4o",
          contextWindowTokens: 16384,
          hasApiKey: true,
          enabled: true,
          configured: true,
          isDefault: true,
          updatedAt: "2026-10-16T00:00:00Z",
        },
        {
          id: "deepseek-r1",
          displayName: "DeepSeek R1",
          description: "深度推理模型",
          baseUrl: "https://deepseek.example.test/v1",
          model: "deepseek-reasoner",
          contextWindowTokens: 65536,
          hasApiKey: true,
          enabled: true,
          configured: true,
          isDefault: false,
          updatedAt: "2026-10-16T00:00:00Z",
        },
      ],
    };
    vi.spyOn(api, "modelGateway").mockResolvedValue(multiGateway);
    const updateSpy = vi.spyOn(api, "saveModelRouting").mockResolvedValue({
      ...multiGateway,
      defaultModelId: "deepseek-r1",
    });
    const testSpy = vi
      .spyOn(api, "testModelProfile")
      .mockResolvedValue({
        ok: true,
        model: "deepseek-reasoner",
        latencyMs: 45,
      });
    const deleteSpy = vi
      .spyOn(api, "deleteModelProfile")
      .mockResolvedValue(gateway);
    await renderSettings();
    expect(find(".gateway-models-section").textContent).toContain(
      "DeepSeek R1",
    );
    await click(buttonByText("设为默认"));
    expect(updateSpy).toHaveBeenCalledWith({ defaultModelId: "deepseek-r1" });
    const testButtons = [...document.body.querySelectorAll("button")].filter(
      (b) => b.textContent?.trim() === "测试此模型",
    );
    await click(testButtons[1]!);
    expect(testSpy).toHaveBeenCalledWith("deepseek-r1");
    expect(find(".notice.success").textContent).toContain("deepseek-reasoner");
    const deleteButtons = [...document.body.querySelectorAll("button")].filter(
      (b) => b.textContent?.trim() === "移除",
    );
    await click(deleteButtons[1]!);
    expect(deleteSpy).toHaveBeenCalledWith("deepseek-r1");
  });
});
