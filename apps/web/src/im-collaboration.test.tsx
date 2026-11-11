// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@teamie/api-client";
import type {
  Attachment,
  Bootstrap,
  Conversation,
  Member,
  Mention,
  Message,
  Pin,
  SavedItem,
  User,
  Workspace,
} from "@teamie/api-client";
import { MessageAttachments } from "./attachments";
import { AttentionInbox } from "./attention";
import {
  buildMentionSpans,
  MentionInput,
  MentionText,
  trimMentionDraft,
} from "./mentions";
import { ConversationView } from "./messages";
import { ShellSidebar } from "./shell-sidebar";
import {
  isRelevantMessage,
  isUserMentioned,
  ordinaryUnreadCount,
  summarizeConversationUnread,
} from "./notification-model";
import {
  applyPinUpdated,
  applyReadUpdated,
  applySavedUpdated,
  ChatStore,
  chatStore,
} from "./store";

const owner: User = {
  id: "user-owner",
  kind: "human",
  platformRole: "user",
  displayName: "林舟",
  email: "linzhou@example.test",
  createdAt: "2026-10-20T00:00:00Z",
};

const peer: User = {
  id: "user-peer",
  kind: "human",
  platformRole: "user",
  displayName: "陈默",
  email: "chenmo@example.test",
  createdAt: "2026-10-20T00:00:00Z",
};

const workspace: Workspace = {
  id: "ws-collab",
  name: "协作研发",
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
  {
    userId: "agent-bot",
    displayName: "检索助手",
    kind: "agent",
    role: "member",
  },
];

const channelA: Conversation = {
  id: "conv-a",
  workspaceId: workspace.id,
  type: "channel",
  name: "general",
  memberIds: members.map((m) => m.userId),
  lastSeq: 5,
  readSeq: 5,
  updatedAt: "2026-10-20T01:00:00Z",
};

const channelB: Conversation = {
  id: "conv-b",
  workspaceId: workspace.id,
  type: "group",
  name: "release-sync",
  memberIds: members.map((m) => m.userId),
  lastSeq: 2,
  readSeq: 2,
  updatedAt: "2026-10-20T01:05:00Z",
};

function makeMessage(seq: number, extra: Partial<Message> = {}): Message {
  return {
    id: `msg-${seq}`,
    clientMessageId: `client-${seq}`,
    conversationId: channelA.id,
    senderId: peer.id,
    senderName: peer.displayName,
    senderKind: "human",
    seq,
    body: `协作消息 #${seq}`,
    createdAt: new Date(
      Date.parse(owner.createdAt) + seq * 60_000,
    ).toISOString(),
    ...extra,
  };
}

let bootstrap: Bootstrap;
const sendTyping = vi.fn();

vi.mock("./auth", () => ({
  useAuth: () => ({
    user: owner,
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
    refresh: vi.fn(),
    sync: vi.fn(),
    sendTyping,
    typing: {},
  }),
}));

let host: HTMLDivElement;
let root: Root;

async function flush() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

