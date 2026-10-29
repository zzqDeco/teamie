import { expect, test } from "@playwright/test";
import { marketplaceFixture } from "./marketplace-fixture";

test("keyboard enters and leaves the cross-origin app, then closes the pane", async ({ page }) => {
  const state = await marketplaceFixture(page);
  const overlay = page.viewportSize()!.width < 1250;
  const open = page.getByRole("button", { name: "群聊应用", exact: true });
  await open.click();
  const pane = page.locator('aside[aria-label="群聊应用"]');
  const frame = page.frameLocator("iframe");
  await expect(frame.locator("#app-input")).toBeVisible();
  await expect(page.locator("iframe")).toHaveAttribute("sandbox", "allow-scripts allow-forms allow-same-origin");
  await page.getByRole("button", { name: "刷新应用会话", exact: true }).focus();
  await page.keyboard.press("Tab");
  await expect(frame.locator("#app-input")).toBeFocused();
  await page.keyboard.type("editable in the app");
  await expect(frame.locator("#app-input")).toHaveValue("editable in the app");
  await page.keyboard.press("Shift+Tab");
  await expect(page.getByRole("button", { name: "刷新应用会话", exact: true })).toBeFocused();
  await page.keyboard.press("Tab");
  await page.keyboard.press("Tab");
  await expect(frame.locator("#app-save")).toBeFocused();
  await page.keyboard.press("Tab");
  if (overlay) {
    await expect(pane.getByRole("button", { name: "管理工作区应用", exact: true })).toBeFocused();
    await page.keyboard.press("Shift+Tab");
    await expect(page.locator("iframe")).toBeFocused();
    await pane.getByRole("button", { name: "关闭群聊应用面板", exact: true }).focus();
    await page.keyboard.press("Escape");
    await expect(open).toBeFocused();
  } else {
    await pane.getByRole("button", { name: "关闭群聊应用面板", exact: true }).click();
  }
  await expect(pane).toHaveCount(0);
  await expect(page.locator("main")).not.toHaveAttribute("inert");
  await page.locator("main textarea").fill("chat is usable after close");
  expect(state.errors).toEqual([]);
});

test("capability refresh closes the pane and never leaves chat inert or reopens it", async ({ page }) => {
  const state = await marketplaceFixture(page);
  const open = page.getByRole("button", { name: "群聊应用", exact: true });
  await open.click();
  await page.frameLocator("iframe").locator("#app-input").focus();
  state.enabled = false;
  await page.clock.fastForward(61000);
  await expect(page.locator('aside[aria-label="群聊应用"]')).toHaveCount(0);
  await expect(page.locator("main")).not.toHaveAttribute("inert");
  await expect(page.locator(".app-shell")).not.toHaveClass(/details-open/);
  await expect(open).toHaveCount(0);
  await page.locator("main textarea").fill("chat works with apps disabled");
  state.enabled = true;
  await page.clock.fastForward(61000);
  await expect(open).toBeVisible();
  await expect(page.locator("iframe")).toHaveCount(0);
  await open.click();
  await expect(page.frameLocator("iframe").locator("#app-input")).toBeVisible();
  expect(state.errors).toEqual([]);
});

test("nested management dialog and breakpoint changes preserve focus and chat access", async ({ page }) => {
  const state = await marketplaceFixture(page);
  await page.getByRole("button", { name: "群聊应用", exact: true }).click();
  const pane = page.locator('aside[aria-label="群聊应用"]');
  await pane.getByRole("button", { name: "管理工作区应用", exact: true }).click();
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
  await pane.getByRole("button", { name: "关闭群聊应用面板", exact: true }).click();
  await page.locator("main textarea").fill("usable after resize");
  expect(state.errors).toEqual([]);
});
