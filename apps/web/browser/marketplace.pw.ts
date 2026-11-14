import { expect, test } from "@playwright/test";
import { marketplaceFixture } from "./marketplace-fixture";

test("keyboard enters and leaves the cross-origin app, then closes the pane while keeping background tab alive", async ({ page }) => {
  const state = await marketplaceFixture(page);
  const overlay = page.viewportSize()!.width <= 1250;
  const open = page.getByRole("button", { name: "小组应用", exact: true });
  await open.click();
  const pane = page.locator('aside[aria-label="小组应用"]');
  await expect(pane.getByRole("list", { name: "已安装小组应用列表" })).toBeVisible();
  expect(state.sessionRequests).toEqual([]);
  await expect(page.locator("iframe")).toHaveCount(0);

  await pane.locator(".mp-launcher-card", { hasText: "Fixture board" }).click();
  const activeFrameLocator = page.frameLocator('#mp-pane-panel-group-board iframe');
  await expect(activeFrameLocator.locator("#app-input")).toBeVisible();
  expect(state.sessionRequests).toEqual(["group:board"]);
  await expect(page.locator('#mp-pane-panel-group-board iframe')).toHaveAttribute("sandbox", "allow-scripts allow-forms allow-same-origin");

  await pane.getByRole("button", { name: "刷新应用会话", exact: true }).focus();
  await page.keyboard.press("Tab");
  await expect(activeFrameLocator.locator("#app-input")).toBeFocused();
  await page.keyboard.type("editable in the app");
  await expect(activeFrameLocator.locator("#app-input")).toHaveValue("editable in the app");
  await page.keyboard.press("Shift+Tab");
  await expect(pane.getByRole("button", { name: "刷新应用会话", exact: true })).toBeFocused();
  await page.keyboard.press("Tab");
  await page.keyboard.press("Tab");
  await expect(activeFrameLocator.locator("#app-save")).toBeFocused();
  await page.keyboard.press("Tab");
  if (overlay) {
    await expect(pane.getByRole("button", { name: "管理工作空间应用", exact: true })).toBeFocused();
    await page.keyboard.press("Shift+Tab");
    await expect(page.locator('#mp-pane-panel-group-board iframe')).toBeFocused();
    await pane.getByRole("button", { name: "关闭小组应用面板", exact: true }).focus();
    await page.keyboard.press("Escape");
    await expect(open).toBeFocused();
  } else {
    await pane.getByRole("button", { name: "关闭小组应用面板", exact: true }).click();
  }
  await expect(pane).toBeHidden();
  await expect(page.locator('#mp-pane-panel-group-board iframe')).toHaveCount(1);
  await expect(page.locator("main")).not.toHaveAttribute("inert");
  await page.locator("main textarea").fill("chat is usable after close");
  expect(state.errors).toEqual([]);
});

test("capability refresh degrades active app to read-only list and never leaves chat inert", async ({ page }) => {
  const state = await marketplaceFixture(page);
  const overlay = page.viewportSize()!.width <= 1250;
  const open = page.getByRole("button", { name: "小组应用", exact: true });
  await open.click();
  const pane = page.locator('aside[aria-label="小组应用"]');
  await pane.locator(".mp-launcher-card", { hasText: "Fixture board" }).click();
  await page.frameLocator('#mp-pane-panel-group-board iframe').locator("#app-input").focus();
  state.enabled = false;
  await page.clock.fastForward(61000);
  await expect(page.locator("iframe")).toHaveCount(0);
  await expect(pane.getByRole("list", { name: "已安装小组应用只读列表" })).toBeVisible();
  await expect(pane.getByRole("button", { name: "管理工作空间应用", exact: true })).toHaveCount(0);
  if (overlay) {
    await pane.getByRole("button", { name: "关闭小组应用面板", exact: true }).click();
    await expect(pane).toHaveCount(0);
  }
  await expect(page.locator("main")).not.toHaveAttribute("inert");
  await page.locator("main textarea").fill("chat works with apps disabled");
  state.enabled = true;
  await page.clock.fastForward(61000);
  if (overlay) {
    await open.click();
  }
  await expect(pane.getByRole("list", { name: "已安装小组应用列表" })).toBeVisible();
  await expect(page.locator("iframe")).toHaveCount(0);
  await pane.locator(".mp-launcher-card", { hasText: "Fixture board" }).click();
  await expect(page.frameLocator('#mp-pane-panel-group-board iframe').locator("#app-input")).toBeVisible();
  expect(state.errors).toEqual([]);
});

