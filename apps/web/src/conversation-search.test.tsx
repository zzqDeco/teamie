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
  bootstrap.conversations = [conversation];
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
    const unread = { ...conversation, readSeq: 89 };
    bootstrap.conversations = [unread];
    chatStore.clear();
    chatStore.begin(user.id);
    chatStore.replaceConversations([unread]);
    await render(
      <ConversationView
        conversation={unread}
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
  it("lists, filters, downloads, locates, and reconciles forwarded attachments in files mode under target conversation ACLs", async () => {
    const forwardedFileMsg: Message = {
      ...message(70, ""),
      attachments: [],
      forwarded: {
        sourceConversationId: "inaccessible-private-conv",
        sourceMessageId: "source-msg-1",
        senderId: "other-user",
        senderName: "外部门同事",
        senderKind: "human",
        createdAt: "2026-10-16T08:00:00Z",
        body: "",
        attachments: [
          {
            id: "copied-spec-70",
            filename: "Architecture Spec.pdf",
            mediaType: "application/pdf",
            sha256: "fixture-spec",
            sizeBytes: 1024,
          },
          {
            id: "copied-notes-70",
            filename: "Meeting Notes.txt",
            mediaType: "text/plain",
            sha256: "fixture-notes",
            sizeBytes: 512,
          },
        ],
      },
    };
    const olderForwardedFileMsg: Message = {
      ...message(20, "归档附件"),
      forwarded: {
        sourceConversationId: "inaccessible-private-conv",
        sourceMessageId: "source-msg-0",
        senderId: "other-user",
        senderName: "外部门同事",
        senderKind: "human",
        createdAt: "2026-10-15T08:00:00Z",
        body: "附带历史表格",
        attachments: [
          {
            id: "copied-sheet-20",
            filename: "Q4 Budget.csv",
            mediaType: "text/csv",
            sha256: "fixture-csv",
            sizeBytes: 256,
          },
        ],
      },
    };
    vi.mocked(api.searchMessages)
      .mockResolvedValueOnce({
        messages: [fileMessage(65), forwardedFileMsg],
        hasMore: true,
      })
      .mockResolvedValueOnce({
        messages: [olderForwardedFileMsg],
        hasMore: false,
      });

    await render(view());
    await click(".search-modes button:last-child");
    await tick();

    // Empty query in files mode lists both forwarded.attachments and ordinary attachments (newest first)
    expect(host.querySelectorAll(".search-result-list > li")).toHaveLength(2);
    expect(host.querySelectorAll(".message-attachments li")).toHaveLength(4);
    expect(host.textContent).toContain("Architecture Spec.pdf");
    expect(host.textContent).toContain("Report 10%_.txt");

    // Pagination via beforeSeq loads older forwarded file message without polluting chatStore or marking read
    await click(".search-more");
    expect(api.searchMessages).toHaveBeenLastCalledWith(
      "channel",
      { q: "", content: "files", beforeSeq: 65 },
      expect.any(AbortSignal),
    );
    expect(host.querySelectorAll(".search-result-list > li")).toHaveLength(3);
    expect(host.textContent).toContain("Q4 Budget.csv");
    expect(chatStore.getSnapshot().messages.channel).toBeUndefined();
    expect(api.read).not.toHaveBeenCalled();

    // Case-insensitive filename search matches forwarded.attachments and filters out non-matching attachments
    vi.mocked(api.searchMessages).mockResolvedValue({
      messages: [forwardedFileMsg],
      hasMore: false,
    });
    await type("spec");
    await tick();
    expect(api.searchMessages).toHaveBeenLastCalledWith(
      "channel",
      { q: "spec", content: "files" },
      expect.any(AbortSignal),
    );
    expect(host.querySelectorAll(".message-attachments li")).toHaveLength(1);
    expect(host.textContent).toContain("Architecture Spec.pdf");
    expect(host.textContent).not.toContain("Meeting Notes.txt");

    // Downloading uses target conversation ("channel") + copied attachment ID, never sourceConversationId
    const download = vi
      .spyOn(api, "attachmentContent")
      .mockResolvedValue(new Blob([new Uint8Array([1, 2, 3])]));
    const create = vi.fn(() => "blob:copied-spec");
    const revoke = vi.fn();
    vi.stubGlobal(
      "URL",
      class extends URL {
        static createObjectURL = create;
        static revokeObjectURL = revoke;
      },
    );
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    await click('[aria-label="下载文件 Architecture Spec.pdf"]');
    expect(download).toHaveBeenCalledWith("channel", "copied-spec-70");
    await tick(1100);

    // Clicking 查看消息 locates the forwarded message in target conversation
    await click(".search-source");
    expect(onLocate).toHaveBeenCalledWith("message-70");

    // Live retraction immediately removes the forwarded file result
    await act(async () => {
      chatStore.merge([
        { ...forwardedFileMsg, retractedAt: "2026-10-17T00:05:00Z" },
      ]);
    });
    expect(host.querySelectorAll(".search-result-list > li")).toHaveLength(0);
    expect(host.textContent).toContain("没有找到文件");
  });
  it("matches forwarded snapshot bodies and comments, reconciles comment edits and retractions, and supports locating and pagination", async () => {
    const commentlessForward: Message = {
      ...message(40, ""),
      forwarded: {
        sourceConversationId: "private-source-conv",
        sourceMessageId: "src-40",
        senderId: "u-author",
        senderName: "陈工",
        senderKind: "human",
        createdAt: "2026-10-16T09:00:00Z",
        body: "快照正文 SnapshotKey <script>alert(1)</script>",
      },
    };
    const commentedSnapshotForward: Message = {
      ...message(50, "请大家评估这条方案"),
      forwarded: {
        sourceConversationId: "private-source-conv",
        sourceMessageId: "src-50",
        senderId: "u-author",
        senderName: "陈工",
        senderKind: "human",
        createdAt: "2026-10-16T10:00:00Z",
        body: "核心结论包含 snapshotkey 指标",
      },
    };
    const commentOnlyForward: Message = {
      ...message(60, "转发说明里提到 SnapshotKey"),
      forwarded: {
        sourceConversationId: "private-source-conv",
        sourceMessageId: "src-60",
        senderId: "u-author",
        senderName: "陈工",
        senderKind: "human",
        createdAt: "2026-10-16T11:00:00Z",
        body: "这段原消息不含搜索词",
      },
    };
    const olderForward: Message = {
      ...message(12, ""),
      forwarded: {
        sourceConversationId: "private-source-conv",
        sourceMessageId: "src-12",
        senderId: "u-old",
        senderName: "王敏",
        senderKind: "human",
        createdAt: "2026-10-15T11:00:00Z",
        body: "更早期的 SnapshotKey 会议纪要",
      },
    };

    vi.mocked(api.searchMessages)
      .mockResolvedValueOnce({
        messages: [
          commentlessForward,
          commentedSnapshotForward,
          commentOnlyForward,
        ],
        hasMore: true,
      })
      .mockResolvedValueOnce({
        messages: [olderForward],
        hasMore: false,
      });

    await render(view());
    await type("SnapshotKey");
    await tick();

    // Newest first: seq 60, seq 50, seq 40
    const items = host.querySelectorAll(".search-result-list > li");
    expect(items).toHaveLength(3);
    expect(host.querySelectorAll(".search-message-result")).toHaveLength(3);
    expect(host.querySelector("script")).toBeNull();
    expect(items[2].textContent).toContain("转发自 陈工：");
    expect(items[2].textContent).toContain("<script>alert(1)</script>");
    expect(items[2].querySelector(".search-comment-snippet")).toBeNull();
    expect(items[1].textContent).toContain("请大家评估这条方案");
    expect(items[1].textContent).toContain("转发自 陈工：");
    const marks = Array.from(host.querySelectorAll("mark")).map(
      (el) => el.textContent,
    );
    expect(marks).toEqual(["SnapshotKey", "snapshotkey", "SnapshotKey"]);

    // Clicking result button locates target message ID in the current conversation
    const buttons = host.querySelectorAll<HTMLButtonElement>(
      ".search-message-result",
    );
    await act(async () => {
      buttons[2].click();
    });
    expect(onLocate).toHaveBeenCalledWith("message-40");

    // Pagination loads older forwarded match without touching timeline or read cursor
    expect(chatStore.getSnapshot().messages.channel).toBeUndefined();
    await click(".search-more");
    expect(api.searchMessages).toHaveBeenLastCalledWith(
      "channel",
      { q: "SnapshotKey", beforeSeq: 40 },
      expect.any(AbortSignal),
    );
    expect(host.querySelectorAll(".search-result-list > li")).toHaveLength(4);
    expect(host.textContent).toContain("转发自 王敏：");
    expect(chatStore.getSnapshot().messages.channel).toBeUndefined();
    expect(api.read).not.toHaveBeenCalled();

    // Editing comment:
    // - commentOnlyForward (seq 60) loses keyword -> drops out because forwarded.body doesn't match
    // - commentedSnapshotForward (seq 50) updates comment -> stays because forwarded.body still matches
    await act(async () => {
      chatStore.merge([
        {
          ...commentOnlyForward,
          body: "已移除该词的转发说明",
          editedAt: "2026-10-17T00:02:00Z",
        },
        {
          ...commentedSnapshotForward,
          body: "更新后的转发留言",
          editedAt: "2026-10-17T00:02:30Z",
        },
      ]);
    });
    expect(host.querySelectorAll(".search-result-list > li")).toHaveLength(3);
    expect(host.textContent).not.toContain("已移除该词的转发说明");
    expect(host.textContent).toContain("更新后的转发留言");

    // Retracting commentlessForward (seq 40) removes it immediately
    await act(async () => {
      chatStore.merge([
        {
          ...commentlessForward,
          body: "",
          forwarded: undefined,
          retractedAt: "2026-10-17T00:03:00Z",
        },
      ]);
    });
    expect(host.querySelectorAll(".search-result-list > li")).toHaveLength(2);
    expect(host.textContent).not.toContain("<script>alert(1)</script>");
  });
});

