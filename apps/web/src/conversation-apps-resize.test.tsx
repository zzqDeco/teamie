// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, createRef } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type {
  Conversation,
  MarketplaceApp,
  User,
  Workspace,
} from "@teamie/api-client";
import {
  clearPersistedAppPaneWidth,
  computeMaxAppPaneWidth,
  ConversationAppsPane,
  DEFAULT_APP_PANE_WIDTH,
  MAX_APP_PANE_WIDTH_CEILING,
  MIN_APP_PANE_WIDTH,
  readPersistedAppPaneWidth,
  writePersistedAppPaneWidth,
} from "./marketplace";
import { ShellMembersPane } from "./shell-members-pane";
import { chatStore } from "./store";

const srcDir = dirname(fileURLToPath(import.meta.url));

const userA: User = {
  id: "u-alice",
  kind: "human",
  platformRole: "user",
  displayName: "Alice",
  email: "alice@example.test",
  createdAt: "2026-11-01T00:00:00Z",
};

const userB: User = {
  id: "u-bob",
  kind: "human",
  platformRole: "user",
  displayName: "Bob",
  email: "bob@example.test",
  createdAt: "2026-11-01T00:00:00Z",
};

const sampleWorkspace: Workspace = {
  id: "ws-1",
  name: "研发空间",
  createdBy: userA.id,
  createdAt: "2026-11-01T00:00:00Z",
};

const groupConvA: Conversation = {
  id: "conv-group-a",
  workspaceId: sampleWorkspace.id,
  type: "group",
  name: "架构讨论群",
  memberIds: [userA.id, userB.id],
  lastSeq: 0,
  readSeq: 0,
  notificationLevel: "all",
  updatedAt: "2026-11-01T00:00:00Z",
};

const groupConvB: Conversation = {
  ...groupConvA,
  id: "conv-group-b",
  name: "发布协同群",
};

const sampleApp: MarketplaceApp = {
  id: "app-board",
  slug: "board-room",
  name: "协作白板",
  description: "群聊实时白板",
  webUrl: "https://board.example.test/ui",
  mcpUrl: "https://board.example.test/mcp",
  enabled: true,
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
};

function dispatchPointer(
  target: EventTarget,
  type: string,
  init: { clientX?: number; pointerId?: number; button?: number } = {},
) {
  const event = new MouseEvent(type, {
    bubbles: true,
    cancelable: true,
    clientX: init.clientX ?? 0,
    button: init.button ?? 0,
  });
  if (init.pointerId !== undefined) {
    Object.defineProperty(event, "pointerId", {
      value: init.pointerId,
      configurable: true,
    });
  }
  target.dispatchEvent(event);
  return event;
}

let host: HTMLDivElement;
let root: Root;
let qc: QueryClient;

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal("innerWidth", 1440);
  localStorage.clear();
  chatStore.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });

  vi.spyOn(api, "conversationApps").mockResolvedValue({ apps: [sampleApp] });
  vi.spyOn(api, "createConversationAppSession").mockResolvedValue({
    app: sampleApp,
    token: "ctx-token-1",
    expiresAt: "2099-01-01T00:00:00Z",
  });
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  qc.clear();
  chatStore.clear();
  localStorage.clear();
  document.body.classList.remove("mp-pane-resizing");
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function flush() {
  for (let i = 0; i < 4; i++) {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
  }
}

async function renderAppsPane(
  overrides: Partial<Parameters<typeof ConversationAppsPane>[0]> = {},
) {
  const noop = () => {};
  await act(async () => {
    root.render(
      <QueryClientProvider client={qc}>
        <ConversationAppsPane
          overlayMembers={false}
          mobile={false}
          sidebar={false}
          workspace={sampleWorkspace}
          conversation={groupConvA}
          user={userA}
          owner={true}
          onClose={noop}
          onOpenWorkspaceApps={noop}
          {...overrides}
        />
      </QueryClientProvider>,
    );
  });
  await flush();
}

function getAppsAside() {
  return host.querySelector<HTMLElement>('aside[aria-label="小组应用"]')!;
}

function getSeparator() {
  return host.querySelector<HTMLDivElement>('[role="separator"]');
}

