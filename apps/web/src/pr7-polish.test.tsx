// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type { Agent, Bootstrap, Conversation, Member, User, Workspace } from "@teamie/api-client";
import { AgentDetail } from "./agents";
import { AuthenticatedApp } from "./authenticated-app";
import { MessageOverflowMenu } from "./chat/message-overflow-menu";
import { ProfileSettings } from "./dialogs";
import { logoutAndCleanup } from "./logout-cleanup";
import { WorkspaceMembers } from "./members";
import { ConversationView } from "./messages";
import type { Participant } from "./participants";
import { WorkspaceHeaderMenu } from "./shell/workspace-header-menu";
import { chatStore } from "./store";
import { toast } from "./toast";

const owner: User = {
  id: "u-owner",
  kind: "human",
  platformRole: "user",
  displayName: "林晨",
  email: "owner@example.test",
  createdAt: "2026-10-18T00:00:00Z",
};
const regularUser: User = {
  id: "u-member",
  kind: "human",
  platformRole: "user",
  displayName: "周晴",
  email: "member@example.test",
  createdAt: "2026-10-18T00:00:00Z",
};
const workspace: Workspace = {
  id: "ws-1",
  name: "研发空间",
  createdBy: owner.id,
  createdAt: owner.createdAt,
};
const general: Conversation = {
  id: "conv-1",
  workspaceId: workspace.id,
  type: "channel",
  name: "general",
  memberIds: [owner.id, regularUser.id],
  lastSeq: 1,
  readSeq: 1,
  updatedAt: owner.createdAt,
};

let activeUser: User = owner;
const logoutMock = vi.fn(async () => {
  api.csrfToken = "";
});
const refreshMock = vi.fn(async () => {});
const syncMock = vi.fn();
let activeCapabilities: Record<string, boolean> = {
  "im.dm.create": true,
  "im.group.create": true,
  "im.channel.create": true,
  "im.files.upload": false,
  "im.presence.publish": false,
  "im.messages.edit": true,
  "im.messages.retract": true,
  "agents.update": true,
  "agents.enabled": true,
};

vi.mock("./auth", () => ({
  useAuth: () => ({
    user: activeUser,
    logout: logoutMock,
    updateUser: vi.fn(),
  }),
}));

vi.mock("./chat", async (original) => {
  const actual = await original<typeof import("./chat")>();
  return {
    ...actual,
    ChatProvider: ({ children }: { children: React.ReactNode }) => <>{children}</>,
    useChat: () => ({
      data: {
        user: activeUser,
        workspaces: [workspace],
        conversations: [general],
        capabilities: activeCapabilities,
        flagsRevision: "1",
        syncCursor: "1",
      } satisfies Bootstrap,
      connection: "live" as const,
      syncError: "",
      refresh: refreshMock,
      sync: syncMock,
      sendTyping: vi.fn(),
    }),
  };
});

vi.mock("./shell", () => ({
  Shell: () => <div data-testid="shell-stub">Shell</div>,
}));

function LocationProbe() {
  const loc = useLocation();
  return <output data-location>{`${loc.pathname}${loc.search}`}</output>;
}

let host: HTMLDivElement;
let root: Root;
let qc: QueryClient;

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  if (!Element.prototype.scrollTo) {
    Element.prototype.scrollTo = () => {};
  }
  activeUser = owner;
  activeCapabilities = {
    "im.dm.create": true,
    "im.group.create": true,
    "im.channel.create": true,
    "im.files.upload": false,
    "im.presence.publish": false,
    "im.messages.edit": true,
    "im.messages.retract": true,
    "agents.update": true,
    "agents.enabled": true,
  };
  api.csrfToken = "";
  localStorage.clear();
  chatStore.clear();
  chatStore.begin(owner.id);
  chatStore.replaceConversations([general]);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  logoutMock.mockClear();
  refreshMock.mockClear();
  syncMock.mockClear();
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  qc.clear();
  localStorage.clear();
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

