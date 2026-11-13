import { Fragment, memo, useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent, RefObject } from "react";
import {
  Copy,
  CornerUpLeft,
  Link2,
  MoreHorizontal,
  Pencil,
  SmilePlus,
  Trash2,
} from "lucide-react";
import { api, ApiError } from "@teamie/api-client";
import type {
  Conversation,
  Member,
  Mention,
  Message,
} from "@teamie/api-client";
import { chatStore, isUserMentioned } from "./store";
import { errorText } from "./errors";
import { toast } from "./toast";
import type { LocalMessage } from "./store";
import { ReactionPicker } from "./chat/reaction-picker";
import { MentionInput } from "./mentions";
import { MessageAttachments } from "./attachments";
import { MessageBody } from "./markdown";
import { Avatar, Button, date, IconButton, Notice, time } from "./ui";
import { formatDateTime } from "./format-datetime";
import { ReactionChips } from "./chat/reaction-chips";
import { MessageSendStatus } from "./chat/message-send-status";
import { MessageActionSheet } from "./chat/message-action-sheet";
import { CollapsibleMessageBody } from "./chat/collapsible-body";
import { useLongPress } from "./chat/long-press";
import { messagePreviewText } from "./chat/plain-preview";
import { recordRecentReaction } from "./chat/emoji-recent";
import { prefersEnterToSend } from "./chat/keyboard-source";
import "./styles/reactions.css";
import "./styles/timeline.css";
import { AgentAvatar, AgentBadge } from "./agent-ui";
import { ICON } from "./icons";
import {
  ForwardedMessageCard,
  MessageCollaborationActions,
  MessageCollaborationIndicators,
} from "./chat/collaboration-ui";
import {
  ConversationBanners,
  ConversationEmptyStart,
  ConversationHeader,
  copyToClipboard,
  emptyMembers,
  messageGrouping,
  messageLink,
  OwnMessageReadReceipt,
  RetractMessageModal,
  useMessageActions,
} from "./chat/conversation-chrome";
export {
  ConversationBanners,
  ConversationEmptyStart,
  ConversationHeader,
  messageGrouping,
  messageLink,
  RetractMessageModal,
  useMessageActions,
};

