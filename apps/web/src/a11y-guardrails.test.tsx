// @vitest-environment jsdom
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ESLint } from "eslint";
import stylelint from "stylelint";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type {
  Agent,
  AttentionPage,
  Bootstrap,
  Conversation,
  Member,
  Message,
  User,
  Workspace,
} from "@teamie/api-client";
import { ForgotPassword, ResetPassword } from "./password-recovery";
import { Landing } from "./public";
import { Join, Login } from "./public/auth-views";
import { Shell } from "./shell";
import { ConversationView } from "./messages";
import { chatStore } from "./store";

const WEB_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

const adminUser: User = {
  id: "admin",
  kind: "human",
  platformRole: "admin",
  displayName: "林舟",
  email: "linzhou@example.test",
  createdAt: "2026-11-12T00:00:00Z",
};

const workspace: Workspace = {
  id: "team",
  name: "产品研发空间",
  createdBy: adminUser.id,
  createdAt: adminUser.createdAt,
};

const channel: Conversation = {
  id: "general",
  workspaceId: workspace.id,
  type: "channel",
  name: "general",
  memberIds: [adminUser.id, "peer"],
  lastSeq: 10,
  readSeq: 10,
  updatedAt: adminUser.createdAt,
};

const members: Member[] = [
  { userId: adminUser.id, displayName: adminUser.displayName, kind: "human", role: "owner" },
  { userId: "peer", displayName: "周予", kind: "human", role: "member" },
];

const ownedAgent: Agent = {
  id: "agent-1",
  principalId: "agent-1",
  name: "产品伙伴",
  ownerId: adminUser.id,
  ownerName: adminUser.displayName,
  description: "整理会议重点",
  workspaceIds: [workspace.id],
  enabled: true,
  createdAt: adminUser.createdAt,
  updatedAt: adminUser.createdAt,
};

let activeUser: User | null = adminUser;
let bootstrap: Bootstrap;

vi.mock("./auth", () => ({
  useAuth: () => ({
    user: activeUser,
    loading: false,
    logout: vi.fn(),
    updateUser: vi.fn(),
    login: vi.fn(),
    register: vi.fn(),
    sessionExpired: false,
    clearSessionExpired: vi.fn(),
  }),
}));

vi.mock("./chat", async (original) => ({
  ...(await original<typeof import("./chat")>()),
  useChat: () => ({
    data: bootstrap,
    loading: false,
    error: "",
    connection: "connected" as const,
    syncError: "",
    refresh: vi.fn(async () => {}),
    sync: vi.fn(),
    sendTyping: vi.fn(),
    typing: {},
  }),
}));

let host: HTMLDivElement;
let root: Root;
let client: QueryClient;

function LocationProbe() {
  const loc = useLocation();
  return <output data-location>{loc.pathname + loc.search}</output>;
}

