import { expect, test } from "@playwright/test";

test("deep-link jump auto-loads newer pages on scroll and merges seamlessly without fetchLatest", async ({
  page,
}) => {
  const user = {
    id: "owner",
    email: "owner@example.test",
    displayName: "Owner",
    kind: "human",
    platformRole: "user",
    createdAt: "2026-11-19T12:00:00Z",
  };
  const peer = {
    userId: "peer-1",
    displayName: "同行",
    kind: "human",
    role: "member",
  };
  const workspace = {
    id: "workspace",
    name: "Fixture workspace",
    createdBy: user.id,
    createdAt: user.createdAt,
  };
  const group = {
    id: "group",
    workspaceId: workspace.id,
    type: "group",
    name: "连续阅读验证",
    memberIds: [user.id, peer.userId],
    lastSeq: 120,
    readSeq: 10,
    notificationLevel: "all",
    updatedAt: user.createdAt,
  };
  const history = Array.from({ length: 120 }, (_, i) => {
    const seq = i + 1;
    return {
      id: `m${seq}`,
      clientMessageId: `c${seq}`,
      conversationId: group.id,
      senderId: peer.userId,
      senderName: peer.displayName,
      senderKind: "human",
      seq,
      body: `第 ${seq} 条历史消息内容，用于验证跳转后连续向下阅读与无缝并入。`,
      createdAt: new Date(Date.parse(user.createdAt) + seq * 60_000).toISOString(),
    };
  });

  const aroundSeqCalls: number[] = [];
  let latestFetchCalls = 0;

  await page.routeWebSocket("**/api/v1/ws", () => {});
  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    let json: unknown;
    if (path.endsWith("/auth/session")) {
      json = { user, csrfToken: "fixture-session" };
    } else if (path.endsWith("/bootstrap")) {
      json = {
        user,
        workspaces: [workspace],
        conversations: [group],
        capabilities: {},
        flagsRevision: "1",
        syncCursor: "120",
      };
    } else if (path.endsWith("/sync")) {
      json = { events: [], cursor: "120", hasMore: false };
    } else if (path.endsWith("/members")) {
      json = {
        members: [
          {
            userId: user.id,
            displayName: user.displayName,
            kind: "human",
            role: "owner",
          },
          peer,
        ],
      };
    } else if (/\/conversations\/group\/messages\/m\d+$/.test(path)) {
      const id = path.split("/").pop()!;
      json = { message: history.find((m) => m.id === id) };
    } else if (path.endsWith("/conversations/group/messages")) {
      const around = url.searchParams.get("aroundSeq");
      const before = url.searchParams.get("beforeSeq");
      if (around) {
        const seq = Number(around);
        aroundSeqCalls.push(seq);
        json = {
          messages: history.filter((m) => m.seq >= Math.max(1, seq - 25) && m.seq <= seq + 24),
          hasMore: false,
        };
      } else if (before) {
        const seq = Number(before);
        const earlier = history.filter((m) => m.seq < seq);
        json = { messages: earlier.slice(-50), hasMore: earlier.length > 50 };
      } else {
        latestFetchCalls += 1;
        json = { messages: history.slice(-50), hasMore: true };
      }
    } else if (path.endsWith("/read")) {
      const body = route.request().postDataJSON() as { seq?: number } | null;
      json = { readSeq: body?.seq ?? group.readSeq };
    } else if (path.endsWith("/pins")) {
      json = { pins: [], pinsVersion: "0" };
    } else if (path.endsWith("/saved")) {
      json = { messageIds: [] };
    } else if (path.endsWith("/conversations")) {
      json = { conversations: [group] };
    } else {
      json = {
        items: [],
        unreadCount: 0,
        attentionUnreadByConversation: {},
        receipts: [],
        presence: [],
        models: [],
        apps: [],
        agents: [],
      };
    }
    await route.fulfill({ json });
  });

  await page.goto("/app/w/workspace/c/group?message=m40");

  await expect(page.locator(".located-banner")).toBeVisible();
  await expect(page.locator("#message-m40")).toBeVisible();
  await expect(page.locator(".load-later")).toBeVisible();
  await expect(page).toHaveURL(/\/app\/w\/workspace\/c\/group\?message=m40$/);

  // Give the initial settled view a moment and confirm bottom auto-load did NOT fire prematurely.
  await page.waitForTimeout(300);
  expect(aroundSeqCalls).toEqual([40]);
  expect(latestFetchCalls).toBe(0);

  const scroller = page.locator(".message-scroll");

  // Scroll downward until each page auto-loads and seamlessly merges at seq 120.
  for (const expectedSeq of [89, 114, 120]) {
    await scroller.evaluate((el) => {
      el.scrollTop = el.scrollHeight;
    });
    await expect(page.locator(`#message-m${expectedSeq}`)).toBeAttached();
  }

  // Merged cleanly back into normal timeline with ?message= cleared and no fetchLatest call.
  await expect(page.locator(".located-banner")).toBeHidden();
  await expect(page.locator(".load-later")).toBeHidden();
  await expect(page).toHaveURL(/\/app\/w\/workspace\/c\/group$/);
  expect(latestFetchCalls).toBe(0);
  expect(aroundSeqCalls).toEqual([40, 65, 90, 115]);
  await expect(page.locator("#message-m40")).toBeAttached();
  await expect(page.locator("#message-m120")).toBeAttached();
});
