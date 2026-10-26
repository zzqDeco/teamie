// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type {
  Attachment,
  Bootstrap,
  Conversation,
  Member,
  Message,
  User,
  Workspace,
} from "@teamie/api-client";
import { MessageAttachments } from "./attachments";
import { messageGrouping } from "./message-item";
import { ConversationView } from "./messages";
import { Shell } from "./shell";
import { chatStore } from "./store";

const owner: User = {
  id: "owner",
  kind: "human",
  platformRole: "user",
  displayName: "创建者",
  email: "owner@example.test",
  createdAt: "2026-10-16T00:00:00Z",
};

const peer: User = {
  id: "peer",
  kind: "human",
  platformRole: "user",
  displayName: "同伴",
  email: "peer@example.test",
  createdAt: "2026-10-16T00:00:00Z",
};

const workspace: Workspace = {
  id: "team",
  name: "产品研发",
  createdBy: owner.id,
  createdAt: owner.createdAt,
};

const secondWorkspace: Workspace = {
  id: "design",
  name: "设计中心",
  createdBy: owner.id,
  createdAt: owner.createdAt,
};

const members: Member[] = [
  {
    userId: owner.id,
    displayName: owner.displayName,
    kind: "human",
    role: "owner",
  },
  {
    userId: peer.id,
    displayName: peer.displayName,
    kind: "human",
    role: "member",
  },
];

const channelOne: Conversation = {
  id: "general",
  workspaceId: workspace.id,
  type: "channel",
  name: "general",
  memberIds: [owner.id, peer.id],
  lastSeq: 2,
  readSeq: 2,
  updatedAt: "2026-10-16T00:00:00Z",
};

const channelTwo: Conversation = {
  id: "alerts",
  workspaceId: workspace.id,
  type: "channel",
  name: "alerts",
  memberIds: [owner.id, peer.id],
  lastSeq: 2,
  readSeq: 2,
  updatedAt: "2026-10-16T01:00:00Z",
};

const designChannel: Conversation = {
  id: "design-general",
  workspaceId: secondWorkspace.id,
  type: "channel",
  name: "design-general",
  memberIds: [owner.id, peer.id],
  lastSeq: 1,
  readSeq: 1,
  updatedAt: "2026-10-16T02:00:00Z",
};

const rasterAttachment: Attachment = {
  id: "att-png",
  filename: "preview.png",
  mediaType: "image/png",
  sizeBytes: 1024,
  sha256: "a".repeat(64),
};

function makeMessage(
  seq: number,
  senderId = peer.id,
  body = `message ${seq}`,
  extra: Partial<Message> = {},
): Message {
  return {
    id: `m${seq}`,
    conversationId: channelOne.id,
    senderId,
    senderName: senderId === owner.id ? owner.displayName : peer.displayName,
    senderKind: "human",
    clientMessageId: `c${seq}`,
    seq,
    body,
    createdAt: new Date(Date.parse(owner.createdAt) + seq * 1000).toISOString(),
    ...extra,
  };
}

let activeUser: User;
let bootstrap: Bootstrap;
const refresh = vi.fn(async () => {});
const sync = vi.fn();
const sendTyping = vi.fn();
const emptyTyping = {};

vi.mock("./auth", () => ({
  useAuth: () => ({
    user: activeUser,
    loading: false,
    logout: vi.fn(),
    updateUser: vi.fn(),
  }),
}));

vi.mock("./chat", async (original) => ({
  ...(await original<typeof import("./chat")>()),
  useChat: () => ({
    data: bootstrap,
    loading: false,
    error: "",
    connection: "connected",
    syncError: "",
    refresh,
    sync,
    sendTyping,
    typing: emptyTyping,
  }),
}));

let host: HTMLDivElement;
let root: Root;
const noop = () => {};

function LocationProbe() {
  const location = useLocation();
  return <output data-testid="path">{location.pathname}</output>;
}

async function flush() {
  await act(async () => {
    await Promise.resolve();
  });
}

