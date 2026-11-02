// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@teamie/api-client";
import type {
  AdminModelProvider,
  Conversation,
  DeveloperMarketplaceApp,
  User,
  Workspace,
} from "@teamie/api-client";
import { ConfirmDialog, Modal, ModalCloseButton } from "./ui";
import { toast } from "./toast";
import { ProviderProfileDialog } from "./admin/provider-profile-dialog";
import { AgentProfileDialog } from "./agents/agent-profile-dialog";
import { WorkspaceDialog } from "./dialogs";
import { DeveloperPortal } from "./developers";
import {
  ConversationAppsPane,
  PlatformMarketplace,
  WorkspaceMarketplaceModal,
} from "./marketplace";
import { ConversationHeader } from "./chat/conversation-chrome";
import { routeWorkspaceId } from "./shell/shell-navigation-helpers";

let currentUser: User;
vi.mock("./auth", () => ({ useAuth: () => ({ user: currentUser }) }));
vi.mock("./chat", () => ({
  useChat: () => ({
    data: { capabilities: { "agents.enabled": true }, workspaces: [] },
    refresh: async () => {},
  }),
}));

const ws = (id: string, name: string): Workspace => ({
  id,
  name,
  createdBy: "u-1",
  createdAt: "2026-11-01T00:00:00Z",
});
const devApp: DeveloperMarketplaceApp = {
  id: "app-1",
  slug: "board",
  name: "协作棋盘",
  description: "",
  webUrl: "https://board.example.test/ui",
  mcpUrl: "https://board.example.test/mcp",
  enabled: true,
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
  isOfficial: false,
  isPublished: true,
  publishedVersion: 2,
  reviewStatus: "approved",
  draftName: "协作棋盘",
  draftDescription: "",
  draftWebUrl: "https://board.example.test/ui",
  draftMcpUrl: "https://board.example.test/mcp",
  hasDraftChanges: false,
  submissionRevision: 1,
  submissionNote: "",
  reviewNote: "",
  adminSuspended: false,
  suspendedReason: "",
};

