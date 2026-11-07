// @vitest-environment jsdom
import { act, lazy, useState } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { Link, MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { RouteBoundary } from "./route-boundary";

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("route loading boundaries", () => {
  it("shows an accessible loading state for a page that has not rendered yet", async () => {
    const PendingPage = lazy(() => new Promise<{ default: () => React.JSX.Element }>(() => {}));
    await act(async () => root.render(
      <MemoryRouter><RouteBoundary><PendingPage /></RouteBoundary></MemoryRouter>,
    ));
    expect(container.querySelector('[role="status"]')?.textContent).toContain("正在加载页面");
  });

  it("loads a page only when selected and preserves surrounding navigation while waiting", async () => {
    let resolve!: (module: { default: () => React.JSX.Element }) => void;
    const pending = new Promise<{ default: () => React.JSX.Element }>((done) => { resolve = done; });
    const load = vi.fn(() => pending);
    const Settings = lazy(load);
    await act(async () => root.render(
      <MemoryRouter>
        <header>工作空间导航<Link to="/settings">账号设置</Link></header>
        <RouteBoundary>
          <Routes>
            <Route path="/" element={<p>当前对话</p>} />
            <Route path="/settings" element={<Settings />} />
          </Routes>
        </RouteBoundary>
      </MemoryRouter>,
    ));
    expect(load).not.toHaveBeenCalled();
    await act(async () => container.querySelector<HTMLAnchorElement>("a")!.click());
    expect(load).toHaveBeenCalledTimes(1);
    expect(container.querySelector("header")?.textContent).toContain("工作空间导航");
    expect(container.textContent).toContain("当前对话");
    await act(async () => resolve({ default: () => <p>已载入设置</p> }));
    expect(container.textContent).toContain("已载入设置");
    expect(container.querySelector('[role="status"]')).toBeNull();
  });

  it("does not remount an already loaded shell when the URL changes", async () => {
    function StatefulShell() {
      const [count, setCount] = useState(0);
      return <><button onClick={() => setCount((value) => value + 1)}>{count}</button><Link to="/next">切换会话</Link></>;
    }
    await act(async () => root.render(
      <MemoryRouter><RouteBoundary><StatefulShell /></RouteBoundary></MemoryRouter>,
    ));
    await act(async () => container.querySelector<HTMLButtonElement>("button")!.click());
    await act(async () => container.querySelector<HTMLAnchorElement>("a")!.click());
    expect(container.querySelector("button")?.textContent).toBe("1");
  });

  it("shows a recoverable failure instead of a blank screen and allows another route to load", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    const FailedPage = lazy(() => Promise.reject(new Error("chunk unavailable")));
    await act(async () => root.render(
      <MemoryRouter>
        <Link to="/other">返回可用页面</Link>
        <RouteBoundary>
          <Routes>
            <Route path="/" element={<FailedPage />} />
            <Route path="/other" element={<p>可用页面</p>} />
          </Routes>
        </RouteBoundary>
      </MemoryRouter>,
    ));
    expect(container.querySelector('[role="alert"]')?.textContent).toContain("页面暂时未能加载");
    expect(container.querySelector("button")?.textContent).toContain("重新加载");
    await act(async () => container.querySelector<HTMLAnchorElement>("a")!.click());
    expect(container.querySelector('[role="alert"]')).toBeNull();
    expect(container.querySelector("p")?.textContent).toBe("可用页面");
  });
});