function LocationProbe() {
  const loc = useLocation();
  return <output data-testid="location">{loc.pathname + loc.search}</output>;
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
  bootstrap = {
    user: owner,
    workspaces: [workspace],
    conversations: [channelA, channelB],
    capabilities: {
      "im.message.mentions": true,
      "im.mentions.broadcast": true,
      "im.presence.publish": true,
      "im.messages.forward": true,
      "im.messages.pin": true,
      "im.messages.save": true,
      "im.unread.mark": true,
    },
    flagsRevision: "collab-v1",
    syncCursor: "0",
  };
  chatStore.begin(owner.id);
  chatStore.replaceConversations([channelA, channelB]);
  vi.spyOn(api, "messages").mockResolvedValue({
    messages: [makeMessage(1), makeMessage(2)],
    hasMore: false,
  });
  vi.spyOn(api, "listPins").mockResolvedValue({ pins: [], pinsVersion: "1" });
  vi.spyOn(api, "listConversationSavedMessages").mockResolvedValue({
    messageIds: [],
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
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("#119 IM Collaboration Web UI", () => {
  it("1. Store reducers wire pin.updated, saved.updated, backward read.updated for self, and retraction cleanup", () => {
    const store = new ChatStore();
    store.begin(owner.id);
    store.replaceConversations([channelA]);
    const m1 = makeMessage(1);
    const m2 = makeMessage(2);
    store.merge([m1, m2]);

    // Pin via pin.updated (action: "pinned" and "unpinned")
    store.applyPinUpdated({
      conversationId: channelA.id,
      messageId: m1.id,
      action: "pinned",
      message: m1,
      pinnedBy: owner.id,
      pinnedByName: owner.displayName,
      pinnedAt: "2026-10-20T02:00:00Z",
    });
    expect(store.getSnapshot().pinsByConversation[channelA.id]).toHaveLength(1);
    expect(store.getSnapshot().pinsByConversation[channelA.id][0].message.id).toBe(
      m1.id,
    );

    // Save via saved.updated (action: "saved" and "unsaved")
    store.applySavedUpdated({
      conversationId: channelA.id,
      messageId: m1.id,
      action: "saved",
      savedAt: "2026-10-20T02:01:00Z",
      message: m1,
    });
    expect(store.getSnapshot().savedByConversation[channelA.id]).toEqual([
      m1.id,
    ]);
    expect(store.getSnapshot().savedMessages).toHaveLength(1);

    // Monotonic readSeq + independent manualUnreadSeq when event.userId === currentUserId
    store.applyReadUpdated(
      {
        conversationId: channelA.id,
        userId: owner.id,
        readSeq: 1,
        manualUnreadSeq: 2,
        unreadCount: 4,
      },
      owner.id,
    );
    const updatedConv = store.getSnapshot().conversations[channelA.id] as Conversation & {
      unreadCount?: number;
    };
    expect(updatedConv.readSeq).toBe(5);
    expect(updatedConv.manualUnreadSeq).toBe(2);
    expect(updatedConv.unreadCount).toBe(4);
    expect(store.getSnapshot().readReceipts[channelA.id][owner.id]).toBe(1);
    expect(store.getSnapshot().manualUnreadByConversation[channelA.id]).toBe(2);

    // Peer read receipt only moves forward and does not mutate caller's conversation.readSeq
    const afterPeer = applyReadUpdated(
      store.getSnapshot(),
      {
        conversationId: channelA.id,
        userId: peer.id,
        readSeq: 3,
      },
      owner.id,
    );
    expect(afterPeer.readReceipts[channelA.id][peer.id]).toBe(3);
    expect(afterPeer.conversations[channelA.id].readSeq).toBe(5);

    // Retracting m1 automatically removes it from pins and saved items
    store.merge([{ ...m1, body: "", retractedAt: "2026-10-20T03:00:00Z" }]);
    expect(store.getSnapshot().pinsByConversation[channelA.id]).toEqual([]);
    expect(store.getSnapshot().savedByConversation[channelA.id]).toEqual([]);
    expect(store.getSnapshot().savedMessages).toEqual([]);

    // Pure reducer helpers also handle unpin/unsave directly
    const s1 = applyPinUpdated(store.getSnapshot(), {
      conversationId: channelA.id,
      messageId: m2.id,
      action: "pinned",
      message: m2,
    });
    const s2 = applyPinUpdated(s1, {
      conversationId: channelA.id,
      messageId: m2.id,
      action: "unpinned",
    });
    expect(s2.pinsByConversation[channelA.id]).toEqual([]);

    const s3 = applySavedUpdated(s2, {
      conversationId: channelA.id,
      messageId: m2.id,
      action: "saved",
      message: m2,
    });
    const s4 = applySavedUpdated(s3, {
      conversationId: channelA.id,
      messageId: m2.id,
      action: "unsaved",
    });
    expect(s4.savedByConversation[channelA.id]).toEqual([]);
  });

  it("2. Always renders persisted message.forwarded cards even when im.messages.forward is false, and forwards via modal when enabled", async () => {
    const forwardedMsg = makeMessage(1, {
      body: "请重点看这段结论",
      forwarded: {
        sourceConversationId: channelB.id,
        sourceMessageId: "orig-agent-1",
        senderId: "agent-bot",
        senderName: "检索助手",
        senderKind: "agent",
        body: "模型推理耗时降低 42%",
        attachments: [
          {
            id: "att-fwd-1",
            filename: "benchmark.csv",
            mediaType: "text/csv",
            sizeBytes: 2048,
            sha256: "f".repeat(64),
          },
        ],
        createdAt: "2026-10-20T00:30:00Z",
      },
    });
    vi.mocked(api.messages).mockResolvedValue({
      messages: [forwardedMsg],
      hasMore: false,
    });

    // Disable write capability im.messages.forward
    bootstrap.capabilities["im.messages.forward"] = false;
    await act(async () => {
      root.render(
        <ConversationView
          conversation={channelA}
          members={members}
          onMembers={() => {}}
          membersOpen={false}
          onMenu={() => {}}
        />,
      );
    });
    await flush();

    // Forwarded card is STILL rendered with sender, AGENT badge, body, and attachment
    const card = host.querySelector('[data-testid="forwarded-card"]');
    expect(card).not.toBeNull();
    expect(card?.textContent).toContain("检索助手");
    expect(card?.textContent).toContain("Agent");
    expect(card?.textContent).toContain("模型推理耗时降低 42%");
    expect(card?.textContent).toContain("benchmark.csv");
    // Forward action button is hidden when flag is false
    expect(host.querySelector('button[aria-label="转发"]')).toBeNull();

    // Enable write capability im.messages.forward
    bootstrap.capabilities["im.messages.forward"] = true;
    const sendSpy = vi.spyOn(api, "send").mockResolvedValue({
      message: makeMessage(6, {
        conversationId: channelB.id,
        body: "同步给发布组",
        forwarded: forwardedMsg.forwarded,
      }),
    });

    await act(async () => {
      root.render(
        <ConversationView
          key="enabled-forward"
          conversation={channelA}
          members={members}
          onMembers={() => {}}
          membersOpen={false}
          onMenu={() => {}}
        />,
      );
    });
    await flush();

    const forwardBtn = host.querySelector<HTMLButtonElement>(
      'button[aria-label="转发"]',
    );
    expect(forwardBtn).not.toBeNull();
    await act(async () => {
      forwardBtn!.click();
    });

    const dialog = document.querySelector('[role="dialog"]');
    expect(dialog).not.toBeNull();
    const select = dialog!.querySelector<HTMLSelectElement>(
      'select[aria-label="目标对话"]',
    )!;
    await act(async () => {
      select.value = channelB.id;
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    const commentInput = dialog!.querySelector<HTMLTextAreaElement>(
      'textarea[aria-label="转发留言"]',
    )!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(
        HTMLTextAreaElement.prototype,
        "value",
      )!.set!.call(commentInput, "同步给发布组");
      commentInput.setSelectionRange(6, 6);
      commentInput.dispatchEvent(new Event("input", { bubbles: true }));
    });

    const submitForm = dialog!.querySelector<HTMLFormElement>("form")!;
    await act(async () => {
      submitForm.dispatchEvent(
        new Event("submit", { bubbles: true, cancelable: true }),
      );
    });
    await flush();

    expect(sendSpy).toHaveBeenCalledWith(
      channelB.id,
      expect.objectContaining({
        body: "同步给发布组",
        forwardOf: {
          conversationId: channelA.id,
          messageId: forwardedMsg.id,
        },
      }),
    );
  });

  it("3. Renders conversation pins header bar, locates pinned messages, and pins/unpins via actions", async () => {
    const m1 = makeMessage(1, { body: "架构评审纪要" });
    const m2 = makeMessage(2, { body: "常规讨论" });
    const initialPin: Pin = {
      message: m1,
      pinnedBy: owner.id,
      pinnedByName: owner.displayName,
      pinnedAt: "2026-10-20T01:10:00Z",
    };
    vi.mocked(api.messages).mockResolvedValue({
      messages: [m1, m2],
      hasMore: false,
    });
    vi.mocked(api.listPins).mockResolvedValue({
      pins: [initialPin],
      pinsVersion: "10",
    });
    const pinSpy = vi.spyOn(api, "pinMessage").mockResolvedValue({
      pin: {
        message: m2,
        pinnedBy: owner.id,
        pinnedByName: owner.displayName,
        pinnedAt: "2026-10-20T01:20:00Z",
      },
      pinsVersion: "11",
    });
    const unpinSpy = vi
      .spyOn(api, "unpinMessage")
      .mockResolvedValue({ pinsVersion: "12" });

    await act(async () => {
      root.render(
        <ConversationView
          conversation={channelA}
          members={members}
          onMembers={() => {}}
          membersOpen={false}
          onMenu={() => {}}
        />,
      );
    });
    await flush();

    // Header bar shows 1 pinned message and m1 has "已置顶" indicator
    const toggleBtn = host.querySelector<HTMLButtonElement>(
      ".pinned-messages-toggle",
    );
    expect(toggleBtn).not.toBeNull();
    expect(toggleBtn?.textContent).toContain("置顶消息 (1)");
    expect(
      host.querySelector("#message-msg-1 .message-pin-badge")?.textContent,
    ).toContain("已置顶");

    // Pin m2 from message action button
    const pinM2Btn = host.querySelector<HTMLButtonElement>(
      '#message-msg-2 button[aria-label="置顶"]',
    )!;
    await act(async () => {
      pinM2Btn.click();
    });
    await flush();
    expect(pinSpy).toHaveBeenCalledWith(channelA.id, m2.id);
    expect(toggleBtn?.textContent).toContain("置顶消息 (2)");

    // Open pinned panel and unpin m1
    await act(async () => {
      toggleBtn!.click();
    });
    const panel = host.querySelector(".pinned-messages-panel");
    expect(panel).not.toBeNull();
    expect(panel?.querySelectorAll(".pinned-message-item")).toHaveLength(2);

    const unpinBtns = panel!.querySelectorAll<HTMLButtonElement>(
      ".pinned-message-unpin",
    );
    await act(async () => {
      unpinBtns[0].click();
    });
    await flush();
    expect(unpinSpy).toHaveBeenCalledWith(channelA.id, m2.id);
    expect(toggleBtn?.textContent).toContain("置顶消息 (1)");
  });

  it("4. Saves/unsaves messages in conversation and displays personal saved items tab in AttentionInbox", async () => {
    const m1 = makeMessage(1, { body: "关键接口契约说明" });
    vi.mocked(api.messages).mockResolvedValue({
      messages: [m1],
      hasMore: false,
    });
    const saveSpy = vi.spyOn(api, "saveMessage").mockResolvedValue({
      saved: {
        conversationId: channelA.id,
        messageId: m1.id,
        savedAt: "2026-10-20T02:00:00Z",
      },
    });
    const unsaveSpy = vi
      .spyOn(api, "unsaveMessage")
      .mockResolvedValue(undefined);

    await act(async () => {
      root.render(
        <ConversationView
          conversation={channelA}
          members={members}
          onMembers={() => {}}
          membersOpen={false}
          onMenu={() => {}}
        />,
      );
    });
    await flush();

    const saveBtn = host.querySelector<HTMLButtonElement>(
      '#message-msg-1 button[aria-label="收藏"]',
    )!;
    await act(async () => {
      saveBtn.click();
    });
    await flush();

    expect(saveSpy).toHaveBeenCalledWith(channelA.id, m1.id);
    expect(
      host.querySelector("#message-msg-1 .message-saved-badge")?.textContent,
    ).toContain("已收藏");

    // Mount AttentionInbox and switch to "收藏" tab
    const savedItem: SavedItem = {
      message: m1,
      conversation: {
        id: channelA.id,
        workspaceId: workspace.id,
        name: channelA.name!,
        type: channelA.type,
      },
      workspaceName: workspace.name,
      savedAt: "2026-10-20T02:00:00Z",
    };
    vi.spyOn(api, "attention").mockResolvedValue({ items: [], unreadCount: 0 });
    const listSavedSpy = vi
      .spyOn(api, "listSavedMessages")
      .mockResolvedValue({ items: [savedItem] });

    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <MemoryRouter initialEntries={["/app/attention"]}>
            <Routes>
              <Route
                path="*"
                element={
                  <>
                    <AttentionInbox onMenu={() => {}} />
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

    const filterBtns = [
      ...host.querySelectorAll<HTMLButtonElement>(".attention-filters button"),
    ];
    const savedTabBtn = filterBtns.find((b) => b.textContent === "收藏")!;
    expect(savedTabBtn).toBeTruthy();
    await act(async () => {
      savedTabBtn.click();
    });
    await flush();

    expect(listSavedSpy).toHaveBeenCalled();
    expect(host.textContent).toContain("关键接口契约说明");
    expect(host.textContent).toContain("协作研发 / general");

    const unsaveInAttention = host.querySelector<HTMLButtonElement>(
      ".attention-unsave-button",
    )!;
    await act(async () => {
      unsaveInAttention.click();
    });
    await flush();
    expect(unsaveSpy).toHaveBeenCalledWith(channelA.id, m1.id);
    expect(host.textContent).toContain("暂无收藏消息");
  });

  it("5. Marks a message as unread, moves readSeq backward, holds auto-read until cleared via 标为已读", async () => {
    const m1 = makeMessage(1);
    const m2 = makeMessage(2);
    const m3 = makeMessage(3);
    vi.mocked(api.messages).mockResolvedValue({
      messages: [m1, m2, m3],
      hasMore: false,
    });
    const markUnreadSpy = vi
      .spyOn(api, "markConversationUnread")
      .mockResolvedValue({
        conversationId: channelA.id,
        manualUnreadSeq: 2,
      });
    const clearUnreadSpy = vi
      .spyOn(api, "clearConversationUnread")
      .mockResolvedValue(undefined);
    const readSpy = vi.mocked(api.read);

    await act(async () => {
      root.render(
        <ConversationView
          conversation={channelA}
          members={members}
          onMembers={() => {}}
          membersOpen={false}
          onMenu={() => {}}
        />,
      );
    });
    await flush();
    readSpy.mockClear();

    const markUnreadBtn = host.querySelector<HTMLButtonElement>(
      '#message-msg-2 button[aria-label="标为未读"]',
    )!;
    expect(markUnreadBtn).not.toBeNull();
    await act(async () => {
      markUnreadBtn.click();
    });
    await flush();

    expect(markUnreadSpy).toHaveBeenCalledWith(channelA.id, 2);
    expect(chatStore.getSnapshot().conversations[channelA.id].readSeq).toBe(5);
    expect(
      chatStore.getSnapshot().manualUnreadByConversation[channelA.id],
    ).toBe(2);
    expect(
      ordinaryUnreadCount(chatStore.getSnapshot().conversations[channelA.id]),
    ).toBe(4);
    // Auto-read did NOT overwrite the manual unread state
    expect(readSpy).not.toHaveBeenCalled();

    const banner = host.querySelector(".manual-unread-banner");
    expect(banner).not.toBeNull();
    const markReadBtn = banner!.querySelector<HTMLButtonElement>("button")!;
    expect(markReadBtn.textContent).toBe("标为已读");

    await act(async () => {
      markReadBtn.click();
    });
    await flush();

    expect(clearUnreadSpy).toHaveBeenCalledWith(channelA.id);
    expect(readSpy).toHaveBeenCalledWith(channelA.id, 3);
    expect(
      chatStore.getSnapshot().manualUnreadByConversation[channelA.id],
    ).toBeUndefined();

    // Preserves earlier readSeq when marking a later message as unread, and muted channels suppress channel unread dot
    const store = new ChatStore();
    store.begin(owner.id);
    store.replaceConversations([
      { ...channelA, readSeq: 2, lastSeq: 10 },
      { ...channelA, id: "conv-muted", readSeq: 5, lastSeq: 5, notificationLevel: "none" },
    ]);
    store.setManualUnread(channelA.id, 8);
    expect(store.getSnapshot().conversations[channelA.id].readSeq).toBe(2);
    expect(ordinaryUnreadCount(store.getSnapshot().conversations[channelA.id])).toBe(8);
    store.setManualUnread(channelA.id, undefined);
    expect(ordinaryUnreadCount(store.getSnapshot().conversations[channelA.id])).toBe(8);

    store.setManualUnread("conv-muted", 3);
    const mutedSummary = summarizeConversationUnread(
      store.getSnapshot().conversations["conv-muted"],
      store.getSnapshot(),
      owner.id,
    );
    expect(mutedSummary.hasUnread).toBe(true);
    expect(mutedSummary.badgeCount).toBe(0);
    expect(mutedSummary.contributesToUrgentCount).toBe(0);
    expect(mutedSummary.contributesToChannelUnreadDot).toBe(false);

    // Reconciles manualUnreadSeq explicitly when readSeq is already ahead of manualUnreadSeq
    store.replaceConversations([{ ...channelA, readSeq: 10, lastSeq: 10 }]);
    store.setManualUnread(channelA.id, 8);
    store.markRead(channelA.id, 10, 8);
    expect(store.getSnapshot().conversations[channelA.id].manualUnreadSeq).toBe(8);
    expect(ordinaryUnreadCount(store.getSnapshot().conversations[channelA.id])).toBe(3);
    store.markRead(channelA.id, 10, null);
    expect(store.getSnapshot().conversations[channelA.id].manualUnreadSeq).toBeUndefined();
    expect(ordinaryUnreadCount(store.getSnapshot().conversations[channelA.id])).toBe(0);

    // Sidebar UI: muted conversations render bold when marked unread, direct/group show
    // muted icon + effective count, channel adds no @ count, and clearing restores muted state
    const mutedChan: Conversation = {
      ...channelA,
      id: "conv-muted-chan",
      name: "muted-chan",
      readSeq: 5,
      lastSeq: 5,
      notificationLevel: "none",
    };
    const mutedGrp: Conversation = {
      ...channelB,
      id: "conv-muted-grp",
      name: "muted-grp",
      readSeq: 5,
      lastSeq: 6,
      notificationLevel: "none",
    };
    const mutedDm: Conversation = {
      id: "conv-muted-dm",
      workspaceId: workspace.id,
      type: "direct",
      name: "",
      memberIds: [owner.id, peer.id],
      readSeq: 2,
      lastSeq: 5,
      updatedAt: "2026-10-20T01:10:00Z",
      notificationLevel: "none",
    };
    const allMuted = [mutedChan, mutedGrp, mutedDm];
    await act(async () => {
      chatStore.replaceConversations(allMuted);
    });

    const renderSidebar = async () => {
      const snap = chatStore.getSnapshot();
      const convs = allMuted.map((c) => snap.conversations[c.id]);
      await act(async () => {
        root.render(
          <MemoryRouter>
            <ShellSidebar
              navigationRef={{ current: null }}
              mobile={false}
              sidebar={false}
              hasOverlayModal={false}
              user={owner}
              busy={false}
              connection="connected"
              connectionLabel="消息已连接"
              isAttentionPage={false}
              isAgentsPage={false}
              snapshot={snap}
              workspaces={[workspace]}
              workspace={workspace}
              owner
              caps={bootstrap.capabilities}
              allAuthorizedConversations={convs}
              channels={[convs[0]]}
              direct={[convs[1], convs[2]]}
              members={members}
              onCloseSidebar={() => {}}
              onSelectWorkspace={() => {}}
              onOpenDialog={() => {}}
              onSignOut={() => {}}
              onShortcuts={() => {}}
              onOpenQuickSwitcher={() => {}}
            />
          </MemoryRouter>,
        );
      });
    };

    const linkFor = (id: string) =>
      host.querySelector<HTMLAnchorElement>(
        `a.conversation-link[href="/app/w/${workspace.id}/c/${id}"]`,
      )!;

    await renderSidebar();
    for (const c of allMuted) {
      const link = linkFor(c.id);
      expect(link.classList.contains("is-muted")).toBe(true);
      expect(link.classList.contains("has-unread")).toBe(false);
      expect(link.querySelector(".conversation-muted-icon")).not.toBeNull();
      expect(link.querySelector(".unread-badge")).toBeNull();
    }

    await act(async () => {
      chatStore.setManualUnread(mutedChan.id, 3); // effective count 3, channel shows bold without count
      chatStore.setManualUnread(mutedGrp.id, 3); // effective count max(6-5, 6-3+1) = 4
      chatStore.setManualUnread(mutedDm.id, 4); // effective count max(5-2, 5-4+1) = 3
    });
    await renderSidebar();

    const chanLink = linkFor(mutedChan.id);
    expect(chanLink.classList.contains("is-muted")).toBe(true);
    expect(chanLink.classList.contains("has-unread")).toBe(true);
    expect(chanLink.querySelector(".conversation-muted-icon")).not.toBeNull();
    expect(chanLink.querySelector(".unread-badge")).toBeNull();

    const grpLink = linkFor(mutedGrp.id);
    expect(grpLink.classList.contains("is-muted")).toBe(true);
    expect(grpLink.classList.contains("has-unread")).toBe(true);
    expect(grpLink.querySelector(".conversation-muted-icon")).not.toBeNull();
    expect(grpLink.querySelector(".unread-badge")?.textContent).toBe("4");

    const dmLink = linkFor(mutedDm.id);
    expect(dmLink.classList.contains("is-muted")).toBe(true);
    expect(dmLink.classList.contains("has-unread")).toBe(true);
    expect(dmLink.querySelector(".conversation-muted-icon")).not.toBeNull();
    expect(dmLink.querySelector(".unread-badge")?.textContent).toBe("3");

    // Clearing markers restores normal dimmed muted presentation without badges
    await act(async () => {
      chatStore.markRead(mutedChan.id, mutedChan.lastSeq, null);
      chatStore.markRead(mutedGrp.id, mutedGrp.lastSeq, null);
      chatStore.markRead(mutedDm.id, mutedDm.lastSeq, null);
    });
    await renderSidebar();

    for (const c of allMuted) {
      const link = linkFor(c.id);
      expect(link.classList.contains("is-muted")).toBe(true);
      expect(link.classList.contains("has-unread")).toBe(false);
      expect(link.querySelector(".conversation-muted-icon")).not.toBeNull();
      expect(link.querySelector(".unread-badge")).toBeNull();
    }
  });

  it("6. Supports @all and @here broadcast mentions autocomplete, spans, and mention detection", async () => {
    let latestDraft = { body: "", mentions: [] as Mention[] };
    function BroadcastHarness() {
      const [draft, setDraft] = useState({
        body: "",
        mentions: [] as Mention[],
      });
      return (
        <MentionInput
          aria-label="消息输入"
          value={draft.body}
          mentions={draft.mentions}
          onChange={(body, mentions) => {
            latestDraft = { body, mentions };
            setDraft({ body, mentions });
          }}
          members={members}
          enabled
          allowBroadcastMentions
          allowHereMention
        />
      );
    }

    await act(async () => {
      root.render(<BroadcastHarness />);
    });

    const textarea = host.querySelector("textarea")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(
        HTMLTextAreaElement.prototype,
        "value",
      )!.set!.call(textarea, "@all");
      textarea.setSelectionRange(4, 4);
      textarea.dispatchEvent(new Event("input", { bubbles: true }));
    });

    const options = [
      ...host.querySelectorAll<HTMLButtonElement>('[role="option"]'),
    ];
    expect(options).toHaveLength(1);
    expect(options[0].textContent).toContain("all");
    expect(options[0].textContent).toContain("所有人");

    await act(async () => {
      options[0].click();
    });

    expect(latestDraft.body).toBe("@all ");
    expect(buildMentionSpans(latestDraft.body, latestDraft.mentions)).toEqual([
      { kind: "all", start: 0, end: 4 },
    ]);
    expect(trimMentionDraft("  @here 请看", [{ kind: "here", start: 2, end: 7 }])).toEqual({
      body: "@here 请看",
      mentions: [{ kind: "here", start: 0, end: 5 }],
    });

    // Mention detection rules
    expect(
      isUserMentioned([{ kind: "all", start: 0, end: 4 }], owner.id),
    ).toBe(true);
    expect(
      isUserMentioned(
        [{ kind: "here", start: 0, end: 5, recipientIds: [owner.id] }],
        owner.id,
      ),
    ).toBe(true);
    expect(
      isUserMentioned(
        [{ kind: "here", start: 0, end: 5, recipientIds: [peer.id] }],
        owner.id,
      ),
    ).toBe(false);
    expect(
      isRelevantMessage(
        channelA,
        makeMessage(9, {
          body: "@all 通知",
          mentions: [{ kind: "all", start: 0, end: 4 }],
        }),
        owner.id,
      ),
    ).toBe(true);

    await act(async () => {
      root.render(
        <MentionText
          body="@all 请查收"
          mentions={[{ kind: "all", start: 0, end: 4 }]}
          userId={owner.id}
        />,
      );
    });
    const pill = host.querySelector(".message-mention.mention-pill");
    expect(pill?.classList.contains("mention-self")).toBe(true);
    expect(pill?.getAttribute("data-mention-kind")).toBe("all");
  });

  it("7. Loads attachment thumbnails through api.attachmentThumbnail into a Blob URL with reserved aspect-ratio box, revokes on unmount, and falls back to file card on failure", async () => {
    const imageAtt: Attachment = {
      id: "att-img-meta",
      filename: "architecture.png",
      mediaType: "image/png",
      sizeBytes: 4096,
      sha256: "c".repeat(64),
      image: {
        width: 640,
        height: 360,
      },
    };
    const thumbBytes = new Uint8Array([137, 80, 78, 71]);
    const thumbSpy = vi
      .spyOn(api, "attachmentThumbnail")
      .mockResolvedValue(thumbBytes as unknown as Blob);
    const contentBlob = new Blob(["orig-bytes"], { type: "image/png" });
    const contentSpy = vi
      .spyOn(api, "attachmentContent")
      .mockResolvedValue(contentBlob);
    const createObjectURL = vi
      .fn()
      .mockReturnValueOnce("blob:thumb-preview")
      .mockReturnValueOnce("blob:orig-download");
    const revokeObjectURL = vi.fn();
    vi.stubGlobal(
      "URL",
      class extends URL {
        static createObjectURL = createObjectURL;
        static revokeObjectURL = revokeObjectURL;
      },
    );
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});

    await act(async () => {
      root.render(
        <MessageAttachments
          conversationId={channelA.id}
          attachments={[imageAtt]}
        />,
      );
    });
    await flush();

    expect(thumbSpy).toHaveBeenCalledWith(
      channelA.id,
      imageAtt.id,
      expect.any(AbortSignal),
    );
    const thumbBtn = host.querySelector<HTMLButtonElement>(
      "button.attachment-thumbnail-button",
    )!;
    expect(thumbBtn).not.toBeNull();
    expect(thumbBtn.style.aspectRatio).toBe("640 / 360");

    const img = host.querySelector<HTMLImageElement>(
      "img.attachment-thumbnail",
    )!;
    expect(img).not.toBeNull();
    expect(img.getAttribute("src")).toBe("blob:thumb-preview");
    expect(img.getAttribute("width")).toBe("640");
    expect(img.getAttribute("height")).toBe("360");

    // Download button still fetches original content
    const downloadBtn = host.querySelector<HTMLButtonElement>(
      `button[aria-label="下载文件 ${imageAtt.filename}"]`,
    )!;
    await act(async () => {
      downloadBtn.click();
    });
    expect(contentSpy).toHaveBeenCalledWith(channelA.id, imageAtt.id);

    // Unmounting revokes the temporary thumbnail Blob URL
    await act(async () => {
      root.render(<div />);
    });
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:thumb-preview");

    // Retries temporary 429 saturation before rendering thumbnail
    thumbSpy
      .mockRejectedValueOnce(new ApiError(429, "transfer_saturated", "Too Many Requests", 1))
      .mockResolvedValueOnce(thumbBytes as unknown as Blob);
    createObjectURL.mockReturnValueOnce("blob:thumb-after-429");
    await act(async () => {
      root.render(
        <MessageAttachments
          conversationId={channelA.id}
          attachments={[{ ...imageAtt, id: "att-img-429" }]}
        />,
      );
    });
    await act(async () => {
      await new Promise((r) => setTimeout(r, 15));
    });
    await flush();
    expect(
      host.querySelector<HTMLImageElement>("img.attachment-thumbnail")?.getAttribute("src"),
    ).toBe("blob:thumb-after-429");
    await act(async () => {
      root.render(<div />);
    });

    // If api.attachmentThumbnail rejects permanently, falls back cleanly to the ordinary file card
    thumbSpy.mockRejectedValueOnce(new Error("thumbnail 404"));
    await act(async () => {
      root.render(
        <MessageAttachments
          conversationId={channelA.id}
          attachments={[imageAtt]}
        />,
      );
    });
    await flush();
    expect(host.querySelector("img.attachment-thumbnail")).toBeNull();
    expect(
      host.querySelector(`button[aria-label="下载文件 ${imageAtt.filename}"]`),
    ).not.toBeNull();
  });

  it("8. Fetches forwarded attachments from target conversation, navigates cross-conversation on 查看原消息, and preserves forward clientMessageId across retries", async () => {
    const forwardedWithImage = makeMessage(1, {
      conversationId: channelA.id,
      body: "跨会话转发带图",
      forwarded: {
        sourceConversationId: channelB.id,
        sourceMessageId: "msg-in-b-99",
        senderId: peer.id,
        senderName: peer.displayName,
        senderKind: "human",
        body: "原图说明",
        attachments: [
          {
            id: "copied-att-in-a",
            filename: "diagram.png",
            mediaType: "image/png",
            sizeBytes: 1024,
            sha256: "d".repeat(64),
            image: { width: 320, height: 180 },
          },
        ],
        createdAt: "2026-10-20T00:40:00Z",
      },
    });
    vi.mocked(api.messages).mockResolvedValue({
      messages: [forwardedWithImage],
      hasMore: false,
    });
    const thumbSpy = vi
      .spyOn(api, "attachmentThumbnail")
      .mockResolvedValue(new Blob(["png"], { type: "image/png" }));
    vi.stubGlobal(
      "URL",
      class extends URL {
        static createObjectURL = vi.fn(() => "blob:fwd-thumb");
        static revokeObjectURL = vi.fn();
      },
    );

    await act(async () => {
      root.render(
        <MemoryRouter
          initialEntries={[`/app/w/${workspace.id}/c/${channelA.id}`]}
        >
          <Routes>
            <Route
              path="*"
              element={
                <>
                  <ConversationView
                    conversation={channelA}
                    members={members}
                    onMembers={() => {}}
                    membersOpen={false}
                    onMenu={() => {}}
                  />
                  <LocationProbe />
                </>
              }
            />
          </Routes>
        </MemoryRouter>,
      );
    });
    await flush();

    // Finding 1: Forwarded attachment thumbnail is fetched from target conversation (channelA.id), NOT sourceConversationId (channelB.id)
    expect(thumbSpy).toHaveBeenCalledWith(
      channelA.id,
      "copied-att-in-a",
      expect.any(AbortSignal),
    );

    // Finding 7: Clicking 查看原消息 on a cross-conversation forward navigates to sourceConversationId
    const jumpBtn = host.querySelector<HTMLButtonElement>(
      ".message-forwarded-jump",
    )!;
    expect(jumpBtn).not.toBeNull();
    await act(async () => {
      jumpBtn.click();
    });
    expect(
      host.querySelector('[data-testid="location"]')?.textContent,
    ).toBe(`/app/w/${workspace.id}/c/${channelB.id}?message=msg-in-b-99`);

    // Finding 6: ForwardMessageModal preserves clientMessageId across retry for identical (message, target, comment)
    let callCount = 0;
    const sendSpy = vi.spyOn(api, "send").mockImplementation(async () => {
      callCount += 1;
      if (callCount === 1) {
        throw new Error("网络超时");
      }
      return {
        message: makeMessage(10, { conversationId: channelB.id }),
      };
    });

    const forwardBtn = host.querySelector<HTMLButtonElement>(
      'button[aria-label="转发"]',
    )!;
    await act(async () => {
      forwardBtn.click();
    });

    const dialog = document.querySelector('[role="dialog"]')!;
    const submitForm = dialog.querySelector<HTMLFormElement>("form")!;

    // First attempt fails
    await act(async () => {
      submitForm.dispatchEvent(
        new Event("submit", { bubbles: true, cancelable: true }),
      );
    });
    await flush();
    expect(sendSpy).toHaveBeenCalledTimes(1);
    const firstClientMsgId = sendSpy.mock.calls[0][1].clientMessageId;
    expect(firstClientMsgId).toBeTruthy();

    // Retry without changing target or comment reuses exact same clientMessageId
    await act(async () => {
      submitForm.dispatchEvent(
        new Event("submit", { bubbles: true, cancelable: true }),
      );
    });
    await flush();
    expect(sendSpy).toHaveBeenCalledTimes(2);
    expect(sendSpy.mock.calls[1][1].clientMessageId).toBe(firstClientMsgId);
  });

  it("9. Paginates saved items with cursor, prunes collaboration state when conversation access is revoked, clears synthetic unread count, and reconciles out-of-order pin snapshots", async () => {
    // Finding 4: Synthetic unreadCount is cleared when manualUnreadSeq is cleared or readSeq catches up
    const store = new ChatStore();
    store.begin(owner.id);
    store.replaceConversations([channelA, channelB]);
    store.setManualUnread(channelA.id, 2);
    expect(
      ordinaryUnreadCount(store.getSnapshot().conversations[channelA.id]),
    ).toBe(4);
    expect(
      summarizeConversationUnread(
        store.getSnapshot().conversations[channelA.id],
        store.getSnapshot(),
        owner.id,
      ).rawUnread,
    ).toBe(4);

    // Clearing manualUnread and marking read to lastSeq clears unreadCount back to 0
    store.setManualUnread(channelA.id, undefined);
    store.markRead(channelA.id, channelA.lastSeq);
    expect(
      ordinaryUnreadCount(store.getSnapshot().conversations[channelA.id]),
    ).toBe(0);
    expect(
      summarizeConversationUnread(
        store.getSnapshot().conversations[channelA.id],
        store.getSnapshot(),
        owner.id,
      ).rawUnread,
    ).toBe(0);

    // Finding 5: Pin snapshot reconciliation when a version-6 pin.updated arrives before version-5 listPins resolves
    const p1: Pin = {
      message: makeMessage(1, { body: "Pin 1" }),
      pinnedBy: owner.id,
      pinnedByName: owner.displayName,
      pinnedAt: "2026-10-20T01:01:00Z",
    };
    const p6Msg = makeMessage(6, { body: "Pin 6" });
    store.applyPinUpdated({
      conversationId: channelA.id,
      messageId: p6Msg.id,
      action: "pinned",
      message: p6Msg,
      pinnedBy: owner.id,
      pinnedByName: owner.displayName,
      pinnedAt: "2026-10-20T01:06:00Z",
      pinsVersion: "6",
    });
    // Now baseline version-5 snapshot arrives late
    store.setConversationPins(channelA.id, [p1], "5");
    const reconciledPins = store.getSnapshot().pinsByConversation[channelA.id];
    expect(reconciledPins.map((p) => p.message.id)).toEqual([
      p6Msg.id,
      p1.message.id,
    ]);
    expect(store.getSnapshot().pinsVersionByConversation?.[channelA.id]).toBe(
      "6",
    );

    // Finding 2 & 3: Saved messages cursor pagination and pruning on conversation revocation
    const savedA: SavedItem = {
      message: makeMessage(1, {
        id: "saved-a-1",
        conversationId: channelA.id,
        body: "Saved in A",
      }),
      conversation: {
        id: channelA.id,
        workspaceId: workspace.id,
        name: channelA.name!,
        type: channelA.type,
      },
      workspaceName: workspace.name,
      savedAt: "2026-10-20T02:00:00Z",
    };
    const savedB: SavedItem = {
      message: makeMessage(2, {
        id: "saved-b-2",
        conversationId: channelB.id,
        body: "Saved in B",
      }),
      conversation: {
        id: channelB.id,
        workspaceId: workspace.id,
        name: channelB.name!,
        type: channelB.type,
      },
      workspaceName: workspace.name,
      savedAt: "2026-10-20T02:05:00Z",
    };

    vi.spyOn(api, "attention").mockResolvedValue({ items: [], unreadCount: 0 });
    const listSavedSpy = vi
      .spyOn(api, "listSavedMessages")
      .mockImplementation(async (opts) => {
        if (opts?.cursor === "cursor-page-2") {
          return { items: [savedB] };
        }
        return { items: [savedA], cursor: "cursor-page-2" };
      });

    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <MemoryRouter initialEntries={["/app/attention?tab=saved"]}>
            <AttentionInbox onMenu={() => {}} />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    await flush();

    expect(listSavedSpy).toHaveBeenCalledWith(
      { limit: 50 },
      expect.any(AbortSignal),
    );
    expect(host.textContent).toContain("Saved in A");
    expect(host.textContent).not.toContain("Saved in B");

    // Click 加载更多 to load second page via cursor
    const loadMoreBtn = [
      ...host.querySelectorAll<HTMLButtonElement>("button.attention-more"),
    ].find((b) => b.textContent?.includes("加载更多"))!;
    expect(loadMoreBtn).toBeTruthy();
    await act(async () => {
      loadMoreBtn.click();
    });
    await flush();

    expect(listSavedSpy).toHaveBeenCalledWith({
      cursor: "cursor-page-2",
      limit: 50,
    });
    expect(host.textContent).toContain("Saved in A");
    expect(host.textContent).toContain("Saved in B");

    // Revoke access to channelB -> savedB is immediately pruned from store and UI
    listSavedSpy.mockResolvedValue({ items: [savedA] });
    await act(async () => {
      chatStore.replaceConversations([channelA]);
    });
    await flush();

    expect(host.textContent).toContain("Saved in A");
    expect(host.textContent).not.toContain("Saved in B");
  });
});
