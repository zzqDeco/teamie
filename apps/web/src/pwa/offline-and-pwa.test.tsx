// @vitest-environment jsdom
import { act, createRef } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@teamie/api-client";
import type { Bootstrap, Conversation, Message, User } from "@teamie/api-client";
import { AuthProvider, useAuth } from "../auth";
import { retryTransientFailures, sendMessage } from "../chat";
import { MessageSendStatus } from "../chat/message-send-status";
import { readComposerDraft } from "../composer-drafts";
import { ConnectionBanner } from "../connection-banner";
import { clearUserLocalUiState } from "../logout-cleanup";
import {
  notifySyncedMessages,
  resetNotificationSession,
  setActiveNotificationContext,
  setNotificationPreferences,
  setNotificationServiceWorkerRegistration,
} from "../notifications";
import { ChatStore, chatStore } from "../store";
import { toast } from "../toast";
import { syncAppBadge } from "./badge";
import { _resetPwaInstallForTest } from "./install";
import { PwaInstallBanner, PwaInstallSettingsCard } from "./install-prompt";
import {
  clearOfflineShellSnapshot,
  clearPendingOfflineLogout,
  readOfflineBootstrap,
  readOfflineShellSnapshot,
  readOfflineWorkspaceMembers,
  readPendingOfflineLogout,
  saveOfflineBootstrap,
  saveOfflineUser,
  saveOfflineWorkspaceMembers,
} from "./offline-cache";
import { initServiceWorker } from "./register";

const user1: User = {
  id: "u-1",
  email: "u1@example.test",
  displayName: "林夕",
  kind: "human",
  platformRole: "user",
  createdAt: "2026-10-17T00:00:00Z",
};

const user2: User = {
  ...user1,
  id: "u-2",
  email: "u2@example.test",
  displayName: "周晴",
};

const conv1: Conversation = {
  id: "c-1",
  workspaceId: "w-1",
  type: "channel",
  name: "general",
  memberIds: ["u-1", "u-2"],
  lastSeq: 5,
  readSeq: 5,
  updatedAt: "2026-11-20T09:00:00Z",
};

const bootstrapFixture: Bootstrap = {
  user: user1,
  workspaces: [
    {
      id: "w-1",
      name: "产品空间",
      createdBy: "u-1",
      createdAt: "2026-10-17T00:00:00Z",
    },
  ],
  conversations: [conv1],
  capabilities: { "im.attachments.upload": true },
  flagsRevision: "test-v1",
  syncCursor: "5",
};

