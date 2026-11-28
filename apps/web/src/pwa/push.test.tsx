// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { api } from "@teamie/api-client";
import type { Bootstrap, Session, User } from "@teamie/api-client";
import { AuthProvider, useAuth } from "../auth";
import {
  revokeWebPushOnLogout,
  subscribeWebPush,
  unsubscribeWebPush,
  urlBase64ToUint8Array,
} from "./push";
import { WebPushSettingsControl } from "./push-settings";

const VALID_VAPID_KEY =
  "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4";

const alice: User = {
  id: "u1",
  email: "alice@example.com",
  displayName: "Alice",
  kind: "human",
  platformRole: "user",
  createdAt: "2026-11-28T00:00:00Z",
};

function makeBootstrap(pushEnabled: boolean): Bootstrap {
  return {
    user: alice,
    workspaces: [],
    conversations: [],
    cursor: "0",
    flagRevision: "im-collab-v2",
    capabilities: { "im.push.web": pushEnabled },
  } as unknown as Bootstrap;
}

function makeFakeSubscription(
  endpoint = "https://fcm.googleapis.com/fcm/send/dev-1",
) {
  const unsubscribe = vi.fn(async () => true);
  return {
    endpoint,
    options: {
      userVisibleOnly: true,
      applicationServerKey: urlBase64ToUint8Array(VALID_VAPID_KEY).buffer,
    },
    toJSON: () => ({
      endpoint,
      keys: {
        p256dh: VALID_VAPID_KEY,
        auth: "BTBZMqHH6r4Tts7J_aSIgg",
      },
    }),
    unsubscribe,
  } as unknown as PushSubscription;
}

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

async function flushMicrotasks(rounds = 4) {
  for (let i = 0; i < rounds; i += 1) {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
  }
}