async function settle(action?: () => void) {
  await act(async () => {
    action?.();
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  await act(async () => {
    await vi.dynamicImportSettled();
  });
}

async function mountShell(initialPath: string, mobile = false) {
  vi.stubGlobal(
    "matchMedia",
    vi.fn((query: string) => ({
      matches: mobile ? query.includes("780") || query.includes("1250") : false,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    })),
  );
  await settle(() =>
    root.render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={[initialPath]}>
          <Shell />
          <LocationProbe />
        </MemoryRouter>
      </QueryClientProvider>,
    ),
  );
  await settle();
}

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      disconnect() {}
    },
  );
  vi.stubGlobal(
    "IntersectionObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  Object.defineProperty(HTMLElement.prototype, "scrollTo", {
    configurable: true,
    value: vi.fn(),
  });
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
    configurable: true,
    value: vi.fn(),
  });
  vi.stubGlobal("scrollTo", vi.fn());

  activeUser = adminUser;
  bootstrap = {
    user: adminUser,
    workspaces: [workspace],
    conversations: [channel],
    capabilities: {
      "agents.enabled": true,
      "agents.discover": true,
      "agents.create": true,
      "agents.update": true,
      "agents.delete": true,
      "im.dm.create": true,
      "im.group.create": true,
      "im.reactions.create": true,
      "im.message.edit": true,
      "im.message.mentions": true,
    },
    flagsRevision: "a11y-guardrails",
    syncCursor: "10",
  };
  localStorage.clear();
  chatStore.begin(adminUser.id);
  chatStore.replaceConversations([channel]);

  vi.spyOn(api, "members").mockResolvedValue({ members });
  vi.spyOn(api, "agents").mockResolvedValue({ agents: [ownedAgent] });
  vi.spyOn(api, "workspaceAgents").mockResolvedValue({ agents: [] });
  vi.spyOn(api, "messages").mockResolvedValue({ messages: [], hasMore: false });
  vi.spyOn(api, "read").mockResolvedValue({ readSeq: channel.readSeq });
  vi.spyOn(api, "attention").mockResolvedValue({
    items: [],
    unreadCount: 0,
  } satisfies AttentionPage);
  vi.spyOn(api, "announcements").mockResolvedValue({
    items: [],
    readSeq: 0,
    latestSeq: 0,
    hasMore: false,
  });
  vi.spyOn(api, "registrationInvites").mockResolvedValue({ invites: [] });
  vi.spyOn(api, "developerMarketplaceApps").mockResolvedValue({ apps: [] });
  vi.spyOn(api, "registration").mockResolvedValue({
    mode: "invite",
    bootstrapRequired: false,
  });
  vi.spyOn(api, "invite").mockResolvedValue({
    workspace,
    expiresAt: "2026-12-01T00:00:00Z",
  });

  client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: Infinity } },
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  client.clear();
  host.remove();
  chatStore.clear();
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("PR-6 accessibility landmarks, navigation, skip link, and route focus", () => {
  it("exposes conversation <nav aria-label='对话'>, workspace aria-current states, bell unread count, and skip link", async () => {
    vi.spyOn(api, "attention").mockResolvedValue({
      items: [],
      unreadCount: 3,
    });

    await mountShell("/app/w/team/c/general");

    // 1. Conversation list landmark
    const conversationNav = host.querySelector<HTMLElement>('nav.sidebar-scroll[aria-label="对话"]');
    expect(conversationNav).not.toBeNull();
    expect(conversationNav?.querySelector(".conversation-link")).not.toBeNull();

    // 2. Skip link is the first focusable element inside .app-shell and targets <main id="main-content" tabIndex={-1}>
    const shell = host.querySelector<HTMLElement>(".app-shell")!;
    const skipLink = shell.firstElementChild as HTMLAnchorElement;
    expect(skipLink.tagName).toBe("A");
    expect(skipLink.className).toBe("skip-link");
    expect(skipLink.getAttribute("href")).toBe("#main-content");
    expect(skipLink.textContent).toBe("跳到主内容");

    const main = host.querySelector<HTMLElement>("main#main-content")!;
    expect(main).not.toBeNull();
    expect(main.getAttribute("tabindex")).toBe("-1");

    await settle(() => skipLink.click());
    expect(document.activeElement).toBe(main);
    expect(host.querySelector("[data-location]")?.textContent).toBe("/app/w/team/c/general");

    // 3. Inbox bell includes unread count in accessible name and hides decorative numeric badge
    const bell = host.querySelector<HTMLButtonElement>(".rail-attention")!;
    expect(bell.getAttribute("aria-label")).toBe("提及与回复（3 条未读）");
    const badge = bell.querySelector(".rail-attention-count")!;
    expect(badge.getAttribute("aria-hidden")).toBe("true");

    // 4. Workspace rail aria-current: active on workspace chat & attention, cleared on account/management surfaces
    const wsButton = () => host.querySelector<HTMLButtonElement>(".workspace-button")!;
    expect(wsButton().getAttribute("aria-current")).toBe("true");

    await settle(() => bell.click());
    expect(host.querySelector("[data-location]")?.textContent).toBe("/app/attention");
    expect(wsButton().getAttribute("aria-current")).toBe("true");

    const agentsRail = host.querySelector<HTMLAnchorElement>(".rail-agents")!;
    await settle(() => agentsRail.click());
    expect(host.querySelector("[data-location]")?.textContent).toBe("/app/agents");
    expect(wsButton().getAttribute("aria-current")).toBeNull();

    const openAccountAndClick = async (href: string) => {
      const trigger = host.querySelector<HTMLButtonElement>(".account-trigger")!;
      await settle(() => trigger.click());
      const link = host.querySelector<HTMLAnchorElement>(`.account-menu a[href="${href}"]`)!;
      await settle(() => link.click());
    };

    await openAccountAndClick("/app/admin");
    expect(host.querySelector("[data-location]")?.textContent).toBe("/app/admin");
    expect(wsButton().getAttribute("aria-current")).toBeNull();

    await openAccountAndClick("/app/developers");
    expect(host.querySelector("[data-location]")?.textContent).toBe("/app/developers");
    expect(wsButton().getAttribute("aria-current")).toBeNull();

    await openAccountAndClick("/app/settings");
    expect(host.querySelector("[data-location]")?.textContent).toBe("/app/settings");
    expect(wsButton().getAttribute("aria-current")).toBeNull();
  });

  it("moves programmatic focus to page h1[tabindex='-1'] on management route transitions", async () => {
    await mountShell("/app/settings");

    const agentsRail = host.querySelector<HTMLAnchorElement>(".rail-agents")!;
    await settle(() => agentsRail.click());
    await settle();

    const agentsHeading = host.querySelector<HTMLElement>("main h1")!;
    expect(agentsHeading.getAttribute("tabindex")).toBe("-1");
    expect(agentsHeading.textContent).toContain("个人 Agent");
    expect(document.activeElement).toBe(agentsHeading);

    const trigger = host.querySelector<HTMLButtonElement>(".account-trigger")!;
    await settle(() => trigger.click());
    const adminLink = host.querySelector<HTMLAnchorElement>('.account-menu a[href="/app/admin"]')!;
    await settle(() => adminLink.click());
    await settle();

    const adminHeading = host.querySelector<HTMLElement>("main h1")!;
    expect(adminHeading.getAttribute("tabindex")).toBe("-1");
    expect(adminHeading.textContent).toContain("注册邀请");
    expect(document.activeElement).toBe(adminHeading);
  });
});