export const MessageItem = memo(function MessageItem({
  message,
  showDate,
  compact,
  showOwner = true,
  previousId,
  nextId,
  replyTarget: target,
  workspaceId,
  conversationId,
  conversationType = "channel",
  userId,
  agentOwnerName,
  mentionMembers,
  allMembers = emptyMembers,
  mentionsEnabled,
  canEdit,
  canRetract,
  canReact = false,
  canViewReceipts = false,
  canForward = true,
  canPin = true,
  canSave = true,
  canMarkUnread = true,
  isPinned = false,
  isSaved = false,
  allowBroadcastMentions = false,
  allowHereMention = true,
  isLatestOwn = false,
  senderReadSeq = 0,
  showUnreadDivider,
  isRovingTarget,
  highlighted,
  actionsOpen,
  editing,
  editBody,
  editMentions,
  actionBusy,
  actionError,
  inputRef,
  setFocusedMessageId,
  setActionMessageId,
  focusMessage,
  locate,
  onEditSubmit,
  setEditBody,
  setEditMentions,
  setEditing,
  startEdit,
  onReply,
  onForward,
  onRetractRequest,
}: {
  message: LocalMessage;
  showDate: boolean;
  compact: boolean;
  /** Show 「由 X 创建」 for an Agent sender (first head of that sender's run). */
  showOwner?: boolean;
  previousId?: string;
  nextId?: string;
  /** Resolved reply target (reconciled cached/store/view versions). */
  replyTarget?: LocalMessage;
  workspaceId: string;
  conversationId: string;
  conversationType?: Conversation["type"];
  userId: string;
  agentOwnerName?: string;
  mentionMembers: Member[];
  allMembers?: Member[];
  mentionsEnabled: boolean;
  canEdit: boolean;
  canRetract: boolean;
  canReact?: boolean;
  canViewReceipts?: boolean;
  canForward?: boolean;
  canPin?: boolean;
  canSave?: boolean;
  canMarkUnread?: boolean;
  isPinned?: boolean;
  isSaved?: boolean;
  allowBroadcastMentions?: boolean;
  allowHereMention?: boolean;
  isLatestOwn?: boolean;
  senderReadSeq?: number;
  showUnreadDivider: boolean;
  isRovingTarget: boolean;
  highlighted: boolean;
  actionsOpen: boolean;
  /** The message being edited, only when it is this row. */
  editing: Message | null;
  editBody: string;
  editMentions: Mention[];
  actionBusy: boolean;
  actionError: string;
  inputRef: RefObject<HTMLTextAreaElement | null>;
  setFocusedMessageId: (id: string) => void;
  setActionMessageId: (
    value: string | ((current: string) => string),
  ) => void;
  focusMessage: (id: string) => void;
  locate: (id: string) => Promise<void>;
  onEditSubmit: (event: FormEvent) => Promise<void>;
  setEditBody: (value: string) => void;
  setEditMentions: (mentions: Mention[]) => void;
  setEditing: (message: Message | null) => void;
  startEdit: (message: Message) => void;
  onReply: (messageId: string) => void;
  onForward?: (message: Message) => void;
  onRetractRequest: (message: Message) => void;
}) {
  const actionTabIndex = isRovingTarget || actionsOpen ? 0 : -1;
  const [copyFeedback, setCopyFeedback] = useState<{
    ok: boolean;
    text: string;
  } | null>(null);
  const [pickerOpen, setPickerOpen] = useState(false);
  const [reactionBusy, setReactionBusy] = useState(false);
  const [sheetOpen, setSheetOpen] = useState(false);
  const isOwn = message.senderId === userId;
  // Rows addressed to me (mention, addressed broadcast, or reply to my message).
  const relevantToMe =
    !isOwn &&
    !message.retractedAt &&
    (isUserMentioned(message.mentions, userId) ||
      (!!target && !target.retractedAt && target.senderId === userId));
  const canOpenSheet = !message.retractedAt && !message.status && editing?.id !== message.id;
  const longPress = useLongPress<HTMLElement>(() => setSheetOpen(true), {
    enabled: canOpenSheet,
    // Interactive children keep their own touch behaviour (links, chips, buttons).
    ignore: (el) => !!el.closest("a, button, input, textarea, [role='dialog'], .message-reactions"),
  });
  const pickerWrapRef = useRef<HTMLDivElement>(null);
  const closePicker = useCallback(() => setPickerOpen(false), []);

  useEffect(() => {
    if (!copyFeedback) return;
    const timer = window.setTimeout(() => setCopyFeedback(null), 2400);
    return () => window.clearTimeout(timer);
  }, [copyFeedback]);

  async function toggleReaction(emoji: string) {
    if (reactionBusy || message.retractedAt || message.status) {
      return;
    }
    const existing = message.reactions?.find((item) => item.emoji === emoji);
    const reactedByMe = !!existing?.userIds.includes(userId);
    if (!reactedByMe && !canReact) {
      return;
    }
    setPickerOpen(false);
    const scope = chatStore.reactionScope(conversationId);
    if (!scope) return;
    setReactionBusy(true);
    try {
      const result = reactedByMe
        ? await api.deleteReaction(conversationId, message.id, emoji)
        : await api.putReaction(conversationId, message.id, emoji);
      if (chatStore.reactionScopeCurrent(scope)) {
        chatStore.setReactions(conversationId, message.id, result.reactions, result.reactionsVersion, scope);
        if (!reactedByMe) recordRecentReaction(userId, emoji);
      }
    } catch (e) {
      if (chatStore.reactionScopeCurrent(scope))
        toast.error(
          e instanceof ApiError && e.code === "reaction_limit"
            ? "每条消息最多 20 种表情回应，请选择已有的表情。"
            : `${reactedByMe ? "取消表情回应" : "表情回应"}失败：${errorText(e)}`,
        );
    } finally {
      setReactionBusy(false);
    }
  }

  async function copy(text: string, success: string) {
    try {
      await copyToClipboard(text);
      setCopyFeedback({ ok: true, text: success });
    } catch {
      setCopyFeedback({
        ok: false,
        text: "无法访问剪贴板，请手动选择内容复制。",
      });
    }
  }
  return (
    <Fragment>
      {showDate && (
        <div className="date-divider">
          <span>{date(message.createdAt)}</span>
        </div>
      )}
      {showUnreadDivider && (
        <div className="unread-divider">
          <span>以下是新消息</span>
        </div>
      )}
      <article
        id={`message-${message.id}`}
        data-seq={message.status ? undefined : message.seq}
        data-own={isOwn ? "true" : undefined}
        tabIndex={isRovingTarget ? 0 : -1}
        {...longPress}
        onFocus={(event) => {
          if (event.currentTarget.contains(event.target as Node)) {
            setFocusedMessageId(message.id);
            setActionMessageId((current) =>
              current && current !== message.id ? "" : current,
            );
          }
        }}
        onKeyDown={(event) => {
          if ((event.target as HTMLElement | null)?.closest('[role="dialog"], [role="alertdialog"]')) return;
          const tag = (event.target as HTMLElement | null)?.tagName;
          if (tag === "INPUT" || tag === "TEXTAREA") return;
          if (
            (event.key === "ArrowUp" || event.key === "ArrowDown") &&
            !event.shiftKey &&
            !event.altKey &&
            !event.metaKey &&
            !event.ctrlKey
          ) {
            event.preventDefault();
            const next = event.key === "ArrowUp" ? previousId : nextId;
            if (next) {
              setActionMessageId("");
              setFocusedMessageId(next);
              focusMessage(next);
            }
            return;
          }
          if (
            event.key === "Enter" &&
            event.target === event.currentTarget &&
            !message.retractedAt &&
            !message.status &&
            editing?.id !== message.id
          ) {
            event.preventDefault();
            setActionMessageId((current) =>
              current === message.id ? "" : message.id,
            );
            return;
          }
          if (event.key === "Escape") {
            event.preventDefault();
            event.stopPropagation();
            setPickerOpen(false);
            setActionMessageId("");
            inputRef.current?.focus();
          }
        }}
        className={`message ${highlighted ? "located" : ""} ${compact ? "compact" : ""} ${relevantToMe ? "relevant-to-me" : ""} ${message.status || ""} ${message.retractedAt ? "retracted" : ""} ${actionsOpen || pickerOpen ? "actions-open" : ""}`}
      >
        <div className="message-avatar">
          {!compact ? (
            message.senderKind === "agent" ? (
              <AgentAvatar seed={message.senderId} />
            ) : (
              <Avatar name={message.senderName} seed={message.senderId} />
            )
          ) : (
            <time
              className="compact-time"
              dateTime={message.createdAt}
              title={formatDateTime(message.createdAt, "full")}
            >
              {time(message.createdAt)}
            </time>
          )}
        </div>
        <div className="message-content">
          {!compact ? (
            <div className="message-meta">
              <b>{message.senderName}</b>
              {message.senderKind === "agent" && (
                <AgentBadge ownerName={agentOwnerName} compact={!showOwner} />
              )}
              {isOwn && <span className="you-label">你</span>}
              <time
                dateTime={message.createdAt}
                title={formatDateTime(message.createdAt, "full")}
              >
                {time(message.createdAt)}
              </time>
              <MessageCollaborationIndicators
                isPinned={isPinned}
                isSaved={isSaved}
              />
            </div>
          ) : (
            (isPinned || isSaved) && (
              <div className="message-meta">
                <MessageCollaborationIndicators
                  isPinned={isPinned}
                  isSaved={isSaved}
                />
              </div>
            )
          )}
          {!message.retractedAt && message.forwarded && (
            <ForwardedMessageCard
              forwarded={message.forwarded}
              conversationId={conversationId}
              workspaceId={workspaceId}
              userId={userId}
              onLocateMessage={(mid) => void locate(mid)}
            />
          )}
          {message.replyToId && (
            <button
              className="message-reply"
              onClick={() => void locate(message.replyToId!)}
            >
              <CornerUpLeft size={ICON.xs} />
              <b>{target?.senderName || "较早的消息"}</b>
              <span>
                {target?.retractedAt
                  ? "消息已撤回"
                  : messagePreviewText(target, "引用了一条较早的消息")}
              </span>
            </button>
          )}
          {editing?.id === message.id ? (
            <form className="edit-message" onSubmit={onEditSubmit}>
              <MentionInput
                autoFocus
                value={editBody}
                mentions={editMentions}
                onChange={(value, mentions) => {
                  setEditBody(value);
                  setEditMentions(mentions);
                }}
                members={mentionMembers}
                enabled={mentionsEnabled}
                allowBroadcastMentions={allowBroadcastMentions}
                allowHereMention={allowHereMention}
                currentUserId={userId}
                required={!editing.attachments?.length}
                aria-label="编辑消息内容"
                onKeyDown={(e) => {
                  if (e.key === "Escape") {
                    setEditing(null);
                    inputRef.current?.focus();
                  } else if (e.key === "Enter" && prefersEnterToSend(e)) {
                    e.preventDefault();
                    e.currentTarget.form?.requestSubmit();
                  }
                }}
              />
              <Notice>{actionError}</Notice>
              <div>
                <span>修改后会显示「已编辑」</span>
                <Button
                  type="button"
                  variant="secondary"
                  size="sm"
                  onClick={() => setEditing(null)}
                >
                  取消
                </Button>
                <Button
                  type="submit"
                  size="sm"
                  busy={actionBusy}
                  disabled={!editBody.trim() && !editing.attachments?.length}
                >
                  保存修改
                </Button>
              </div>
            </form>
          ) : (
            <CollapsibleMessageBody
              contentKey={`${message.retractedAt ? "retracted" : message.body}\u0000${message.editedAt || ""}`}
            >
              {message.retractedAt ? (
                "这条消息已撤回"
              ) : (
                <MessageBody
                  body={message.body}
                  mentions={message.mentions}
                  userId={userId}
                />
              )}
              {message.editedAt && !message.retractedAt && (
                <span className="edited-label">（已编辑）</span>
              )}
            </CollapsibleMessageBody>
          )}
          {!message.retractedAt && !!message.attachments?.length && (
            <MessageAttachments
              conversationId={conversationId}
              attachments={message.attachments}
            />
          )}
          {!message.retractedAt && !!message.reactions?.length && (
            <ReactionChips
              reactions={message.reactions}
              userId={userId}
              members={allMembers}
              canReact={canReact}
              busy={reactionBusy}
              onToggle={(emoji) => void toggleReaction(emoji)}
            />
          )}
          {!message.retractedAt &&
            !message.status &&
            message.senderId === userId &&
            canViewReceipts && (
              <OwnMessageReadReceipt
                conversationId={conversationId}
                conversationType={conversationType}
                seq={message.seq}
                userId={userId}
                senderReadSeq={senderReadSeq}
                allMembers={allMembers}
                isLatestOwn={isLatestOwn}
              />
            )}
          <MessageSendStatus
            message={message}
            conversationId={conversationId}
            inputRef={inputRef}
          />
          {copyFeedback && (
            <span
              className={`send-status ${copyFeedback.ok ? "" : "failed-status"}`}
              role={copyFeedback.ok ? "status" : "alert"}
            >
              {copyFeedback.text}
            </span>
          )}
        </div>
        {!message.retractedAt &&
          !message.status &&
          editing?.id !== message.id && (
            <Fragment>
              <IconButton
                label={`消息操作：${message.senderName}`}
                className="message-more-button"
                tabIndex={actionTabIndex}
                aria-expanded={actionsOpen}
                onClick={() => setActionMessageId(actionsOpen ? "" : message.id)}
              >
                <MoreHorizontal size={ICON.md} />
              </IconButton>
              <div className="message-actions">
                {canReact && (
                  <div className="reaction-picker-wrap" ref={pickerWrapRef}>
                    <IconButton
                      label="添加表情回应"
                      tabIndex={actionTabIndex}
                      aria-expanded={pickerOpen}
                      aria-haspopup="menu"
                      disabled={reactionBusy}
                      onClick={() => setPickerOpen((open) => !open)}
                    >
                      <SmilePlus size={ICON.sm} />
                    </IconButton>
                    {pickerOpen && (
                      <ReactionPicker
                        anchorRef={pickerWrapRef}
                        reactions={message.reactions}
                        userId={userId}
                        onSelect={(emoji) => void toggleReaction(emoji)}
                        onClose={closePicker}
                      />
                    )}
                  </div>
                )}
                <IconButton
                  label={`回复 ${message.senderName}`}
                  tabIndex={actionTabIndex}
                  onClick={() => onReply(message.id)}
                >
                  <CornerUpLeft size={ICON.sm} />
                </IconButton>
                <MessageCollaborationActions
                  conversationId={conversationId}
                  message={message}
                  actionTabIndex={actionTabIndex}
                  isPinned={isPinned}
                  isSaved={isSaved}
                  canForward={canForward && Boolean(onForward)}
                  canPin={canPin}
                  canSave={canSave}
                  canMarkUnread={canMarkUnread}
                  onForward={(msg) => onForward?.(msg)}
                />
                {!!message.body && (
                  <IconButton
                    label="复制文本"
                    tabIndex={actionTabIndex}
                    onClick={() => void copy(message.body, "已复制消息文本")}
                  >
                    <Copy size={ICON.xs} />
                  </IconButton>
                )}
                <IconButton
                  label="复制消息链接"
                  tabIndex={actionTabIndex}
                  onClick={() =>
                    void copy(
                      messageLink(workspaceId, conversationId, message.id),
                      "已复制消息链接",
                    )
                  }
                >
                  <Link2 size={ICON.xs} />
                </IconButton>
                {isOwn && canEdit && (
                  <IconButton
                    label="编辑消息"
                    tabIndex={actionTabIndex}
                    onClick={() => startEdit(message)}
                  >
                    <Pencil size={ICON.xs} />
                  </IconButton>
                )}
                {isOwn && canRetract && (
                  <IconButton
                    label="撤回消息"
                    className="danger-hover"
                    tabIndex={actionTabIndex}
                    onClick={() => onRetractRequest(message)}
                  >
                    <Trash2 size={ICON.xs} />
                  </IconButton>
                )}
              </div>
            </Fragment>
          )}
        {sheetOpen && canOpenSheet && (
          <MessageActionSheet
            conversationId={conversationId}
            message={message}
            permissions={{
              canReact,
              canForward: canForward && Boolean(onForward),
              canPin,
              canSave,
              canMarkUnread,
              canEdit,
              canRetract,
              isPinned,
              isSaved,
              isOwn,
            }}
            onClose={() => setSheetOpen(false)}
            onReact={(emoji) => void toggleReaction(emoji)}
            onReply={() => onReply(message.id)}
            onForward={() => onForward?.(message)}
            onCopyText={() => void copy(message.body, "已复制消息文本")}
            onCopyLink={() =>
              void copy(messageLink(workspaceId, conversationId, message.id), "已复制消息链接")
            }
            onEdit={() => startEdit(message)}
            onRetract={() => onRetractRequest(message)}
          />
        )}
      </article>
    </Fragment>
  );
});

