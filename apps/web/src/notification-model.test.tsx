// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { describe, expect, it } from "vitest";
import type { AttentionItem, Conversation, Message, Workspace } from "@teamie/api-client";
import { ChatStore, summarizeConversationUnread } from "./store";
import { computeMessageReadSummary } from "./engagement-store";
import { computeShellAttention } from "./shell-sidebar";
import { UnreadBadge } from "./unread-badge";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

const userId = "self";
const workspace: Workspace = { id: "w", name: "W", createdBy: userId, createdAt: "2026-10-27T00:00:00Z" };
function conversation(type: Conversation["type"], notificationLevel: Conversation["notificationLevel"]): Conversation {
  return { id: "c", workspaceId: workspace.id, name: "C", type, memberIds: [userId, "other"], lastSeq: 3, readSeq: 0, updatedAt: "2026-10-27T00:00:00Z", notificationLevel };
}
function message(seq: number): Message {
  return { id: `m${seq}`, conversationId: "c", senderId: "other", senderName: "Other", senderKind: "human", clientMessageId: `client${seq}`, body: "hello", seq, createdAt: "2026-10-27T00:00:00Z" };
}
function storeFor(c: Conversation): ChatStore {
  const store = new ChatStore();
  store.begin(userId);
  store.replaceConversations([c]);
  return store;
}