const localMsg = (overrides: Partial<Message> = {}): Message => ({
  id: "local:q-1",
  conversationId: "c-1",
  senderId: "u-1",
  senderName: "林夕",
  senderKind: "human",
  clientMessageId: "q-1",
  seq: Number.MAX_SAFE_INTEGER,
  body: "离线先发一条",
  createdAt: "2026-11-20T09:01:00Z",
  ...overrides,
});

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  localStorage.clear();
  chatStore.clear();
  clearOfflineShellSnapshot();
  clearPendingOfflineLogout();
  resetNotificationSession(null);
  setNotificationServiceWorkerRegistration(null);
  toast.clear();
  api.csrfToken = "";
  _resetPwaInstallForTest();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  chatStore.clear();
  clearOfflineShellSnapshot();
  clearPendingOfflineLogout();
  resetNotificationSession(null);
  setNotificationServiceWorkerRegistration(null);
  toast.clear();
  api.csrfToken = "";
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("offline shell cache & warm offline boot", () => {
  it("persists non-sensitive shell metadata without secrets, isolates accounts, and wipes on logout", () => {
    saveOfflineBootstrap(bootstrapFixture);
    saveOfflineWorkspaceMembers("u-1", "w-1", [
      {
        userId: "u-2",
        displayName: "周晴",
        role: "member",
        kind: "human",
      },
    ]);

    const raw = localStorage.getItem("teamie.offline.shell.v1.u-1")!;
    expect(raw).not.toContain("csrf");
    expect(readOfflineBootstrap("u-1")?.workspaces).toHaveLength(1);
    expect(readOfflineWorkspaceMembers("u-1", "w-1")).toHaveLength(1);

    // Switching to another account removes the previous account's offline snapshot.
    saveOfflineUser(user2);
    expect(localStorage.getItem("teamie.offline.shell.v1.u-1")).toBeNull();
    expect(readOfflineShellSnapshot()?.user.id).toBe("u-2");

    const clearAppBadge = vi.fn(async () => {});
    Object.defineProperty(navigator, "clearAppBadge", {
      value: clearAppBadge,
      configurable: true,
    });
    clearUserLocalUiState("u-2");
    expect(readOfflineShellSnapshot()).toBeNull();
    expect(clearAppBadge).toHaveBeenCalledOnce();
  });

  it("boots into warm offline mode on network failure, reconciles on online, and wipes cache on 401", async () => {
    saveOfflineBootstrap(bootstrapFixture);

    let authSnapshot: ReturnType<typeof useAuth> | null = null;
    function Probe() {
      authSnapshot = useAuth();
      return (
        <div data-offline={String(authSnapshot.offlineMode)}>
          {authSnapshot.user?.displayName || "none"}
        </div>
      );
    }

    const sessionSpy = vi
      .spyOn(api, "session")
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValueOnce({ user: user1, csrfToken: "restored-csrf" });

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => {
      root.render(
        <QueryClientProvider client={qc}>
          <MemoryRouter initialEntries={["/app"]}>
            <AuthProvider>
              <Probe />
            </AuthProvider>
          </MemoryRouter>
        </QueryClientProvider>,
      );
      await new Promise((r) => setTimeout(r, 0));
    });

    expect(authSnapshot!.offlineMode).toBe(true);
    expect(authSnapshot!.user?.id).toBe("u-1");
    expect(api.csrfToken).toBe("");
    expect(chatStore.getSnapshot().conversations["c-1"]).toBeDefined();

    // Online event silently verifies session and exits offline mode without wiping store.
    await act(async () => {
      window.dispatchEvent(new Event("online"));
      await new Promise((r) => setTimeout(r, 0));
    });
    expect(sessionSpy).toHaveBeenCalledTimes(2);
    expect(authSnapshot!.offlineMode).toBe(false);
    expect(api.csrfToken).toBe("restored-csrf");

    await act(async () => root.unmount());
    root = createRoot(host);

    // A 401 on cold start must immediately clear offline cache and refuse warm boot.
    saveOfflineBootstrap(bootstrapFixture);
    sessionSpy.mockRejectedValueOnce(new ApiError(401, "unauthorized", "expired"));
    await act(async () => {
      root.render(
        <QueryClientProvider client={qc}>
          <MemoryRouter initialEntries={["/app"]}>
            <AuthProvider>
              <Probe />
            </AuthProvider>
          </MemoryRouter>
        </QueryClientProvider>,
      );
      await new Promise((r) => setTimeout(r, 0));
    });
    expect(authSnapshot!.user).toBeNull();
    expect(authSnapshot!.offlineMode).toBe(false);
    expect(readOfflineShellSnapshot()).toBeNull();
  });

  it("persists offline logout intent, blocks silent cookie re-auth on reconnect, and completes server logout", async () => {
    saveOfflineBootstrap(bootstrapFixture);
    let authSnapshot: ReturnType<typeof useAuth> | null = null;
    function Probe() {
      authSnapshot = useAuth();
      return <div>{authSnapshot.user?.displayName || "signed-out"}</div>;
    }

    const sessionSpy = vi
      .spyOn(api, "session")
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValueOnce({ user: user1, csrfToken: "stale-cookie-csrf" });
    const logoutSpy = vi.spyOn(api, "logout").mockResolvedValue(undefined);

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => {
      root.render(
        <QueryClientProvider client={qc}>
          <MemoryRouter initialEntries={["/app"]}>
            <AuthProvider>
              <Probe />
            </AuthProvider>
          </MemoryRouter>
        </QueryClientProvider>,
      );
      await new Promise((r) => setTimeout(r, 0));
    });

    expect(authSnapshot!.offlineMode).toBe(true);
    expect(authSnapshot!.user?.id).toBe("u-1");

    // Sign out while offline: clears local cache and records pending server revocation.
    await act(async () => {
      await authSnapshot!.logout();
    });
    expect(authSnapshot!.user).toBeNull();
    expect(readOfflineShellSnapshot()).toBeNull();
    expect(readPendingOfflineLogout()?.userId).toBe("u-1");
    expect(logoutSpy).not.toHaveBeenCalled();

    // Reload after connectivity returns: live HttpOnly cookie must NOT silently sign back in;
    // AuthProvider must instead complete api.logout() and stay signed out.
    await act(async () => root.unmount());
    root = createRoot(host);
    await act(async () => {
      root.render(
        <QueryClientProvider client={qc}>
          <MemoryRouter initialEntries={["/login"]}>
            <AuthProvider>
              <Probe />
            </AuthProvider>
          </MemoryRouter>
        </QueryClientProvider>,
      );
      await new Promise((r) => setTimeout(r, 0));
    });

    expect(sessionSpy).toHaveBeenCalledTimes(2);
    expect(logoutSpy).toHaveBeenCalledOnce();
    expect(authSnapshot!.user).toBeNull();
    expect(api.csrfToken).toBe("");
    expect(readPendingOfflineLogout()).toBeNull();
  });

  it("renders offline warm-boot banner copy and retry action when offlineCached is true", async () => {
    const onRetry = vi.fn();
    await act(async () => {
      root.render(
        <ConnectionBanner
          connection="offline"
          offlineCached
          syncError=""
          showSyncError
          onRetry={onRetry}
        />,
      );
    });
    await act(async () => {
      await new Promise((r) => setTimeout(r, 20));
    });
    expect(host.textContent).toContain("离线模式（已展示本地缓存，联网后自动同步）");
    const retryBtn = host.querySelector<HTMLButtonElement>(".connection-banner button")!;
    expect(retryBtn.textContent).toContain("重试连接");
    retryBtn.click();
    expect(onRetry).toHaveBeenCalledOnce();
  });
});

