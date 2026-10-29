// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type { Conversation, MarketplaceApp, User, Workspace } from "@teamie/api-client";
import { ConversationHeader } from "./chat/conversation-chrome";
import {
  ConversationAppsPane,
  PlatformMarketplace,
  WorkspaceMarketplaceModal,
} from "./marketplace";

let currentUser: User = {
  id: "u-admin",
  kind: "human",
  platformRole: "admin",
  displayName: "平台管理员",
  email: "admin@example.test",
  createdAt: "2026-10-16T00:00:00Z",
};

vi.mock("./auth", () => ({
  useAuth: () => ({ user: currentUser }),
}));

const sampleApp: MarketplaceApp = {
  id: "app-1",
  slug: "board-room",
  name: "协作棋盘",
  description: "支持多人与 Agent 同台互动的群聊应用",
  webUrl: "https://board.example.test/ui",
  mcpUrl: "https://board.example.test/mcp",
  enabled: true,
  createdAt: "2026-10-16T00:00:00Z",
  updatedAt: "2026-10-16T00:00:00Z",
};

const sampleWorkspace: Workspace = {
  id: "ws-1",
  name: "研发空间",
  createdBy: "u-admin",
  createdAt: "2026-10-16T00:00:00Z",
};

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  currentUser = {
    id: "u-admin",
    kind: "human",
    platformRole: "admin",
    displayName: "平台管理员",
    email: "admin@example.test",
    createdAt: "2026-10-16T00:00:00Z",
  };
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
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

async function typeInput(node: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      "value",
    )!.set!.call(node, value);
    node.dispatchEvent(new Event("input", { bubbles: true }));
    node.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

