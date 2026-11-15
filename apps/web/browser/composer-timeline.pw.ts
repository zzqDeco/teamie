import { expect, test } from "@playwright/test";

test("composer preserves rounded border corners and compact timestamps align on hover", async ({
  page,
}) => {
  const user = {
    id: "owner",
    email: "owner@example.test",
    displayName: "Owner",
    kind: "human",
    platformRole: "user",
    createdAt: "2026-11-14T12:00:00Z",
  };
  const agent = {
    userId: "agent-1",
    displayName: "长工",
    kind: "agent",
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
    name: "狼人杀联调房",
    memberIds: [user.id, agent.userId],
    lastSeq: 2,
    readSeq: 2,
    notificationLevel: "all",
    updatedAt: user.createdAt,
  };
  const messages = [
    {
      id: "m1",
      clientMessageId: "c1",
      conversationId: group.id,
      senderId: agent.userId,
      senderName: agent.displayName,
      senderKind: "agent",
      seq: 1,
      body: "决赛今晚凌晨三点，BLG 打 GEN，官方首发还没挂出来，开打前我再对一次",
      createdAt: "2026-11-14T12:03:00Z",
    },
    {
      id: "m2",
      clientMessageId: "c2",
      conversationId: group.id,
      senderId: agent.userId,
      senderName: agent.displayName,
      senderKind: "agent",
      seq: 2,
      body: "GEN 淘汰赛一局没丢，BLG 这边已经把 HLE、DK、T1 挨个送回家了，Knight 跟 Chovy 头回在世界赛碰面就直接是决赛",
      createdAt: "2026-11-14T12:04:00Z",
    },
  ];

  await page.routeWebSocket("**/api/v1/ws", () => {});
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    let json: unknown;
    if (path.endsWith("/auth/session")) {
      json = { user, csrfToken: "fixture-session" };
    } else if (path.endsWith("/bootstrap")) {
      json = {
        user,
        workspaces: [workspace],
        conversations: [group],
        capabilities: {
          "im.marketplace.enabled": true,
          "im.attachments.enabled": true,
        },
        flagsRevision: "1",
        syncCursor: "2",
      };
    } else if (path.endsWith("/sync")) {
      json = { events: [], cursor: "2", hasMore: false };
    } else if (path.endsWith("/members")) {
      json = {
        members: [
          {
            userId: user.id,
            displayName: user.displayName,
            kind: "human",
            role: "owner",
          },
          agent,
        ],
      };
    } else if (path.endsWith("/messages")) {
      json = { messages, hasMore: false };
    } else if (path.endsWith("/read")) {
      json = { readSeq: 2 };
    } else if (path.endsWith("/apps")) {
      json = { apps: [] };
    } else if (path.endsWith("/agents")) {
      json = { agents: [] };
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
      };
    }
    await route.fulfill({ json });
  });

  await page.goto("/app/w/workspace/c/group");
  const composerTextarea = page.locator('.composer textarea[aria-label="消息内容"]');
  await expect(composerTextarea).toBeVisible();

  const textareaBg = await composerTextarea.evaluate(
    (el) => getComputedStyle(el).backgroundColor,
  );
  expect(textareaBg).toBe("rgba(0, 0, 0, 0)");

  const compactRow = page.locator("#message-m2");
  await expect(compactRow).toHaveClass(/\bcompact\b/);
  const compactTime = compactRow.locator(".compact-time");

  // Ensure pointer is away from the compact row first so we verify rest state.
  await composerTextarea.hover();
  await expect
    .poll(() => compactTime.evaluate((el) => getComputedStyle(el).opacity))
    .toBe("0");

  // Reveal on hover (or .actions-open on touch viewports) and verify vertical alignment.
  await compactRow.hover();
  const moreBtn = compactRow.locator(".message-more-button");
  if (await moreBtn.isVisible()) {
    await moreBtn.click();
  }
  await expect
    .poll(() => compactTime.evaluate((el) => getComputedStyle(el).opacity))
    .toBe("1");

  const alignmentDelta = await compactRow.evaluate((row) => {
    const timeRect = row.querySelector(".compact-time")!.getBoundingClientRect();
    const bodyEl = row.querySelector(".message-body")!;
    const range = document.createRange();
    const textNode = bodyEl.querySelector("p")?.firstChild ?? bodyEl.firstChild!;
    range.setStart(textNode, 0);
    range.setEnd(textNode, 1);
    const firstCharRect = range.getBoundingClientRect();
    const timeCenterY = timeRect.top + timeRect.height / 2;
    const textCenterY = firstCharRect.top + firstCharRect.height / 2;
    return Math.abs(timeCenterY - textCenterY);
  });
  expect(alignmentDelta).toBeLessThanOrEqual(2.5);
});
