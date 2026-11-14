// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type { MarketplaceApp, ConversationAppSessionResp, User, Workspace, Conversation } from "@teamie/api-client";
import { ConversationAppsPane } from "./marketplace";

const appA: MarketplaceApp = { id: "app-a", slug: "a", name: "App A", description: "", webUrl: "https://apps.example.test/a", mcpUrl: "https://apps.example.test/a/mcp", enabled: true, createdAt: "", updatedAt: "" };
const appB: MarketplaceApp = { ...appA, id: "app-b", slug: "b", name: "App B", webUrl: "https://apps.example.test/b", mcpUrl: "https://apps.example.test/b/mcp" };
const user: User = { id: "user", displayName: "User", kind: "human", platformRole: "user", email: "u@example.test", createdAt: "" };
const workspace: Workspace = { id: "ws", name: "Workspace", createdBy: user.id, createdAt: "" };
const conversation: Conversation = { id: "group", workspaceId: workspace.id, type: "group", name: "Group", memberIds: [user.id], updatedAt: "", lastSeq: 0, readSeq: 0 };
const sessionFor = (app: MarketplaceApp, token = `token-${app.id}`): ConversationAppSessionResp => ({ app, token, expiresAt: "2099-01-01T00:00:00Z" });

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

let host: HTMLDivElement;
let root: Root;
let qc: QueryClient;

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  vi.spyOn(api, "conversationApps").mockResolvedValue({ apps: [appA, appB] });
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  qc.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function flush() {
  for (let i = 0; i < 4; i++) await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); });
}

async function renderPane(conv = conversation, mobile = false) {
  await act(async () => root.render(
    <QueryClientProvider client={qc}>
      <ConversationAppsPane overlayMembers={mobile} mobile={mobile} sidebar={false} workspace={workspace} conversation={conv} user={user} owner={false} onClose={() => {}} onOpenWorkspaceApps={() => {}} />
    </QueryClientProvider>,
  ));
  await flush();
}

async function openApp(app: MarketplaceApp) {
  if (!host.querySelector(".mp-launcher-list")) {
    const addBtn = host.querySelector<HTMLButtonElement>('[aria-label="打开新应用"]')!;
    expect(addBtn).not.toBeNull();
    await act(async () => addBtn.click());
    await flush();
  }
  const card = [...host.querySelectorAll<HTMLButtonElement>(".mp-launcher-card")].find(
    node => node.querySelector("b")?.textContent === app.name,
  )!;
  expect(card).toBeDefined();
  await act(async () => card.click());
  await flush();
}

async function closeTab(app: MarketplaceApp) {
  const closeBtn = host.querySelector<HTMLButtonElement>(`[aria-label="关闭标签页：${app.name}"]`)!;
  expect(closeBtn).not.toBeNull();
  await act(async () => closeBtn.click());
  await flush();
}

function frame(app: MarketplaceApp, convId = conversation.id) {
  return host.querySelector<HTMLIFrameElement>(
    `#mp-pane-panel-${convId}-${app.id} iframe[title="小组应用：${app.name}"]`,
  );
}

function captureMessages(iframe: HTMLIFrameElement) {
  const send = vi.fn();
  Object.defineProperty(iframe, "contentWindow", { value: { postMessage: send }, configurable: true });
  return send;
}

async function appEvent(iframe: HTMLIFrameElement, type: string) {
  await act(async () => window.dispatchEvent(new MessageEvent("message", {
    origin: "https://apps.example.test", source: iframe.contentWindow, data: { type },
  })));
}