test("nested management dialog and breakpoint changes preserve focus and chat access", async ({ page }) => {
  const state = await marketplaceFixture(page);
  await page.getByRole("button", { name: "小组应用", exact: true }).click();
  const pane = page.locator('aside[aria-label="小组应用"]');
  await pane.getByRole("button", { name: "管理工作空间应用", exact: true }).click();
  await expect(page.locator(".modal")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.locator(".modal")).toHaveCount(0);
  await expect(pane).toBeVisible();
  await page.setViewportSize({ width: 1440, height: 900 });
  await expect(page.locator("main")).not.toHaveAttribute("inert");
  await expect(pane).not.toHaveAttribute("role", "dialog");
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(pane).toHaveAttribute("role", "dialog");
  await expect(page.locator("main")).toHaveAttribute("inert");
  await pane.getByRole("button", { name: "关闭小组应用面板", exact: true }).click();
  await page.locator("main textarea").fill("usable after resize");
  expect(state.errors).toEqual([]);
});

test("preserves cross-origin iframe state across tabs, channels, groups, and pane collapse without reloading", async ({ page }) => {
  const state = await marketplaceFixture(page);
  const mobile = page.viewportSize()!.width <= 780;
  const overlay = page.viewportSize()!.width <= 1250;
  const openAppsButton = page.getByRole("button", { name: "小组应用", exact: true });
  const pane = page.locator('aside[aria-label="小组应用"]');

  const navigateSidebar = async (href: string) => {
    if (mobile) {
      await page.locator(".app-main .mobile-menu").click();
    }
    await page.locator(`.navigation-sidebar a[href="${href}"]`).click();
  };

  // Open group 1 apps -> launch Fixture board & type state inside cross-origin iframe
  await openAppsButton.click();
  await pane.locator(".mp-launcher-card", { hasText: "Fixture board" }).click();
  const group1Board = page.frameLocator('#mp-pane-panel-group-board iframe');
  await group1Board.locator("#app-input").fill("group-1-board-state");

  // Open second tab in group 1 via "+" -> launch Fixture notes & type state
  await pane.getByRole("button", { name: "打开新应用", exact: true }).click();
  await pane.locator(".mp-launcher-card", { hasText: "Fixture notes" }).click();
  const group1Notes = page.frameLocator('#mp-pane-panel-group-notes iframe');
  await group1Notes.locator("#notes-input").fill("group-1-notes-state");
  await expect(openAppsButton.locator(".mp-running-badge")).toHaveText("2");

  // Switch back to Fixture board tab -> still holds "group-1-board-state"
  await pane.getByRole("tab", { name: "Fixture board" }).click();
  await expect(group1Board.locator("#app-input")).toHaveValue("group-1-board-state");

  // Switch to #general channel, then to Second group -> open Fixture board in Second group
  if (overlay) {
    await pane.getByRole("button", { name: "关闭小组应用面板", exact: true }).click();
  }
  await navigateSidebar("/app/w/workspace/c/general");
  await expect(pane).toBeHidden();

  await navigateSidebar("/app/w/workspace/c/group-2");
  await openAppsButton.click();
  await pane.locator(".mp-launcher-card", { hasText: "Fixture board" }).click();
  const group2Board = page.frameLocator('#mp-pane-panel-group-2-board iframe');
  await group2Board.locator("#app-input").fill("group-2-board-isolated");
  if (overlay) {
    await pane.getByRole("button", { name: "关闭小组应用面板", exact: true }).click();
  }

  // Return to Fixture group -> verify zero reload (state intact in both tabs) & close notes tab
  await navigateSidebar("/app/w/workspace/c/group");
  if (overlay) {
    await openAppsButton.click();
  }
  await expect(pane).toBeVisible();
  await expect(group1Board.locator("#app-input")).toHaveValue("group-1-board-state");
  await pane.getByRole("tab", { name: "Fixture notes" }).click();
  await expect(group1Notes.locator("#notes-input")).toHaveValue("group-1-notes-state");

  // Total session requests: 3 (group:board, group:notes, group-2:board) — zero reloads!
  expect(state.sessionRequests).toEqual(["group:board", "group:notes", "group-2:board"]);

  await pane.getByRole("button", { name: "关闭标签页：Fixture notes", exact: true }).click();
  await expect(page.locator('#mp-pane-panel-group-notes iframe')).toHaveCount(0);
  await expect(openAppsButton.locator(".mp-running-badge")).toHaveText("1");
  expect(state.errors).toEqual([]);
});
