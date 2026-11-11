import { AtSign, BellOff } from "lucide-react";
import type { AttentionUnreadDTO, Conversation } from "@teamie/api-client";
import { summarizeConversationUnread } from "./notification-model";
import type { ConversationUnreadSummary, NotificationProjection } from "./notification-model";
import { ICON } from "./icons";

type UnreadSnapshot = {
  notificationProjection?: NotificationProjection;
  manualUnreadByConversation?: Record<string, number>;
};

/**
 * {@link summarizeConversationUnread} that also honours a manual unread marker
 * held only in the collaboration store (for example right after 标为未读,
 * before the conversation record is refreshed).
 */
export function conversationUnreadSummary(
  conversation: Conversation,
  snapshot: UnreadSnapshot,
  userId: string,
  projection?: ReadonlyArray<AttentionUnreadDTO>,
): ConversationUnreadSummary {
  const manual = snapshot.manualUnreadByConversation?.[conversation.id];
  const effective =
    !conversation.manualUnreadSeq && typeof manual === "number" && manual > 0
      ? { ...conversation, manualUnreadSeq: manual }
      : conversation;
  return summarizeConversationUnread(effective, snapshot, userId, projection);
}

/**
 * The single unread indicator for a conversation row: muted icon, unread (or
 * channel `@`) count, or the mentions-only icon. Pass `count` instead of a
 * summary for plain counters such as the inbox's unread filter.
 */
export function UnreadBadge({
  summary,
  count,
  conversationType,
  showLevelIcons = true,
  showDot = false,
}: {
  summary?: ConversationUnreadSummary;
  count?: number;
  conversationType?: Conversation["type"];
  showLevelIcons?: boolean;
  /** Mark uncounted unread (ordinary channel traffic, manual markers) with a dot. */
  showDot?: boolean;
}) {
  if (!summary) {
    if (!count || count <= 0) return null;
    return (
      <span className="unread-badge" aria-label={`${count} 条未读`}>
        {count > 99 ? "99+" : count}
      </span>
    );
  }
  const mutedIcon =
    summary.muted && showLevelIcons ? (
      <BellOff size={ICON.xs} className="conversation-muted-icon" aria-label="已静音" role="img" />
    ) : null;
  if (summary.badgeCount > 0) {
    const badge = (
      <span
        className={`unread-badge ${summary.badgePrefix ? "mention-badge" : ""}`}
        aria-label={`${summary.badgeCount} 条未读`}
        title={
          conversationType === "channel" && summary.projectionStale
            ? "截至最近同步的未读提醒"
            : undefined
        }
      >
        {summary.badgePrefix}
        {summary.badgeCount > 99 ? "99+" : summary.badgeCount}
      </span>
    );
    return mutedIcon ? (
      <>
        {mutedIcon}
        {badge}
      </>
    ) : (
      badge
    );
  }
  const levelIcon =
    mutedIcon ||
    (summary.level === "mentions" && showLevelIcons ? (
      <AtSign size={ICON.xs} className="conversation-mentions-icon" aria-label="仅提及通知" role="img" />
    ) : null);
  const dot =
    summary.hasUnread && showDot ? (
      <i className="unread-dot" role="img" aria-label="有未读消息" />
    ) : null;
  if (levelIcon && dot) {
    return (
      <>
        {levelIcon}
        {dot}
      </>
    );
  }
  return levelIcon || dot;
}
