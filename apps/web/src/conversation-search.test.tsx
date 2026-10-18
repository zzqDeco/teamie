// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@teamie/api-client";
import type {
  Bootstrap,
  Conversation,
  Message,
  MessagePage,
  User,
} from "@teamie/api-client";
import { ConversationSearch } from "./conversation-search";
import { ConversationView } from "./messages";
import { chatStore } from "./store";

const user: User = {
  id: "human",
  email: "member@example.test",
  displayName: "林舟",
  kind: "human",
  platformRole: "user",
  createdAt: "2026-10-17T00:00:00Z",
};
const conversation: Conversation = {
  id: "channel",
  workspaceId: "workspace",
  name: "项目协作",
  type: "channel",
  memberIds: [user.id, "agent"],
  lastSeq: 90,
  readSeq: 90,
  updatedAt: user.createdAt,
};
const bootstrap: Bootstrap = {
  user,
  workspaces: [],
  conversations: [conversation],
  capabilities: {},
  flagsRevision: "fixture",
  syncCursor: "0",
};
const sendTyping = vi.fn();
vi.mock("./auth", () => ({ useAuth: () => ({ user }) }));
vi.mock("./chat", async (original) => ({
  ...(await original<typeof import("./chat")>()),
  useChat: () => ({
    data: bootstrap,
    syncError: "",
    sync: vi.fn(),
    sendTyping,
    typing: {},
  }),
}));
const message = (
  seq: number,
  body = "Needle 10%_ <img src=x onerror=alert(1)>",
): Message => ({
  id: `message-${seq}`,
  clientMessageId: `client-${seq}`,
  conversationId: "channel",
  senderId: "agent",
  senderName: "小助手",
  senderKind: "agent",
  seq,
  body,
  createdAt: user.createdAt,
});
const fileMessage = (seq: number): Message => ({
  ...message(seq, "交付完成"),
  attachments: [
    {
      id: `report-${seq}`,
      filename: "Report 10%_.txt",
      mediaType: "text/plain",
      sha256: "fixture",
      sizeBytes: 3,
    },
    {
      id: `other-${seq}`,
      filename: "其他文件.pdf",
      mediaType: "application/pdf",
      sha256: "fixture",
      sizeBytes: 7,
    },
  ],
});
let host: HTMLDivElement, root: Root;
const onClose = vi.fn(),
  onLocate = vi.fn();
