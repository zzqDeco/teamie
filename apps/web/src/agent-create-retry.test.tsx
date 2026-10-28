// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import { AgentProfileDialog } from "./agents/agent-profile-dialog";

vi.mock("./auth", () => ({ useAuth: () => ({ user: { id: "owner", kind: "human" } }) }));
vi.mock("./chat", () => ({ useChat: () => ({ data: { capabilities: { "agents.enabled": true } } }) }));

let root: Root, host: HTMLDivElement, client: QueryClient;
beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  api.csrfToken = "create-session";
  host = document.createElement("div"); document.body.append(host);
  root = createRoot(host);
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
});
afterEach(async () => {
  await act(async () => root.unmount()); host.remove(); client.clear();
  api.csrfToken = ""; vi.restoreAllMocks(); vi.unstubAllGlobals();
});
async function mount(onClose = vi.fn()) {
  vi.spyOn(api, "modelCatalog").mockResolvedValue({ models: [] });
  await act(async () => root.render(<MemoryRouter><QueryClientProvider client={client}><AgentProfileDialog workspaces={[]} onClose={onClose} /></QueryClientProvider></MemoryRouter>));
  return onClose;
}
async function name(value: string) {
  const input = host.querySelector("input")!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function submit() {
  await act(async () => host.querySelector("form")!.requestSubmit());
}
const saved = { id: "created-agent", principalId: "created-agent", ownerId: "owner", ownerName: "Owner", name: "Assistant", description: "", workspaceIds: [], enabled: true, createdAt: "2026-10-28T00:00:00Z", updatedAt: "2026-10-28T00:00:00Z" };

describe("Agent form creation identity through the real API client", () => {
  it.each(["reconcile failure", "lost response"])("reuses the key and payload after %s", async (failure) => {
    const fetch = vi.fn<typeof globalThis.fetch>()
      .mockImplementationOnce(async () => {
        if (failure === "lost response") throw new TypeError("Network error");
        return new Response(JSON.stringify({ error: { code: "control_unavailable", message: "private detail" } }), { status: 503 });
      })
      .mockResolvedValueOnce(new Response(JSON.stringify({ agent: saved }), { status: 201 }));
    vi.stubGlobal("fetch", fetch);
    const closed = await mount();
    await name("Assistant"); await submit();
    expect(closed).not.toHaveBeenCalled();
    expect(host.textContent).toContain("操作未完成");
    expect(host.textContent).not.toContain("private detail");
    await submit();
    expect(fetch).toHaveBeenCalledTimes(2);
    const [first, second] = fetch.mock.calls.map(([, options]) => options!);
    const key = new Headers(first.headers).get("Idempotency-Key");
    expect(key).toMatch(/^[a-f0-9-]{36}$/);
    expect(new Headers(second.headers).get("Idempotency-Key")).toBe(key);
    expect(first.body).toBe(second.body);
    expect(JSON.parse(first.body as string)).not.toHaveProperty("idempotencyKey");
    expect(new Headers(second.headers).get("X-CSRF-Token")).toBe("create-session");
    expect(closed).toHaveBeenCalledOnce();
    expect(client.getQueryData(["agent", "owner", saved.id])).toEqual({ agent: saved });
  });
  it("uses a new key when the submitted creation intent changes", async () => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(async () => new Response(JSON.stringify({ error: { code: "control_unavailable" } }), { status: 503 }));
    vi.stubGlobal("fetch", fetch); await mount();
    await name("Original"); await submit();
    await name("Changed"); await submit();
    const keys = fetch.mock.calls.map(([, options]) => new Headers(options?.headers).get("Idempotency-Key"));
    expect(keys[0]).toBeTruthy(); expect(keys[1]).toBeTruthy(); expect(keys[0]).not.toBe(keys[1]);
  });
});