describe("notification contract regressions", () => {
  it.each((["channel", "direct", "group"] as const).flatMap((type) => (["all", "mentions", "none"] as const).map((level) => [type, level] as const)))("uses one unread model for %s / %s", (type, level) => {
    const c = { ...conversation(type, level), lastSeq: 11 };
    const store = storeFor(c);
    const projection = [{ conversationId: c.id, unreadCount: 1 }];
    const summary = summarizeConversationUnread(c, store.getSnapshot(), userId, projection);
    const expected = level === "none" ? 0 : type === "channel" ? 1 : 11;
    expect(summary.badgeCount).toBe(expected);
    expect(summary.contributesToUrgentCount).toBe(expected);
    expect(summary.hasUnread).toBe(level !== "none");
    expect(summary.contributesToWorkspaceDot).toBe(level !== "none");
    expect(summary.badgePrefix).toBe(type === "channel" && expected > 0 ? "@ " : "");
  });

  it("retains confirmed counts as stale through partial offline reads and restores them with their entities", () => {
    const values = new Map<string, string>();
    const storage = { getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => { values.set(key, value); }, removeItem: (key: string) => { values.delete(key); } };
    const c = conversation("channel", "mentions");
    const store = new ChatStore(storage);
    store.begin(userId);
    store.replaceConversations([c]);
    expect(store.getSnapshot().notificationProjection).toBeUndefined();
    expect(store.setAttentionProjection([{ conversationId: c.id, unreadCount: 2 }], { userId, generation: store.generation, epoch: store.notificationEpoch })).toBe(true);
    store.markRead(c.id, 1);
    expect(store.getSnapshot().notificationProjection).toEqual({ byConversation: { c: 2 }, stale: true });
    const restored = new ChatStore(storage);
    restored.begin(userId);
    expect(restored.getSnapshot().notificationProjection).toEqual({ byConversation: { c: 2 }, stale: true });
    expect(summarizeConversationUnread(restored.getSnapshot().conversations.c, restored.getSnapshot(), userId).projectionStale).toBe(true);
    restored.markRead(c.id, c.lastSeq);
    expect(summarizeConversationUnread(restored.getSnapshot().conversations.c, restored.getSnapshot(), userId).badgeCount).toBe(0);
    restored.replaceConversations([]);
    expect(restored.getSnapshot().notificationProjection?.byConversation).toEqual({});
    restored.begin("another-user");
    expect(restored.getSnapshot().notificationProjection).toBeUndefined();
  });
  it("uses caller-excluding channel receipt counts directly", () => {
    const result = computeMessageReadSummary({
      receipts: { conversationId: "c", eligibleCount: 1, readSeqCounts: [{ readSeq: 3, count: 1 }] },
      seq: 3, senderId: userId, senderReadSeq: 3, conversationType: "channel", members: [],
    });
    expect(result?.label).toBe("1 人已读");
  });

  it.each(["direct", "group"] as const)("retains ordinary %s unread under mentions because it is relevant by contract", (type) => {
    const c = conversation(type, "mentions");
    const store = storeFor(c);
    store.merge([message(1), message(2), message(3)]);
    expect(summarizeConversationUnread(c, store.getSnapshot(), userId).badgeCount).toBe(3);
  });

  it("shows no numeric channel badge for ordinary all-level unread", () => {
    const c = conversation("channel", "all");
    const store = storeFor(c);
    const summary = summarizeConversationUnread(c, store.getSnapshot(), userId);
    expect(summary.hasUnread).toBe(true);
    expect(summary.badgeCount).toBe(0);
  });

  it("does not leave muted unread in title when attention is a one-item page", () => {
    const c = conversation("channel", "none");
    const store = storeFor(c);
    const item: AttentionItem = { message: message(3), conversation: { id: c.id, workspaceId: c.workspaceId, name: c.name, type: c.type }, workspaceName: "W", reasons: ["mention"], isRead: false };
    const result = computeShellAttention({ workspaces: [workspace], snapshot: store.getSnapshot(), userId, attentionData: { unreadCount: 3, channelUnreadCount: 3, items: [item] } });
    expect(result.urgentCount).toBe(0);
  });

  it.each(
    (["direct", "group", "channel"] as const).flatMap((type) =>
      (["all", "mentions", "none"] as const).map((level) => [type, level] as const),
    ),
  )(
    "honors manualUnreadSeq across %s / %s and restores muted/quiet state once cleared",
    async (type, level) => {
      const base: Conversation = {
        ...conversation(type, level),
        lastSeq: 6,
        readSeq: 6,
      };
      const store = storeFor(base);
      // Seed a stale mention count from before readSeq caught up to ensure manual
      // markers on a fully-read channel never resurrect or invent @ counts.
      store.setAttentionProjection(
        [{ conversationId: base.id, unreadCount: 2 }],
        {
          userId,
          generation: store.generation,
          epoch: store.notificationEpoch,
        },
      );
      store.setManualUnread(base.id, 3); // effective ordinary unread = 6 - 3 + 1 = 4

      const snap = store.getSnapshot();
      const marked = snap.conversations[base.id];
      const summary = summarizeConversationUnread(marked, snap, userId);

      expect(summary.rawUnread).toBe(4);
      expect(summary.hasUnread).toBe(true);
      expect(summary.contributesToWorkspaceDot).toBe(true);
      expect(summary.badgeCount).toBe(type === "channel" ? 0 : 4);
      expect(summary.badgePrefix).toBe("");
      expect(summary.contributesToUrgentCount).toBe(
        level === "none" || type === "channel" ? 0 : 4,
      );
      expect(summary.contributesToChannelUnreadDot).toBe(
        type === "channel" && level !== "none",
      );

      const shellAttention = computeShellAttention({
        workspaces: [workspace],
        snapshot: snap,
        userId,
      });
      expect(shellAttention.urgentCount).toBe(
        level === "none" || type === "channel" ? 0 : 4,
      );
      expect(shellAttention.hasChannelUnread).toBe(
        type === "channel" && level !== "none",
      );

      // Verify UnreadBadge in sidebar mode (showLevelIcons=true, showDot=false)
      // and quick-switcher mode (showLevelIcons=false, showDot=true).
      const host = document.createElement("div");
      const root = createRoot(host);
      try {
        await act(async () => {
          root.render(
            <div>
              <div data-testid="sidebar">
                <UnreadBadge summary={summary} conversationType={type} />
              </div>
              <div data-testid="switcher">
                <UnreadBadge
                  summary={summary}
                  conversationType={type}
                  showLevelIcons={false}
                  showDot
                />
              </div>
            </div>,
          );
        });
        const sidebarEl = host.querySelector('[data-testid="sidebar"]')!;
        const switcherEl = host.querySelector('[data-testid="switcher"]')!;

        if (type === "channel") {
          expect(sidebarEl.querySelector(".unread-badge")).toBeNull();
          expect(
            Boolean(sidebarEl.querySelector(".conversation-muted-icon")),
          ).toBe(level === "none");
          expect(
            Boolean(sidebarEl.querySelector(".conversation-mentions-icon")),
          ).toBe(level === "mentions");
          expect(switcherEl.querySelector(".unread-badge")).toBeNull();
          expect(switcherEl.querySelector(".unread-dot")).not.toBeNull();
        } else {
          expect(sidebarEl.querySelector(".unread-badge")?.textContent).toBe("4");
          expect(
            Boolean(sidebarEl.querySelector(".conversation-muted-icon")),
          ).toBe(level === "none");
          expect(switcherEl.querySelector(".unread-badge")?.textContent).toBe("4");
          expect(switcherEl.querySelector(".unread-dot")).toBeNull();
        }

        // Clearing the marker restores normal notification-level state.
        store.markRead(base.id, base.lastSeq, null);
        const clearedSnap = store.getSnapshot();
        const clearedSummary = summarizeConversationUnread(
          clearedSnap.conversations[base.id],
          clearedSnap,
          userId,
        );
        expect(clearedSummary.rawUnread).toBe(0);
        expect(clearedSummary.hasUnread).toBe(false);
        expect(clearedSummary.badgeCount).toBe(0);
        expect(clearedSummary.contributesToWorkspaceDot).toBe(false);
        expect(clearedSummary.contributesToUrgentCount).toBe(0);
        expect(clearedSummary.contributesToChannelUnreadDot).toBe(false);

        await act(async () => {
          root.render(
            <div data-testid="switcher-cleared">
              <UnreadBadge
                summary={clearedSummary}
                conversationType={type}
                showLevelIcons={false}
                showDot
              />
            </div>,
          );
        });
        expect(
          host.querySelector('[data-testid="switcher-cleared"]')!.innerHTML,
        ).toBe("");
      } finally {
        await act(async () => root.unmount());
      }
    },
  );

  it.each(
    (["direct", "group", "channel"] as const).flatMap((type) =>
      (["all", "mentions", "none"] as const).map((level) => [type, level] as const),
    ),
  )(
    "combines manualUnreadSeq with unread cursor messages in %s / %s without inflating channel @ counts",
    (type, level) => {
      const base: Conversation = {
        ...conversation(type, level),
        lastSeq: 8,
        readSeq: 5,
      };
      const store = storeFor(base);
      store.setManualUnread(base.id, 3); // effective U = max(8-5, 8-3+1) = 6
      const snap = store.getSnapshot();
      const projection = [{ conversationId: base.id, unreadCount: 2 }];
      const summary = summarizeConversationUnread(
        snap.conversations[base.id],
        snap,
        userId,
        projection,
      );

      expect(summary.hasUnread).toBe(true);
      if (type === "channel") {
        expect(summary.badgeCount).toBe(level === "none" ? 0 : 2);
        expect(summary.badgePrefix).toBe(level === "none" ? "" : "@ ");
        expect(summary.contributesToUrgentCount).toBe(level === "none" ? 0 : 2);
        expect(summary.contributesToChannelUnreadDot).toBe(level !== "none");
      } else {
        expect(summary.badgeCount).toBe(6);
        expect(summary.badgePrefix).toBe("");
        expect(summary.contributesToUrgentCount).toBe(level === "none" ? 0 : 6);
        expect(summary.contributesToChannelUnreadDot).toBe(false);
      }
    },
  );

});
