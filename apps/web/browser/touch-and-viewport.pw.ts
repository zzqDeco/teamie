import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";

async function mountConversationFixture(page: Page) {
  const user = {
    id: "owner",
    email: "owner@example.test",
    displayName: "Owner",
    kind: "human",
    platformRole: "user",
    createdAt: "2026-11-14T12:00:00Z",
  };
  const peer = {
    userId: "peer-1",
    displayName: "林远",
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
    name: "移动端联调组",
    memberIds: [user.id, peer.userId],
    lastSeq: 8,
    readSeq: 8,
    notificationLevel: "all",
    updatedAt: user.createdAt,
  };
  const messages = Array.from({ length: 8 }, (_, idx) => ({
    id: `m${idx + 1}`,
    clientMessageId: `c${idx + 1}`,
    conversationId: group.id,
    senderId: idx % 2 === 0 ? peer.userId : user.id,
    senderName: idx % 2 === 0 ? peer.displayName : user.displayName,
    senderKind: "human",
    seq: idx + 1,
    body: `第 ${idx + 1} 条用于移动端长按与视口缩放回归验证的消息内容`,
    createdAt: new Date(Date.parse("2026-11-14T12:00:00Z") + idx * 60_000).toISOString(),
  }));

  const mutations: string[] = [];

  await page.routeWebSocket("**/api/v1/ws", () => {});
  await page.route("**/api/v1/**", async (route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname;
    if (req.method() !== "GET") {
      mutations.push(`${req.method()} ${path}`);
    }
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
          "im.reactions.create": true,
          "im.messages.forward": true,
          "im.messages.pin": true,
          "im.messages.save": true,
          "im.unread.mark": true,
          "im.message.edit": true,
          "im.message.retract": true,
        },
        flagsRevision: "1",
        syncCursor: "8",
      };
    } else if (path.endsWith("/sync")) {
      json = { events: [], cursor: "8", hasMore: false };
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
    } else if (path.endsWith("/messages")) {
      json = { messages, hasMore: false };
    } else if (path.endsWith("/read")) {
      json = { readSeq: 8 };
    } else if (path.endsWith("/save")) {
      json = { saved: { conversationId: group.id, messageId: "m8", savedAt: user.createdAt } };
    } else if (path.endsWith("/unread")) {
      json = { conversationId: group.id, manualUnreadSeq: 8 };
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
  await expect(page.locator("#message-m8")).toBeVisible();
  return { mutations };
}

test("long-press on touch opens action sheet without ghost-clicking any menu item on release", async ({
  page,
  hasTouch,
  browserName,
}) => {
  test.skip(!hasTouch, "Touch long-press check runs on touch-enabled mobile/WebKit projects");

  const { mutations } = await mountConversationFixture(page);
  mutations.length = 0;

  const targetMsg = page.locator("#message-m8 .message-text");
  await expect(targetMsg).toBeVisible();

  // Coarse pointer / touch devices suppress native selection callout on message rows to avoid colliding with the sheet.
  const msgSelection = await page.locator("#message-m8").evaluate((el) => {
    const cs = getComputedStyle(el);
    return {
      userSelect: cs.userSelect,
      webkitUserSelect: cs.getPropertyValue("-webkit-user-select"),
      matchesTouchMedia:
        window.matchMedia("(pointer: coarse)").matches ||
        window.matchMedia("(hover: none)").matches,
    };
  });
  if (msgSelection.matchesTouchMedia) {
    expect([msgSelection.userSelect, msgSelection.webkitUserSelect]).toContain("none");
  }

  const box = await targetMsg.boundingBox();
  expect(box).not.toBeNull();
  const pressX = Math.round(box!.x + box!.width / 2);
  const pressY = Math.round(box!.y + box!.height / 2);

  const sheet = page.locator(".message-action-sheet");

  if (browserName === "chromium") {
    // Exercise genuine native Chromium touch gesture via CDP (touchStart -> hold 700ms -> touchEnd)
    // so the browser itself synthesizes any post-lift compatibility mouse/click at (pressX, pressY)
    // directly on top of the bottom sheet that mounted under the finger.
    const cdp = await page.context().newCDPSession(page);
    await cdp.send("Input.dispatchTouchEvent", {
      type: "touchStart",
      touchPoints: [{ x: pressX, y: pressY, id: 1 }],
    });
    await page.waitForTimeout(700);
    await expect(sheet).toBeVisible();
    await cdp.send("Input.dispatchTouchEvent", {
      type: "touchEnd",
      touchPoints: [],
    });
    await page.waitForTimeout(150);
  } else {
    // WebKit touch simulation: hold touch pointerdown past LONG_PRESS_MS, then lift & synthesize ghost burst on sheet item.
    await page.evaluate(
      ({ x, y }) => {
        const target = document.elementFromPoint(x, y) ?? document.querySelector("#message-m8 .message-text")!;
        target.dispatchEvent(
          new PointerEvent("pointerdown", {
            bubbles: true,
            cancelable: true,
            pointerType: "touch",
            isPrimary: true,
            pointerId: 42,
            clientX: x,
            clientY: y,
          }),
        );
      },
      { x: pressX, y: pressY },
    );
    await page.waitForTimeout(700);
    await expect(sheet).toBeVisible();

    const saveButton = sheet.locator(".message-sheet-action", { hasText: "收藏" });
    const saveBox = await saveButton.boundingBox();
    expect(saveBox).not.toBeNull();
    const liftX = Math.round(saveBox!.x + saveBox!.width / 2);
    const liftY = Math.round(saveBox!.y + saveBox!.height / 2);

    await page.evaluate(
      ({ x, y }) => {
        const el = document.elementFromPoint(x, y) as HTMLElement | null;
        const target = el ?? document.querySelector<HTMLElement>(".message-action-sheet .message-sheet-action")!;
        window.dispatchEvent(
          new PointerEvent("pointerup", {
            bubbles: true,
            cancelable: true,
            pointerType: "touch",
            isPrimary: true,
            pointerId: 42,
            clientX: x,
            clientY: y,
          }),
        );
        target.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true, clientX: x, clientY: y }));
        target.dispatchEvent(new MouseEvent("mouseup", { bubbles: true, cancelable: true, clientX: x, clientY: y }));
        target.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true, clientX: x, clientY: y }));
      },
      { x: liftX, y: liftY },
    );
  }

  // Sheet stays open and zero unintended mutations (save / unread / pin) occurred.
  await expect(sheet).toBeVisible();
  expect(mutations.filter((m) => !m.endsWith("/read"))).toEqual([]);

  // A subsequent deliberate tap on "回复" works immediately.
  const replyButton = sheet.locator(".message-sheet-action", { hasText: "回复" });
  await replyButton.tap();
  await expect(sheet).toHaveCount(0);
  await expect(page.locator(".reply-preview")).toBeVisible();
});