describe("Web Push helper & settings UI", () => {
  const origServiceWorker = navigator.serviceWorker;
  const origPushManager = (window as unknown as { PushManager?: unknown })
    .PushManager;
  const origNotification = window.Notification;

  let activeSub: PushSubscription | null = null;
  let subscribeMock: ReturnType<typeof vi.fn>;
  let getSubscriptionMock: ReturnType<typeof vi.fn>;
  let container: HTMLDivElement | null = null;
  let root: Root | null = null;

  beforeEach(() => {
    activeSub = null;
    subscribeMock = vi.fn(async () => {
      activeSub = makeFakeSubscription();
      return activeSub;
    });
    getSubscriptionMock = vi.fn(async () => activeSub);

    const reg = {
      active: {} as ServiceWorker,
      pushManager: {
        getSubscription: getSubscriptionMock,
        subscribe: subscribeMock,
      },
    } as unknown as ServiceWorkerRegistration;

    Object.defineProperty(navigator, "serviceWorker", {
      configurable: true,
      value: {
        getRegistration: vi.fn(async () => reg),
        ready: Promise.resolve(reg),
      },
    });
    Object.defineProperty(window, "PushManager", {
      configurable: true,
      value: function PushManager() {},
    });
    Object.defineProperty(window, "Notification", {
      configurable: true,
      value: {
        permission: "granted" as NotificationPermission,
        requestPermission: vi.fn(
          async () => "granted" as NotificationPermission,
        ),
      },
    });
  });

  afterEach(async () => {
    if (root) {
      await act(async () => {
        root?.unmount();
      });
      root = null;
    }
    if (container) {
      container.remove();
      container = null;
    }
    vi.restoreAllMocks();
    Object.defineProperty(navigator, "serviceWorker", {
      configurable: true,
      value: origServiceWorker,
    });
    Object.defineProperty(window, "PushManager", {
      configurable: true,
      value: origPushManager,
    });
    Object.defineProperty(window, "Notification", {
      configurable: true,
      value: origNotification,
    });
  });

  function mountNode(node: React.ReactNode) {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    act(() => {
      root?.render(node);
    });
    return container;
  }

  it("converts base64url VAPID key, subscribes, upserts, and unsubscribes cleanly", async () => {
    const bytes = urlBase64ToUint8Array(VALID_VAPID_KEY);
    expect(bytes.length).toBe(65);
    expect(bytes[0]).toBe(0x04);

    const upsertSpy = vi.spyOn(api, "upsertPushSubscription").mockResolvedValue({
      subscription: {
        id: "sub-1",
        endpointHash: "abc",
        endpointHost: "fcm.googleapis.com",
        userAgent: "test",
        createdAt: "2026-11-28T00:00:00Z",
        updatedAt: "2026-11-28T00:00:00Z",
      },
    });
    const deleteSpy = vi
      .spyOn(api, "deletePushSubscription")
      .mockResolvedValue(undefined);

    const sub = await subscribeWebPush(VALID_VAPID_KEY);
    expect(subscribeMock).toHaveBeenCalledOnce();
    expect(upsertSpy).toHaveBeenCalledWith(
      expect.objectContaining({
        endpoint: sub.endpoint,
        keys: {
          p256dh: VALID_VAPID_KEY,
          auth: "BTBZMqHH6r4Tts7J_aSIgg",
        },
      }),
    );

    await unsubscribeWebPush();
    expect(deleteSpy).toHaveBeenCalledWith(sub.endpoint);
    expect(sub.unsubscribe).toHaveBeenCalledOnce();
  });

  it("rolls back newly created browser subscription if backend upsert fails", async () => {
    vi.spyOn(api, "upsertPushSubscription").mockRejectedValue(
      new Error("server error"),
    );

    await expect(subscribeWebPush(VALID_VAPID_KEY)).rejects.toThrow(
      "server error",
    );
    expect(activeSub?.unsubscribe).toHaveBeenCalledOnce();
  });

  it("revokes push subscription before api.logout() during logout", async () => {
    const sub = makeFakeSubscription();
    activeSub = sub;
    const order: string[] = [];

    const session: Session = { user: alice, csrfToken: "csrf-1" };
    vi.spyOn(api, "session").mockResolvedValue(session);
    vi.spyOn(api, "deletePushSubscription").mockImplementation(async () => {
      order.push("deletePush");
    });
    vi.spyOn(api, "logout").mockImplementation(async () => {
      order.push("logout");
    });

    function LogoutButton() {
      const { user, logout } = useAuth();
      if (!user) return <div data-testid="state">logged-out</div>;
      return (
        <button type="button" onClick={() => void logout()}>
          do-logout
        </button>
      );
    }

    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const el = mountNode(
      <QueryClientProvider client={qc}>
        <MemoryRouter>
          <AuthProvider>
            <LogoutButton />
          </AuthProvider>
        </MemoryRouter>
      </QueryClientProvider>,
    );

    await flushMicrotasks();
    const btn = el.querySelector("button");
    expect(btn?.textContent).toBe("do-logout");

    await act(async () => {
      btn?.click();
      await Promise.resolve();
    });
    await flushMicrotasks();

    expect(el.textContent).toContain("logged-out");
    expect(order).toEqual(["deletePush", "logout"]);
    expect(sub.unsubscribe).toHaveBeenCalledOnce();
  });

  it("revokeWebPushOnLogout swallows backend errors cleanly", async () => {
    activeSub = makeFakeSubscription();
    vi.spyOn(api, "deletePushSubscription").mockRejectedValue(
      new Error("network down"),
    );
    await expect(revokeWebPushOnLogout()).resolves.toBeUndefined();
    expect(activeSub.unsubscribe).toHaveBeenCalledOnce();
  });

  it("revokeWebPushOnLogout aborts hanging server DELETE and completes local unsubscribe before returning", async () => {
    const sub = makeFakeSubscription();
    activeSub = sub;
    let capturedSignal: AbortSignal | undefined;
    let unsubscribedBeforeReturn = false;
    (sub.unsubscribe as ReturnType<typeof vi.fn>).mockImplementation(async () => {
      unsubscribedBeforeReturn = true;
      return true;
    });
    vi.spyOn(api, "deletePushSubscription").mockImplementation(
      (_endpoint, signal) => {
        capturedSignal = signal;
        return new Promise<void>(() => {
          // Intentionally never settles to simulate a stalled network request.
        });
      },
    );

    await revokeWebPushOnLogout(30);
    expect(capturedSignal?.aborted).toBe(true);
    expect(unsubscribedBeforeReturn).toBe(true);
    expect(sub.unsubscribe).toHaveBeenCalledOnce();
  });

  it("hides WebPushSettingsControl when im.push.web is false and toggles when enabled", async () => {
    const session: Session = { user: alice, csrfToken: "csrf-1" };
    vi.spyOn(api, "session").mockResolvedValue(session);
    const configSpy = vi.spyOn(api, "pushConfig").mockResolvedValue({
      enabled: true,
      vapidPublicKey: VALID_VAPID_KEY,
      maxSubscriptions: 10,
    });
    const upsertSpy = vi.spyOn(api, "upsertPushSubscription").mockResolvedValue({
      subscription: {
        id: "sub-1",
        endpointHash: "abc",
        endpointHost: "fcm.googleapis.com",
        userAgent: "test",
        createdAt: "2026-11-28T00:00:00Z",
        updatedAt: "2026-11-28T00:00:00Z",
      },
    });

    // 1. Flag off -> zero pushConfig requests, switch hidden.
    const qcOff = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const elOff = mountNode(
      <QueryClientProvider client={qcOff}>
        <MemoryRouter>
          <AuthProvider>
            <WebPushSettingsControl notificationPermission="granted" />
          </AuthProvider>
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await flushMicrotasks();
    act(() => {
      qcOff.setQueryData<Bootstrap>(["bootstrap", "u1"], makeBootstrap(false));
    });
    await flushMicrotasks();
    expect(elOff.textContent).not.toContain("开启后台锁屏推送");
    expect(configSpy).not.toHaveBeenCalled();

    await act(async () => {
      root?.unmount();
    });
    elOff.remove();
    root = null;
    container = null;

    // 2. Flag on + server enabled -> renders switch and subscribes on toggle.
    const qcOn = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const elOn = mountNode(
      <QueryClientProvider client={qcOn}>
        <MemoryRouter>
          <AuthProvider>
            <WebPushSettingsControl notificationPermission="granted" />
          </AuthProvider>
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await flushMicrotasks();
    act(() => {
      qcOn.setQueryData<Bootstrap>(["bootstrap", "u1"], makeBootstrap(true));
    });
    await flushMicrotasks(12);
    expect(elOn.textContent).toContain("开启后台锁屏推送");

    const toggle = elOn.querySelector<HTMLInputElement>('input[type="checkbox"]');
    expect(toggle).not.toBeNull();
    expect(toggle?.disabled).toBe(false);

    await act(async () => {
      toggle?.click();
      await Promise.resolve();
    });
    await flushMicrotasks(12);
    expect(upsertSpy).toHaveBeenCalledOnce();
  });
});
