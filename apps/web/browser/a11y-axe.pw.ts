import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";

const WCAG_AA_TAGS = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"];
const BLOCKING_IMPACTS = new Set(["critical", "serious"]);

async function expectNoSeriousA11yViolations(page: Page, contextLabel: string) {
  const results = await new AxeBuilder({ page })
    .withTags(WCAG_AA_TAGS)
    .analyze();
  const blocking = results.violations.filter((violation) =>
    BLOCKING_IMPACTS.has(violation.impact ?? ""),
  );
  const summary = blocking
    .map(
      (v) =>
        `[${v.impact}] ${v.id}: ${v.help} (${v.nodes
          .slice(0, 3)
          .map((n) => n.target.join(" "))
          .join(", ")})`,
    )
    .join("\n");
  expect(blocking, `${contextLabel}\n${summary}`).toEqual([]);
}

async function mockAuthenticatedRoutes(page: Page) {
  const user = {
    id: "admin-1",
    email: "admin@example.test",
    displayName: "林青",
    kind: "human",
    platformRole: "admin",
    createdAt: "2026-11-14T12:00:00Z",
  };
  const peer = {
    userId: "peer-1",
    displayName: "周予",
    kind: "human",
    role: "member",
  };
  const agent = {
    userId: "agent-1",
    displayName: "长工",
    kind: "agent",
    role: "member",
    ownerName: "林青",
    agentEnabled: true,
  };
  const workspace = {
    id: "workspace",
    name: "设计研发空间",
    createdBy: user.id,
    createdAt: user.createdAt,
  };
  const channel = {
    id: "general",
    workspaceId: workspace.id,
    type: "channel",
    name: "general",
    memberIds: [user.id, peer.userId, agent.userId],
    lastSeq: 1,
    readSeq: 1,
    notificationLevel: "all",
    updatedAt: user.createdAt,
  };
  const group = {
    id: "group",
    workspaceId: workspace.id,
    type: "group",
    name: "产品评审小组",
    memberIds: [user.id, peer.userId, agent.userId],
    lastSeq: 3,
    readSeq: 2,
    notificationLevel: "all",
    updatedAt: user.createdAt,
  };
  const messages = [
    {
      id: "m1",
      clientMessageId: "c1",
      conversationId: group.id,
      senderId: user.id,
      senderName: user.displayName,
      senderKind: "human",
      seq: 1,
      body: "今天下午三点评审无障碍改动方案。",
      createdAt: "2026-11-14T12:00:00Z",
    },
    {
      id: "m2",
      clientMessageId: "c2",
      conversationId: group.id,
      senderId: peer.userId,
      senderName: peer.displayName,
      senderKind: "human",
      seq: 2,
      body: "收到，我把对比度测试结果贴上来。",
      replyToId: "m1",
      reactions: [{ emoji: "👍", count: 1, userIds: [user.id] }],
      createdAt: "2026-11-14T12:01:00Z",
    },
    {
      id: "m3",
      clientMessageId: "c3",
      conversationId: group.id,
      senderId: agent.userId,
      senderName: agent.displayName,
      senderKind: "agent",
      seq: 3,
      body: "已完成亮暗双主题扫描。",
      createdAt: "2026-11-14T12:02:00Z",
    },
  ];
  const attentionItems = [
    {
      message: messages[1],
      conversation: {
        id: group.id,
        workspaceId: workspace.id,
        name: group.name,
        type: group.type,
      },
      workspaceName: workspace.name,
      reasons: ["reply"],
      unread: true,
    },
  ];

  await page.routeWebSocket("**/api/v1/ws", () => {});
  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const pathname = url.pathname;
    let json: unknown;
    if (pathname.endsWith("/auth/session")) {
      json = { user, csrfToken: "fixture-session" };
    } else if (pathname.endsWith("/bootstrap")) {
      json = {
        user,
        workspaces: [workspace],
        conversations: [channel, group],
        capabilities: {
          "im.attachments.upload": true,
          "im.message.edit": true,
          "im.message.retract": true,
          "im.message.mentions": true,
          "im.mentions.broadcast": true,
          "im.reactions.create": true,
          "im.read_receipts.view": true,
          "im.notifications.configure": true,
          "im.presence.publish": true,
          "im.marketplace.enabled": true,
          "im.dm.create": true,
          "im.group.create": true,
        },
        flagsRevision: "1",
        syncCursor: "3",
      };
    } else if (pathname.endsWith("/sync")) {
      json = { events: [], cursor: "3", hasMore: false };
    } else if (pathname.endsWith("/members")) {
      json = {
        members: [
          {
            userId: user.id,
            displayName: user.displayName,
            kind: "human",
            role: "owner",
          },
          peer,
          agent,
        ],
      };
    } else if (pathname.endsWith("/messages/search")) {
      json = { messages: [messages[1]], hasMore: false };
    } else if (pathname.endsWith("/messages")) {
      json = { messages, hasMore: false };
    } else if (pathname.endsWith("/read")) {
      json = { readSeq: 3 };
    } else if (pathname.endsWith("/attention/unread-count")) {
      json = { unreadCount: 1, attentionUnreadByConversation: { [group.id]: 1 } };
    } else if (pathname.endsWith("/attention")) {
      json = { items: attentionItems, hasMore: false };
    } else if (pathname.endsWith("/saved")) {
      json = { items: [], hasMore: false };
    } else if (pathname.endsWith("/admin/registration-invites")) {
      json = {
        invites: [
          {
            id: "inv-1",
            codePrefix: "TEAM-ABCD",
            note: "前端评审成员",
            status: "active",
            createdAt: "2026-11-14T10:00:00Z",
          },
        ],
      };
    } else if (pathname.endsWith("/announcements/active")) {
      json = { announcements: [] };
    } else if (pathname.endsWith("/apps")) {
      json = { apps: [] };
    } else if (pathname.endsWith("/agents")) {
      json = { agents: [] };
    } else if (pathname.endsWith("/conversations")) {
      json = { conversations: [channel, group] };
    } else {
      json = {
        items: [],
        unreadCount: 0,
        attentionUnreadByConversation: {},
        receipts: [],
        presence: [],
        models: [],
        pins: [],
        announcements: [],
      };
    }
    await route.fulfill({ json });
  });
}

