import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it, vi } from "vitest";

const swSource = readFileSync(
  join(dirname(fileURLToPath(import.meta.url)), "../../public/sw.js"),
  "utf8",
);

class FakeCache {
  readonly store = new Map<string, Response>();
  private keyOf(req: RequestInfo | URL): string {
    if (typeof req === "string") return req;
    if (req instanceof URL) return req.pathname + req.search;
    const url = new URL(req.url);
    return url.pathname + url.search;
  }
  async addAll(paths: string[]) {
    for (const p of paths) {
      this.store.set(p, new Response(`precached:${p}`, { status: 200 }));
    }
  }
  async put(req: RequestInfo | URL, res: Response) {
    this.store.set(this.keyOf(req), res);
  }
  async match(req: RequestInfo | URL) {
    return this.store.get(this.keyOf(req));
  }
}

function createSwHarness(origin = "https://teamie.example") {
  const listeners = new Map<string, Array<(event: unknown) => void>>();
  const cacheBuckets = new Map<string, FakeCache>();
  const skipWaiting = vi.fn();
  const claim = vi.fn(async () => {});
  const openWindow = vi.fn(async (url: string) => ({ url }));
  const windowClients: Array<{
    url: string;
    focus: ReturnType<typeof vi.fn>;
    postMessage: ReturnType<typeof vi.fn>;
  }> = [];

  const caches = {
    open: vi.fn(async (name: string) => {
      let bucket = cacheBuckets.get(name);
      if (!bucket) {
        bucket = new FakeCache();
        cacheBuckets.set(name, bucket);
      }
      return bucket;
    }),
    keys: vi.fn(async () => Array.from(cacheBuckets.keys())),
    delete: vi.fn(async (name: string) => cacheBuckets.delete(name)),
  };

  const fetchMock = vi.fn<typeof fetch>();

  const selfObj = {
    location: { origin },
    skipWaiting,
    clients: {
      claim,
      matchAll: vi.fn(async () => windowClients),
      openWindow,
    },
    addEventListener(type: string, cb: (event: unknown) => void) {
      const list = listeners.get(type) || [];
      list.push(cb);
      listeners.set(type, list);
    },
  };

  const fn = new Function("self", "caches", "fetch", "Response", "URL", swSource);
  fn(selfObj, caches, fetchMock, Response, URL);

  async function dispatchExtendable(type: string, extra: Record<string, unknown> = {}) {
    const promises: Promise<unknown>[] = [];
    const event = {
      ...extra,
      waitUntil: (p: Promise<unknown>) => {
        promises.push(Promise.resolve(p));
      },
    };
    for (const cb of listeners.get(type) || []) cb(event);
    await Promise.all(promises);
  }

  async function dispatchFetch(
    url: string,
    opts: { method?: string; mode?: string } = {},
  ) {
    const promises: Promise<unknown>[] = [];
    let responded: Promise<Response> | null = null;
    const request = {
      url,
      method: opts.method || "GET",
      mode: opts.mode || "cors",
    };
    const event = {
      request,
      waitUntil: (p: Promise<unknown>) => {
        promises.push(Promise.resolve(p));
      },
      respondWith: (p: Promise<Response> | Response) => {
        responded = Promise.resolve(p);
      },
    };
    for (const cb of listeners.get("fetch") || []) cb(event);
    const pending = responded as Promise<Response> | null;
    const response = pending ? await pending : null;
    await Promise.all(promises);
    return { intercepted: pending !== null, response };
  }

  return {
    cacheBuckets,
    caches,
    fetchMock,
    skipWaiting,
    claim,
    openWindow,
    windowClients,
    dispatchExtendable,
    dispatchFetch,
    dispatchMessage: (data: unknown) => {
      for (const cb of listeners.get("message") || []) cb({ data });
    },
  };
}