describe("Marketplace platform admin catalog", () => {
  it("blocks non-admin users from managing the platform catalog", async () => {
    currentUser = {
      ...currentUser,
      id: "u-member",
      platformRole: "user",
    };
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => {
      root.render(
        <QueryClientProvider client={qc}>
          <MemoryRouter>
            <PlatformMarketplace />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    expect(document.body.textContent).toContain("需要平台管理员权限");
  });

  it("lets platform admins register, rotate secret, toggle enabled, and delete marketplace apps", async () => {
    const listSpy = vi
      .spyOn(api, "adminMarketplaceApps")
      .mockResolvedValue({ apps: [sampleApp] });
    const createSpy = vi
      .spyOn(api, "createAdminMarketplaceApp")
      .mockResolvedValue({
        app: {
          ...sampleApp,
          id: "app-2",
          slug: "quiz-hub",
          name: "群聊竞答",
        },
        signingSecret: "secret-initial-123",
      });
    const rotateSpy = vi
      .spyOn(api, "rotateAdminMarketplaceAppSecret")
      .mockResolvedValue({
        app: sampleApp,
        signingSecret: "secret-rotated-456",
      });
    const updateSpy = vi
      .spyOn(api, "updateAdminMarketplaceApp")
      .mockResolvedValue({
        app: { ...sampleApp, enabled: false },
      });
    const deleteSpy = vi
      .spyOn(api, "deleteAdminMarketplaceApp")
      .mockResolvedValue(undefined);

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => {
      root.render(
        <QueryClientProvider client={qc}>
          <MemoryRouter>
            <PlatformMarketplace />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    await flush();

    expect(listSpy).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).toContain("协作棋盘");
    expect(document.body.textContent).toContain("board-room");

    const inputs = [...document.body.querySelectorAll<HTMLInputElement>("form input")];
    await typeInput(inputs[0], "群聊竞答");
    await typeInput(inputs[1], "quiz-hub");
    await typeInput(inputs[2], "互动问答面板");
    await typeInput(inputs[3], "https://quiz.example.test/ui");
    await typeInput(inputs[4], "https://quiz.example.test/mcp");

    const form = document.body.querySelector<HTMLFormElement>("form.settings-form")!;
    await act(async () => {
      form.requestSubmit();
    });
    await flush();

    expect(createSpy).toHaveBeenCalledWith({
      name: "群聊竞答",
      slug: "quiz-hub",
      description: "互动问答面板",
      webUrl: "https://quiz.example.test/ui",
      mcpUrl: "https://quiz.example.test/mcp",
      enabled: true,
    });
    const secretInput = document.body.querySelector<HTMLInputElement>(
      'input[aria-label="应用签名密钥"]',
    );
    expect(secretInput?.value).toBe("secret-initial-123");

    const rotateBtn = document.body.querySelector<HTMLButtonElement>(
      'button[aria-label="轮换 协作棋盘 密钥"]',
    )!;
    await act(async () => {
      rotateBtn.click();
    });
    await flush();
    expect(rotateSpy).toHaveBeenCalledWith("app-1");
    expect(
      document.body.querySelector<HTMLInputElement>(
        'input[aria-label="应用签名密钥"]',
      )?.value,
    ).toBe("secret-rotated-456");

    const disableBtn = [...document.body.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "停用",
    )!;
    await act(async () => {
      disableBtn.click();
    });
    await flush();
    expect(updateSpy).toHaveBeenCalledWith("app-1", { enabled: false });

    const deleteBtn = document.body.querySelector<HTMLButtonElement>(
      'button[aria-label="删除 协作棋盘"]',
    )!;
    await act(async () => {
      deleteBtn.click();
    });
    await flush();
    expect(deleteSpy).toHaveBeenCalledWith("app-1");
  });
});

describe("Workspace marketplace installation modal", () => {
  it("allows workspace owners to install and uninstall catalog apps", async () => {
    vi.spyOn(api, "marketplaceCatalog").mockResolvedValue({ apps: [sampleApp] });
    const wsAppsSpy = vi
      .spyOn(api, "workspaceApps")
      .mockResolvedValueOnce({ apps: [] })
      .mockResolvedValue({ apps: [sampleApp] });
    const installSpy = vi
      .spyOn(api, "installWorkspaceApp")
      .mockResolvedValue({ app: sampleApp });
    const uninstallSpy = vi
      .spyOn(api, "uninstallWorkspaceApp")
      .mockResolvedValue(undefined);

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => {
      root.render(
        <QueryClientProvider client={qc}>
          <WorkspaceMarketplaceModal
            workspace={sampleWorkspace}
            isOwner={true}
            onClose={() => {}}
          />
        </QueryClientProvider>,
      );
    });
    await flush();

    expect(wsAppsSpy).toHaveBeenCalledWith("ws-1");
    const installBtn = [...document.body.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "安装到工作空间",
    )!;
    await act(async () => {
      installBtn.click();
    });
    await flush();
    expect(installSpy).toHaveBeenCalledWith("ws-1", "app-1");

    const uninstallBtn = [...document.body.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "卸载应用",
    )!;
    await act(async () => {
      uninstallBtn.click();
    });
    await flush();
    expect(uninstallSpy).toHaveBeenCalledWith("ws-1", "app-1");
  });
});

describe("Group conversation apps button & iframe postMessage handshake", () => {
  const baseConv: Conversation = {
    id: "conv-group-1",
    workspaceId: "ws-1",
    type: "group",
    name: "产品讨论群",
    memberIds: ["u-admin", "u-member"],
    lastSeq: 0,
    readSeq: 0,
    notificationLevel: "all",
    updatedAt: "2026-10-16T00:00:00Z",
  };

  it("renders the group apps toggle button only for group conversations", async () => {
    const noop = () => {};
    await act(async () => {
      root.render(
        <ConversationHeader
          conversation={baseConv}
          name="产品讨论群"
          membersOpen={false}
          appsOpen={false}
          onMenu={noop}
          onSearchOpen={noop}
          onMembers={noop}
          onApps={noop}
        />,
      );
    });
    expect(
      document.body.querySelector('button[aria-label="群聊应用"]'),
    ).not.toBeNull();

    for (const nonGroupType of ["channel", "direct"] as const) {
      await act(async () => {
        root.render(
          <ConversationHeader
            conversation={{ ...baseConv, type: nonGroupType }}
            name="非群聊"
            membersOpen={false}
            appsOpen={false}
            onMenu={noop}
            onSearchOpen={noop}
            onMembers={noop}
            onApps={noop}
          />,
        );
      });
      expect(
        document.body.querySelector('button[aria-label="群聊应用"]'),
      ).toBeNull();
    }
  });

  it("mints a symmetric Context Token and posts teamie:mcp-app:init when the iframe is ready", async () => {
    vi.spyOn(api, "conversationApps").mockResolvedValue({ apps: [sampleApp] });
    const sessionSpy = vi
      .spyOn(api, "createConversationAppSession")
      .mockResolvedValue({
        app: sampleApp,
        token: "jwt-context-token-xyz",
        expiresAt: "2026-10-16T01:00:00Z",
      });

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => {
      root.render(
        <QueryClientProvider client={qc}>
          <ConversationAppsPane
            overlayMembers={false}
            mobile={false}
            sidebar={false}
            workspace={sampleWorkspace}
            conversation={baseConv}
            user={currentUser}
            owner={true}
            onClose={() => {}}
            onOpenWorkspaceApps={() => {}}
          />
        </QueryClientProvider>,
      );
    });
    await flush();

    expect(sessionSpy).toHaveBeenCalledWith("conv-group-1", "app-1");
    const iframe = document.body.querySelector<HTMLIFrameElement>(
      'iframe[title="群聊应用：协作棋盘"]',
    );
    expect(iframe).not.toBeNull();
    expect(iframe?.getAttribute("src")).toBe("https://board.example.test/ui");

    const postMessageSpy = vi.fn();
    Object.defineProperty(iframe!, "contentWindow", {
      value: { postMessage: postMessageSpy },
      configurable: true,
    });

    await act(async () => {
      window.dispatchEvent(
        new MessageEvent("message", {
          origin: "https://board.example.test",
          source: iframe!.contentWindow,
          data: { type: "teamie:mcp-app:ready" },
        }),
      );
    });

    expect(postMessageSpy).toHaveBeenCalledWith(
      {
        type: "teamie:mcp-app:init",
        payload: {
          appId: "app-1",
          slug: "board-room",
          name: "协作棋盘",
          webUrl: "https://board.example.test/ui",
          mcpUrl: "https://board.example.test/mcp",
          token: "jwt-context-token-xyz",
          expiresAt: "2026-10-16T01:00:00Z",
          workspaceId: "ws-1",
          conversationId: "conv-group-1",
          user: {
            id: "u-admin",
            displayName: "平台管理员",
          },
        },
      },
      "https://board.example.test",
    );
  });
});