async function mountConversation(
  conversation: Conversation = channelOne,
  extra: Partial<Parameters<typeof ConversationView>[0]> = {},
) {
  await act(async () => {
    root.render(
      <ConversationView
        key={conversation.id}
        conversation={conversation}
        members={members}
        onMembers={noop}
        membersOpen={false}
        onMenu={noop}
        {...extra}
      />,
    );
  });
  await flush();
}

async function mountShell(initialEntry: string) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={[initialEntry]}>
          <Routes>
            <Route
              path="*"
              element={
                <>
                  <Shell />
                  <LocationProbe />
                </>
              }
            />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );
  });
  await flush();
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
  Object.defineProperty(window, "matchMedia", {
    writable: true,
    value: (query: string) => ({
      matches: false,
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
    }),
  });
  Object.defineProperty(HTMLElement.prototype, "scrollTo", {
    configurable: true,
    value: vi.fn(),
  });
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
    configurable: true,
    value: vi.fn(),
  });
  sessionStorage.clear();
  localStorage.clear();
  activeUser = owner;
  bootstrap = {
    user: activeUser,
    workspaces: [workspace, secondWorkspace],
    conversations: [channelOne, channelTwo, designChannel],
    capabilities: {
      "im.attachments.upload": true,
      "im.message.mentions": true,
      "im.message.edit": true,
      "im.message.retract": true,
    },
    flagsRevision: "conveniences",
    syncCursor: "0",
  };
  chatStore.begin(owner.id);
  chatStore.replaceConversations(bootstrap.conversations);
  vi.spyOn(api, "members").mockResolvedValue({ members });
  vi.spyOn(api, "workspaceAgents").mockResolvedValue({ agents: [] });
  vi.spyOn(api, "attention").mockResolvedValue({ items: [], unreadCount: 0 });
  vi.spyOn(api, "messages").mockResolvedValue({
    messages: [makeMessage(1), makeMessage(2)],
    hasMore: false,
  });
  vi.spyOn(api, "read").mockImplementation(async (_id, seq) => ({
    readSeq: seq,
  }));
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  chatStore.clear();
  localStorage.clear();
  sessionStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  delete (HTMLElement.prototype as Partial<HTMLElement>).scrollTo;
  delete (HTMLElement.prototype as Partial<HTMLElement>).scrollIntoView;
});