describe("PR 7 polish & IA", () => {
  it("opens WorkspaceHeaderMenu with role-appropriate items and keyboard navigation", async () => {
    const onOpenDialog = vi.fn();
    const onNavigate = vi.fn();
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={[`/app/w/${workspace.id}/c/${general.id}`]}>
          <WorkspaceHeaderMenu
            workspace={workspace}
            owner={true}
            canCreateChannel={true}
            active={true}
            onOpenDialog={onOpenDialog}
            onNavigate={onNavigate}
          />
          <LocationProbe />
        </MemoryRouter>,
      );
    });

    const trigger = host.querySelector<HTMLButtonElement>(".workspace-title-trigger")!;
    expect(trigger.textContent).toContain("研发空间");
    await act(async () => trigger.click());

    const ownerItems = Array.from(
      host.querySelectorAll<HTMLButtonElement>('.workspace-title-menu [role="menuitem"]'),
    );
    expect(ownerItems.map((el) => el.textContent?.trim())).toEqual([
      "成员目录",
      "邀请成员",
      "工作空间应用",
      "创建频道",
    ]);

    // Escape closes the menu and restores focus to trigger
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(host.querySelector(".workspace-title-menu")).toBeNull();
    expect(document.activeElement).toBe(trigger);

    // Non-owner sees only 成员目录 and 工作空间应用
    await act(async () => {
      root.render(
        <MemoryRouter initialEntries={[`/app/w/${workspace.id}/c/${general.id}`]}>
          <WorkspaceHeaderMenu
            workspace={workspace}
            owner={false}
            canCreateChannel={true}
            active={true}
            onOpenDialog={onOpenDialog}
            onNavigate={onNavigate}
          />
        </MemoryRouter>,
      );
    });
    await act(async () => {
      host.querySelector<HTMLButtonElement>(".workspace-title-trigger")!.click();
    });
    const memberItems = Array.from(
      host.querySelectorAll<HTMLButtonElement>('.workspace-title-menu [role="menuitem"]'),
    );
    expect(memberItems.map((el) => el.textContent?.trim())).toEqual([
      "成员目录",
      "工作空间应用",
    ]);
  });

  it("allows workspace owner to remove only real non-owner members and never discovered-only Agents", async () => {
    const participants: Participant[] = [
      {
        userId: owner.id,
        displayName: owner.displayName,
        kind: "human",
        role: "owner",
        workspaceMember: true,
      },
      {
        userId: regularUser.id,
        displayName: regularUser.displayName,
        kind: "human",
        role: "member",
        workspaceMember: true,
      },
      {
        userId: "agent-discovered",
        displayName: "测试助手",
        kind: "agent",
        role: "member",
        workspaceMember: false,
        agentEnabled: true,
        ownerId: owner.id,
        ownerName: owner.displayName,
      },
    ];
    const removeSpy = vi.spyOn(api, "removeMember").mockResolvedValue(undefined);
    const toastSuccess = vi.spyOn(toast, "success");

    await act(async () => {
      root.render(
        <QueryClientProvider client={qc}>
          <MemoryRouter>
            <WorkspaceMembers
              workspace={workspace}
              members={participants}
              agents={[]}
              loading={false}
              loadError={false}
              onRetry={() => {}}
            />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });

    const removeButtons = Array.from(
      host.querySelectorAll<HTMLButtonElement>("button"),
    ).filter((btn) => btn.textContent?.trim() === "移出工作空间");
    // Only regularUser (real non-owner member) gets a remove button; self and discovered-only Agent do not
    expect(removeButtons).toHaveLength(1);
    expect(removeButtons[0].closest("li")?.textContent).toContain("周晴");

    await act(async () => removeButtons[0].click());
    const dialog = document.body.querySelector('[role="dialog"]');
    expect(dialog).not.toBeNull();
    expect(dialog?.textContent).toContain("移出工作空间");
    const confirmBtn = dialog!.querySelector<HTMLButtonElement>(".button.danger")!;
    expect(confirmBtn).not.toBeNull();

    await act(async () => confirmBtn.click());
    await flush();
    expect(removeSpy).toHaveBeenCalledWith(workspace.id, regularUser.id);
    expect(toastSuccess).toHaveBeenCalledWith("已将 周晴 移出工作空间");

    // Non-owner sees zero remove buttons
    activeUser = regularUser;
    await act(async () => {
      root.render(
        <QueryClientProvider client={qc}>
          <MemoryRouter>
            <WorkspaceMembers
              workspace={workspace}
              members={participants}
              agents={[]}
              loading={false}
              loadError={false}
              onRetry={() => {}}
            />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    const nonOwnerRemoveButtons = Array.from(
      host.querySelectorAll<HTMLButtonElement>("button"),
    ).filter((btn) => btn.textContent?.trim() === "移出工作空间");
    expect(nonOwnerRemoveButtons).toHaveLength(0);
  });

  it("isolates keyboard navigation inside MessageOverflowMenu and groups retract behind a danger separator", async () => {
    const parentKeyDown = vi.fn();
    const onRetract = vi.fn();
    let open = true;
    await act(async () => {
      root.render(
        <div role="presentation" onKeyDown={parentKeyDown}>
          <MessageOverflowMenu
            open={open}
            onOpenChange={(next) => {
              open = next;
            }}
            tabIndex={0}
            canPin={true}
            isPinned={false}
            canMarkUnread={true}
            hasBody={true}
            canEdit={true}
            canRetract={true}
            onTogglePin={() => {}}
            onMarkUnread={() => {}}
            onCopyText={() => {}}
            onCopyLink={() => {}}
            onEdit={() => {}}
            onRetract={onRetract}
          />
        </div>,
      );
    });
    const menu = host.querySelector<HTMLElement>(".message-overflow-menu")!;
    const items = Array.from(menu.querySelectorAll<HTMLButtonElement>('[role="menuitem"]'));
    expect(items.map((el) => el.textContent?.trim())).toEqual([
      "置顶",
      "标为未读",
      "复制文本",
      "复制消息链接",
      "编辑消息",
      "撤回消息",
    ]);
    expect(menu.querySelector('[role="separator"]')).not.toBeNull();
    expect(items[items.length - 1].classList.contains("danger")).toBe(true);

    items[0].focus();
    await act(async () => {
      menu.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));
    });
    expect(parentKeyDown).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(items[1]);
    await act(async () => {
      await new Promise((resolve) => requestAnimationFrame(resolve));
    });
    expect(document.activeElement).toBe(items[1]);

    await act(async () => items[items.length - 1].click());
    expect(onRetract).toHaveBeenCalledTimes(1);
  });

  it("clears all per-user local UI state on logoutAndCleanup and in standalone ProfileSettings", async () => {
    const seedKeys = (uid: string) => {
      localStorage.setItem(`teamie.nav.${uid}`, '{"lastChatPath":"/app/w/ws-1/c/conv-1"}');
      localStorage.setItem(`teamie.appPaneWidth.${uid}`, "420");
      localStorage.setItem(`teamie.sidebarSections.${uid}`, '{"channels":true}');
      localStorage.setItem(`teamie.reactions.recent.v1:${uid}`, '["👍"]');
    };

    // Direct helper call
    seedKeys(owner.id);
    const resetAll = vi.fn();
    const navigate = vi.fn();
    await logoutAndCleanup({
      userId: owner.id,
      logout: logoutMock,
      navigate,
      appTabs: { resetAll },
    });
    expect(resetAll).toHaveBeenCalledTimes(1);
    expect(navigate).toHaveBeenCalledWith("/login");
    expect(localStorage.getItem(`teamie.nav.${owner.id}`)).toBeNull();
    expect(localStorage.getItem(`teamie.appPaneWidth.${owner.id}`)).toBeNull();
    expect(localStorage.getItem(`teamie.sidebarSections.${owner.id}`)).toBeNull();
    expect(localStorage.getItem(`teamie.reactions.recent.v1:${owner.id}`)).toBeNull();

    // Standalone ProfileSettings (mounted without Shell / ConversationAppTabsProvider)
    seedKeys(owner.id);
    await act(async () => {
      root.render(
        <QueryClientProvider client={qc}>
          <MemoryRouter initialEntries={["/app/settings"]}>
            <Routes>
              <Route path="/app/settings" element={<ProfileSettings />} />
              <Route path="/login" element={<div data-testid="login-page">Login</div>} />
            </Routes>
            <LocationProbe />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    const signOutBtn = Array.from(host.querySelectorAll<HTMLButtonElement>("button")).find(
      (btn) => btn.textContent?.trim() === "退出登录",
    )!;
    expect(signOutBtn).toBeDefined();
    await act(async () => signOutBtn.click());
    await flush();
    expect(host.querySelector("[data-location]")?.textContent).toBe("/login");
    expect(localStorage.getItem(`teamie.nav.${owner.id}`)).toBeNull();
    expect(localStorage.getItem(`teamie.appPaneWidth.${owner.id}`)).toBeNull();
    expect(localStorage.getItem(`teamie.sidebarSections.${owner.id}`)).toBeNull();
    expect(localStorage.getItem(`teamie.reactions.recent.v1:${owner.id}`)).toBeNull();
  });

  it("prevents browser file navigation on global drop and toasts when dropping on a conversation with uploads disabled", async () => {
    vi.spyOn(api, "messages").mockResolvedValue({ messages: [], hasMore: false });
    vi.spyOn(api, "listPins").mockResolvedValue({ pins: [], pinsVersion: "1" });
    vi.spyOn(api, "read").mockResolvedValue({ readSeq: 1 });
    const toastError = vi.spyOn(toast, "error");
    const members: Member[] = [
      { userId: owner.id, displayName: owner.displayName, kind: "human", role: "owner" },
      { userId: regularUser.id, displayName: regularUser.displayName, kind: "human", role: "member" },
    ];

    await act(async () => {
      root.render(
        <QueryClientProvider client={qc}>
          <MemoryRouter>
            <AuthenticatedApp />
            <ConversationView
              conversation={general}
              members={members}
              onMembers={() => {}}
              membersOpen={false}
              onMenu={() => {}}
            />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    await flush();

    // Window-level drop outside upload zone is prevented
    const windowDrop = new Event("drop", { bubbles: true, cancelable: true });
    Object.defineProperty(windowDrop, "dataTransfer", {
      value: { types: ["Files"], files: [new File(["hi"], "a.txt")] },
    });
    window.dispatchEvent(windowDrop);
    expect(windowDrop.defaultPrevented).toBe(true);

    // Dropping on ConversationView when uploadsEnabled === false prevents default and toasts
    const view = host.querySelector<HTMLElement>(".conversation-view")!;
    const convDrop = new Event("drop", { bubbles: true, cancelable: true });
    Object.defineProperty(convDrop, "dataTransfer", {
      value: { types: ["Files"], files: [new File(["hi"], "a.txt")] },
    });
    await act(async () => {
      view.dispatchEvent(convDrop);
    });
    expect(convDrop.defaultPrevented).toBe(true);
    expect(toastError).toHaveBeenCalledWith("文件上传已关闭");
  });

  it("renders a 发消息 action in AgentDetail between 编辑资料 and 停用 and opens a direct conversation", async () => {
    const agent: Agent = {
      id: "agent-1",
      principalId: "agent-user-1",
      ownerId: owner.id,
      ownerName: owner.displayName,
      name: "值班助手",
      description: "负责值班与答疑",
      enabled: true,
      workspaceIds: [workspace.id],
      createdAt: owner.createdAt,
      updatedAt: owner.createdAt,
    };
    const dmConv: Conversation = {
      id: "dm-agent-1",
      workspaceId: workspace.id,
      type: "direct",
      name: "",
      memberIds: [owner.id, agent.principalId],
      lastSeq: 0,
      readSeq: 0,
      updatedAt: owner.createdAt,
    };
    vi.spyOn(api, "agent").mockResolvedValue({ agent });
    vi.spyOn(api, "modelCatalog").mockResolvedValue({ models: [], defaultModelId: "" });
    const createDmSpy = vi
      .spyOn(api, "createConversation")
      .mockResolvedValue({ conversation: dmConv });

    await act(async () => {
      root.render(
        <QueryClientProvider client={qc}>
          <MemoryRouter initialEntries={["/app/agents/agent-1"]}>
            <Routes>
              <Route path="/app/agents/:agentId" element={<AgentDetail agentId="agent-1" />} />
              <Route path="/app/w/:workspaceId/c/:conversationId" element={<div>DM View</div>} />
            </Routes>
            <LocationProbe />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    await flush();

    const actionButtons = Array.from(
      host.querySelectorAll<HTMLButtonElement>(".agent-detail-actions button"),
    );
    expect(actionButtons.map((btn) => btn.textContent?.trim())).toEqual([
      "编辑资料",
      "发消息",
      "停用",
    ]);

    await act(async () => actionButtons[1].click());
    await flush();
    expect(createDmSpy).toHaveBeenCalledWith(workspace.id, {
      type: "direct",
      memberIds: [agent.principalId],
    });
    expect(host.querySelector("[data-location]")?.textContent).toBe(
      `/app/w/${workspace.id}/c/${dmConv.id}`,
    );
  });
});