describe("ConversationAppsPane resizable desktop width & persistence (Issue #135)", () => {
  it("uses 400px desktop default width on ConversationAppsPane while keeping ShellMembersPane untouched", async () => {
    await renderAppsPane();

    const appsAside = getAppsAside();
    expect(appsAside.classList.contains("members-pane")).toBe(true);
    expect(appsAside.classList.contains("mp-apps-pane")).toBe(true);
    expect(appsAside.style.getPropertyValue("--mp-pane-width")).toBe(
      `${DEFAULT_APP_PANE_WIDTH}px`,
    );
    expect(DEFAULT_APP_PANE_WIDTH).toBe(400);

    const separator = getSeparator()!;
    expect(separator).not.toBeNull();
    expect(separator.getAttribute("aria-orientation")).toBe("vertical");
    expect(separator.getAttribute("aria-label")).toBe("调节小组应用面板宽度");
    expect(separator.getAttribute("aria-valuenow")).toBe("400");
    expect(separator.getAttribute("aria-valuemin")).toBe(
      String(MIN_APP_PANE_WIDTH),
    );
    expect(separator.getAttribute("aria-valuemax")).toBe("640");
    expect(separator.tabIndex).toBe(0);

    // Verify ShellMembersPane stays untouched (no mp-apps-pane class, no inline width var, no separator).
    const membersHost = document.createElement("div");
    document.body.append(membersHost);
    const membersRoot = createRoot(membersHost);
    const membersRef = createRef<HTMLElement>();
    const noop = () => {};
    try {
      await act(async () => {
        membersRoot.render(
          <MemoryRouter>
            <ShellMembersPane
              membersRef={membersRef}
              overlayMembers={false}
              mobile={false}
              sidebar={false}
              workspace={sampleWorkspace}
              conversation={groupConvA}
              userId={userA.id}
              owner={true}
              caps={{}}
              busy={false}
              members={[]}
              visibleMembers={[]}
              isLoading={false}
              hasError={false}
              onClose={noop}
              onStartDirect={noop}
              onRequestRemove={noop}
              onManageGroup={noop}
              onInvite={noop}
            />
          </MemoryRouter>,
        );
      });
      const membersAside = membersHost.querySelector<HTMLElement>(
        'aside[aria-label="对话成员"]',
      )!;
      expect(membersAside.className).toBe("members-pane");
      expect(membersAside.style.getPropertyValue("--mp-pane-width")).toBe("");
      expect(membersHost.querySelector('[role="separator"]')).toBeNull();
    } finally {
      await act(async () => membersRoot.unmount());
      membersHost.remove();
    }
  });

  it("resizes via pointer drag with setPointerCapture, enforces min/max bounds, and re-clamps on window resize", async () => {
    await renderAppsPane();
    const appsAside = getAppsAside();
    const separator = getSeparator()!;

    const setCaptureSpy = vi.fn();
    const releaseCaptureSpy = vi.fn();
    Object.defineProperty(separator, "setPointerCapture", {
      value: setCaptureSpy,
      configurable: true,
    });
    Object.defineProperty(separator, "releasePointerCapture", {
      value: releaseCaptureSpy,
      configurable: true,
    });

    // Non-primary button should be ignored.
    await act(async () => {
      dispatchPointer(separator, "pointerdown", {
        clientX: 1040,
        pointerId: 3,
        button: 2,
      });
    });
    expect(setCaptureSpy).not.toHaveBeenCalled();

    // Drag left by 120px -> width increases 400 -> 520.
    await act(async () => {
      dispatchPointer(separator, "pointerdown", {
        clientX: 1040,
        pointerId: 7,
      });
    });
    expect(setCaptureSpy).toHaveBeenCalledWith(7);

    await act(async () => {
      // Unrelated pointerId is ignored.
      dispatchPointer(window, "pointermove", { clientX: 700, pointerId: 99 });
    });
    expect(appsAside.style.getPropertyValue("--mp-pane-width")).toBe("400px");

    await act(async () => {
      dispatchPointer(window, "pointermove", { clientX: 920, pointerId: 7 });
    });
    expect(appsAside.style.getPropertyValue("--mp-pane-width")).toBe("520px");
    expect(separator.getAttribute("aria-valuenow")).toBe("520");

    // Drag far left past viewport allowance -> clamps to maxWidth (1440 - 800 = 640).
    await act(async () => {
      dispatchPointer(window, "pointermove", { clientX: 100, pointerId: 7 });
    });
    expect(appsAside.style.getPropertyValue("--mp-pane-width")).toBe("640px");
    expect(separator.getAttribute("aria-valuenow")).toBe("640");

    // Drag far right -> clamps to MIN_APP_PANE_WIDTH (280).
    await act(async () => {
      dispatchPointer(window, "pointermove", { clientX: 1380, pointerId: 7 });
    });
    expect(appsAside.style.getPropertyValue("--mp-pane-width")).toBe("280px");
    expect(separator.getAttribute("aria-valuenow")).toBe("280");

    // Settle at 540px and release pointer.
    await act(async () => {
      dispatchPointer(separator, "pointermove", { clientX: 900, pointerId: 7 });
      dispatchPointer(separator, "pointerup", { clientX: 900, pointerId: 7 });
    });
    expect(releaseCaptureSpy).toHaveBeenCalledWith(7);
    expect(appsAside.style.getPropertyValue("--mp-pane-width")).toBe("540px");
    expect(readPersistedAppPaneWidth(userA.id)).toBe(540);

    // Shrink browser window to 1160px -> maxWidth becomes 360px and clamps effective width.
    vi.stubGlobal("innerWidth", 1160);
    await act(async () => {
      window.dispatchEvent(new Event("resize"));
    });
    expect(appsAside.style.getPropertyValue("--mp-pane-width")).toBe("360px");
    expect(separator.getAttribute("aria-valuenow")).toBe("360");
    expect(separator.getAttribute("aria-valuemax")).toBe("360");

    // Widen browser window to 1920px -> restores preferred 540px and caps maxWidth at 960px.
    vi.stubGlobal("innerWidth", 1920);
    await act(async () => {
      window.dispatchEvent(new Event("resize"));
    });
    expect(appsAside.style.getPropertyValue("--mp-pane-width")).toBe("540px");
    expect(separator.getAttribute("aria-valuenow")).toBe("540");
    expect(separator.getAttribute("aria-valuemax")).toBe(
      String(MAX_APP_PANE_WIDTH_CEILING),
    );
    expect(computeMaxAppPaneWidth(1920)).toBe(960);
  });

  it("shields .mp-iframe during active drag, cleans up on pointerup/pointercancel/blur/overlay/unmount, and never re-requests app session", async () => {
    await renderAppsPane();
    const appsAside = getAppsAside();
    const separator = getSeparator()!;
    expect(host.querySelector(".mp-iframe")).toBeNull();
    expect(api.createConversationAppSession).toHaveBeenCalledTimes(0);

    await act(async () => {
      host.querySelector<HTMLButtonElement>(".mp-launcher-card")!.click();
    });
    await flush();

    const initialIframe = host.querySelector<HTMLIFrameElement>(".mp-iframe")!;
    expect(initialIframe).not.toBeNull();
    expect(api.createConversationAppSession).toHaveBeenCalledTimes(1);

    // 1) Active drag -> pointerup cleanup
    await act(async () => {
      dispatchPointer(separator, "pointerdown", { clientX: 1040, pointerId: 1 });
    });
    expect(appsAside.classList.contains("mp-apps-pane-dragging")).toBe(true);
    expect(document.body.classList.contains("mp-pane-resizing")).toBe(true);

    await act(async () => {
      dispatchPointer(window, "pointermove", { clientX: 960, pointerId: 1 });
      dispatchPointer(window, "pointermove", { clientX: 910, pointerId: 1 });
      dispatchPointer(window, "pointerup", { clientX: 910, pointerId: 1 });
    });
    expect(appsAside.classList.contains("mp-apps-pane-dragging")).toBe(false);
    expect(document.body.classList.contains("mp-pane-resizing")).toBe(false);

    // 2) Active drag -> pointercancel cleanup
    await act(async () => {
      dispatchPointer(separator, "pointerdown", { clientX: 910, pointerId: 2 });
    });
    expect(appsAside.classList.contains("mp-apps-pane-dragging")).toBe(true);
    expect(document.body.classList.contains("mp-pane-resizing")).toBe(true);
    await act(async () => {
      dispatchPointer(window, "pointercancel", { pointerId: 2 });
    });
    expect(appsAside.classList.contains("mp-apps-pane-dragging")).toBe(false);
    expect(document.body.classList.contains("mp-pane-resizing")).toBe(false);

    // 3) Active drag -> window blur cleanup
    await act(async () => {
      dispatchPointer(separator, "pointerdown", { clientX: 910, pointerId: 3 });
    });
    expect(appsAside.classList.contains("mp-apps-pane-dragging")).toBe(true);
    expect(document.body.classList.contains("mp-pane-resizing")).toBe(true);
    await act(async () => {
      window.dispatchEvent(new Event("blur"));
    });
    expect(appsAside.classList.contains("mp-apps-pane-dragging")).toBe(false);
    expect(document.body.classList.contains("mp-pane-resizing")).toBe(false);

    // 4) Active drag -> switching into overlayMembers cleans up drag state
    await act(async () => {
      dispatchPointer(separator, "pointerdown", { clientX: 910, pointerId: 4 });
    });
    expect(document.body.classList.contains("mp-pane-resizing")).toBe(true);
    await renderAppsPane({ overlayMembers: true });
    expect(getAppsAside().classList.contains("mp-apps-pane-dragging")).toBe(
      false,
    );
    expect(document.body.classList.contains("mp-pane-resizing")).toBe(false);

    // Switch back to desktop, verify iframe was never remounted and session was never re-requested!
    await renderAppsPane({ overlayMembers: false });
    expect(host.querySelector(".mp-iframe")).toBe(initialIframe);
    expect(api.createConversationAppSession).toHaveBeenCalledTimes(1);

    // 5) Active drag -> unmount cleans up body class
    await act(async () => {
      dispatchPointer(getSeparator()!, "pointerdown", {
        clientX: 910,
        pointerId: 5,
      });
    });
    expect(document.body.classList.contains("mp-pane-resizing")).toBe(true);
    await act(async () => {
      root.unmount();
    });
    expect(document.body.classList.contains("mp-pane-resizing")).toBe(false);
    root = createRoot(host);
  });

  it("resets width to default 400px and persists on double-clicking the resize handle", async () => {
    writePersistedAppPaneWidth(userA.id, 560);
    await renderAppsPane();

    const appsAside = getAppsAside();
    const separator = getSeparator()!;
    expect(appsAside.style.getPropertyValue("--mp-pane-width")).toBe("560px");
    expect(separator.getAttribute("aria-valuenow")).toBe("560");

    await act(async () => {
      separator.dispatchEvent(
        new MouseEvent("dblclick", { bubbles: true, cancelable: true }),
      );
    });
    expect(appsAside.style.getPropertyValue("--mp-pane-width")).toBe("400px");
    expect(separator.getAttribute("aria-valuenow")).toBe("400");
    expect(readPersistedAppPaneWidth(userA.id)).toBe(400);
  });

  it("supports keyboard resizing via ArrowLeft, ArrowRight, Shift+Arrows, Home, and End", async () => {
    await renderAppsPane();
    const appsAside = getAppsAside();
    const separator = getSeparator()!;

    const pressKey = async (
      key: string,
      opts: KeyboardEventInit = {},
    ): Promise<KeyboardEvent> => {
      const event = new KeyboardEvent("keydown", {
        key,
        bubbles: true,
        cancelable: true,
        ...opts,
      });
      await act(async () => {
        separator.dispatchEvent(event);
      });
      return event;
    };

    // ArrowLeft widens by 24px (400 -> 424)
    const leftEvt = await pressKey("ArrowLeft");
    expect(leftEvt.defaultPrevented).toBe(true);
    expect(separator.getAttribute("aria-valuenow")).toBe("424");
    expect(appsAside.style.getPropertyValue("--mp-pane-width")).toBe("424px");

    // Shift+ArrowLeft widens by 48px (424 -> 472)
    await pressKey("ArrowLeft", { shiftKey: true });
    expect(separator.getAttribute("aria-valuenow")).toBe("472");

    // ArrowRight narrows by 24px (472 -> 448)
    const rightEvt = await pressKey("ArrowRight");
    expect(rightEvt.defaultPrevented).toBe(true);
    expect(separator.getAttribute("aria-valuenow")).toBe("448");

    // Modifier chords (Ctrl/Alt) are not hijacked
    const ctrlEvt = await pressKey("ArrowLeft", { ctrlKey: true });
    expect(ctrlEvt.defaultPrevented).toBe(false);
    expect(separator.getAttribute("aria-valuenow")).toBe("448");

    // Home jumps to MIN_APP_PANE_WIDTH (280)
    const homeEvt = await pressKey("Home");
    expect(homeEvt.defaultPrevented).toBe(true);
    expect(separator.getAttribute("aria-valuenow")).toBe("280");

    // End jumps to maxWidth (640 at 1440px viewport)
    const endEvt = await pressKey("End");
    expect(endEvt.defaultPrevented).toBe(true);
    expect(separator.getAttribute("aria-valuenow")).toBe("640");
    expect(readPersistedAppPaneWidth(userA.id)).toBe(640);
  });

  it("hides the separator, omits custom desktop width style, and preserves drawer semantics in overlayMembers mode", async () => {
    writePersistedAppPaneWidth(userA.id, 520);
    await renderAppsPane({ overlayMembers: true, mobile: true, sidebar: false });

    const appsAside = getAppsAside();
    expect(getSeparator()).toBeNull();
    expect(appsAside.style.getPropertyValue("--mp-pane-width")).toBe("");
    expect(appsAside.getAttribute("role")).toBe("dialog");
    expect(appsAside.getAttribute("aria-modal")).toBe("true");
    expect(host.querySelector(".members-shade")).not.toBeNull();
    expect(
      host.querySelector('[data-overlay-focus-boundary="start"]'),
    ).not.toBeNull();
    expect(
      host.querySelector('[data-overlay-focus-boundary="end"]'),
    ).not.toBeNull();

    // Verify cascade layer order: .members-pane.mp-apps-pane is in @layer features while
    // <=1250px and <=780px drawer rules live in @layer layout with zero !important.
    const marketplaceCss = readFileSync(
      join(srcDir, "styles/marketplace.css"),
      "utf8",
    );
    expect(marketplaceCss).toMatch(
      /@layer\s+features\s*\{[\s\S]*?\.members-pane\.mp-apps-pane/,
    );
    expect(marketplaceCss).toContain("pointer-events: none;");
    expect(marketplaceCss).not.toContain("!important");
  });

  it("persists width per user across remount & conversation switches, falls back on corrupt storage, and clears on logout or account switch", async () => {
    chatStore.begin(userA.id);
    await renderAppsPane({ conversation: groupConvA, user: userA });

    // Resize userA's pane to 512px
    const separator = getSeparator()!;
    await act(async () => {
      dispatchPointer(separator, "pointerdown", { clientX: 1000, pointerId: 1 });
      dispatchPointer(window, "pointermove", { clientX: 888, pointerId: 1 });
      dispatchPointer(window, "pointerup", { clientX: 888, pointerId: 1 });
    });
    expect(getAppsAside().style.getPropertyValue("--mp-pane-width")).toBe(
      "512px",
    );
    expect(localStorage.getItem(`teamie.appPaneWidth.${userA.id}`)).toBe("512");

    // Switch conversation to groupConvB and remount -> preserves 512px
    await act(async () => root.unmount());
    root = createRoot(host);
    await renderAppsPane({ conversation: groupConvB, user: userA });
    expect(getAppsAside().style.getPropertyValue("--mp-pane-width")).toBe(
      "512px",
    );

    // Corrupt storage values fall back safely to 400px; out-of-range numbers clamp
    for (const corrupt of ["not-a-number", "NaN", "Infinity", "", "   "]) {
      localStorage.setItem(`teamie.appPaneWidth.${userA.id}`, corrupt);
      expect(readPersistedAppPaneWidth(userA.id, 640)).toBe(400);
    }
    localStorage.setItem(`teamie.appPaneWidth.${userA.id}`, "120");
    expect(readPersistedAppPaneWidth(userA.id, 640)).toBe(280);
    localStorage.setItem(`teamie.appPaneWidth.${userA.id}`, "5000");
    expect(readPersistedAppPaneWidth(userA.id, 640)).toBe(640);

    // Account switch (userA -> userB): clears userA's cached width and isolates userB at 400px
    writePersistedAppPaneWidth(userA.id, 530);
    expect(localStorage.getItem(`teamie.appPaneWidth.${userA.id}`)).toBe("530");

    await renderAppsPane({ conversation: groupConvB, user: userB });
    expect(localStorage.getItem(`teamie.appPaneWidth.${userA.id}`)).toBeNull();
    expect(getAppsAside().style.getPropertyValue("--mp-pane-width")).toBe(
      "400px",
    );

    // Logout via chatStore.clear() clears active user's persisted width
    chatStore.begin(userB.id);
    writePersistedAppPaneWidth(userB.id, 490);
    expect(localStorage.getItem(`teamie.appPaneWidth.${userB.id}`)).toBe("490");
    chatStore.clear();
    expect(localStorage.getItem(`teamie.appPaneWidth.${userB.id}`)).toBeNull();

    // Direct helper clearPersistedAppPaneWidth is idempotent and safe for empty userId
    writePersistedAppPaneWidth("u-temp", 460);
    clearPersistedAppPaneWidth("");
    expect(localStorage.getItem("teamie.appPaneWidth.u-temp")).toBe("460");
    clearPersistedAppPaneWidth("u-temp");
    expect(localStorage.getItem("teamie.appPaneWidth.u-temp")).toBeNull();
  });
});
