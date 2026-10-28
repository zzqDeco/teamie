// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ReactionPicker } from "./reaction-picker";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function rect(left: number, top: number, width: number, height: number) {
  return new DOMRect(left, top, width, height);
}

describe("reaction picker boundaries and portal interactions", () => {
  let host: HTMLDivElement;
  let anchor: HTMLDivElement;
  let trigger: HTMLButtonElement;
  let root: Root;
  let clip: DOMRect;
  let target: DOMRect;
  let resize: () => void;
  const onClose = vi.fn();
  const onSelect = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    vi.stubGlobal("innerWidth", 390);
    vi.stubGlobal("innerHeight", 844);
    vi.stubGlobal("visualViewport", undefined);
    vi.stubGlobal("ResizeObserver", class {
      constructor(callback: () => void) { resize = callback; }
      observe() {}
      disconnect() {}
    });
    clip = rect(0, 64, 390, 708);
    target = rect(213, 740, 32, 32);
    host = document.createElement("div");
    host.className = "message-scroll";
    host.style.overflow = "hidden";
    anchor = document.createElement("div");
    trigger = document.createElement("button");
    anchor.append(trigger);
    host.append(anchor);
    document.body.append(host);
    const mount = document.createElement("div");
    host.append(mount);
    root = createRoot(mount);
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
      if (this === host) return clip;
      if (this === anchor) return target;
      if (this.classList.contains("reaction-picker-popover")) return rect(0, 0, 280, 42);
      return rect(0, 0, 0, 0);
    });
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    host.remove();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  async function open() {
    await act(async () => root.render(
      <ReactionPicker anchorRef={{ current: anchor }} reactions={[]} userId="self" onSelect={onSelect} onClose={onClose} />,
    ));
    return document.querySelector<HTMLDivElement>(".reaction-picker-popover")!;
  }

  it.each([
    { viewport: "desktop", edge: "first", width: 1440, x: 317, right: 1317, y: 76, bottom: 745 },
    { viewport: "desktop", edge: "last", width: 1440, x: 317, right: 1317, y: 684, bottom: 745 },
    { viewport: "mobile", edge: "first", width: 390, x: 0, right: 245, y: 76, bottom: 772 },
    { viewport: "mobile", edge: "last", width: 390, x: 0, right: 245, y: 740, bottom: 772 },
  ])("keeps the $viewport $edge message picker inside visible bounds", async ({ edge, width, x, right, y, bottom }) => {
    vi.stubGlobal("innerWidth", width);
    clip = rect(x, 64, width - x, bottom - 64);
    target = rect(right - 32, y, 32, 32);
    const menu = await open();
    expect(menu.parentElement).toBe(document.body);
    expect(host.contains(menu)).toBe(false);
    const left = Number.parseFloat(menu.style.left);
    const top = Number.parseFloat(menu.style.top);
    expect(left).toBeGreaterThanOrEqual(x + 8);
    expect(left + 280).toBeLessThanOrEqual(width - 8);
    expect(top).toBeGreaterThanOrEqual(72);
    expect(top + 42).toBeLessThanOrEqual(bottom - 8);
    if (edge === "last") expect(top + 42).toBeLessThan(target.top);
    else expect(top).toBeGreaterThan(target.bottom);
  });

  it("repositions on resize and scrolling, and closes when its message leaves view", async () => {
    const menu = await open();
    target = rect(330, 400, 32, 32);
    window.dispatchEvent(new Event("resize"));
    expect(menu.style.top).toBe("438px");
    expect(menu.style.left).toBe("82px");
    target = rect(330, 200, 32, 32);
    host.dispatchEvent(new Event("scroll"));
    expect(menu.style.top).toBe("238px");
    target = rect(330, 100, 32, 32);
    resize();
    expect(menu.style.top).toBe("138px");
    target = rect(330, 0, 32, 32);
    host.dispatchEvent(new Event("scroll"));
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("respects the visual viewport when mobile browser chrome or the keyboard reduces it", async () => {
    const viewport = Object.assign(new EventTarget(), { offsetLeft: 0, offsetTop: 50, width: 390, height: 400 });
    vi.stubGlobal("visualViewport", viewport);
    target = rect(213, 380, 32, 32);
    const menu = await open();
    expect(Number.parseFloat(menu.style.top) + 42).toBeLessThanOrEqual(442);
    viewport.height = 350;
    viewport.dispatchEvent(new Event("resize"));
    expect(Number.parseFloat(menu.style.top) + 42).toBeLessThanOrEqual(392);
  });

  it("treats a portaled emoji and its trigger as inside, while outside presses dismiss", async () => {
    const menu = await open();
    const emoji = menu.querySelector("button")!;
    emoji.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    trigger.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    expect(onClose).not.toHaveBeenCalled();
    await act(async () => emoji.click());
    expect(onSelect).toHaveBeenCalledExactlyOnceWith("👍");
    document.body.dispatchEvent(new MouseEvent("pointerdown", { bubbles: true }));
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("supports keyboard selection and restores the trigger on Escape", async () => {
    const menu = await open();
    const buttons = menu.querySelectorAll("button");
    expect(document.activeElement).toBe(buttons[0]);
    buttons[0].dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true }));
    expect(document.activeElement).toBe(buttons[7]);
    buttons[7].dispatchEvent(new KeyboardEvent("keydown", { key: "Home", bubbles: true }));
    expect(document.activeElement).toBe(buttons[0]);
    buttons[0].dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(document.activeElement).toBe(trigger);
    expect(onClose).toHaveBeenCalledOnce();
  });
});