describe.each([false, true])("Marketplace session isolation (mobile=%s)", (mobile) => {
  it("does not initialize a new same-origin app with the previous app's token while keeping the previous tab alive in background", async () => {
    const pendingB = deferred<ConversationAppSessionResp>();
    vi.spyOn(api, "createConversationAppSession").mockImplementation(async (_conv, id) => id === appA.id ? sessionFor(appA) : pendingB.promise);
    await renderPane(conversation, mobile);
    expect(api.createConversationAppSession).not.toHaveBeenCalled();
    expect(frame(appA)).toBeNull();

    await openApp(appA);
    const a = frame(appA)!;
    expect(a).not.toBeNull();

    await openApp(appB);
    expect(frame(appA)).toBe(a);
    expect(a.closest<HTMLElement>(".mp-frame-slot")?.hidden).toBe(true);
    expect(frame(appB)).toBeNull();

    await act(async () => pendingB.resolve(sessionFor(appB)));
    const b = frame(appB)!;
    const send = captureMessages(b);
    await act(async () => b.dispatchEvent(new Event("load")));
    expect(send).toHaveBeenCalledWith(expect.objectContaining({ payload: expect.objectContaining({ appId: appB.id, token: "token-app-b" }) }), "https://apps.example.test");
    expect(send.mock.calls.every(([message]) => message.payload.token !== "token-app-a")).toBe(true);
  });

  it.each(["resolve", "reject"] as const)("ignores a late %s from a closed previous app tab", async (outcome) => {
    const pendingA = deferred<ConversationAppSessionResp>();
    vi.spyOn(api, "createConversationAppSession").mockImplementation(async (_conv, id) => id === appA.id ? pendingA.promise : sessionFor(appB));
    await renderPane(conversation, mobile);
    await openApp(appA);
    await closeTab(appA);
    await openApp(appB);
    const b = frame(appB)!;
    const send = captureMessages(b);
    await act(async () => {
      if (outcome === "resolve") pendingA.resolve(sessionFor(appA));
      else pendingA.reject(new Error("old app request failed"));
    });
    expect(frame(appB)).toBe(b);
    expect(frame(appA)).toBeNull();
    expect(host.textContent).not.toContain("old app request failed");
    await appEvent(b, "teamie:mcp-app:ready");
    expect(send.mock.calls.every(([message]) => message.payload.appId === appB.id)).toBe(true);
    expect(send).toHaveBeenCalled();
  });
});

it("isolates a pending refresh when switching conversations with the same app kept alive in both groups", async () => {
  const oldRefresh = deferred<ConversationAppSessionResp>();
  let oldCalls = 0;
  vi.spyOn(api, "createConversationAppSession").mockImplementation(async (conv) => {
    if (conv !== conversation.id) return sessionFor(appA, "new-group-token");
    return ++oldCalls === 1 ? sessionFor(appA) : oldRefresh.promise;
  });
  await renderPane();
  await openApp(appA);
  const oldFrame = frame(appA, conversation.id)!;
  const oldSend = captureMessages(oldFrame);
  await appEvent(oldFrame, "teamie:mcp-app:refresh-token");

  const newConversation = { ...conversation, id: "another-group" };
  await renderPane(newConversation);
  expect(oldFrame.isConnected).toBe(true);
  expect(oldFrame.closest<HTMLElement>(".mp-frame-slot")?.hidden).toBe(true);

  await openApp(appA);
  const current = frame(appA, "another-group")!;
  const send = captureMessages(current);
  expect(current).not.toBe(oldFrame);
  await act(async () => oldRefresh.resolve(sessionFor(appA, "old-group-refresh-token")));
  await appEvent(current, "teamie:mcp-app:ready");
  expect(send).toHaveBeenCalledWith(expect.objectContaining({ payload: expect.objectContaining({ conversationId: "another-group", token: "new-group-token" }) }), "https://apps.example.test");
  expect(send.mock.calls.every(([message]) => message.payload.token === "new-group-token")).toBe(true);
  expect(oldSend).toHaveBeenCalledWith(expect.objectContaining({ payload: expect.objectContaining({ conversationId: "group", token: "old-group-refresh-token" }) }), "https://apps.example.test");
});

it("delivers only the newest refresh result to the existing iframe", async () => {
  const first = deferred<ConversationAppSessionResp>();
  const second = deferred<ConversationAppSessionResp>();
  vi.spyOn(api, "createConversationAppSession")
    .mockResolvedValueOnce(sessionFor(appA))
    .mockImplementationOnce(() => first.promise)
    .mockImplementationOnce(() => second.promise);
  await renderPane();
  await openApp(appA);
  const current = frame(appA)!;
  const send = captureMessages(current);
  await appEvent(current, "teamie:mcp-app:refresh-token");
  await appEvent(current, "teamie:mcp-app:refresh-token");
  await act(async () => second.resolve(sessionFor(appA, "newest-token")));
  expect(send).toHaveBeenCalled();
  await act(async () => first.resolve(sessionFor(appA, "stale-token")));
  expect(frame(appA)).toBe(current);
  expect(send.mock.calls.every(([message]) => message.payload.token === "newest-token")).toBe(true);
});

it("ignores ready and refresh requests from another origin or window", async () => {
  const request = vi.spyOn(api, "createConversationAppSession").mockResolvedValue(sessionFor(appA));
  await renderPane();
  await openApp(appA);
  const current = frame(appA)!;
  const send = captureMessages(current);
  await act(async () => {
    window.dispatchEvent(new MessageEvent("message", { origin: "https://wrong.example.test", source: current.contentWindow, data: { type: "teamie:mcp-app:ready" } }));
    window.dispatchEvent(new MessageEvent("message", { origin: "https://apps.example.test", source: window, data: { type: "teamie:mcp-app:refresh-token" } }));
  });
  expect(send).not.toHaveBeenCalled();
  expect(request).toHaveBeenCalledTimes(1);
});