test("visualViewport pinch-zoom preserves shell height and iOS keyboard offsetTop aligns fixed shell", async ({
  page,
}) => {
  await page.addInitScript(() => {
    const listeners: Record<string, Set<EventListener>> = {
      resize: new Set(),
      scroll: new Set(),
    };
    const state: {
      height: number | null;
      width: number | null;
      offsetTop: number;
      offsetLeft: number;
      scale: number;
    } = {
      height: null,
      width: null,
      offsetTop: 0,
      offsetLeft: 0,
      scale: 1,
    };
    const fakeViewport = {
      get height() {
        return state.height ?? window.innerHeight;
      },
      get width() {
        return state.width ?? window.innerWidth;
      },
      get offsetTop() {
        return state.offsetTop;
      },
      get offsetLeft() {
        return state.offsetLeft;
      },
      get scale() {
        return state.scale;
      },
      addEventListener(type: string, cb: EventListener) {
        listeners[type]?.add(cb);
      },
      removeEventListener(type: string, cb: EventListener) {
        listeners[type]?.delete(cb);
      },
    };
    Object.defineProperty(window, "visualViewport", {
      configurable: true,
      get: () => fakeViewport,
    });
    (window as unknown as Record<string, unknown>).__setVisualViewport = (
      next: Partial<typeof state>,
      eventType: "resize" | "scroll" = "resize",
    ) => {
      Object.assign(state, next);
      for (const cb of listeners[eventType] ?? []) {
        cb(new Event(eventType));
      }
    };
  });

  await mountConversationFixture(page);

  const initialHeight = await page.evaluate(() => window.innerHeight);
  const initialCssHeight = await page.evaluate(() =>
    document.documentElement.style.getPropertyValue("--app-height"),
  );
  expect(initialCssHeight).toBe(`${initialHeight}px`);

  // Simulate pinch zoom (scale = 2, height halved, offsetTop panned): --app-height must NOT collapse.
  await page.evaluate(() => {
    const setVp = (window as unknown as Record<string, (n: object, e?: string) => void>).__setVisualViewport;
    setVp({ height: Math.round(window.innerHeight / 2), offsetTop: 160, scale: 2 }, "resize");
  });
  const zoomedVars = await page.evaluate(() => ({
    height: document.documentElement.style.getPropertyValue("--app-height"),
    top: document.documentElement.style.getPropertyValue("--app-top"),
  }));
  expect(zoomedVars).toEqual({ height: `${initialHeight}px`, top: "0px" });

  // Simulate iOS soft keyboard open (scale = 1, visualViewport shrunk and offsetTop shifted).
  const keyboardHeight = Math.max(260, Math.round(initialHeight * 0.58));
  await page.evaluate((h) => {
    const setVp = (window as unknown as Record<string, (n: object, e?: string) => void>).__setVisualViewport;
    setVp({ height: h, offsetTop: 36, scale: 1 }, "scroll");
  }, keyboardHeight);

  const keyboardVars = await page.evaluate(() => ({
    height: document.documentElement.style.getPropertyValue("--app-height"),
    top: document.documentElement.style.getPropertyValue("--app-top"),
  }));
  expect(keyboardVars).toEqual({ height: `${keyboardHeight}px`, top: "36px" });

  // On mobile viewports (<= 780px), the fixed .app-shell tracks --app-top and --app-height.
  const viewportWidth = page.viewportSize()!.width;
  if (viewportWidth <= 780) {
    const shellRect = await page.locator(".app-shell").boundingBox();
    expect(shellRect).not.toBeNull();
    expect(Math.round(shellRect!.y)).toBe(36);
    expect(Math.round(shellRect!.height)).toBe(keyboardHeight);
    const headerRect = await page.locator(".conversation-header").boundingBox();
    const composerRect = await page.locator(".composer-area").boundingBox();
    expect(headerRect).not.toBeNull();
    expect(composerRect).not.toBeNull();
    expect(headerRect!.y).toBeGreaterThanOrEqual(35);
    expect(composerRect!.y + composerRect!.height).toBeLessThanOrEqual(36 + keyboardHeight + 2);
  }
});