describe("public/sw.js security and offline behavior", () => {
  it("precaches shell assets on install and purges only obsolete teamie-shell caches on activate", async () => {
    const sw = createSwHarness();
    sw.cacheBuckets.set("teamie-shell-old123", new FakeCache());
    sw.cacheBuckets.set("unrelated-cache", new FakeCache());

    await sw.dispatchExtendable("install");
    await sw.dispatchExtendable("activate");

    expect(sw.cacheBuckets.has("teamie-shell-old123")).toBe(false);
    expect(sw.cacheBuckets.has("unrelated-cache")).toBe(true);
    expect(sw.claim).toHaveBeenCalledOnce();
    const active = sw.cacheBuckets.get("teamie-shell-__TEAMIE_SW_VERSION__")!;
    expect(active.store.has("/index.html")).toBe(true);
    expect(active.store.has("/manifest.webmanifest")).toBe(true);
    expect(active.store.has("/icon-192.png")).toBe(true);
    expect(active.store.has("/icon-maskable-512.png")).toBe(true);
  });

  it("never intercepts or caches /api/*, /api/v1/ws, /internal/*, /sw.js, POST, or cross-origin requests", async () => {
    const sw = createSwHarness();
    await sw.dispatchExtendable("install");

    const bypassCases = [
      { url: "https://teamie.example/api/v1/auth/session" },
      { url: "https://teamie.example/api/v1/conversations/c1/messages" },
      { url: "https://teamie.example/api/v1/ws" },
      { url: "https://teamie.example/internal/metrics" },
      { url: "https://teamie.example/sw.js" },
      { url: "https://teamie.example/version.json" },
      { url: "https://teamie.example/healthz" },
      { url: "https://teamie.example/assets/app.js", method: "POST" },
      { url: "https://cdn.other.example/assets/app.js" },
    ];

    for (const testCase of bypassCases) {
      const result = await sw.dispatchFetch(testCase.url, {
        method: testCase.method,
      });
      expect(result.intercepted, testCase.url).toBe(false);
    }
    expect(sw.fetchMock).not.toHaveBeenCalled();
  });

  it("handles navigation network-first, caches only canonical /index.html without query secrets, and falls back offline", async () => {
    const sw = createSwHarness();
    await sw.dispatchExtendable("install");

    const htmlRes = new Response("<html>fresh</html>", {
      status: 200,
      headers: { "content-type": "text/html; charset=utf-8" },
    });
    Object.defineProperty(htmlRes, "type", { value: "basic" });
    sw.fetchMock.mockResolvedValueOnce(htmlRes);

    const onlineNav = await sw.dispatchFetch(
      "https://teamie.example/login?mode=register&inviteCode=TOP-SECRET-CODE",
      { mode: "navigate" },
    );
    expect(onlineNav.intercepted).toBe(true);
    expect(await onlineNav.response!.text()).toBe("<html>fresh</html>");

    const active = sw.cacheBuckets.get("teamie-shell-__TEAMIE_SW_VERSION__")!;
    for (const key of active.store.keys()) {
      expect(key).not.toContain("TOP-SECRET-CODE");
      expect(key).not.toContain("inviteCode");
    }

    // Offline navigation falls back to cached /index.html
    sw.fetchMock.mockRejectedValueOnce(new TypeError("Failed to fetch"));
    const offlineNav = await sw.dispatchFetch("https://teamie.example/app/w/w1/c/c1", {
      mode: "navigate",
    });
    expect(offlineNav.intercepted).toBe(true);
    expect(await offlineNav.response!.text()).toBe("<html>fresh</html>");
  });

  it("serves hashed /assets/* cache-first and skips waiting on TEAMIE_SKIP_WAITING", async () => {
    const sw = createSwHarness();
    await sw.dispatchExtendable("install");

    const jsRes = new Response("console.log('ok')", { status: 200 });
    Object.defineProperty(jsRes, "type", { value: "basic" });
    sw.fetchMock.mockResolvedValueOnce(jsRes);

    const first = await sw.dispatchFetch("https://teamie.example/assets/index-123.js");
    expect(first.intercepted).toBe(true);
    expect(await first.response!.text()).toBe("console.log('ok')");
    expect(sw.fetchMock).toHaveBeenCalledTimes(1);

    const second = await sw.dispatchFetch("https://teamie.example/assets/index-123.js");
    expect(second.intercepted).toBe(true);
    expect(await second.response!.text()).toBe("console.log('ok')");
    expect(sw.fetchMock).toHaveBeenCalledTimes(1);

    sw.dispatchMessage({ type: "TEAMIE_SKIP_WAITING" });
    expect(sw.skipWaiting).toHaveBeenCalledOnce();
  });

  it("handles notificationclick by focusing same-origin app window, posting safe relative URL, or opening window", async () => {
    const sw = createSwHarness();
    const close = vi.fn();
    const client = {
      url: "https://teamie.example/app/w/w1/c/c1",
      focus: vi.fn(async () => {}),
      postMessage: vi.fn(),
    };
    sw.windowClients.push(client);

    await sw.dispatchExtendable("notificationclick", {
      notification: {
        close,
        data: { url: "/app/w/w1/c/c2?message=m9" },
      },
    });
    expect(close).toHaveBeenCalledOnce();
    expect(client.postMessage).toHaveBeenCalledWith({
      type: "TEAMIE_NOTIFICATION_CLICK",
      url: "/app/w/w1/c/c2?message=m9",
    });
    expect(client.focus).toHaveBeenCalledOnce();

    // Unsafe external or protocol-relative URLs are sanitized back to /app
    sw.windowClients.length = 0;
    await sw.dispatchExtendable("notificationclick", {
      notification: {
        close: vi.fn(),
        data: { url: "https://evil.example/phish" },
      },
    });
    expect(sw.openWindow).toHaveBeenLastCalledWith("/app");

    await sw.dispatchExtendable("notificationclick", {
      notification: {
        close: vi.fn(),
        data: { url: "//evil.example/phish" },
      },
    });
    expect(sw.openWindow).toHaveBeenLastCalledWith("/app");
  });
});