for (const theme of ["light", "dark"] as const) {
  test.describe(`WCAG 2.1 AA axe scan (${theme})`, () => {
    test.beforeEach(async ({ page }) => {
      await page.addInitScript((mode) => {
        localStorage.setItem("teamie.theme", mode);
      }, theme);
    });

    test("landing page has zero critical/serious WCAG 2.1 AA violations", async ({
      page,
    }) => {
      await page.route("**/api/v1/auth/session", async (route) => {
        await route.fulfill({ status: 401, json: { error: { code: "unauthorized", message: "unauthorized" } } });
      });
      await page.goto("/");
      await expect(page.locator("h1")).toBeVisible();
      await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
      await expectNoSeriousA11yViolations(page, `landing (${theme})`);
    });

    test("chat, search modal, attention, members, settings, and admin have zero critical/serious WCAG 2.1 AA violations", async ({
      page,
    }) => {
      // Runs 7 full Axe WCAG 2.1 AA page scans sequentially; give 2-vCPU CI runners 3x timeout.
      test.slow();
      await mockAuthenticatedRoutes(page);

      // 1. Main conversation timeline
      await page.goto("/app/w/workspace/c/group");
      await expect(page.locator("#message-m2")).toBeVisible();
      await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
      await expectNoSeriousA11yViolations(page, `chat (${theme})`);

      // 1b. Expanded emoji reaction picker popover
      const row = page.locator("#message-m2");
      await row.hover();
      const moreBtn = row.locator(".message-more-button");
      if (await moreBtn.isVisible()) {
        await moreBtn.click();
      }
      await row.locator('button[aria-label="添加表情回应"]').click();
      await page.locator('button[aria-label="更多表情"]').click();
      await expect(page.locator(".emoji-panel-grid")).toBeVisible();
      await expectNoSeriousA11yViolations(page, `emoji picker (${theme})`);
      await page.keyboard.press("Escape");
      await expect(page.locator(".reaction-picker-popover")).toBeHidden();

      // 2. Search modal inside conversation
      await page.locator('button[aria-label="搜索会话"]').click();
      const searchInput = page.locator('.conversation-search input[type="search"]');
      await expect(searchInput).toBeVisible();
      await searchInput.fill("对比度");
      await expect(page.locator(".search-message-result")).toBeVisible();
      await expectNoSeriousA11yViolations(page, `search modal (${theme})`);
      await page.keyboard.press("Escape");
      await expect(searchInput).toBeHidden();

      // 3. Attention (mentions & replies)
      await page.goto("/app/attention");
      await expect(page.locator(".attention-view")).toBeVisible();
      await expectNoSeriousA11yViolations(page, `attention (${theme})`);

      // 4. Workspace members directory
      await page.goto("/app/w/workspace/members");
      await expect(page.locator(".members-view")).toBeVisible();
      await expectNoSeriousA11yViolations(page, `members (${theme})`);

      // 5. Account settings
      await page.goto("/app/settings");
      await expect(page.locator(".settings-view")).toBeVisible();
      await expectNoSeriousA11yViolations(page, `settings (${theme})`);

      // 6. Platform admin
      await page.goto("/app/admin");
      await expect(page.locator(".admin-view")).toBeVisible();
      await expectNoSeriousA11yViolations(page, `admin (${theme})`);
    });
  });
}