describe("offline send queue, reload recovery, and MessageSendStatus", () => {
  it("queues offline sends without fetch, restores sending/queued across reload, and drains FIFO on recovery", async () => {
    chatStore.begin("u-1");
    chatStore.replaceConversations([conv1]);
    const sendSpy = vi.spyOn(api, "send");

    // Offline send queues without firing network fetch
    await sendMessage(localMsg({ clientMessageId: "q-1", body: "第一条" }), chatStore.generation, {
      offline: true,
    });
    expect(sendSpy).not.toHaveBeenCalled();
    expect(chatStore.getSnapshot().messages["c-1"]).toEqual([
      expect.objectContaining({
        clientMessageId: "q-1",
        status: "queued",
        permanent: false,
      }),
    ]);

    // Simulate reload while a second message was mid-"sending"
    chatStore.merge([
      {
        ...localMsg({
          id: "local:q-2",
          clientMessageId: "q-2",
          body: "第二条",
          createdAt: "2026-11-20T09:02:00Z",
        }),
        status: "sending",
      },
    ]);
    chatStore.flush();

    const reloaded = new ChatStore(localStorage);
    reloaded.begin("u-1");
    expect(
      reloaded.getSnapshot().messages["c-1"].map((m) => ({
        id: m.clientMessageId,
        status: m.status,
        permanent: m.permanent,
      })),
    ).toEqual([
      { id: "q-1", status: "queued", permanent: false },
      { id: "q-2", status: "queued", permanent: false },
    ]);

    // Serial FIFO drain sends both with original clientMessageId
    const sentOrder: string[] = [];
    sendSpy.mockImplementation(async (_cid, draft) => {
      sentOrder.push(draft.clientMessageId);
      return {
        message: {
          ...localMsg({
            id: `srv-${draft.clientMessageId}`,
            clientMessageId: draft.clientMessageId,
            body: draft.body,
            seq: sentOrder.length,
          }),
        },
      };
    });
    // Put q-2 in queued state on active chatStore as well
    chatStore.merge([
      {
        ...localMsg({
          id: "local:q-2",
          clientMessageId: "q-2",
          body: "第二条",
          createdAt: "2026-11-20T09:02:00Z",
        }),
        status: "queued",
        permanent: false,
      },
    ]);
    await retryTransientFailures(chatStore.generation);
    expect(sentOrder).toEqual(["q-1", "q-2"]);
    expect(
      chatStore.getSnapshot().messages["c-1"].every((m) => !m.status),
    ).toBe(true);
  });

  it("supports editing and cancelling queued messages in MessageSendStatus", async () => {
    chatStore.begin("u-1");
    chatStore.replaceConversations([conv1]);
    const queued = {
      ...localMsg({ clientMessageId: "q-edit", body: "待修改草稿" }),
      status: "queued" as const,
      permanent: false,
    };
    chatStore.merge([queued]);

    const inputRef = createRef<HTMLTextAreaElement>();
    await act(async () => {
      root.render(
        <>
          <textarea ref={inputRef} />
          <MessageSendStatus
            message={queued}
            conversationId="c-1"
            inputRef={inputRef}
          />
        </>,
      );
    });

    expect(host.textContent).toContain("等待网络恢复后发送");
    const buttons = Array.from(host.querySelectorAll<HTMLButtonElement>(".queued-status button"));
    expect(buttons.map((b) => b.textContent?.trim())).toEqual(["编辑", "取消发送"]);

    await act(async () => {
      buttons[0].click();
    });
    expect(readComposerDraft("c-1").body).toBe("待修改草稿");
    expect(chatStore.getSnapshot().messages["c-1"]).toHaveLength(0);
  });
});

