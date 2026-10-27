// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type {
  Bootstrap,
  Conversation,
  Member,
  Message,
  User,
  Workspace,
} from "@teamie/api-client";
import { ConversationView } from "./messages";
import { WorkspaceMembers } from "./members";
import { Shell } from "./shell";
import { pickAdjacentConversation } from "./shell-sidebar";
import { chatStore, summarizeConversationUnread } from "./store";
import { presenceStore, receiptStore } from "./engagement-store";
import {
  notifySyncedMessages,
  resetNotificationSession,
  setActiveNotificationContext,
  setNotificationPreferences,
} from "./notifications";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

const selfUser: User = {
  id: "u-self",
  kind: "human",
  platformRole: "user",
  displayName: "林舟",
  email: "self@example.test",
  createdAt: "2026-10-20T00:00:00Z",
};

const peerAlice: Member = {
  userId: "u-alice",
  displayName: "陈晨",
  kind: "human",
  role: "member",
};

const peerBob: Member = {
  userId: "u-bob",
  displayName: "周牧",
  kind: "human",
  role: "member",
};

const agentBot: Member = {
  userId: "u-bot",
  displayName: "代码助手",
  kind: "agent",
  role: "member",
  ownerName: "林舟",
};

const members: Member[] = [
  {
    userId: selfUser.id,
    displayName: selfUser.displayName,
    kind: "human",
    role: "owner",
  },
  peerAlice,
  peerBob,
  agentBot,
];

const workspace: Workspace = {
  id: "ws-1",
  name: "核心研发",
  createdBy: selfUser.id,
  createdAt: selfUser.createdAt,
};

let bootstrap: Bootstrap;
const noop = () => {};

vi.mock("./auth", () => ({
  useAuth: () => ({
    user: selfUser,
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
    refresh: vi.fn(async () => {}),
    sync: vi.fn(),
    reconnect: vi.fn(),
    sendTyping: vi.fn(),
    typing: {},
  }),
}));

function makeConversation(
  id: string,
  type: Conversation["type"],
  extra: Partial<Conversation> = {},
): Conversation {
  return {
    id,
    workspaceId: workspace.id,
    type,
    name: type === "direct" ? "" : `会话-${id}`,
    memberIds:
      type === "direct"
        ? [selfUser.id, peerAlice.userId]
        : [selfUser.id, peerAlice.userId, peerBob.userId],
    lastSeq: 2,
    readSeq: 2,
    updatedAt: "2026-10-20T01:00:00Z",
    ...extra,
  };
}

function makeMessage(
  conversationId: string,
  seq: number,
  senderId: string,
  extra: Partial<Message> = {},
): Message {
  const sender =
    members.find((m) => m.userId === senderId) || members[0];
  return {
    id: `${conversationId}-m${seq}`,
    conversationId,
    senderId,
    senderName: sender.displayName,
    senderKind: sender.kind,
    clientMessageId: `${conversationId}-c${seq}`,
    seq,
    body: `消息内容 ${seq}`,
    createdAt: new Date(Date.parse(selfUser.createdAt) + seq * 60_000).toISOString(),
    ...extra,
  };
}

async function flush() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