describe("PR-6 timeline ARIA semantics", () => {
  it("wires role='log', aria-busy, date/unread separator roles, and valid aria-labelledby on full and compact messages", async () => {
    let resolveEarlier!: (value: { messages: Message[]; hasMore: boolean }) => void;
    const earlierPending = new Promise<{ messages: Message[]; hasMore: boolean }>((resolve) => {
      resolveEarlier = resolve;
    });

    const firstMsg: Message = {
      id: "m1",
      clientMessageId: "c1",
      conversationId: channel.id,
      senderId: "peer",
      senderName: "周予",
      senderKind: "human",
      seq: 9,
      body: "第一条消息",
      createdAt: "2026-11-12T08:00:00Z",
    };
    const groupedMsg: Message = {
      id: "m2",
      clientMessageId: "c2",
      conversationId: channel.id,
      senderId: "peer",
      senderName: "周予",
      senderKind: "human",
      seq: 10,
      body: "紧接着的第二条分组消息",
      createdAt: "2026-11-12T08:01:00Z",
    };

    const compactContinuation: Message = {
      id: "m3",
      clientMessageId: "c3",
      conversationId: channel.id,
      senderId: "peer",
      senderName: "周予",
      senderKind: "human",
      seq: 11,
      body: "同组连续紧凑消息",
      createdAt: "2026-11-12T08:02:00Z",
    };

    const unreadChannel: Conversation = {
      ...channel,
      lastSeq: 11,
      readSeq: 9,
      manualUnreadSeq: 9,
    };
    chatStore.replaceConversations([unreadChannel]);

    vi.spyOn(api, "messages")
      .mockResolvedValueOnce({
        messages: [firstMsg, groupedMsg, compactContinuation],
        hasMore: true,
      })
      .mockReturnValueOnce(earlierPending);

    await settle(() =>
      root.render(
        <ConversationView
          conversation={unreadChannel}
          members={members}
          onMembers={() => {}}
          membersOpen={false}
          onMenu={() => {}}
        />,
      ),
    );
    await settle();

    const log = host.querySelector<HTMLElement>(".message-scroll")!;
    expect(log.getAttribute("role")).toBe("log");
    expect(log.getAttribute("aria-live")).toBe("polite");
    expect(log.getAttribute("aria-relevant")).toBe("additions");
    expect(log.getAttribute("aria-label")).toBe("消息历史");
    expect(log.getAttribute("aria-busy")).toBeNull();

    // Date and unread dividers have role="separator"
    const dateDivider = host.querySelector<HTMLElement>(".date-divider")!;
    expect(dateDivider.getAttribute("role")).toBe("separator");
    expect(dateDivider.getAttribute("aria-label")).toBeTruthy();

    const unreadDivider = host.querySelector<HTMLElement>(".unread-divider")!;
    expect(unreadDivider.getAttribute("role")).toBe("separator");
    expect(unreadDivider.getAttribute("aria-label")).toBe("以下是新消息");

    const articles = Array.from(host.querySelectorAll<HTMLElement>("article.message"));
    expect(articles).toHaveLength(3);
    expect(articles[0].classList.contains("compact")).toBe(false);
    expect(articles[2].classList.contains("compact")).toBe(true);

    for (const article of articles) {
      const labelledBy = article.getAttribute("aria-labelledby");
      expect(labelledBy).toBeTruthy();
      const ids = labelledBy!.split(/\s+/);
      expect(ids).toHaveLength(2);
      const [senderEl, timeEl] = ids.map((id) => document.getElementById(id));
      expect(senderEl).not.toBeNull();
      expect(senderEl?.textContent?.trim()).toBe("周予");
      expect(timeEl).not.toBeNull();
      expect(timeEl?.tagName).toBe("TIME");
      expect(timeEl?.textContent?.trim().length).toBeGreaterThan(0);
    }

    // Trigger loadingOlder and verify aria-busy="true" while pending
    const loadEarlierBtn = host.querySelector<HTMLButtonElement>(".load-earlier")!;
    await settle(() => loadEarlierBtn.click());
    expect(log.getAttribute("aria-busy")).toBe("true");

    await settle(() => resolveEarlier({ messages: [], hasMore: false }));
    expect(log.getAttribute("aria-busy")).toBeNull();
  });
});