describe("conversation search readability and keyboard (PR-3)", () => {
  it("shows a Markdown-free snippet centred on a match deep in a long message, with a short aria-label", async () => {
    const lines = Array.from({ length: 12 }, (_, i) => `第 ${i + 1} 行 **很长的** 说明文字，用来把命中词推到很后面。`);
    lines.splice(9, 0, "这里有 [关键词](https://a.example) 出现");
    vi.mocked(api.searchMessages).mockResolvedValue({ messages: [message(5, lines.join("\n"))], hasMore: false });
    await render(view());
    await type("关键词");
    await tick();
    const result = host.querySelector<HTMLButtonElement>(".search-message-result")!;
    expect(result.querySelector("mark")?.textContent).toBe("关键词");
    expect(result.textContent).not.toContain("**");
    expect(result.textContent).not.toContain("](");
    expect(result.textContent!.startsWith("…")).toBe(true);
    expect(Array.from(result.textContent!.replace(/…/g, "")).length).toBeLessThanOrEqual(120);
    expect(result.getAttribute("aria-label")).toMatch(/^小助手 · .+\d{2}:\d{2}$/);
    expect(result.getAttribute("aria-label")).not.toContain("关键词");
    expect(host.querySelector(".search-result-meta time")?.getAttribute("title")).toMatch(/\d{4}年/);
  });

  it("moves between results with ↑/↓ from the input and activates with Enter", async () => {
    vi.mocked(api.searchMessages).mockResolvedValue({
      messages: [message(1, "needle one"), message(2, "needle two"), message(3, "needle three")],
      hasMore: false,
    });
    await render(view());
    await type("needle");
    await tick();
    const input = host.querySelector<HTMLInputElement>('input[type="search"]')!;
    const buttons = [...host.querySelectorAll<HTMLButtonElement>(".search-message-result")];
    expect(buttons).toHaveLength(3);
    const key = async (target: Element, k: string) =>
      act(async () => { target.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true })); });
    await key(input, "ArrowDown");
    expect(document.activeElement).toBe(buttons[0]);
    await key(buttons[0], "ArrowDown");
    expect(document.activeElement).toBe(buttons[1]);
    await key(buttons[1], "End");
    expect(document.activeElement).toBe(buttons[2]);
    await key(buttons[2], "Home");
    expect(document.activeElement).toBe(buttons[0]);
    await key(buttons[0], "ArrowUp");
    expect(document.activeElement).toBe(input);
    await key(input, "ArrowDown");
    await act(async () => (document.activeElement as HTMLButtonElement).click());
    expect(onLocate).toHaveBeenCalledWith("message-3");
  });

  it("restores the last keyword for the same account and conversation only", async () => {
    vi.mocked(api.searchMessages).mockResolvedValue({ messages: [message(1, "保留 关键词")], hasMore: false });
    await render(view());
    await type("关键词");
    await tick();
    await render(<div />);
    await render(view());
    await tick();
    const input = host.querySelector<HTMLInputElement>('input[type="search"]')!;
    expect(input.value).toBe("关键词");
    expect(host.querySelector("mark")?.textContent).toBe("关键词");
    await render(view("other"));
    expect(host.querySelector<HTMLInputElement>('input[type="search"]')!.value).toBe("");
    await render(<div />);
    await act(async () => chatStore.begin("another-account"));
    await render(view());
    expect(host.querySelector<HTMLInputElement>('input[type="search"]')!.value).toBe("");
  });
});
