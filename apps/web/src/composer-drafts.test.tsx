// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@teamie/api-client";
import type { Bootstrap, Conversation, Member, Message, User } from "@teamie/api-client";
import { readComposerDraft } from "./composer-drafts";
import { ConversationView } from "./messages";
import { chatStore } from "./store";
import type { LocalMessage } from "./store";

const user: User = {
  id: "human", kind: "human", platformRole: "user", displayName: "Human",
  email: "draft@example.invalid", createdAt: "2026-10-17T00:00:00Z",
};
const conversation: Conversation = {
  id: "current", workspaceId: "team", type: "group", name: "Current",
  memberIds: [user.id, "peer"], lastSeq: 100, readSeq: 100, updatedAt: user.createdAt,
};
const otherConversation: Conversation = { ...conversation, id: "other", name: "Other" };
const members: Member[] = [
  { userId: "peer", displayName: "Peer", kind: "human", role: "member" },
];
const source: Message = {
  id: "source", conversationId: conversation.id, senderId: "peer", senderName: "Peer",
  senderKind: "human", clientMessageId: "source-request", seq: 2,
  body: "ORIGINAL_REPLY_BODY", createdAt: user.createdAt,
};
let activeUser: User;
let bootstrap: Bootstrap;
let latest: Record<string, Message[]>;
const sendTyping = vi.fn();
vi.mock("./auth", () => ({ useAuth: () => ({ user: activeUser }) }));
vi.mock("./chat", async (original) => ({
  ...(await original<typeof import("./chat")>()),
  useChat: () => ({ data: bootstrap, syncError: "", sync: vi.fn(), sendTyping, typing: {} }),
}));