describe("PR-6 public pages titles, diagram roles, decorative glyphs, and rAF scroll throttling", () => {
  it("sets Chinese document.title across all public routes and throttles Landing scroll via requestAnimationFrame", async () => {
    activeUser = null;
    const queuedFrames: FrameRequestCallback[] = [];
    const rAF = vi.fn((cb: FrameRequestCallback) => {
      queuedFrames.push(cb);
      return queuedFrames.length;
    });
    const cAF = vi.fn();
    vi.stubGlobal("requestAnimationFrame", rAF);
    vi.stubGlobal("cancelAnimationFrame", cAF);
    vi.stubGlobal("innerWidth", 1280);
    vi.stubGlobal("innerHeight", 800);
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => ({
        matches: false,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      })),
    );

    // 1. Landing ("/")
    await settle(() =>
      root.render(
        <MemoryRouter initialEntries={["/"]}>
          <Landing />
        </MemoryRouter>,
      ),
    );
    expect(document.title).toBe("Teamie — 人与 Agent 同席的团队协作空间");

    const heroVisual = host.querySelector<HTMLElement>(".hero-visual")!;
    expect(heroVisual.getAttribute("role")).toBe("img");
    expect(heroVisual.getAttribute("aria-label")).toBe("Teamie 团队沟通界面示意");
    expect(heroVisual.querySelector(".demo-window")?.getAttribute("aria-hidden")).toBe("true");
    expect(heroVisual.querySelector(".floating-note")?.getAttribute("aria-hidden")).toBe("true");
    expect(heroVisual.querySelector(".hero-asterisk")?.getAttribute("aria-hidden")).toBe("true");

    const storyCanvas = host.querySelector<HTMLElement>(".story-stage-canvas")!;
    expect(storyCanvas.getAttribute("role")).toBe("img");
    expect(storyCanvas.getAttribute("aria-label")).toBe("Teamie 分幕场景演示");

    // Multiple scroll events within one animation frame schedule only a single rAF callback
    rAF.mockClear();
    window.dispatchEvent(new Event("scroll"));
    window.dispatchEvent(new Event("scroll"));
    window.dispatchEvent(new Event("scroll"));
    expect(rAF).toHaveBeenCalledTimes(1);

    // Unmounting while a frame is pending cancels the queued animation frame
    await settle(() =>
      root.render(
        <QueryClientProvider client={client}>
          <MemoryRouter key="login" initialEntries={["/login"]}>
            <Login />
          </MemoryRouter>
        </QueryClientProvider>,
      ),
    );
    expect(cAF).toHaveBeenCalled();
    expect(document.title).toBe("登录 — Teamie");

    // 2. Register mode ("/login?mode=register")
    await settle(() =>
      root.render(
        <QueryClientProvider client={client}>
          <MemoryRouter key="register" initialEntries={["/login?mode=register"]}>
            <Login />
          </MemoryRouter>
        </QueryClientProvider>,
      ),
    );
    expect(document.title).toBe("注册账号 — Teamie");

    // 3. Forgot password ("/forgot-password")
    await settle(() =>
      root.render(
        <MemoryRouter key="forgot" initialEntries={["/forgot-password"]}>
          <ForgotPassword />
        </MemoryRouter>,
      ),
    );
    expect(document.title).toBe("找回密码 — Teamie");

    // 4. Reset password ("/reset-password")
    await settle(() =>
      root.render(
        <MemoryRouter key="reset" initialEntries={["/reset-password"]}>
          <ResetPassword />
        </MemoryRouter>,
      ),
    );
    expect(document.title).toBe("重置密码 — Teamie");

    // 5. Workspace invitation ("/join/:inviteCode")
    await settle(() =>
      root.render(
        <QueryClientProvider client={client}>
          <MemoryRouter key="join" initialEntries={["/join/join-team"]}>
            <Routes>
              <Route path="/join/:inviteCode" element={<Join />} />
            </Routes>
          </MemoryRouter>
        </QueryClientProvider>,
      ),
    );
    expect(document.title).toBe("加入工作空间 — Teamie");
  });
});

