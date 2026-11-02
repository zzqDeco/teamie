// @vitest-environment jsdom
import { act, Suspense } from "react";
import type { ReactElement } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { lazyView } from "./lazy-view";

describe("lazyView", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
  });

  it("renders Suspense fallback while pending and swaps to the resolved route view", async () => {
    let calls = 0;
    let resolveChunk!: (mod: { default: () => ReactElement }) => void;
    const DeferredRoute = lazyView(() => {
      calls += 1;
      return new Promise<{ default: () => ReactElement }>((resolve) => {
        resolveChunk = resolve;
      });
    });

    expect(calls).toBe(1);

    await act(async () => {
      root.render(
        <Suspense fallback={<div data-testid="route-loading">加载中</div>}>
          <DeferredRoute />
        </Suspense>,
      );
    });

    expect(calls).toBe(1);

    expect(
      container.querySelector('[data-testid="route-loading"]')?.textContent,
    ).toBe("加载中");

    await act(async () => {
      resolveChunk({
        default: () => <section data-testid="route-ready">已就绪</section>,
      });
    });

    expect(
      container.querySelector('[data-testid="route-ready"]')?.textContent,
    ).toBe("已就绪");
    expect(container.querySelector('[data-testid="route-loading"]')).toBeNull();
  });
});