let host: HTMLDivElement, root: Root;
const noop = () => {};
const preview = () => host.querySelector(".reply-preview");
const input = () => host.querySelector<HTMLTextAreaElement>("textarea")!;
async function show(current = conversation, initialMessageId = "", keyed = true) {
  await act(async () => {
    root.render(<ConversationView
      key={keyed ? `${current.id}:${initialMessageId}` : "reused-view"}
      conversation={current} initialMessageId={initialMessageId} members={members}
      onMembers={noop} membersOpen={false} onMenu={noop}
    />);
  });
}
async function type(value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input(), value);
    input().setSelectionRange(value.length, value.length);
    input().dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function selectReply() {
  await act(async () => {
    host.querySelector<HTMLButtonElement>('[aria-label="回复 Peer"]')!.click();
  });
}
async function selectContextReply() {
  latest[conversation.id] = [];
  await show(conversation, source.id);
  await selectReply();
  await type("Unsent draft");
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date(user.createdAt));
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
  vi.stubGlobal("matchMedia", () => ({ matches: false }));
  Object.defineProperty(HTMLElement.prototype, "scrollTo", { configurable: true, value: vi.fn() });
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: vi.fn() });
  activeUser = user;
  bootstrap = {
    user, workspaces: [], conversations: [conversation, otherConversation],
    capabilities: { "im.message.mentions": true }, flagsRevision: "drafts", syncCursor: "0",
  };
  localStorage.clear();
  chatStore.begin(user.id);
  chatStore.replaceConversations(bootstrap.conversations);
  latest = { [conversation.id]: [source], [otherConversation.id]: [] };
  vi.spyOn(api, "messages").mockImplementation(async (id) => ({ messages: latest[id] || [], hasMore: false }));
  vi.spyOn(api, "message").mockResolvedValue({ message: source });
  vi.spyOn(api, "messagesAround").mockResolvedValue({ messages: [source], hasMore: false });
  vi.spyOn(api, "read").mockResolvedValue({ readSeq: 100 });
  vi.spyOn(api, "send").mockImplementation(async (id, draft) => ({
    message: { ...source, ...draft, id: "sent", conversationId: id, senderId: activeUser.id, seq: 101 },
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  chatStore.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  delete (HTMLElement.prototype as Partial<HTMLElement>).scrollTo;
  delete (HTMLElement.prototype as Partial<HTMLElement>).scrollIntoView;
});

describe("ID-only composer replies", () => {
  it.each(["retracted", "edited"])("restores the current %s target after an update while another conversation is open", async (change) => {
    await show();
    await selectReply();
    await type("Unsent draft");
    await show(otherConversation);
    const updated = change === "retracted"
      ? { ...source, body: "", retractedAt: "2026-10-17T00:01:00Z" }
      : { ...source, body: "EDITED_REPLY_BODY", editedAt: "2026-10-17T00:01:00Z" };
    // Even a stale latest-page response cannot replace the durable newer version.
    await act(async () => { chatStore.merge([updated]); });
    await show();
    expect(input().value).toBe("Unsent draft");
    expect(preview()?.textContent).not.toContain(source.body);
    expect(preview()?.textContent).toContain(change === "retracted" ? "消息已撤回" : updated.body);
    expect(host.querySelector(".message-text")?.textContent).toContain(change === "retracted" ? "这条消息已撤回" : updated.body);
  });

  it("preserves body, selected mentions and reply ID across same-conversation source-route remounts", async () => {
    await show(conversation, source.id);
    await selectReply();
    await type("@Pe");
    await act(async () => { host.querySelector<HTMLButtonElement>('[role="option"]')!.click(); });
    await type("@Peer follow up");
    await show();
    expect(input().value).toBe("@Peer follow up");
    expect(preview()?.textContent).toContain(source.body);
    await act(async () => { host.querySelector<HTMLButtonElement>('[aria-label="发送消息"]')!.click(); });
    expect(api.send).toHaveBeenCalledWith(conversation.id, expect.objectContaining({
      body: "@Peer follow up", replyToId: source.id,
      mentions: [{ userId: "peer", start: 0, end: 5 }],
    }));
    expect(input().value).toBe("");
    expect(preview()).toBeNull();
  });

  it("reads a missing target without adding reply-only bodies to the normal timeline or localStorage", async () => {
    await selectContextReply();
    await show(otherConversation);
    const current = { ...source, body: "CURRENT_FETCHED_REPLY", editedAt: "2026-10-17T00:01:00Z" };
    vi.mocked(api.message).mockResolvedValue({ message: current });
    await show();
    expect(api.message).toHaveBeenLastCalledWith(conversation.id, source.id, expect.any(AbortSignal));
    expect(preview()?.textContent).toContain(current.body);
    expect(chatStore.getSnapshot().messages[conversation.id] || []).toEqual([]);
    // Persistence is deferred; write the pending snapshot before inspecting it.
    chatStore.flush();
    expect(localStorage.getItem(`teamie.sync.v1.${user.id}`)).not.toContain(current.body);
    expect(host.querySelectorAll(".message")).toHaveLength(0);
    await act(async () => { chatStore.merge([{ ...current, body: "", retractedAt: "2026-10-17T00:02:00Z" }]); });
    expect(preview()?.textContent).toContain("消息已撤回");
    expect(preview()?.textContent).not.toContain(current.body);
  });

  it("discards an older missing-target response when sync supplies an edited version during the read", async () => {
    await selectContextReply();
    await show(otherConversation);
    const pending = deferred<{ message: Message }>();
    vi.mocked(api.message).mockReturnValue(pending.promise);
    await show();
    expect(preview()?.textContent).toContain("正在加载原消息");
    const signal = vi.mocked(api.message).mock.lastCall![2]!;
    await act(async () => { chatStore.merge([{ ...source, body: "SYNC_EDIT", editedAt: "2026-10-17T00:01:00Z" }]); });
    expect(signal.aborted).toBe(false);
    await act(async () => { pending.resolve({ message: source }); });
    expect(preview()?.textContent).toContain("SYNC_EDIT");
    expect(preview()?.textContent).not.toContain(source.body);
  });

  it.each(["retracted", "edited"])("keeps a newer %s single-message version when an older latest page arrives afterward", async (change) => {
    await selectContextReply();
    await show(otherConversation);
    const pendingLatest = deferred<{ messages: Message[]; hasMore: boolean }>();
    vi.mocked(api.messages).mockImplementation((id) => id === conversation.id
      ? pendingLatest.promise
      : Promise.resolve({ messages: [], hasMore: false }));
    const updated = change === "retracted"
      ? { ...source, body: "", retractedAt: "2026-10-17T00:01:00Z" }
      : { ...source, body: "LATEST_REPLY_EDIT", editedAt: "2026-10-17T00:01:00Z" };
    vi.mocked(api.message).mockResolvedValue({ message: updated });
    await show();
    expect(preview()?.textContent).toContain(change === "retracted" ? "消息已撤回" : updated.body);
    await act(async () => { pendingLatest.resolve({ messages: [source], hasMore: false }); });
    // Effects have now settled; an ordinary composer render must retain the
    // accepted newer version after the older page becomes available.
    await type("Unsent draft after latest load");
    expect(preview()?.textContent).toContain(change === "retracted" ? "消息已撤回" : updated.body);
    expect(preview()?.textContent).not.toContain(source.body);
    const reads = vi.mocked(api.message).mock.calls.length;
    activeUser = { ...user, displayName: "Updated profile" };
    bootstrap = { ...bootstrap, user: activeUser };
    await show();
    await type("Unsent draft after profile refresh");
    expect(api.message).toHaveBeenCalledTimes(reads);
    expect(preview()?.textContent).toContain(change === "retracted" ? "消息已撤回" : updated.body);
    expect(preview()?.textContent).not.toContain(source.body);
  });

  it("does not redisplay an earlier fetched body when the same component revisits a conversation", async () => {
    await selectContextReply();
    await show(otherConversation, "", false);
    await show(conversation, "", false);
    expect(preview()?.textContent).toContain(source.body);
    await show(otherConversation, "", false);
    const pending = deferred<{ message: Message }>();
    vi.mocked(api.message).mockReturnValue(pending.promise);
    await show(conversation, "", false);
    expect(preview()?.textContent).toContain("正在加载原消息");
    expect(preview()?.textContent).not.toContain(source.body);
    await act(async () => { pending.resolve({ message: { ...source, body: "", retractedAt: "2026-10-17T00:01:00Z" } }); });
    expect(preview()?.textContent).toContain("消息已撤回");
  });

  it.each(["navigation", "cancel", "account reset", "permission removal"])("cancels a missing-target read on %s and ignores a late response", async (change) => {
    await selectContextReply();
    await show(otherConversation);
    const pending = deferred<{ message: Message }>();
    vi.mocked(api.message).mockReturnValue(pending.promise);
    await show();
    const signal = vi.mocked(api.message).mock.lastCall![2]!;
    if (change === "navigation") await show(otherConversation);
    else if (change === "cancel") {
      await act(async () => { host.querySelector<HTMLButtonElement>('[aria-label="取消回复"]')!.click(); });
      expect(input().value).toBe("Unsent draft");
    } else if (change === "account reset") {
      await act(async () => {
        activeUser = { ...user, id: "another-account" };
        bootstrap = { ...bootstrap, user: activeUser };
        chatStore.begin(activeUser.id);
        chatStore.replaceConversations(bootstrap.conversations);
      });
    } else {
      await act(async () => { chatStore.replaceConversations([otherConversation]); });
    }
    expect(signal.aborted).toBe(true);
    await act(async () => { pending.resolve({ message: source }); });
    expect(preview()).toBeNull();
    expect(host.textContent).not.toContain(source.body);
    if (change === "permission removal" || change === "account reset") {
      await act(async () => {
        activeUser = user;
        bootstrap = { ...bootstrap, user };
        chatStore.begin(user.id);
        chatStore.replaceConversations(bootstrap.conversations);
      });
      await show();
      expect(input().value).toBe("");
      expect(preview()).toBeNull();
    }
  });

  it("clears a definitively inaccessible reply reference while retaining text and selected mentions", async () => {
    await selectContextReply();
    await type("@Pe");
    await act(async () => { host.querySelector<HTMLButtonElement>('[role="option"]')!.click(); });
    await type("@Peer follow up");
    await show(otherConversation);
    vi.mocked(api.message).mockRejectedValue(new ApiError(404, "not_found", "Not accessible"));
    await show();
    expect(preview()).toBeNull();
    expect(input().value).toBe("@Peer follow up");
    await act(async () => { host.querySelector<HTMLButtonElement>('[aria-label="发送消息"]')!.click(); });
    expect(api.send).toHaveBeenCalledWith(conversation.id, expect.objectContaining({
      body: "@Peer follow up", mentions: [{ userId: "peer", start: 0, end: 5 }],
    }));
    expect(vi.mocked(api.send).mock.lastCall![1].replyToId).toBeUndefined();
  });

  it("retains an ID-only selection after a transient read failure", async () => {
    await selectContextReply();
    await show(otherConversation);
    vi.mocked(api.message).mockRejectedValue(new ApiError(503, "unavailable", "Retry later"));
    await show();
    expect(preview()?.textContent).toContain("原消息暂时无法加载");
    expect(preview()?.textContent).not.toContain(source.body);
    expect(input().value).toBe("Unsent draft");
    await act(async () => { host.querySelector<HTMLButtonElement>('[aria-label="发送消息"]')!.click(); });
    expect(api.send).toHaveBeenCalledWith(conversation.id, expect.objectContaining({ replyToId: source.id }));
  });
});

describe("permanently failed messages", () => {
  const failed = (overrides: Partial<LocalMessage> = {}): LocalMessage => ({
    id: "local:fix-me", conversationId: conversation.id, senderId: user.id,
    senderName: user.displayName, senderKind: "human", clientMessageId: "fix-me",
    seq: Number.MAX_SAFE_INTEGER, body: "@Peer please fix", createdAt: user.createdAt,
    mentions: [{ userId: "peer", start: 0, end: 5 }], replyToId: source.id,
    status: "failed", permanent: true, error: "提及的成员无效，请修改后重新发送。",
    ...overrides,
  });
  const restoreButton = () =>
    [...host.querySelectorAll<HTMLButtonElement>(".failed-status button")].find(
      (button) => button.textContent?.includes("编辑并重发"),
    );

  it("restores body, mentions and reply into the composer, discards the failed copy and focuses input", async () => {
    await act(async () => { chatStore.merge([failed()]); });
    await show();
    expect(host.textContent).toContain("@Peer please fix");
    await act(async () => { restoreButton()!.click(); });
    expect(input().value).toBe("@Peer please fix");
    expect(document.activeElement).toBe(input());
    expect(readComposerDraft(conversation.id)).toEqual({
      body: "@Peer please fix",
      mentions: [{ userId: "peer", start: 0, end: 5 }],
      replyToId: source.id,
    });
    expect(preview()?.textContent).toContain(source.body);
    expect(host.querySelector(".failed-status")).toBeNull();
    expect(chatStore.getSnapshot().messages[conversation.id]?.some(
      (message) => message.clientMessageId === "fix-me",
    )).toBe(false);
    await act(async () => { host.querySelector<HTMLButtonElement>('[aria-label="发送消息"]')!.click(); });
    expect(api.send).toHaveBeenCalledWith(conversation.id, expect.objectContaining({
      body: "@Peer please fix",
      mentions: [{ userId: "peer", start: 0, end: 5 }],
      replyToId: source.id,
    }));
  });

  it("appends to an in-progress draft with shifted mention offsets", async () => {
    await act(async () => { chatStore.merge([failed({ replyToId: undefined })]); });
    await show();
    await type("Keep me");
    await act(async () => { restoreButton()!.click(); });
    expect(input().value).toBe("Keep me\n@Peer please fix");
    expect(readComposerDraft(conversation.id).mentions).toEqual([
      { userId: "peer", start: 8, end: 13 },
    ]);
  });

  it("offers editing only for permanent failures; transient failures keep retry", async () => {
    await act(async () => { chatStore.merge([failed({ permanent: false })]); });
    await show();
    expect(restoreButton()).toBeUndefined();
    expect(host.querySelector(".failed-status")?.textContent).toContain("重试");
  });
});

describe("message copy actions", () => {
  const button = (label: string) =>
    host.querySelector<HTMLButtonElement>(`[aria-label="${label}"]`);

  it("copies the raw body and an absolute message deep link", async () => {
    const writeText = vi.fn(async () => {});
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    latest[conversation.id] = [{ ...source, body: "**raw** body" }];
    await show();
    await act(async () => { button("复制文本")!.click(); });
    expect(writeText).toHaveBeenLastCalledWith("**raw** body");
    expect(host.querySelector('[role="status"].send-status')?.textContent).toBe("已复制消息文本");
    await act(async () => { button("复制消息链接")!.click(); });
    expect(writeText).toHaveBeenLastCalledWith(
      `${window.location.origin}/app/w/team/c/current?message=source`,
    );
    expect(host.querySelector('[role="status"].send-status')?.textContent).toBe("已复制消息链接");
    await act(async () => { vi.advanceTimersByTime(3000); });
    expect(host.querySelector(".send-status")).toBeNull();
  });

  it("reports clipboard failures and hides text copy for retracted messages", async () => {
    vi.stubGlobal("navigator", {
      ...navigator,
      clipboard: { writeText: vi.fn(async () => { throw new Error("denied"); }) },
    });
    latest[conversation.id] = [
      source,
      { ...source, id: "gone", clientMessageId: "gone-request", seq: 3, body: "", retractedAt: user.createdAt },
    ];
    await show();
    expect(host.textContent).toContain("这条消息已撤回");
    expect(host.querySelectorAll('[aria-label="复制消息链接"]')).toHaveLength(1);
    expect(host.querySelectorAll('[aria-label="复制文本"]')).toHaveLength(1);
    await act(async () => { button("复制文本")!.click(); });
    const alert = host.querySelector('[role="alert"].send-status');
    expect(alert?.textContent).toContain("无法访问剪贴板");
  });
});