describe("PR-6 automated ESLint and Stylelint guardrails", () => {
  it("enforces ESLint jsx-a11y, Button variant prop contract, and formatDateTime locale ban", async () => {
    const eslint = new ESLint({ cwd: WEB_ROOT });

    const [badResult] = await eslint.lintText(
      [
        'import { Button } from "./ui";',
        "export function BadSample() {",
        '  const formatted = new Date().toLocaleDateString("zh-CN");',
        '  return (',
        "    <div>",
        '      <Button className="secondary">{formatted}</Button>',
        '      <span aria-label="在线状态" />',
        "    </div>",
        "  );",
        "}",
      ].join("\n"),
      { filePath: path.join(WEB_ROOT, "src/guardrail-sample.tsx") },
    );
    expect(badResult.errorCount).toBeGreaterThanOrEqual(3);
    expect(badResult.messages.some((m) => m.message.includes('<Button variant="..."'))).toBe(true);
    expect(badResult.messages.some((m) => m.message.includes("formatDateTime"))).toBe(true);
    expect(badResult.messages.some((m) => m.message.includes("generic <span>"))).toBe(true);

    const [goodResult] = await eslint.lintText(
      [
        'import { Button } from "./ui";',
        "export function GoodSample() {",
        "  return (",
        '    <aside tabIndex={-1} aria-label="侧栏">',
        '      <div role="separator" tabIndex={0} aria-valuenow={360} aria-valuemin={280} aria-valuemax={520} aria-label="调节宽度" />',
        '      <Button variant="secondary" size="sm">保存</Button>',
        "    </aside>",
        "  );",
        "}",
      ].join("\n"),
      { filePath: path.join(WEB_ROOT, "src/guardrail-sample.tsx") },
    );
    expect(goodResult.errorCount).toBe(0);

    // src/format-datetime.ts is exempted from the .toLocale*String rule
    const [exemptResult] = await eslint.lintText(
      'export const sample = (d: Date) => d.toLocaleDateString("zh-CN");\n',
      { filePath: path.join(WEB_ROOT, "src/format-datetime.ts") },
    );
    expect(exemptResult.errorCount).toBe(0);
  });

  it("enforces Stylelint strict token values, duplicate selector ban, and @layer features outline ban", async () => {
    const configFile = path.join(WEB_ROOT, "stylelint.config.js");
    const codeFilename = path.join(WEB_ROOT, "src/styles/guardrail-sample.css");

    const badCss = await stylelint.lint({
      configFile,
      codeFilename,
      code: [
        "@layer features {",
        "  .sample-box {",
        "    color: #333333;",
        "    font-size: 13px;",
        "    padding: 12px;",
        "    z-index: 50;",
        "    outline: none;",
        "  }",
        "  .sample-box {",
        "    background-color: rgb(255, 255, 255);",
        "  }",
        "}",
      ].join("\n"),
    });

    expect(badCss.errored).toBe(true);
    const warnings = badCss.results[0]?.warnings ?? [];
    const rulesTriggered = new Set(warnings.map((w) => w.rule));
    expect(rulesTriggered.has("scale-unlimited/declaration-strict-value")).toBe(true);
    expect(rulesTriggered.has("no-duplicate-selectors")).toBe(true);
    expect(rulesTriggered.has("teamie/no-features-outline-none")).toBe(true);

    const goodCss = await stylelint.lint({
      configFile,
      codeFilename,
      code: [
        "@layer components {",
        "  .sample-handle {",
        "    color: var(--text);",
        "    font-size: var(--font-sm);",
        "    padding: var(--space-3);",
        "    z-index: var(--z-local);",
        "  }",
        "}",
      ].join("\n"),
    });
    expect(goodCss.errored).toBe(false);
    expect(goodCss.results[0]?.warnings ?? []).toHaveLength(0);
  });

  it("places coarse-pointer message action rules in @layer layout so touch tablets (> 780px) override layout-base", async () => {
    const conveniences = await fs.promises.readFile(
      path.join(WEB_ROOT, "src/styles/conveniences.css"),
      "utf8",
    );
    expect(conveniences).toMatch(
      /@layer\s+layout\s*\{\s*@media\s*\(hover:\s*none\),\s*\(pointer:\s*coarse\)\s*\{[\s\S]*?\.message-more-button\s*\{\s*display:\s*inline-flex;/,
    );
  });
});