describe("SW notifications, SW update lifecycle, app badge, and PWA install UI", () => {
  it("prefers ServiceWorkerRegistration.showNotification and clears SW notifications on reset", async () => {
    const closeSw = vi.fn();
    const showNotification = vi.fn(async () => {});
    const getNotifications = vi.fn(async () => [
      { tag: "teamie-message-m-99", close: closeSw },
    ]);
    setNotificationServiceWorkerRegistration({
      showNotification,
      getNotifications,
    } as unknown as ServiceWorkerRegistration);

    const windowNotify = vi.fn();
    vi.stubGlobal(
      "Notification",
      Object.assign(windowNotify, { permission: "granted" }),
    );

    chatStore.begin("u-1");
    chatStore.replaceConversations([{ ...conv1, readSeq: 0 }]);
    const incoming: Message = {
      id: "m-99",
      conversationId: "c-1",
      senderId: "u-2",
      senderName: "周晴",
      senderKind: "human",
      clientMessageId: "srv-99",
      seq: 6,
      body: "@林夕 请看一下",
      mentions: [{ userId: "u-1", start: 0, end: 3 }],
      createdAt: "2026-11-20T09:10:00Z",
    };
    chatStore.merge([incoming]);
    setActiveNotificationContext(null);
    setNotificationPreferences({ enabled: true, showPreview: false }, "u-1");

    notifySyncedMessages(
      [{ conversationId: "c-1", messageId: "m-99" }],
      chatStore.getSnapshot(),
      "u-1",
    );

    expect(showNotification).toHaveBeenCalledWith(
      "周晴 · general",
      expect.objectContaining({
        body: "在消息中提及了你",
        icon: "/icon-192.png",
        badge: "/icon-192.png",
        tag: "teamie-message-m-99",
        data: { url: "/app/w/w-1/c/c-1?message=m-99" },
      }),
    );
    expect(windowNotify).not.toHaveBeenCalled();

    await new Promise((r) => setTimeout(r, 0));
    closeSw.mockClear();
    resetNotificationSession(null);
    await new Promise((r) => setTimeout(r, 0));
    expect(closeSw).toHaveBeenCalledOnce();
  });

  it("drops delayed SW showNotification fallback if session was reset or notifications were disabled", async () => {
    let rejectSw!: (err: Error) => void;
    const showNotification = vi.fn(
      () => new Promise<void>((_res, rej) => { rejectSw = rej; }),
    );
    setNotificationServiceWorkerRegistration({
      showNotification,
    } as unknown as ServiceWorkerRegistration);

    const windowNotify = vi.fn();
    vi.stubGlobal(
      "Notification",
      Object.assign(windowNotify, { permission: "granted" }),
    );

    chatStore.begin("u-1");
    chatStore.replaceConversations([{ ...conv1, readSeq: 0 }]);
    const incoming: Message = {
      id: "m-101",
      conversationId: "c-1",
      senderId: "u-2",
      senderName: "周晴",
      senderKind: "human",
      clientMessageId: "srv-101",
      seq: 7,
      body: "@林夕 机密内容",
      mentions: [{ userId: "u-1", start: 0, end: 3 }],
      createdAt: "2026-11-20T09:11:00Z",
    };
    chatStore.merge([incoming]);
    setActiveNotificationContext(null);
    setNotificationPreferences({ enabled: true, showPreview: true }, "u-1");

    notifySyncedMessages(
      [{ conversationId: "c-1", messageId: "m-101" }],
      chatStore.getSnapshot(),
      "u-1",
    );
    expect(showNotification).toHaveBeenCalledOnce();

    // User signs out BEFORE showNotification rejects: fallback must NOT fire!
    resetNotificationSession(null);
    chatStore.clear();
    rejectSw(new Error("SW unavailable"));
    await new Promise((r) => setTimeout(r, 0));
    expect(windowNotify).not.toHaveBeenCalled();
  });

  it("handles waiting SW updates via toast action and routes SW notification clicks", async () => {
    const swListeners = new Map<string, Array<(e: Event) => void>>();
    const waitingPost = vi.fn();
    const onReload = vi.fn();
    const onNavigate = vi.fn();
    const toastSpy = vi.spyOn(toast, "info");

    const reg = {
      active: {} as ServiceWorker,
      waiting: { postMessage: waitingPost } as unknown as ServiceWorker,
      installing: null,
      addEventListener: vi.fn(),
    } as unknown as ServiceWorkerRegistration;

    const cleanup = initServiceWorker(
      {
        controller: {} as ServiceWorker,
        ready: Promise.resolve(reg),
        register: vi.fn(async () => reg),
        addEventListener: (type, cb) => {
          const list = swListeners.get(type) || [];
          list.push(cb as (e: Event) => void);
          swListeners.set(type, list);
        },
        removeEventListener: vi.fn(),
      },
      { onReload, onNavigate },
    );

    await new Promise((r) => setTimeout(r, 0));
    expect(toastSpy).toHaveBeenCalledWith(
      "新版本已就绪",
      expect.objectContaining({
        duration: 0,
        action: expect.objectContaining({ label: "点击刷新" }),
      }),
    );

    // Clicking toast action sends TEAMIE_SKIP_WAITING, then controllerchange triggers reload
    const action = toastSpy.mock.calls[0][1]!.action!;
    action.onClick();
    expect(waitingPost).toHaveBeenCalledWith({ type: "TEAMIE_SKIP_WAITING" });
    for (const cb of swListeners.get("controllerchange") || []) {
      cb(new Event("controllerchange"));
    }
    expect(onReload).toHaveBeenCalledOnce();

    // SW notification click message routes through SPA navigator
    for (const cb of swListeners.get("message") || []) {
      cb({
        data: {
          type: "TEAMIE_NOTIFICATION_CLICK",
          url: "/app/w/w-1/c/c-1?message=m-5",
        },
      } as unknown as Event);
    }
    expect(onNavigate).toHaveBeenCalledWith("/app/w/w-1/c/c-1?message=m-5");
    cleanup();
  });

  it("syncs app badge counts via Badging API and handles PWA install prompt & dismissal", async () => {
    const setAppBadge = vi.fn(async () => {});
    const clearAppBadge = vi.fn(async () => {});
    Object.defineProperty(navigator, "setAppBadge", {
      value: setAppBadge,
      configurable: true,
    });
    Object.defineProperty(navigator, "clearAppBadge", {
      value: clearAppBadge,
      configurable: true,
    });

    syncAppBadge(4);
    expect(setAppBadge).toHaveBeenCalledWith(4);
    syncAppBadge(0);
    expect(clearAppBadge).toHaveBeenCalledOnce();

    const promptFn = vi.fn(async () => {});
    const installEvent = new Event("beforeinstallprompt", {
      cancelable: true,
    }) as Event & {
      prompt: () => Promise<void>;
      userChoice: Promise<{ outcome: "accepted" | "dismissed"; platform: string }>;
    };
    installEvent.prompt = promptFn;
    installEvent.userChoice = Promise.resolve({
      outcome: "dismissed",
      platform: "web",
    });

    await act(async () => {
      root.render(
        <>
          <PwaInstallBanner mobile />
          <PwaInstallSettingsCard />
        </>,
      );
    });
    await act(async () => {
      window.dispatchEvent(installEvent);
    });
    expect(installEvent.defaultPrevented).toBe(true);
    expect(host.querySelector(".pwa-install-banner")).not.toBeNull();

    // Dismissing banner persists preference while settings card stays accessible
    const dismissBtn = host.querySelector<HTMLButtonElement>(
      'button[aria-label="不再提示安装"]',
    )!;
    await act(async () => {
      dismissBtn.click();
    });
    expect(host.querySelector(".pwa-install-banner")).toBeNull();
    expect(localStorage.getItem("teamie.pwa.installDismissed.v1")).toBe("1");

    const settingsInstallBtn = host.querySelector<HTMLButtonElement>(
      ".pwa-install-card button",
    )!;
    await act(async () => {
      settingsInstallBtn.click();
    });
    expect(promptFn).toHaveBeenCalledOnce();
  });
});
