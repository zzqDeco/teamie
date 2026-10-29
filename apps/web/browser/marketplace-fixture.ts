import type { Page } from "@playwright/test";

// Exercise the actual router, capability refresh, focus hook and cross-origin
// frame in a browser. Only HTTP services/model decisions are fixtures.
export async function marketplaceFixture(page: Page) {
  const user = { id: "owner", email: "owner@example.test", displayName: "Owner", kind: "human", platformRole: "user", createdAt: "2026-10-29T00:00:00Z" };
  const workspace = { id: "workspace", name: "Fixture workspace", createdBy: user.id, createdAt: user.createdAt };
  const group = { id: "group", workspaceId: workspace.id, type: "group", name: "Fixture group", memberIds: [user.id], lastSeq: 0, readSeq: 0, notificationLevel: "all", updatedAt: user.createdAt };
  const channel = { ...group, id: "general", type: "channel", name: "general" };
  const app = { id: "board", slug: "board", name: "Fixture board", description: "", webUrl: "https://board.example.test/ui", mcpUrl: "https://board.example.test/mcp", enabled: true, createdAt: user.createdAt, updatedAt: user.createdAt };
  const state = { enabled: true, refreshes: 0, errors: [] as string[] };
  page.on("pageerror", error => state.errors.push(error.message));
  await page.clock.install();
  await page.routeWebSocket("**/api/v1/ws", () => {});
  await page.route("https://board.example.test/**", route => route.fulfill({
    contentType: "text/html",
    body: '<!doctype html><title>Cross origin fixture</title><label>App input<input id="app-input"></label><button id="app-save">Save</button>',
  }));
  await page.route("**/api/v1/**", async route => {
    const path = new URL(route.request().url()).pathname;
    let json: unknown;
    if (path.endsWith("/auth/session")) json = { user, csrfToken: "fixture-session" };
    else if (path.endsWith("/bootstrap")) {
      state.refreshes++;
      json = { user, workspaces: [workspace], conversations: [channel, group], capabilities: { "im.marketplace.enabled": state.enabled }, flagsRevision: String(state.enabled), syncCursor: "0" };
    } else if (path.endsWith("/sync")) json = { events: [], cursor: "0", hasMore: false };
    else if (path.endsWith("/members")) json = { members: [{ userId: user.id, displayName: user.displayName, kind: "human", role: "owner" }] };
    else if (path.endsWith("/apps/board/session")) json = { app, token: "fixture-only-context", expiresAt: "2099-12-31T00:00:00Z" };
    else if (path.endsWith("/apps")) json = { apps: [app] };
    else if (path.endsWith("/messages")) json = { messages: [], hasMore: false };
    else if (path.endsWith("/read")) json = { readSeq: 0 };
    else if (path.endsWith("/agents")) json = { agents: [] };
    else if (path.endsWith("/conversations")) json = { conversations: [channel, group] };
    else json = { items: [], unreadCount: 0, attentionUnreadByConversation: {}, receipts: [], presence: [], models: [] };
    await route.fulfill({ json });
  });
  await page.goto("/app/w/workspace/c/group");
  return state;
}