describe("IM engagement suite: reactions, read receipts, presence, and notification levels", () => {
  let host: HTMLDivElement;
  let root: Root;
  let queryClient: QueryClient;

  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
    presenceStore.reset();
    receiptStore.reset();
    resetNotificationSession(null);
    setActiveNotificationContext(undefined, null);
    vi.stubGlobal("matchMedia", () => ({
      matches: false,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe() {}
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

    const conv = makeConversation("c-group", "group");
    bootstrap = {
      user: selfUser,
      workspaces: [workspace],
      conversations: [conv],
      capabilities: {
        "im.message.mentions": true,
        "im.reactions.create": true,
        "im.read_receipts.view": true,
        "im.presence.publish": true,
        "im.notifications.configure": true,
        "im.dm.create": true,
        "im.group.create": true,
      },
      flagsRevision: "engage-1",
      syncCursor: "10",
    };
    chatStore.begin(selfUser.id);
    chatStore.replaceConversations(bootstrap.conversations);
    vi.spyOn(api, "read").mockImplementation(async (_id, seq) => ({
      readSeq: seq,
    }));
    vi.spyOn(api, "readReceipts").mockResolvedValue({
      conversationId: "c-group",
      eligibleCount: 3,
      members: [
        { userId: selfUser.id, readSeq: 2 },
        { userId: peerAlice.userId, readSeq: 2 },
        { userId: peerBob.userId, readSeq: 1 },
      ],
      readSeqCounts: [
        { readSeq: 2, count: 2 },
        { readSeq: 1, count: 1 },
      ],
    });
    vi.spyOn(api, "workspacePresence").mockResolvedValue({
      items: [
        {
          userId: peerAlice.userId,
          state: "online",
        },
        {
          userId: peerBob.userId,
          state: "away",
        },
      ],
    });
    vi.spyOn(api, "members").mockResolvedValue({ members });

    queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false, gcTime: 0 } },
    });
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    host.remove();
    chatStore.clear();
    presenceStore.reset();
    receiptStore.reset();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it("adds and removes emoji reactions, applies reaction.updated sync events, and keeps persisted chips read-only when im.reactions.create is disabled", async () => {
    const conv = makeConversation("c-group", "group");
    const m1 = makeMessage(conv.id, 1, peerAlice.userId);
    vi.spyOn(api, "messages").mockResolvedValue({
      messages: [m1],
      hasMore: false,
    });
    const putSpy = vi.spyOn(api, "putReaction").mockResolvedValue({
      reactions: [{ emoji: "👍", count: 1, userIds: [selfUser.id] }],
      reactionsVersion: "1",
    });
    const deleteSpy = vi.spyOn(api, "deleteReaction").mockResolvedValue({
      reactions: [],
      reactionsVersion: "2",
    });

    await act(async () => {
      root.render(
        <MemoryRouter>
          <ConversationView
            conversation={conv}
            members={members}
            onMembers={noop}
            membersOpen={false}
            onMenu={noop}
          />
        </MemoryRouter>,
      );
    });
    await flush();

    // Open reaction picker and click 👍
    const addReactionBtn = host.querySelector<HTMLButtonElement>(
      'button[aria-label="添加表情回应"]',
    )!;
    expect(addReactionBtn).toBeTruthy();
    await act(async () => {
      addReactionBtn.click();
    });
    await flush();
    const thumbsUpOption = host.querySelector<HTMLButtonElement>(
      '.reaction-picker-emoji',
    )!;
    expect(thumbsUpOption).toBeTruthy();
    await act(async () => {
      thumbsUpOption.click();
    });
    await flush();

    expect(putSpy).toHaveBeenCalledWith(conv.id, m1.id, "👍");
    const chip = host.querySelector<HTMLButtonElement>(".reaction-chip.active")!;
    expect(chip).toBeTruthy();
    expect(chip.textContent).toContain("👍1");
    expect(chip.getAttribute("title")).toBe("你 回应了 👍");

    // Simulate peerAlice adding 🎉 via reaction.updated sync event
    await act(async () => {
      chatStore.apply({
        cursor: "11",
        hasMore: false,
        events: [
          {
            id: "11",
            type: "reaction.updated",
            workspaceId: workspace.id,
            conversationId: conv.id,
            messageId: m1.id,
            reactions: [
              { emoji: "👍", count: 1, userIds: [selfUser.id] },
              { emoji: "🎉", count: 1, userIds: [peerAlice.userId] },
            ],
            createdAt: "2026-10-20T01:02:00Z",
          },
        ],
      });
    });
    const chips = host.querySelectorAll<HTMLButtonElement>(".reaction-chip");
    expect(chips).toHaveLength(2);
    expect(chips[1].getAttribute("title")).toBe("陈晨 回应了 🎉");

    // Click own active 👍 chip to remove reaction
    await act(async () => {
      chips[0].click();
    });
    await flush();
    expect(deleteSpy).toHaveBeenCalledWith(conv.id, m1.id, "👍");

    // When im.reactions.create is disabled, own active reactions can still be removed (DELETE) while others' chips are disabled
    bootstrap = {
      ...bootstrap,
      capabilities: {
        ...bootstrap.capabilities,
        "im.reactions.create": false,
      },
    };
    await act(async () => {
      chatStore.setReactions(conv.id, m1.id, [
        { emoji: "🔥", count: 2, userIds: [peerAlice.userId, peerBob.userId] },
        { emoji: "👀", count: 1, userIds: [selfUser.id] },
      ]);
      root.render(
        <MemoryRouter>
          <ConversationView
            conversation={{ ...conv }}
            members={members}
            onMembers={noop}
            membersOpen={false}
            onMenu={noop}
          />
        </MemoryRouter>,
      );
    });
    await flush();
    expect(
      host.querySelector('button[aria-label="添加表情回应"]'),
    ).toBeNull();
    const disabledChips = host.querySelectorAll<HTMLButtonElement>(".reaction-chip");
    expect(disabledChips).toHaveLength(2);
    expect(disabledChips[0].disabled).toBe(true);
    expect(disabledChips[0].textContent).toContain("🔥2");
    expect(disabledChips[1].disabled).toBe(false);
    expect(disabledChips[1].textContent).toContain("👀1");
    await act(async () => {
      disabledChips[1].click();
    });
    await flush();
    expect(deleteSpy).toHaveBeenCalledWith(conv.id, m1.id, "👀");
  });

  it("renders read receipts for direct ('已读'/'未读'), group ('N 人已读' with read/unread member popover), and channel ('N 人已读' without member names)", async () => {
    // 1. Direct chat: 未读 -> 已读 on receipts.changed
    const dmConv = makeConversation("c-dm", "direct");
    const dmMsg = makeMessage(dmConv.id, 2, selfUser.id);
    vi.spyOn(api, "messages").mockResolvedValue({
      messages: [dmMsg],
      hasMore: false,
    });
    const receiptsSpy = vi.spyOn(api, "readReceipts").mockResolvedValueOnce({
      conversationId: dmConv.id,
      eligibleCount: 2,
      members: [
        { userId: selfUser.id, readSeq: 2 },
        { userId: peerAlice.userId, readSeq: 1 },
      ],
      readSeqCounts: [
        { readSeq: 2, count: 1 },
        { readSeq: 1, count: 1 },
      ],
    });

    await act(async () => {
      root.render(
        <MemoryRouter>
          <ConversationView
            key={dmConv.id}
            conversation={dmConv}
            members={members}
            onMembers={noop}
            membersOpen={false}
            onMenu={noop}
          />
        </MemoryRouter>,
      );
    });
    await flush();

    expect(
      host.querySelector(".message-read-receipt")?.textContent,
    ).toBe("未读");

    receiptsSpy.mockResolvedValueOnce({
      conversationId: dmConv.id,
      eligibleCount: 2,
      members: [
        { userId: selfUser.id, readSeq: 2 },
        { userId: peerAlice.userId, readSeq: 2 },
      ],
      readSeqCounts: [{ readSeq: 2, count: 2 }],
    });
    await act(async () => {
      window.dispatchEvent(new Event("focus"));
    });
    await flush();
    expect(
      host.querySelector(".message-read-receipt")?.textContent,
    ).toBe("已读");

    // 2. Group chat: 1 人已读 + expandable popover with read/unread member lists
    const groupConv = makeConversation("c-group", "group");
    const groupMsg = makeMessage(groupConv.id, 2, selfUser.id);
    vi.spyOn(api, "messages").mockResolvedValue({
      messages: [groupMsg],
      hasMore: false,
    });
    receiptsSpy.mockResolvedValueOnce({
      conversationId: groupConv.id,
      eligibleCount: 3,
      members: [
        { userId: selfUser.id, readSeq: 2 },
        { userId: peerAlice.userId, readSeq: 2 },
        { userId: peerBob.userId, readSeq: 1 },
      ],
      readSeqCounts: [
        { readSeq: 2, count: 2 },
        { readSeq: 1, count: 1 },
      ],
    });

    await act(async () => {
      root.render(
        <MemoryRouter>
          <ConversationView
            key={groupConv.id}
            conversation={groupConv}
            members={members}
            onMembers={noop}
            membersOpen={false}
            onMenu={noop}
          />
        </MemoryRouter>,
      );
    });
    await flush();

    const groupReceiptBtn = host.querySelector<HTMLButtonElement>(
      "button.message-read-receipt",
    )!;
    expect(groupReceiptBtn.textContent).toBe("1 人已读");
    await act(async () => {
      groupReceiptBtn.click();
    });
    const popover = host.querySelector(".read-receipt-popover")!;
    expect(popover).toBeTruthy();
    expect(popover.textContent).toContain("已读 (1)");
    expect(popover.textContent).toContain("陈晨");
    expect(popover.textContent).toContain("未读 (1)");
    expect(popover.textContent).toContain("周牧");

    // 3. Channel chat: N 人已读 from readSeqCounts without member names
    const chanConv = makeConversation("c-chan", "channel");
    const chanMsg = makeMessage(chanConv.id, 2, selfUser.id);
    vi.spyOn(api, "messages").mockResolvedValue({
      messages: [chanMsg],
      hasMore: false,
    });
    receiptsSpy.mockResolvedValueOnce({
      conversationId: chanConv.id,
      eligibleCount: 5,
      readSeqCounts: [
        { readSeq: 2, count: 3 },
        { readSeq: 1, count: 2 },
      ],
    });

    await act(async () => {
      root.render(
        <MemoryRouter>
          <ConversationView
            key={chanConv.id}
            conversation={chanConv}
            members={members}
            onMembers={noop}
            membersOpen={false}
            onMenu={noop}
          />
        </MemoryRouter>,
      );
    });
    await flush();

    // Backend readSeqCounts already excludes caller: 3 at seq 2 = 3 人已读, rendered as non-expandable span
    const chanReceipt = host.querySelector(".message-read-receipt")!;
    expect(chanReceipt.tagName).toBe("SPAN");
    expect(chanReceipt.textContent).toBe("3 人已读");
  });

  it("displays human online/away/offline presence indicators in direct headers and member directory while keeping Agents on Agent badges", async () => {
    presenceStore.applyWorkspaceSnapshot(
      [selfUser.id, peerAlice.userId, peerBob.userId],
      [
        {
          userId: peerAlice.userId,
          state: "online",
        },
        {
          userId: peerBob.userId,
          state: "away",
        },
      ],
    );

    const dmConv = makeConversation("c-dm", "direct");
    vi.spyOn(api, "messages").mockResolvedValue({
      messages: [],
      hasMore: false,
    });

    await act(async () => {
      root.render(
        <MemoryRouter>
          <ConversationView
            conversation={dmConv}
            members={members}
            onMembers={noop}
            membersOpen={false}
            onMenu={noop}
          />
        </MemoryRouter>,
      );
    });
    await flush();

    expect(host.querySelector(".header-presence")?.textContent).toContain("在线");

    // Realtime presence hint transitions peerAlice to away
    await act(async () => {
      presenceStore.hint(peerAlice.userId, "away");
    });
    expect(host.querySelector(".header-presence")?.textContent).toContain("离开");

    // Render WorkspaceMembers directory: human members show 在线/离开/离线, Agent does not
    await act(async () => {
      root.render(
        <MemoryRouter>
          <WorkspaceMembers
            workspace={workspace}
            members={members}
            agents={[]}
            loading={false}
            loadError={false}
            onRetry={noop}
          />
        </MemoryRouter>,
      );
    });
    await flush();

    const badges = Array.from(
      host.querySelectorAll(".member-presence-badge"),
    ).map((el) => el.textContent);
    // self is offline (not in snapshot items), peerAlice is away, peerBob is away; agentBot has no human presence badge
    expect(badges).toEqual(["离线", "离开", "离开"]);
  });

  it("configures per-conversation notification levels ('all' | 'mentions' | 'none') and unifies sidebar badges, workspace unread dots, and desktop notifications", async () => {
    const chanAll = makeConversation("c-all", "channel", {
      name: "general",
      lastSeq: 5,
      readSeq: 2,
      notificationLevel: "all",
    });
    const chanMentions = makeConversation("c-mentions", "channel", {
      name: "releases",
      lastSeq: 5,
      readSeq: 2,
      notificationLevel: "mentions",
    });
    const chanMuted = makeConversation("c-muted", "channel", {
      name: "watercooler",
      lastSeq: 8,
      readSeq: 1,
      notificationLevel: "none",
    });
    bootstrap.conversations = [chanAll, chanMentions, chanMuted];
    chatStore.replaceConversations(bootstrap.conversations);

    const snap = chatStore.getSnapshot();
    const sumAll = summarizeConversationUnread(chanAll, snap, selfUser.id);
    expect(sumAll.hasUnread).toBe(true);
    expect(sumAll.badgeCount).toBe(0);
    expect(sumAll.contributesToWorkspaceDot).toBe(true);

    // "mentions" with 0 unread mentions suppresses both badge and bold unread state
    const sumMentionsQuiet = summarizeConversationUnread(
      chanMentions,
      snap,
      selfUser.id,
    );
    expect(sumMentionsQuiet.hasUnread).toBe(false);
    expect(sumMentionsQuiet.badgeCount).toBe(0);
    expect(sumMentionsQuiet.contributesToWorkspaceDot).toBe(false);

    // "none" (muted) suppresses badge, bold, workspace dot, and unread cycling
    const sumMuted = summarizeConversationUnread(chanMuted, snap, selfUser.id);
    expect(sumMuted.muted).toBe(true);
    expect(sumMuted.hasUnread).toBe(false);
    expect(sumMuted.badgeCount).toBe(0);
    expect(sumMuted.contributesToWorkspaceDot).toBe(false);

    // Alt+Shift+Down unread conversation picker skips muted conversations
    expect(
      pickAdjacentConversation(
        [chanAll, chanMuted],
        chanAll.id,
        1,
        true,
      ),
    ).toBeUndefined();

    // Desktop notifications respect per-conversation notification level
    setNotificationPreferences({ enabled: true, showPreview: true }, selfUser.id);
    const createdNotifications: Array<{ title: string }> = [];
    vi.stubGlobal(
      "Notification",
      class {
        static permission = "granted";
        constructor(title: string) {
          createdNotifications.push({ title });
        }
        close() {}
      },
    );

    const mutedMsg = makeMessage(chanMuted.id, 9, peerAlice.userId, {
      body: "@林舟 被静音的提及",
      mentions: [{ userId: selfUser.id, start: 0, end: 3 }],
    });
    const quietMsg = makeMessage(chanMentions.id, 6, peerAlice.userId, {
      body: "普通进展",
    });
    const mentionMsg = makeMessage(chanMentions.id, 7, peerAlice.userId, {
      body: "@林舟 请确认发布",
      mentions: [{ userId: selfUser.id, start: 0, end: 3 }],
    });
    chatStore.merge([mutedMsg, quietMsg, mentionMsg]);
    const latestSnap = chatStore.getSnapshot();

    // Mention message in muted conversation -> no notification
    notifySyncedMessages(
      [{ conversationId: chanMuted.id, messageId: mutedMsg.id }],
      latestSnap,
      selfUser.id,
    );
    expect(createdNotifications).toHaveLength(0);

    // Ordinary message in "mentions"-only conversation -> no notification
    notifySyncedMessages(
      [{ conversationId: chanMentions.id, messageId: quietMsg.id }],
      latestSnap,
      selfUser.id,
    );
    expect(createdNotifications).toHaveLength(0);

    // @mention message in "mentions"-only conversation -> triggers notification
    notifySyncedMessages(
      [{ conversationId: chanMentions.id, messageId: mentionMsg.id }],
      latestSnap,
      selfUser.id,
    );
    expect(createdNotifications).toHaveLength(1);

    // Header notification level menu updates level via API and preference.updated sync event
    const setLevelSpy = vi
      .spyOn(api, "setNotificationLevel")
      .mockResolvedValue({
        conversationId: chanAll.id,
        notificationLevel: "none",
      });
    vi.spyOn(api, "messages").mockResolvedValue({
      messages: [],
      hasMore: false,
    });
    vi.spyOn(api, "attention").mockResolvedValue({
      items: [],
      unreadCount: 0,
    });
    vi.spyOn(api, "agents").mockResolvedValue({ agents: [] });

    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <MemoryRouter initialEntries={[`/app/w/${workspace.id}/c/${chanAll.id}`]}>
            <Routes>
              <Route path="/app/*" element={<Shell />} />
            </Routes>
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    await flush();

    const notifTrigger = host.querySelector<HTMLButtonElement>(
      'button[aria-label^="会话通知设置"]',
    )!;
    expect(notifTrigger).toBeTruthy();
    await act(async () => {
      notifTrigger.click();
    });
    const muteOption = Array.from(
      host.querySelectorAll<HTMLButtonElement>(".notification-level-option"),
    ).find((btn) => btn.textContent?.includes("静音"))!;
    expect(muteOption).toBeTruthy();
    await act(async () => {
      muteOption.click();
    });
    await flush();

    expect(setLevelSpy).toHaveBeenCalledWith(chanAll.id, "none");
    expect(
      chatStore.getSnapshot().conversations[chanAll.id]?.notificationLevel,
    ).toBe("none");

    // Verify preference.updated sync event updates conversation notification level
    await act(async () => {
      chatStore.apply({
        cursor: "12",
        hasMore: false,
        events: [
          {
            id: "12",
            type: "preference.updated",
            workspaceId: workspace.id,
            conversationId: chanAll.id,
            notificationLevel: "mentions",
            createdAt: "2026-10-20T01:03:00Z",
          },
        ],
      });
    });
    expect(
      chatStore.getSnapshot().conversations[chanAll.id]?.notificationLevel,
    ).toBe("mentions");
  });
});
