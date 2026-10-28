import { Fragment, memo, useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent, RefObject } from "react";
import {
  Copy,
  CornerUpLeft,
  Link2,
  LoaderCircle,
  MoreHorizontal,
  Pencil,
  PencilLine,
  RefreshCw,
  SmilePlus,
  Trash2,
} from "lucide-react";
import { api } from "@teamie/api-client";
import type {
  Conversation,
  Member,
  Mention,
  Message,
} from "@teamie/api-client";
import { sendMessage } from "./chat";
import { chatStore } from "./store";
import type { LocalMessage } from "./store";
import { ReactionPicker } from "./chat/reaction-picker";
import { MentionInput } from "./mentions";
import { restoreComposerDraft } from "./composer-drafts";
import { MessageAttachments } from "./attachments";
import { MessageBody } from "./markdown";
import { Avatar, Button, date, IconButton, Notice, time } from "./ui";
import { AgentAvatar, AgentBadge } from "./agent-ui";
import { ICON } from "./icons";
import {
  ConversationBanners,
  ConversationEmptyStart,
  ConversationHeader,
  copyToClipboard,
  discardFailedMessage,
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
  onRetractRequest,
}: {
  message: LocalMessage;
  showDate: boolean;
  compact: boolean;
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
  onRetractRequest: (message: Message) => void;
}) {
  const actionTabIndex = isRovingTarget || actionsOpen ? 0 : -1;
  const [copyFeedback, setCopyFeedback] = useState<{
    ok: boolean;
    text: string;
  } | null>(null);
  const [pickerOpen, setPickerOpen] = useState(false);
  const [reactionBusy, setReactionBusy] = useState(false);
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
      }
    } catch {
      // Ignore transient reaction toggle failures
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
        data-own={message.senderId === userId ? "true" : undefined}
        tabIndex={isRovingTarget ? 0 : -1}
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
        className={`message ${highlighted ? "located" : ""} ${compact ? "compact" : ""} ${message.status || ""} ${message.retractedAt ? "retracted" : ""} ${actionsOpen || pickerOpen ? "actions-open" : ""}`}
      >
        <div className="message-avatar">
          {!compact ? (
            message.senderKind === "agent" ? (
              <AgentAvatar />
            ) : (
              <Avatar name={message.senderName} seed={message.senderId} />
            )
          ) : (
            <span className="compact-time">{time(message.createdAt)}</span>
          )}
        </div>
        <div className="message-content">
          {!compact && (
            <div className="message-meta">
              <b>{message.senderName}</b>
              {message.senderKind === "agent" && (
                <AgentBadge ownerName={agentOwnerName} />
              )}
              {message.senderId === userId && (
                <span className="you-label">你</span>
              )}
              <time dateTime={message.createdAt}>{time(message.createdAt)}</time>
            </div>
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
                  : target?.body ||
                    (target?.attachments?.length
                      ? `${target.attachments.length} 个文件`
                      : "引用了一条较早的消息")}
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
                required={!editing.attachments?.length}
                aria-label="编辑消息内容"
                onKeyDown={(e) => {
                  if (e.key === "Escape") {
                    setEditing(null);
                    inputRef.current?.focus();
                  } else if (
                    e.key === "Enter" &&
                    !e.shiftKey &&
                    !window.matchMedia("(pointer: coarse)").matches
                  ) {
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
                  className="secondary small-button"
                  onClick={() => setEditing(null)}
                >
                  取消
                </Button>
                <Button
                  type="submit"
                  className="small-button"
                  busy={actionBusy}
                  disabled={!editBody.trim() && !editing.attachments?.length}
                >
                  保存修改
                </Button>
              </div>
            </form>
          ) : (
            <div className="message-text message-body">
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
            </div>
          )}
          {!message.retractedAt && !!message.attachments?.length && (
            <MessageAttachments
              conversationId={conversationId}
              attachments={message.attachments}
            />
          )}
          {!message.retractedAt &&
            !!message.reactions &&
            message.reactions.length > 0 && (
              <div className="message-reactions" role="group" aria-label="表情回应">
                {message.reactions.map((reaction) => {
                  const active = reaction.userIds.includes(userId);
                  const reactorNames = reaction.userIds.map((uid) => {
                    if (uid === userId) return "你";
                    return (
                      allMembers.find((member) => member.userId === uid)
                        ?.displayName || "成员"
                    );
                  });
                  const tooltip = `${reactorNames.join("、")} 回应了 ${reaction.emoji}`;
                  return (
                    <button
                      key={reaction.emoji}
                      type="button"
                      className={`reaction-chip ${active ? "active" : ""}`}
                      disabled={(!canReact && !active) || reactionBusy}
                      aria-pressed={active}
                      title={tooltip}
                      aria-label={`${reaction.emoji} ${reaction.count} 人回应：${reactorNames.join("、")}`}
                      onClick={() => void toggleReaction(reaction.emoji)}
                    >
                      <span className="reaction-chip-emoji">{reaction.emoji}</span>
                      <span className="reaction-chip-count">{reaction.count}</span>
                    </button>
                  );
                })}
              </div>
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
          {message.status === "sending" && (
            <span className="send-status">
              <LoaderCircle size={ICON.xs} className="spin" /> 正在发送
            </span>
          )}
          {message.status === "failed" && (
            <span className="send-status failed-status">
              {message.error || "发送失败"}
              {!(message as LocalMessage).permanent && (
                <button
                  onClick={() =>
                    void sendMessage(
                      message as LocalMessage,
                      chatStore.generation,
                    )
                  }
                >
                  <RefreshCw size={ICON.xs} />
                  重试
                </button>
              )}
              {(message as LocalMessage).permanent && !!message.body && (
                <button
                  title={
                    message.attachments?.length
                      ? "恢复文字到输入框，附件需要重新添加"
                      : undefined
                  }
                  onClick={() => {
                    restoreComposerDraft(conversationId, message);
                    discardFailedMessage(conversationId, message);
                    inputRef.current?.focus();
                  }}
                >
                  <PencilLine size={ICON.xs} />
                  编辑并重发
                </button>
              )}
              <button
                onClick={() => discardFailedMessage(conversationId, message)}
              >
                <Trash2 size={ICON.xs} />
                删除
              </button>
            </span>
          )}
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
                {message.senderId === userId && canEdit && (
                  <IconButton
                    label="编辑消息"
                    tabIndex={actionTabIndex}
                    onClick={() => startEdit(message)}
                  >
                    <Pencil size={ICON.xs} />
                  </IconButton>
                )}
                {message.senderId === userId && canRetract && (
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
      </article>
    </Fragment>
  );
});