describe("PR-F IM conveniences", () => {
  it("1. uploads files pasted into the composer and files dragged/dropped onto .conversation-view", async () => {
    const upload = vi
      .spyOn(api, "uploadAttachment")
      .mockResolvedValueOnce({ attachment: rasterAttachment })
      .mockResolvedValueOnce({
        attachment: { ...rasterAttachment, id: "att-drop", filename: "dropped.png" },
      });

    await mountConversation(channelOne);

    const textarea = host.querySelector<HTMLTextAreaElement>(
      'textarea[aria-label="消息内容"]',
    )!;
    expect(textarea).toBeTruthy();

    const pastedFile = new File(["png-bytes"], "pasted.png", {
      type: "image/png",
    });
    const pasteEvent = new Event("paste", { bubbles: true, cancelable: true });
    Object.defineProperty(pasteEvent, "clipboardData", {
      value: { files: [pastedFile] },
    });

    let notCancelled = true;
    await act(async () => {
      notCancelled = textarea.dispatchEvent(pasteEvent);
    });
    expect(notCancelled).toBe(false);
    expect(upload).toHaveBeenCalledTimes(1);
    expect(upload.mock.calls[0][0]).toBe(channelOne.id);
    expect(upload.mock.calls[0][1]).toBe(pastedFile);
    expect(host.textContent).toContain("pasted.png");

    // Drag and drop onto .conversation-view
    const view = host.querySelector<HTMLElement>(".conversation-view")!;
    const dragEnterEvent = new Event("dragenter", {
      bubbles: true,
      cancelable: true,
    });
    Object.defineProperty(dragEnterEvent, "dataTransfer", {
      value: { types: ["Files"], files: [] },
    });
    await act(async () => {
      view.dispatchEvent(dragEnterEvent);
    });

    const overlay = host.querySelector(
      '.conversation-drop-overlay[role="region"][aria-label="释放文件以上传到当前对话"]',
    );
    expect(overlay).not.toBeNull();

    await act(async () => {
      textarea.blur();
    });
    const droppedFile = new File(["drop-bytes"], "dropped.png", {
      type: "image/png",
    });
    const dropEvent = new Event("drop", { bubbles: true, cancelable: true });
    Object.defineProperty(dropEvent, "dataTransfer", {
      value: { types: ["Files"], files: [droppedFile] },
    });
    await act(async () => {
      view.dispatchEvent(dropEvent);
    });

    expect(host.querySelector(".conversation-drop-overlay")).toBeNull();
    expect(upload).toHaveBeenCalledTimes(2);
    expect(upload.mock.calls[1][1]).toBe(droppedFile);
    expect(host.textContent).toContain("dropped.png");
    expect(document.activeElement).toBe(textarea);
  });

  it("2. renders inline raster image thumbnails + lightbox modal, excludes SVG from inline preview, and revokes object URLs on unmount", async () => {
    const blob = new Blob([new Uint8Array([137, 80, 78, 71])], {
      type: "image/png",
    });
    const downloadSpy = vi
      .spyOn(api, "attachmentContent")
      .mockResolvedValue(blob);
    const createUrl = vi.fn(() => "blob:teamie-preview-url");
    const revokeUrl = vi.fn();
    vi.stubGlobal(
      "URL",
      class extends URL {
        static createObjectURL = createUrl;
        static revokeObjectURL = revokeUrl;
      },
    );

    const svgAttachment: Attachment = {
      id: "att-svg",
      filename: "vector.svg",
      mediaType: "image/svg+xml",
      sizeBytes: 512,
      sha256: "b".repeat(64),
    };

    await act(async () => {
      root.render(
        <MessageAttachments
          conversationId={channelOne.id}
          attachments={[rasterAttachment, svgAttachment]}
        />,
      );
    });
    await flush();

    // Only the PNG is fetched for inline preview; SVG is never auto-fetched
    expect(downloadSpy).toHaveBeenCalledTimes(1);
    expect(downloadSpy).toHaveBeenCalledWith(channelOne.id, rasterAttachment.id);
    expect(createUrl).toHaveBeenCalledWith(blob);

    const thumb = host.querySelector<HTMLImageElement>("img.attachment-thumbnail");
    expect(thumb).not.toBeNull();
    expect(thumb?.getAttribute("alt")).toBe("preview.png");
    expect(thumb?.getAttribute("src")).toBe("blob:teamie-preview-url");
    expect(host.querySelector('img[alt="vector.svg"]')).toBeNull();

    // Clicking the thumbnail button opens the lightbox modal
    const thumbBtn = host.querySelector<HTMLButtonElement>(
      "button.attachment-thumbnail-button",
    )!;
    await act(async () => {
      thumbBtn.click();
    });

    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog).not.toBeNull();
    expect(dialog?.querySelector("h2")?.textContent).toBe("preview.png");
    expect(dialog?.textContent).toContain("下载原图");

    // Unmounting revokes the object URL
    await act(async () => {
      root.render(null);
    });
    expect(revokeUrl).toHaveBeenCalledWith("blob:teamie-preview-url");
  });

  it("3. updates .jump-latest with unread count and @ mention badge when new messages arrive while scrolled up", async () => {
    await mountConversation(channelOne);

    const scroll = host.querySelector<HTMLElement>(".message-scroll")!;
    Object.defineProperty(scroll, "scrollHeight", {
      configurable: true,
      value: 1000,
    });
    Object.defineProperty(scroll, "clientHeight", {
      configurable: true,
      value: 200,
    });
    Object.defineProperty(scroll, "scrollTop", {
      configurable: true,
      writable: true,
      value: 100,
    });

    await act(async () => {
      scroll.dispatchEvent(new Event("scroll"));
    });

    const jumpBtn = () => host.querySelector<HTMLButtonElement>(".jump-latest");
    expect(jumpBtn()?.textContent).toBe("回到最新消息");
    expect(jumpBtn()?.classList.contains("has-unread")).toBe(false);

    // Arrival of a new message while !atBottom increments unread count
    await act(async () => {
      chatStore.merge([makeMessage(3, peer.id, "New unread message")]);
    });
    expect(jumpBtn()?.textContent).toBe("1 条新消息");
    expect(jumpBtn()?.classList.contains("has-unread")).toBe(true);
    expect(jumpBtn()?.classList.contains("has-mention")).toBe(false);

    // Arrival of a message mentioning owner adds "@ " prefix and .has-mention
    await act(async () => {
      chatStore.merge([
        makeMessage(4, peer.id, "@创建者 请查看", {
          mentions: [{ userId: owner.id, start: 0, end: 4 }],
        }),
      ]);
    });
    expect(jumpBtn()?.textContent).toBe("@ 2 条新消息");
    expect(jumpBtn()?.classList.contains("has-unread")).toBe(true);
    expect(jumpBtn()?.classList.contains("has-mention")).toBe(true);
  });

  it("4. never marks the first message after the unread divider as compact", async () => {
    const m1 = makeMessage(1, peer.id, "first from peer");
    const m2 = makeMessage(2, peer.id, "second from peer within 1s");

    expect(messageGrouping(m2, m1, false).compact).toBe(true);
    expect(messageGrouping(m2, m1, true).compact).toBe(false);

    const unreadConv: Conversation = { ...channelOne, readSeq: 1, lastSeq: 2 };
    bootstrap.conversations = [unreadConv];
    chatStore.clear();
    chatStore.begin(owner.id);
    chatStore.replaceConversations([unreadConv]);
    vi.spyOn(api, "messages").mockResolvedValue({
      messages: [m1, m2],
      hasMore: false,
    });

    await mountConversation(unreadConv);

    const divider = host.querySelector(".unread-divider");
    expect(divider).not.toBeNull();
    const firstUnreadRow = host.querySelector<HTMLElement>("#message-m2");
    expect(firstUnreadRow).not.toBeNull();
    expect(firstUnreadRow?.classList.contains("compact")).toBe(false);
    expect(firstUnreadRow?.querySelector(".message-meta b")?.textContent).toContain(
      peer.displayName,
    );
  });

  it("5. persists recent navigation per user in localStorage, allows Alt+ArrowDown from empty composer, and auto-focuses composer on conversation switch", async () => {
    await mountShell("/app/w/team/c/alerts");

    const storedRaw = localStorage.getItem(`teamie.nav.${owner.id}`);
    expect(storedRaw).toBeTruthy();
    const stored = JSON.parse(storedRaw!);
    expect(stored.lastChatPath).toBe("/app/w/team/c/alerts");
    expect(stored.lastConversationByWorkspace[workspace.id]).toBe("alerts");
    expect(stored.recentConversationIds).toContain("alerts");

    // Composer is auto-focused on non-coarse pointers when switching to a conversation
    const composer = host.querySelector<HTMLTextAreaElement>(
      'textarea[aria-label="消息内容"]',
    )!;
    expect(composer).toBeTruthy();
    expect(document.activeElement).toBe(composer);

    // Alt+ArrowUp inside empty composer switches conversation
    await act(async () => {
      composer.dispatchEvent(
        new KeyboardEvent("keydown", {
          key: "ArrowUp",
          altKey: true,
          bubbles: true,
          cancelable: true,
        }),
      );
    });
    expect(host.querySelector('[data-testid="path"]')?.textContent).toBe(
      "/app/w/team/c/general",
    );

    // Unmount and remount on /app/settings: settings back link restores persisted lastChatPath from localStorage
    await act(async () => root.unmount());
    root = createRoot(host);
    await mountShell("/app/settings");

    const backChatLink = host.querySelector<HTMLAnchorElement>(
      ".settings-header a",
    )!;
    expect(backChatLink).toBeTruthy();
    expect(backChatLink.getAttribute("href")).toBe("/app/w/team/c/general");
    await act(async () => {
      backChatLink.click();
    });
    expect(host.querySelector('[data-testid="path"]')?.textContent).toBe(
      "/app/w/team/c/general",
    );

    // Clearing store on sign-out / account switch removes user's persisted nav state
    act(() => {
      chatStore.clear();
    });
    expect(localStorage.getItem(`teamie.nav.${owner.id}`)).toBeNull();
  });
});
