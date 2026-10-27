// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type { ModelGateway, User } from "@teamie/api-client";
import { GatewayConfigurationFields } from "./agent-ui";
import type { GatewayFormValue } from "./agent-ui";
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
  onSave: (value: GatewayFormValue) => Promise<boolean>,
) {
  await act(async () => {
    root.render(<GatewayConfigurationFields initial={initial} onSave={onSave} />);
  });
}

describe("model gateway configuration form", () => {
  it("keeps typed secrets after a failed save and clears them only after success", async () => {
    const onSave = vi
      .fn<(value: GatewayFormValue) => Promise<boolean>>()
      .mockResolvedValueOnce(false)
      .mockResolvedValueOnce(true);
    await renderFields(onSave);
    await type('[name="gatewayApiKey"]', "sk-new-secret");
    await type('[name="gatewayWebSearchApiKey"]', "tvly-new-secret");
    await submit();
    expect(onSave).toHaveBeenLastCalledWith(
      expect.objectContaining({
        apiKey: "sk-new-secret",
        webSearchApiKey: "tvly-new-secret",
      }),
    );
    expect(find<HTMLInputElement>('[name="gatewayApiKey"]').value).toBe(
      "sk-new-secret",
    );
    expect(
      find<HTMLInputElement>('[name="gatewayWebSearchApiKey"]').value,
    ).toBe("tvly-new-secret");
    await submit();
    expect(onSave).toHaveBeenCalledTimes(2);
    expect(find<HTMLInputElement>('[name="gatewayApiKey"]').value).toBe("");
    expect(
      find<HTMLInputElement>('[name="gatewayWebSearchApiKey"]').value,
    ).toBe("");
  });

  it("confirms before disabling the gateway or clearing saved keys", async () => {
    const onSave = vi
      .fn<(value: GatewayFormValue) => Promise<boolean>>()
      .mockResolvedValue(true);
    await renderFields(onSave);
    // An ordinary save of an enabled gateway needs no confirmation.
    await submit();
    expect(onSave).toHaveBeenCalledTimes(1);
    expect(document.querySelector('[role="dialog"]')).toBeNull();

    const enabledSwitch = find<HTMLInputElement>(
      ".agent-enabled-switch input:not([name])",
    );
    await click(enabledSwitch);
    expect(enabledSwitch.checked).toBe(false);
    await submit();
    const dialog = find('[role="dialog"]');
    expect(dialog.textContent).toContain("所有 Agent 将暂停调用模型");
    expect(onSave).toHaveBeenCalledTimes(1);
    await click(buttonByText("取消"));
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(onSave).toHaveBeenCalledTimes(1);

    await click(enabledSwitch);
    await click(find('.gateway-clear-key input[type="checkbox"]'));
    await click(find('[name="gatewayClearWebSearchApiKey"]'));
    await submit();
    const impacts = find('[role="dialog"]').textContent;
    expect(impacts).toContain("清除 API Key");
    expect(impacts).toContain("清除 Tavily API Key");
    expect(impacts).not.toContain("停用模型网关");
    await click(buttonByText("确认保存"));
    expect(onSave).toHaveBeenCalledTimes(2);
    expect(onSave).toHaveBeenLastCalledWith(
      expect.objectContaining({
        enabled: true,
        clearApiKey: true,
        clearWebSearchApiKey: true,
      }),
    );
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it("opens the collapsed advanced section when its required field blocks submit", async () => {
    const onSave = vi
      .fn<(value: GatewayFormValue) => Promise<boolean>>()
      .mockResolvedValue(true);
    await renderFields(onSave);
    const details = find<HTMLDetailsElement>(".gateway-advanced");
    expect(details.open).toBe(false);
    await type('[name="gatewayContextWindow"]', "");
    await submit();
    expect(onSave).not.toHaveBeenCalled();
    expect(details.open).toBe(true);
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
    const saveButton = buttonByText("保存配置");
    expect(saveButton.disabled).toBe(true);
    expect(saveButton.querySelector(".spin")).toBeNull();
    await act(async () => {
      finishTest({ ok: true, model: "model-id", latencyMs: 120 });
    });
    expect(find(".notice.success").textContent).toContain("连接测试通过");
    expect(find(".gateway-saved-state").textContent).toContain(
      "连接测试已通过",
    );
    expect(buttonByText("保存配置").disabled).toBe(false);

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

  it("shows client-side validation copy from the page and keeps the key after a failed save", async () => {
    vi.spyOn(api, "modelGateway").mockResolvedValue(gateway);
    const save = vi.spyOn(api, "saveModelGateway");
    await renderSettings();
    await type('[name="gatewayBaseUrl"]', "https://user:pw@model.example.test/v1");
    await type('[name="gatewayApiKey"]', "sk-keep-me");
    await submit();
    expect(save).not.toHaveBeenCalled();
    expect(find(".gateway-configuration-form .notice.error").textContent).toBe(
      "服务地址不得包含密钥、认证信息或查询参数。",
    );
    expect(find<HTMLInputElement>('[name="gatewayApiKey"]').value).toBe(
      "sk-keep-me",
    );
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
    const updateSpy = vi
      .spyOn(api, "updateModelProfile")
      .mockResolvedValue({
        ...multiGateway,
        defaultModelId: "deepseek-r1",
      });
    const testSpy = vi
      .spyOn(api, "testModelProfile")
      .mockResolvedValue({ ok: true, model: "deepseek-reasoner", latencyMs: 45 });
    const deleteSpy = vi
      .spyOn(api, "deleteModelProfile")
      .mockResolvedValue(gateway);
    await renderSettings();
    expect(find(".gateway-models-section").textContent).toContain("DeepSeek R1");
    await click(buttonByText("设为默认"));
    expect(updateSpy).toHaveBeenCalledWith("deepseek-r1", { isDefault: true });
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