let host: HTMLDivElement, root: Root, client: QueryClient;
beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  currentUser = {
    id: "u-1",
    kind: "human",
    platformRole: "user",
    displayName: "林舟",
    email: "u1@example.test",
    createdAt: "2026-11-01T00:00:00Z",
  };
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  client.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function render(node: React.ReactNode, path = "/app") {
  await act(async () =>
    root.render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={[path]}>{node}</MemoryRouter>
      </QueryClientProvider>,
    ),
  );
  await flush();
}
async function flush() {
  for (let i = 0; i < 4; i++)
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
}
const dialogs = () => [...document.body.querySelectorAll<HTMLElement>('[role="dialog"]')];
const topDialog = () => dialogs().at(-1)!;
function button(text: string, scope: ParentNode = document.body) {
  const found = [...scope.querySelectorAll("button")].find(
    (b) => b.textContent?.trim() === text,
  );
  if (!found) throw new Error(`Missing button: ${text}`);
  return found;
}
async function click(node: HTMLElement) {
  await act(async () => node.click());
  await flush();
}
async function escape() {
  await act(async () => {
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
  });
}
async function type(node: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(node, value);
    node.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

describe("ConfirmDialog and Modal close requests", () => {
  it("focuses Cancel, lists impact before confirming and blocks every dismissal while busy", async () => {
    const onCancel = vi.fn();
    const onConfirm = vi.fn();
    function Harness({ busy }: { busy: boolean }) {
      return (
        <ConfirmDialog
          tone="danger"
          title="删除？"
          impact={["影响一", "影响二"]}
          confirmLabel="确认删除"
          busy={busy}
          onCancel={onCancel}
          onConfirm={onConfirm}
        />
      );
    }
    await render(<Harness busy={false} />);
    expect(document.activeElement?.textContent?.trim()).toBe("取消");
    const impact = topDialog().querySelector(".confirm-impact")!;
    expect([...impact.querySelectorAll("li")].map((li) => li.textContent)).toEqual(["影响一", "影响二"]);
    expect(button("确认删除").className).toContain("danger");
    expect(button("确认删除").getAttribute("aria-describedby")).toBe(impact.id);
    await click(button("确认删除"));
    expect(onConfirm).toHaveBeenCalledOnce();

    await render(<Harness busy />);
    await escape();
    await click(topDialog().querySelector<HTMLButtonElement>(".modal-close")!);
    expect(button("取消").disabled).toBe(true);
    expect(onCancel).not.toHaveBeenCalled();

    await render(<Harness busy={false} />);
    await escape();
    expect(onCancel).toHaveBeenCalledOnce();
  });

  it("routes in-dialog cancel buttons through the dirty discard confirmation", async () => {
    const onClose = vi.fn();
    function Harness() {
      const [dirty, setDirty] = useState(false);
      return (
        <Modal title="编辑" onClose={onClose} dirty={dirty}>
          <input aria-label="名称" onInput={() => setDirty(true)} />
          <ModalCloseButton>取消</ModalCloseButton>
        </Modal>
      );
    }
    await render(<Harness />);
    await type(document.querySelector<HTMLInputElement>('input[aria-label="名称"]')!, "x");
    await click(button("取消"));
    expect(onClose).not.toHaveBeenCalled();
    expect(topDialog().textContent).toContain("放弃未保存的更改？");
    await click(button("放弃"));
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("asks before closing a workspace invite whose link was never copied", async () => {
    vi.spyOn(api, "createInvite").mockResolvedValue({
      code: "invite-code",
      expiresAt: "2026-11-08T00:00:00Z",
    });
    const onClose = vi.fn();
    await render(<WorkspaceDialog kind="invite" workspace={ws("w-1", "研发")} onClose={onClose} />);
    await act(async () => document.querySelector("form")!.requestSubmit());
    await flush();
    await click(button("完成"));
    expect(onClose).not.toHaveBeenCalled();
    expect(topDialog().textContent).toContain("邀请链接还没有复制");
  });

  it("reports a missing workspace name inline instead of a native bubble", async () => {
    const create = vi.spyOn(api, "createWorkspace");
    await render(<WorkspaceDialog kind="workspace" onClose={() => {}} />);
    const form = document.querySelector("form")!;
    expect(form.noValidate).toBe(true);
    await act(async () => form.requestSubmit());
    expect(create).not.toHaveBeenCalled();
    const input = document.querySelector<HTMLInputElement>('input[name="name"]')!;
    expect(input.getAttribute("aria-invalid")).toBe("true");
    expect(document.getElementById(input.getAttribute("aria-describedby")!)?.textContent).toBe(
      "请填写工作空间名称。",
    );
    expect(document.activeElement).toBe(input);
  });
});

describe("model gateway provider confirmations", () => {
  const provider: AdminModelProvider = {
    id: "openai",
    displayName: "OpenAI",
    baseUrl: "https://api.example.test/v1",
    hasApiKey: true,
    enabled: true,
    configured: true,
    modelCount: 3,
    updatedAt: "2026-11-01T00:00:00Z",
  };
  it.each([
    ["clearing the saved API Key", "providerClearApiKey", "API Key 将被永久清除"],
    ["disabling the provider", "providerEnabled", "3 个模型将从 Agent 可选目录中隐藏"],
  ])("requires confirmation before %s", async (_, field, copy) => {
    const onSave = vi.fn().mockResolvedValue(true);
    const onClose = vi.fn();
    await render(<ProviderProfileDialog initial={provider} onSave={onSave} onClose={onClose} />);
    await click(document.querySelector<HTMLInputElement>(`input[name="${field}"]`)!);
    await act(async () => document.querySelector("form")!.requestSubmit());
    expect(onSave).not.toHaveBeenCalled();
    expect(topDialog().textContent).toContain(copy);
    expect(topDialog().textContent).toContain("平滑回退到平台默认模型");
    await click(button("取消", topDialog()));
    expect(onSave).not.toHaveBeenCalled();
    await act(async () => document.querySelector("form")!.requestSubmit());
    await click(button("确认保存"));
    expect(onSave).toHaveBeenCalledOnce();
    expect(onClose).toHaveBeenCalledOnce();
  });
  it("saves ordinary edits without a confirmation", async () => {
    const onSave = vi.fn().mockResolvedValue(true);
    await render(<ProviderProfileDialog initial={provider} onSave={onSave} onClose={() => {}} />);
    await act(async () => document.querySelector("form")!.requestSubmit());
    expect(onSave).toHaveBeenCalledOnce();
  });
});

describe("first Agent guidance", () => {
  const workspaces = [ws("w-1", "研发"), ws("w-2", "市场")];
  function mountDialog(initialWorkspaceId?: string) {
    vi.spyOn(api, "modelCatalog").mockResolvedValue({ models: [] });
    return render(
      <AgentProfileDialog
        workspaces={workspaces}
        initialWorkspaceId={initialWorkspaceId}
        onClose={() => {}}
      />,
    );
  }
  const checked = () =>
    [...document.querySelectorAll<HTMLInputElement>(".visibility-option input")].map((i) => i.checked);

  it("preselects the workspace the owner came from, but only a current membership", async () => {
    expect(routeWorkspaceId("/app/w/w-2/c/general")).toBe("w-2");
    expect(routeWorkspaceId("/app")).toBe("");
    await mountDialog("w-2");
    expect(checked()).toEqual([false, true]);
    expect(document.querySelector(".field-warning")).toBeNull();
    await act(async () => root.unmount());
    root = createRoot(host);
    await mountDialog("not-a-member");
    expect(checked()).toEqual([false, false]);
  });

  it("warns about zero workspaces and requires explicit confirmation before creating", async () => {
    const create = vi.spyOn(api, "createAgent").mockResolvedValue({
      agent: {
        id: "a-1",
        principalId: "a-1",
        ownerId: "u-1",
        ownerName: "林舟",
        name: "助手",
        description: "",
        workspaceIds: [],
        enabled: true,
        createdAt: "2026-11-01T00:00:00Z",
        updatedAt: "2026-11-01T00:00:00Z",
      },
    } as Awaited<ReturnType<typeof api.createAgent>>);
    await mountDialog();
    expect(document.querySelector(".field-warning")?.textContent).toContain("未选择工作空间");
    await type(document.querySelector<HTMLInputElement>("form input")!, "助手");
    await act(async () => document.querySelector("form")!.requestSubmit());
    expect(create).not.toHaveBeenCalled();
    expect(topDialog().textContent).toContain("无法在成员列表中发现它");
    expect(document.activeElement?.textContent?.trim()).toBe("返回选择");
    await click(button("返回选择"));
    expect(create).not.toHaveBeenCalled();
    await act(async () => document.querySelector("form")!.requestSubmit());
    await click(button("仍然创建"));
    expect(create).toHaveBeenCalledOnce();
    expect(create.mock.calls[0][0].workspaceIds).toEqual([]);
  });

  it("routes the dialog cancel button through the unsaved-changes prompt", async () => {
    const onClose = vi.fn();
    vi.spyOn(api, "modelCatalog").mockResolvedValue({ models: [] });
    await render(<AgentProfileDialog workspaces={workspaces} onClose={onClose} />);
    await type(document.querySelector<HTMLInputElement>("form input")!, "助手");
    await click(button("取消"));
    expect(onClose).not.toHaveBeenCalled();
    expect(topDialog().textContent).toContain("放弃未保存的更改？");
  });
});

describe("developer and marketplace failures are not empty states", () => {
  it("shows 加载失败 with retry for the developer app list and confirms draft deletion", async () => {
    const list = vi
      .spyOn(api, "developerMarketplaceApps")
      .mockRejectedValueOnce(new ApiError(503, "identity_unavailable", "down"))
      .mockResolvedValue({ apps: [{ ...devApp, isPublished: false, publishedVersion: 0, reviewStatus: "draft" }] });
    const remove = vi.spyOn(api, "deleteDeveloperMarketplaceApp").mockResolvedValue(undefined);
    await render(<DeveloperPortal workspaces={[]} />, "/app/developers");
    expect(host.textContent).toContain("加载失败");
    expect(host.textContent).not.toContain("你还没有创建任何应用草稿");
    await click(button("重新加载"));
    expect(list).toHaveBeenCalledTimes(2);
    await click(host.querySelector<HTMLButtonElement>('button[aria-label="删除草稿 协作棋盘"]')!);
    expect(remove).not.toHaveBeenCalled();
    expect(topDialog().textContent).toContain("App ID 与签名密钥将一起被永久删除");
    await click(button("删除草稿", topDialog()));
    expect(remove).toHaveBeenCalledWith("app-1");
  });

  it("distinguishes a missing developer app from a failed load", async () => {
    const detail = vi
      .spyOn(api, "developerMarketplaceApp")
      .mockRejectedValueOnce(new ApiError(500, "internal", "boom"));
    await render(<DeveloperPortal appId="app-1" workspaces={[]} />, "/app/developers/apps/app-1");
    expect(host.textContent).toContain("加载失败");
    expect(host.textContent).not.toContain("未找到该开发者应用");
    detail.mockRejectedValueOnce(new ApiError(404, "not_found", "gone"));
    await click(button("重新加载"));
    expect(host.textContent).toContain("未找到该开发者应用");
  });

  it("confirms a secret rotation and keeps the new secret until 我已保存", async () => {
    vi.spyOn(api, "developerMarketplaceApp").mockResolvedValue({ app: devApp, reviews: [] });
    const rotate = vi
      .spyOn(api, "rotateDeveloperMarketplaceAppSecret")
      .mockResolvedValue({ app: devApp, signingSecret: "new-secret" });
    await render(<DeveloperPortal appId="app-1" workspaces={[]} />, "/app/developers/apps/app-1");
    await click(button("轮换签名密钥"));
    expect(rotate).not.toHaveBeenCalled();
    expect(topDialog().textContent).toContain("旧密钥将立即失效");
    await click(button("轮换密钥", topDialog()));
    expect(rotate).toHaveBeenCalledWith("app-1");
    const secret = () => document.querySelector<HTMLInputElement>('input[aria-label="应用签名密钥"]');
    expect(secret()?.value).toBe("new-secret");
    await escape();
    await click(topDialog().querySelector<HTMLButtonElement>(".modal-close")!);
    expect(secret()).not.toBeNull();
    await click(button("我已保存"));
    expect(secret()).toBeNull();
  });

  it("shows 加载失败 for the platform catalog instead of the empty filter copy", async () => {
    currentUser = { ...currentUser, platformRole: "admin" };
    vi.spyOn(api, "adminMarketplaceApps").mockRejectedValue(new ApiError(503, "chat_unavailable", "down"));
    await render(<PlatformMarketplace />);
    expect(host.textContent).toContain("加载失败");
    expect(host.textContent).not.toContain("当前筛选条件下暂无应用");
  });

  it("shows 加载失败 for a group's apps instead of the not-installed copy", async () => {
    const conversation: Conversation = {
      id: "c-1",
      workspaceId: "w-1",
      type: "group",
      name: "小组",
      memberIds: ["u-1"],
      lastSeq: 0,
      readSeq: 0,
      updatedAt: "2026-11-01T00:00:00Z",
    };
    const apps = vi
      .spyOn(api, "conversationApps")
      .mockRejectedValueOnce(new ApiError(503, "chat_unavailable", "down"))
      .mockResolvedValue({ apps: [] });
    await render(
      <ConversationAppsPane
        overlayMembers={false}
        mobile={false}
        sidebar={false}
        workspace={ws("w-1", "研发")}
        conversation={conversation}
        user={currentUser}
        owner={false}
        onClose={() => {}}
        onOpenWorkspaceApps={() => {}}
      />,
    );
    expect(host.textContent).toContain("加载失败");
    expect(host.textContent).not.toContain("暂无已安装应用");
    await click(button("重新加载"));
    expect(apps).toHaveBeenCalledTimes(2);
    expect(host.textContent).toContain("请联系工作空间创建者安装");
  });
});

describe("write failures are never silent", () => {
  it("toasts mapped copy when installing a workspace app fails", async () => {
    const error = vi.spyOn(toast, "error");
    vi.spyOn(api, "marketplaceCatalog").mockResolvedValue({ apps: [devApp] });
    vi.spyOn(api, "workspaceApps").mockResolvedValue({ apps: [] });
    vi.spyOn(api, "developerMarketplaceApps").mockResolvedValue({ apps: [] });
    vi.spyOn(api, "installWorkspaceApp").mockRejectedValue(
      new ApiError(403, "feature_disabled", "off"),
    );
    await render(<WorkspaceMarketplaceModal workspace={ws("w-1", "研发")} isOwner onClose={() => {}} />);
    await click(button("安装到工作空间"));
    expect(error).toHaveBeenCalledWith("安装失败：该功能已由平台关闭，暂时无法使用。");
  });

  it("toasts when a notification level change fails and keeps the current level", async () => {
    const error = vi.spyOn(toast, "error");
    vi.spyOn(api, "setNotificationLevel").mockRejectedValue(new TypeError("Failed to fetch"));
    const conversation: Conversation = {
      id: "c-2",
      workspaceId: "w-1",
      type: "channel",
      name: "general",
      memberIds: ["u-1"],
      lastSeq: 0,
      readSeq: 0,
      updatedAt: "2026-11-01T00:00:00Z",
    };
    await render(
      <ConversationHeader
        conversation={conversation}
        name="general"
        membersOpen={false}
        appsOpen={false}
        onMenu={() => {}}
        onSearchOpen={() => {}}
        onMembers={() => {}}
        onApps={() => {}}
        canConfigureNotifications
      />,
    );
    await click(host.querySelector<HTMLButtonElement>('button[aria-label^="会话通知设置"]')!);
    const mute = [...host.querySelectorAll<HTMLButtonElement>(".notification-level-option")].find((b) =>
      b.textContent?.includes("静音"),
    )!;
    await click(mute);
    expect(error).toHaveBeenCalledWith("通知设置未保存：网络连接失败，请检查网络后重试。");
    expect(host.querySelector('button[aria-label^="会话通知设置"]')?.getAttribute("aria-label")).toContain("全部");
  });
});
