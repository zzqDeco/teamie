// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api, ApiError } from "@teamie/api-client";
import type { ModelGateway } from "@teamie/api-client";
import { ModelGatewaySettings } from "./model-gateway";

vi.mock("./auth", () => ({ useAuth: () => ({ user: { id: "admin", kind: "human", platformRole: "admin" } }) }));
const original: ModelGateway = {
  routing: { defaultModelId: "m", effectiveModelId: "m" },
  status: "ready",
  webSearch: { provider: "tavily", hasApiKey: false, enabled: false, configured: false },
  config: { baseUrl: "https://old.example.test/v1", model: "model-a", hasApiKey: true, enabled: true, configured: true, contextWindowTokens: 32768, updatedAt: "2026-10-29T00:00:00Z" },
  providers: [{ id: "p", displayName: "Provider", baseUrl: "https://old.example.test/v1", hasApiKey: true, enabled: true, configured: true, modelCount: 1, updatedAt: "2026-10-29T00:00:00Z" }],
  models: [{ id: "m", providerId: "p", providerDisplayName: "Provider", providerEnabled: true, displayName: "Model", description: "", baseUrl: "https://old.example.test/v1", model: "model-a", contextWindowTokens: 32768, hasApiKey: true, enabled: true, effectiveEnabled: true, configured: true, isDefault: true, updatedAt: "2026-10-29T00:00:00Z" }],
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
function button(text: string): HTMLButtonElement {
  const found = [...document.body.querySelectorAll("button")].find(b => b.textContent?.trim() === text);
  if (!found) throw new Error(`Missing button ${text}`);
  return found;
}
async function click(text: string) { await act(async () => button(text).click()); }

type TestResult = { ok: boolean; model: string; latencyMs: number };
function deferred() {
  let resolve!: (result: TestResult) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<TestResult>((ok, fail) => { resolve = ok; reject = fail; });
  return { promise, resolve, reject };
}
async function renderSettings() {
  vi.spyOn(api, "modelGateway").mockResolvedValue(original);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => {
    root.render(<QueryClientProvider client={client}><MemoryRouter><ModelGatewaySettings /></MemoryRouter></QueryClientProvider>);
  });
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); });
}
async function editConfiguration(target: "provider" | "model") {
  const updated: ModelGateway = {
    ...original,
    providers: original.providers!.map(p => ({ ...p, baseUrl: "https://new.example.test/v1", updatedAt: "2026-10-29T01:00:00Z" })),
    models: original.models!.map(m => ({ ...m, baseUrl: "https://new.example.test/v1", model: "model-new" })),
  };
  const spy = target === "provider"
    ? vi.spyOn(api, "updateModelProvider").mockResolvedValue(updated)
    : vi.spyOn(api, "updateModelProfile").mockResolvedValue(updated);
  await click(target === "provider" ? "编辑服务商" : "编辑");
  const field = document.querySelector<HTMLInputElement>(target === "provider" ? '[name="providerBaseUrl"]' : '[name="profileModel"]')!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(field, target === "provider" ? "https://new.example.test/v1" : "model-new");
    field.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await click(target === "provider" ? "保存服务商" : "保存模型");
  expect(spy).toHaveBeenCalledWith(target === "provider" ? "p" : "m", expect.objectContaining(target === "provider" ? { baseUrl: "https://new.example.test/v1" } : { model: "model-new" }));
  expect(document.body.textContent).toContain("https://new.example.test/v1");
}
it.each([
  ["provider", "success"], ["provider", "failure"],
  ["model", "success"], ["model", "failure"],
] as const)("discards old %s test %s without clearing a newer pending test", async (target, outcome) => {
  const old = deferred(), fresh = deferred();
  const testSpy = vi.spyOn(api, "testModelProfile")
    .mockReturnValueOnce(old.promise).mockReturnValueOnce(fresh.promise);
  await renderSettings();
  await click("测试此模型");
  expect(testSpy).toHaveBeenCalledWith("m");
  await editConfiguration(target);
  expect(document.querySelector(".gateway-model-test-result")).toBeNull();
  const testButton = button("测试此模型");
  await click("测试此模型");
  expect(testSpy).toHaveBeenCalledTimes(2);
  await act(async () => {
    if (outcome === "success") old.resolve({ ok: true, model: "model-old", latencyMs: 350 });
    else old.reject(new ApiError(502, "upstream_error", "old provider failed"));
  });
  expect(document.querySelector(".gateway-model-test-result")).toBeNull();
  expect(testButton.disabled).toBe(true);
  expect(document.querySelector(".gateway-saved-state")?.textContent).not.toContain("连接测试已通过");
  await act(async () => { fresh.resolve({ ok: true, model: "model-new", latencyMs: 3 }); });
  expect(document.querySelector(".gateway-model-test-result.ok")?.textContent).toContain("model-new · 3 ms");
  expect(button("测试此模型").disabled).toBe(false);
});
it.each(["success", "failure"])("invalidates a pending default connection test %s on provider edits", async (outcome) => {
  const pending = deferred();
  vi.spyOn(api, "testModelGateway").mockReturnValue(pending.promise);
  await renderSettings();
  await click("测试连接");
  await editConfiguration("provider");
  await act(async () => {
    if (outcome === "success") pending.resolve({ ok: true, model: "stale-default", latencyMs: 350 });
    else pending.reject(new ApiError(502, "upstream_error", "stale-default"));
  });
  expect(document.querySelector(".gateway-saved-state")?.textContent).toContain("连接尚需测试验证");
  expect(document.body.textContent).not.toContain("stale-default");
  expect(document.querySelector(".notice.error")).toBeNull();
});