const view = (id = "channel") => (
  <ConversationSearch
    key={id}
    conversationId={id}
    onClose={onClose}
    onLocate={onLocate}
  />
);
async function render(node: ReactNode) {
  await act(async () => {
    root.render(node);
  });
}
async function tick(ms = 300) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}
async function type(value: string) {
  await act(async () => {
    const input = host.querySelector<HTMLInputElement>('input[type="search"]')!;
    Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      "value",
    )!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function click(selector: string) {
  await act(async () => {
    host.querySelector<HTMLButtonElement>(selector)!.click();
  });
}
const deferred = () => {
  let resolve!: (page: MessagePage) => void;
  const promise = new Promise<MessagePage>((done) => {
    resolve = done;
  });
  return { promise, resolve };
};
beforeEach(() => {
  vi.useFakeTimers();
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal("matchMedia", () => ({ matches: false }));
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      disconnect() {}
    },
  );
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) =>
    setTimeout(() => callback(16), 16),
  );
  Object.defineProperty(HTMLElement.prototype, "scrollTo", {
    configurable: true,
    value: vi.fn(),
  });
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
    configurable: true,
    value: vi.fn(),
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  chatStore.begin(user.id);
  chatStore.replaceConversations([conversation]);
  vi.spyOn(api, "searchMessages").mockResolvedValue({
    messages: [],
    hasMore: false,
  });
  vi.spyOn(api, "messages").mockResolvedValue({
    messages: [message(90, "最新消息")],
    hasMore: true,
  });
  vi.spyOn(api, "read").mockResolvedValue({ readSeq: 90 });
  onClose.mockClear();
  onLocate.mockClear();
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  chatStore.clear();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("conversation search", () => {
  it("searches literal text, highlights escaped content, and keeps result pages outside the timeline", async () => {
    vi.mocked(api.searchMessages).mockResolvedValue({
      messages: [message(2)],
      hasMore: false,
    });
    await render(view());
    await tick();
    expect(api.searchMessages).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(host.querySelector("input"));
    await type("  10%_  ");
    await tick();
    expect(api.searchMessages).toHaveBeenCalledWith(
      "channel",
      { q: "10%_" },
      expect.any(AbortSignal),
    );
    expect(host.querySelector("mark")?.textContent).toBe("10%_");
    expect(host.querySelector("img")).toBeNull();
    expect(host.textContent).toContain("<img src=x onerror=alert(1)>");
    expect(host.textContent).toContain("Agent");
    expect(chatStore.getSnapshot().messages.channel).toBeUndefined();
    await click(".search-message-result");
    expect(onLocate).toHaveBeenCalledWith("message-2");
    expect(api.read).not.toHaveBeenCalled();
  });
  it("browses files without a keyword, pages by source message, filters filenames, and downloads authenticated bytes", async () => {
    vi.mocked(api.searchMessages)
      .mockResolvedValueOnce({ messages: [fileMessage(65)], hasMore: true })
      .mockResolvedValueOnce({ messages: [fileMessage(2)], hasMore: false });
    await render(view());
    await click(".search-modes button:last-child");
    await tick();
    expect(api.searchMessages).toHaveBeenLastCalledWith(
      "channel",
      { q: "", content: "files" },
      expect.any(AbortSignal),
    );
    expect(host.querySelectorAll(".message-attachments li")).toHaveLength(2);
    await click(".search-more");
    expect(api.searchMessages).toHaveBeenLastCalledWith(
      "channel",
      { q: "", content: "files", beforeSeq: 65 },
      expect.any(AbortSignal),
    );
    expect(host.querySelectorAll(".search-result-list > li")).toHaveLength(2);
    vi.mocked(api.searchMessages).mockResolvedValue({
      messages: [fileMessage(2)],
      hasMore: false,
    });
    await type("REPORT");
    await tick();
    expect(host.querySelectorAll(".message-attachments li")).toHaveLength(1);
    expect(host.textContent).not.toContain("其他文件.pdf");
    const download = vi
      .spyOn(api, "attachmentContent")
      .mockResolvedValue(new Blob([new Uint8Array([0, 255, 1])]));
    const create = vi.fn(() => "blob:search-file");
    const revoke = vi.fn();
    vi.stubGlobal(
      "URL",
      class extends URL {
        static createObjectURL = create;
        static revokeObjectURL = revoke;
      },
    );
    const save = vi
      .spyOn(HTMLAnchorElement.prototype, "click")
      .mockImplementation(() => {});
    await click('[aria-label="下载文件 Report 10%_.txt"]');
    expect(download).toHaveBeenCalledWith("channel", "report-2");
    expect(create).toHaveBeenCalledOnce();
    expect(save).toHaveBeenCalledOnce();
    await tick(1100);
    expect(revoke).toHaveBeenCalledWith("blob:search-file");
    await click(".search-source");
    expect(onLocate).toHaveBeenCalledWith("message-2");
  });
  it("removes edited and retracted matches using sync, including a retraction before a stale search response", async () => {
    const pending = deferred();
    vi.mocked(api.searchMessages).mockReturnValue(pending.promise);
    await render(view());
    await type("needle");
    await tick();
    await act(async () => {
      chatStore.merge([{ ...message(2), retractedAt: "2026-10-17T00:01:00Z" }]);
      pending.resolve({ messages: [message(2), message(3)], hasMore: false });
    });
    expect(host.querySelectorAll(".search-result-list > li")).toHaveLength(1);
    await act(async () => {
      chatStore.merge([
        { ...message(3, "已经改成其他内容"), editedAt: "2026-10-17T00:02:00Z" },
      ]);
    });
    expect(host.querySelectorAll(".search-result-list > li")).toHaveLength(0);
    expect(host.textContent).not.toContain("<img");
    expect(host.textContent).toContain("没有找到消息");
  });
  it("aborts old queries and ignores late results after query, mode and conversation changes", async () => {
    const first = deferred();
    vi.mocked(api.searchMessages).mockReturnValueOnce(first.promise);
    await render(view());
    await type("old");
    await tick();
    const oldSignal = vi.mocked(api.searchMessages).mock.calls[0][2]!;
    vi.mocked(api.searchMessages).mockResolvedValue({
      messages: [message(4, "new result")],
      hasMore: false,
    });
    await type("new");
    await tick();
    expect(oldSignal.aborted).toBe(true);
    await act(async () =>
      first.resolve({
        messages: [message(2, "old private result")],
        hasMore: false,
      }),
    );
    expect(host.textContent).toContain("new result");
    expect(host.textContent).not.toContain("old private result");
    const second = deferred();
    vi.mocked(api.searchMessages).mockReturnValueOnce(second.promise);
    await type("report");
    await tick();
    vi.mocked(api.searchMessages).mockResolvedValue({
      messages: [fileMessage(5)],
      hasMore: false,
    });
    await click(".search-modes button:last-child");
    await tick();
    await act(async () =>
      second.resolve({
        messages: [message(2, "report private text")],
        hasMore: false,
      }),
    );
    expect(host.textContent).not.toContain("report private text");
    const third = deferred();
    vi.mocked(api.searchMessages).mockReturnValueOnce(third.promise);
    await type("late");
    await tick();
    await render(view("another-channel"));
    await act(async () =>
      third.resolve({
        messages: [message(2, "late old conversation")],
        hasMore: false,
      }),
    );
    expect(host.textContent).not.toContain("late old conversation");
  });
  it("discards outstanding search and pagination across an account change or close", async () => {
    const pending = deferred();
    vi.mocked(api.searchMessages).mockReturnValueOnce(pending.promise);
    await render(view());
    await type("secret");
    await tick();
    vi.mocked(api.searchMessages).mockResolvedValue({
      messages: [],
      hasMore: false,
    });
    await act(async () => {
      chatStore.begin("another-human");
      pending.resolve({
        messages: [message(2, "secret old account")],
        hasMore: false,
      });
    });
    await tick();
    expect(host.textContent).not.toContain("secret old account");
    vi.mocked(api.searchMessages).mockResolvedValueOnce({
      messages: [message(10, "secret new account")],
      hasMore: true,
    });
    await click('[aria-label="搜索"]');
    await tick();
    const next = deferred();
    vi.mocked(api.searchMessages).mockReturnValueOnce(next.promise);
    await click(".search-more");
    const signal = vi.mocked(api.searchMessages).mock.calls.at(-1)![2]!;
    await render(<p>已关闭搜索</p>);
    expect(signal.aborted).toBe(true);
    await act(async () =>
      next.resolve({
        messages: [message(2, "secret late page")],
        hasMore: false,
      }),
    );
    expect(host.textContent).toBe("已关闭搜索");
  });
  it("handles authorization failures, retries and rejects overlong Unicode queries before sending", async () => {
    vi.mocked(api.searchMessages).mockRejectedValueOnce(
      new ApiError(404, "not_found", "Conversation not found"),
    );
    await render(view());
    await type("报告");
    await tick();
    expect(host.querySelector('[role="alert"]')?.textContent).toBe(
      "内容不存在或已无法访问。",
    );
    expect(host.querySelectorAll(".search-result-list > li")).toHaveLength(0);
    vi.mocked(api.searchMessages).mockResolvedValue({
      messages: [message(2, "报告")],
      hasMore: false,
    });
    await click(".search-more");
    await tick();
    expect(host.querySelectorAll(".search-result-list > li")).toHaveLength(1);
    const count = vi.mocked(api.searchMessages).mock.calls.length;
    await type("😀".repeat(201));
    await tick();
    expect(host.textContent).toContain("关键词最多 200 个字符");
    expect(api.searchMessages).toHaveBeenCalledTimes(count);
  });
  it("locates an unloaded search result through current authorized history and restores focus after the dialog closes", async () => {
    const original = message(2, "old requested file");
    vi.mocked(api.searchMessages).mockResolvedValue({
      messages: [original],
      hasMore: false,
    });
    const single = vi
      .spyOn(api, "message")
      .mockResolvedValue({ message: original });
    const around = vi
      .spyOn(api, "messagesAround")
      .mockResolvedValue({ messages: [original], hasMore: false });
    await render(
      <ConversationView
        conversation={conversation}
        members={[]}
        onMembers={vi.fn()}
        membersOpen={false}
        onMenu={vi.fn()}
      />,
    );
    await click('[aria-label="搜索会话"]');
    await type("old");
    await tick();
    await click(".search-message-result");
    await tick(30);
    expect(host.querySelector('[role="dialog"]')).toBeNull();
    expect(single).toHaveBeenCalledWith("channel", original.id);
    expect(around).toHaveBeenCalledWith("channel", 2);
    expect(document.activeElement?.id).toBe("message-message-2");
    expect(chatStore.getSnapshot().messages.channel).toHaveLength(1);
    expect(api.read).not.toHaveBeenCalled();
  });
  it("pauses timeline read acknowledgements while search covers unread messages", async () => {
    await render(
      <ConversationView
        conversation={{ ...conversation, readSeq: 0 }}
        members={[]}
        onMembers={vi.fn()}
        membersOpen={false}
        onMenu={vi.fn()}
      />,
    );
    await click('[aria-label="搜索会话"]');
    await tick(1100);
    expect(api.read).not.toHaveBeenCalled();
    await click('[aria-label="关闭弹窗"]');
    await tick(650);
    expect(api.read).toHaveBeenCalledWith("channel", 90);
  });
});
