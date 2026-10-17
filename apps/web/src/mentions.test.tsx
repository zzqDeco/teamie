// @vitest-environment jsdom
import { act, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@teamie/api-client";
import type {
  Bootstrap,
  Conversation,
  Member,
  Mention,
  Message,
  User,
} from "@teamie/api-client";
import { MentionInput, MentionText, trimMentionDraft } from "./mentions";
import { ConversationView } from "./messages";
import { chatStore } from "./store";

const user: User = {
  id: "human-one",
  displayName: "伙伴",
  kind: "human",
  platformRole: "user",
  email: "one@example.test",
  createdAt: "2026-10-17T00:00:00Z",
};
const members: Member[] = [
  {
    userId: user.id,
    displayName: user.displayName,
    kind: "human",
    role: "member",
  },
  { userId: "human-two", displayName: "伙伴", kind: "human", role: "member" },
  { userId: "agent", displayName: "助手", kind: "agent", role: "member" },
  { userId: "outside", displayName: "私密成员", kind: "agent", role: "member" },
];
const conversation: Conversation = {
  id: "group",
  workspaceId: "workspace",
  type: "group",
  name: "协作",
  memberIds: [user.id, "human-two", "agent"],
  lastSeq: 100,
  readSeq: 100,
  updatedAt: user.createdAt,
};
const bootstrap: Bootstrap = {
  user,
  workspaces: [],
  conversations: [conversation],
  capabilities: { "im.message.mentions": true },
  flagsRevision: "mentions",
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
let host: HTMLDivElement, root: Root;
let draft: { body: string; mentions: Mention[] };
const submit = vi.fn();
function Harness() {
  const [value, setValue] = useState({ body: "", mentions: [] as Mention[] });
  useEffect(() => { draft = value; }, [value]);
  return (
    <MentionInput
      aria-label="消息内容"
      value={value.body}
      mentions={value.mentions}
      onChange={(body, mentions) => setValue({ body, mentions })}
      members={members.slice(0, 3)}
      enabled
      onKeyDown={submit}
    />
  );
}
async function render(node: ReactNode) {
  await act(async () => {
    root.render(node);
  });
}
async function type(value: string) {
  const input = host.querySelector("textarea")!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(
      HTMLTextAreaElement.prototype,
      "value",
    )!.set!.call(input, value);
    input.setSelectionRange(value.length, value.length);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function key(value: string, composing = false) {
  await act(async () => {
    host
      .querySelector("textarea")!
      .dispatchEvent(
        new KeyboardEvent("keydown", {
          key: value,
          bubbles: true,
          cancelable: true,
          isComposing: composing,
        }),
      );
  });
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
  vi.stubGlobal("matchMedia", () => ({ matches: false }));
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
    callback(0);
    return 0;
  });
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
  vi.spyOn(api, "messages").mockResolvedValue({ messages: [], hasMore: false });
  vi.spyOn(api, "read").mockResolvedValue({ readSeq: 100 });
  submit.mockClear();
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  chatStore.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("structured mention composing", () => {
  it("selects duplicate human names by identity, then an Agent with a tag, without submitting", async () => {
    await render(<Harness />);
    await type("😀 @");
    expect(host.querySelectorAll('[role="option"]')).toHaveLength(3);
    await key("ArrowDown");
    await key("Enter");
    expect(draft).toEqual({
      body: "😀 @伙伴 ",
      mentions: [{ userId: "human-two", start: 3, end: 6 }],
    });
    expect(submit).not.toHaveBeenCalled();
    await type(draft.body + "@");
    const options = [
      ...host.querySelectorAll<HTMLButtonElement>('[role="option"]'),
    ];
    const agent = options.find((option) =>
      option.textContent?.includes("助手"),
    )!;
    expect(agent.textContent).toContain("Agent");
    await act(async () => agent.click());
    expect(draft.mentions.map((m) => m.userId)).toEqual(["human-two", "agent"]);
    await type(draft.body.replace("@伙伴", "@其他人"));
    expect(draft.mentions.map((m) => m.userId)).toEqual(["agent"]);
  });
  it("does not infer plain @ text or steal IME Enter, and rebases offsets after trimming", async () => {
    await render(<Harness />);
    await type("  @伙伴");
    expect(draft.mentions).toEqual([]);
    await key("Enter", true);
    expect(draft.mentions).toEqual([]);
    await key("Enter");
    expect(trimMentionDraft(draft.body, draft.mentions)).toEqual({
      body: "@伙伴",
      mentions: [{ userId: "human-one", start: 0, end: 3 }],
    });
    await render(
      <MentionText
        body="<img> @伙伴"
        mentions={[{ userId: user.id, start: 6, end: 9 }]}
        userId={user.id}
      />,
    );
    expect(host.querySelector("img")).toBeNull();
    expect(host.querySelector(".mention-self")?.textContent).toBe("@伙伴");
  });
  it("only offers current conversation participants and sends the selected metadata", async () => {
    const send = vi
      .spyOn(api, "send")
      .mockImplementation(async (_id, data) => ({
        message: {
          ...data,
          id: "sent",
          conversationId: conversation.id,
          senderId: user.id,
          senderName: user.displayName,
          senderKind: "human",
          seq: 101,
          createdAt: user.createdAt,
        },
      }));
    await render(
      <ConversationView
        conversation={conversation}
        members={members}
        onMembers={vi.fn()}
        membersOpen={false}
        onMenu={vi.fn()}
      />,
    );
    await type("@");
    expect(host.querySelectorAll('[role="option"]')).toHaveLength(3);
    expect(host.textContent).not.toContain("私密成员");
    await key("ArrowDown");
    await key("ArrowDown");
    await key("Enter");
    await act(async () =>
      host.querySelector<HTMLButtonElement>('[aria-label="发送消息"]')!.click(),
    );
    expect(send).toHaveBeenCalledWith(
      "group",
      expect.objectContaining({
        body: "@助手",
        mentions: [{ userId: "agent", start: 0, end: 3 }],
      }),
    );
  });
});

describe("reply navigation", () => {
  const original: Message = {
    id: "old-message",
    conversationId: "group",
    senderId: "agent",
    senderKind: "agent",
    senderName: "助手",
    clientMessageId: "original",
    body: "原始任务",
    seq: 2,
    createdAt: user.createdAt,
  };
  const reply: Message = {
    ...original,
    id: "latest-message",
    body: "交付结果",
    clientMessageId: "reply",
    seq: 100,
    replyToId: original.id,
  };
  const view = () => (
    <ConversationView
      conversation={conversation}
      members={members}
      onMembers={vi.fn()}
      membersOpen={false}
      onMenu={vi.fn()}
    />
  );
  it("loads unloaded context, focuses the source, preserves retractions and returns to latest", async () => {
    vi.mocked(api.messages).mockResolvedValue({
      messages: [reply],
      hasMore: true,
    });
    const single = vi
      .spyOn(api, "message")
      .mockResolvedValue({ message: original });
    const around = vi
      .spyOn(api, "messagesAround")
      .mockResolvedValue({ messages: [original], hasMore: false });
    await render(view());
    expect(host.querySelector(".message-reply")?.textContent).toContain(
      "较早的消息",
    );
    await act(async () =>
      host.querySelector<HTMLButtonElement>(".message-reply")!.click(),
    );
    expect(single).toHaveBeenCalledWith("group", original.id);
    expect(around).toHaveBeenCalledWith("group", 2);
    expect(document.activeElement?.id).toBe("message-old-message");
    expect(host.textContent).toContain("原始任务");
    expect(chatStore.getSnapshot().messages.group).toHaveLength(1);
    await act(async () =>
      host.querySelector<HTMLButtonElement>(".located-banner button")!.click(),
    );
    expect(host.querySelector(".located-banner")).toBeNull();
    expect(host.textContent).toContain("交付结果");
    expect(host.querySelector(".message-reply")?.textContent).toContain("助手");
    expect(host.querySelector(".message-reply")?.textContent).toContain(
      "原始任务",
    );
    await act(async () =>
      host.querySelector<HTMLButtonElement>(".message-reply")!.click(),
    );
    await act(async () =>
      chatStore.merge([{ ...original, retractedAt: "2026-10-17T00:01:00Z" }]),
    );
    expect(host.textContent).not.toContain("原始任务");
    expect(host.textContent).toContain("这条消息已撤回");
    await act(async () =>
      host.querySelector<HTMLButtonElement>(".located-banner button")!.click(),
    );
    expect(host.querySelector(".located-banner")).toBeNull();
    expect(host.textContent).toContain("交付结果");
    expect(host.querySelector(".message-reply")?.textContent).toContain("助手");
    expect(host.querySelector(".message-reply")?.textContent).toContain(
      "消息已撤回",
    );
  });
  it("renders cached conversation messages immediately without a loading flash while refreshing in the background", async () => {
    chatStore.merge([reply]);
    let resolveLatest!: (value: {
      messages: Message[];
      hasMore: boolean;
    }) => void;
    vi.mocked(api.messages).mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveLatest = resolve;
        }),
    );
    await render(view());
    expect(host.querySelector(".message-loading")).toBeNull();
    expect(host.textContent).toContain("交付结果");
    const newer: Message = {
      ...reply,
      id: "newer-message",
      clientMessageId: "newer",
      body: "后台刷新的新消息",
      seq: 101,
    };
    await act(async () =>
      resolveLatest({ messages: [reply, newer], hasMore: false }),
    );
    expect(host.textContent).toContain("后台刷新的新消息");
  });
  it("retains an attention source as reply context without reading newer cached messages", async () => {
    chatStore.merge([reply]);
    vi.mocked(api.messages).mockResolvedValue({
      messages: [reply],
      hasMore: true,
    });
    vi.mocked(api.read).mockResolvedValue({ readSeq: original.seq });
    vi.spyOn(api, "message").mockResolvedValue({ message: original });
    vi.spyOn(api, "messagesAround").mockResolvedValue({
      messages: [
        original,
        {
          ...original,
          id: "neighbor-message",
          clientMessageId: "neighbor",
          body: "相邻上下文",
          seq: 3,
        },
      ],
      hasMore: false,
    });
    vi.useFakeTimers();
    try {
      await render(
        <ConversationView
          conversation={{ ...conversation, readSeq: 0 }}
          members={members}
          onMembers={vi.fn()}
          membersOpen={false}
          onMenu={vi.fn()}
          initialMessageId={original.id}
        />,
      );
      await act(async () => vi.advanceTimersByTimeAsync(650));
      expect(api.messages).not.toHaveBeenCalled();
      expect(api.read).toHaveBeenCalledExactlyOnceWith("group", original.seq);
      expect(document.activeElement?.id).toBe(`message-${original.id}`);
      await act(async () =>
        host.querySelector<HTMLButtonElement>(".located-banner button")!.click(),
      );
      expect(host.querySelector(".located-banner")).toBeNull();
      expect(host.textContent).toContain("交付结果");
      expect(host.querySelector(".message-reply")?.textContent).toContain(
        "原始任务",
      );
    } finally {
      vi.useRealTimers();
    }
  });
  it.each(["context", "sync"])(
    "does not acknowledge or retain stale reply text when %s retracts a delayed attention source",
    async (source) => {
      chatStore.merge([reply]);
      vi.mocked(api.messages).mockResolvedValue({
        messages: [reply],
        hasMore: true,
      });
      let finishFirst!: (value: { message: Message }) => void;
      vi.spyOn(api, "message").mockImplementation(
        () =>
          new Promise((resolve) => {
            finishFirst = resolve;
          }),
      );
      const retracted = {
        ...original,
        retractedAt: "2026-10-17T00:01:00Z",
      };
      vi.spyOn(api, "messagesAround").mockResolvedValue({
        messages: [source === "context" ? retracted : original],
        hasMore: false,
      });
      await render(
        <ConversationView
          conversation={{ ...conversation, readSeq: 0 }}
          members={members}
          onMembers={vi.fn()}
          membersOpen={false}
          onMenu={vi.fn()}
          initialMessageId={original.id}
        />,
      );
      expect(api.read).not.toHaveBeenCalled();
      await act(async () => {
        if (source === "sync") chatStore.merge([retracted]);
        finishFirst({ message: original });
      });
      expect(api.read).not.toHaveBeenCalled();
      expect(host.textContent).not.toContain("原始任务");
      expect(host.querySelector(`#message-${original.id}`)?.textContent).toContain(
        "这条消息已撤回",
      );
      await act(async () =>
        host.querySelector<HTMLButtonElement>(".located-banner button")!.click(),
      );
      expect(host.querySelector(".message-reply")?.textContent).toContain(
        "消息已撤回",
      );
      expect(host.textContent).not.toContain("原始任务");
      expect(api.read).not.toHaveBeenCalled();
    },
  );
  it("does not render a denied or late response after navigating away", async () => {
    vi.mocked(api.messages).mockResolvedValue({
      messages: [reply],
      hasMore: true,
    });
    const single = vi
      .spyOn(api, "message")
      .mockRejectedValue(new ApiError(404, "not_found", "missing"));
    const around = vi.spyOn(api, "messagesAround");
    await render(view());
    await act(async () =>
      host.querySelector<HTMLButtonElement>(".message-reply")!.click(),
    );
    expect(host.textContent).toContain("原消息已不可访问");
    expect(around).not.toHaveBeenCalled();
    let finish!: (value: { message: Message }) => void;
    single.mockImplementation(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    await act(async () =>
      host.querySelector<HTMLButtonElement>(".message-reply")!.click(),
    );
    await render(<p>已切换会话</p>);
    await act(async () => finish({ message: original }));
    expect(around).not.toHaveBeenCalled();
    expect(host.textContent).toBe("已切换会话");
  });
});
