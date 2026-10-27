import type { AttentionItem, AttentionUnreadDTO, Conversation, Message, NotificationLevel } from "@teamie/api-client";

export type NotificationProjection = {
  byConversation: Record<string, number>;
  stale: boolean;
};

export function effectiveNotificationLevel(conversation?: Pick<Conversation, "notificationLevel"> | null): NotificationLevel {
  const level = conversation?.notificationLevel;
  return level === "mentions" || level === "none" ? level : "all";
}

export function isRelevantMessage(conversation: Conversation, message: Message, userId: string, parent?: Message): boolean {
  if (message.senderId === userId || message.retractedAt) return false;
  return conversation.type !== "channel" || Boolean(
    message.mentions?.some((mention) => mention.userId === userId) ||
    (parent && parent.id === message.replyToId && parent.conversationId === message.conversationId && parent.senderId === userId && !parent.retractedAt),
  );
}

export type ConversationUnreadSummary = {
  level: NotificationLevel;
  muted: boolean;
  rawUnread: number;
  mentionUnread: number;
  hasUnread: boolean;
  badgeCount: number;
  badgePrefix: string;
  contributesToWorkspaceDot: boolean;
  contributesToUrgentCount: number;
  contributesToChannelUnreadDot: boolean;
  projectionKnown: boolean;
  projectionStale: boolean;
};

export function summarizeConversationUnread(
  conversation: Conversation,
  snapshot: { notificationProjection?: NotificationProjection },
  _userId: string,
  projection?: ReadonlyArray<AttentionUnreadDTO | AttentionItem>,
): ConversationUnreadSummary {
  const level = effectiveNotificationLevel(conversation);
  const rawUnread = Math.max(0, conversation.lastSeq - conversation.readSeq);
  const supplied = projection?.find((item): item is AttentionUnreadDTO => "unreadCount" in item && item.conversationId === conversation.id);
  const count = supplied?.unreadCount ?? snapshot.notificationProjection?.byConversation[conversation.id];
  const mentionUnread = rawUnread === 0 ? 0 : Math.max(0, count ?? 0);
  const relevantUnread = conversation.type === "channel" ? mentionUnread : rawUnread;
  const muted = level === "none";
  const hasUnread = !muted && (level === "all" ? rawUnread > 0 : relevantUnread > 0);
  const badgeCount = muted ? 0 : relevantUnread;
  return {
    level, muted, rawUnread, mentionUnread, hasUnread, badgeCount,
    badgePrefix: conversation.type === "channel" && badgeCount > 0 ? "@ " : "",
    contributesToWorkspaceDot: hasUnread,
    contributesToUrgentCount: badgeCount,
    contributesToChannelUnreadDot: conversation.type === "channel" && level === "all" && rawUnread > 0,
    projectionKnown: rawUnread === 0 || count !== undefined,
    projectionStale: !supplied && Boolean(snapshot.notificationProjection?.stale),
  };
}

// Reaction changes and preference changes do not invalidate the quantity projection.
export function notificationInputsChanged(
  before: { userId: string; conversations: Record<string, Conversation>; messages: Record<string, Message[]> },
  after: { userId: string; conversations: Record<string, Conversation>; messages: Record<string, Message[]> },
): boolean {
  if (before.userId !== after.userId) return true;
  const ids = Object.keys(after.conversations);
  if (Object.keys(before.conversations).length !== ids.length) return true;
  for (const id of ids) {
    const a = before.conversations[id], b = after.conversations[id];
    if (!a || a.workspaceId !== b.workspaceId || a.lastSeq !== b.lastSeq || a.readSeq !== b.readSeq || a.memberIds.join("\0") !== b.memberIds.join("\0")) return true;
  }
  for (const id of new Set([...Object.keys(before.messages), ...Object.keys(after.messages)])) {
    const a = before.messages[id], b = after.messages[id];
    if (a === b) continue;
    if (!a || !b || a.length !== b.length) return true;
    for (let i = 0; i < a.length; i++) {
      const x = a[i], y = b[i];
      if (x.id !== y.id || x.seq !== y.seq || x.senderId !== y.senderId || x.body !== y.body || x.replyToId !== y.replyToId || x.editedAt !== y.editedAt || x.retractedAt !== y.retractedAt || JSON.stringify(x.mentions) !== JSON.stringify(y.mentions)) return true;
    }
  }
  return false;
}
