import { Fragment } from "react";
import type { FormEvent, RefObject } from "react";
import {
  CornerUpLeft,
  LoaderCircle,
  MoreHorizontal,
  Pencil,
  RefreshCw,
  Trash2,
} from "lucide-react";
import { api } from "@teamie/api-client";
import type { Member, Mention, Message } from "@teamie/api-client";
import { sendMessage } from "./chat";
import { chatStore } from "./store";
import type { LocalMessage } from "./store";
import { MentionInput } from "./mentions";
import { MessageAttachments } from "./attachments";
import { MessageBody } from "./markdown";
import { Avatar, Button, date, IconButton, Notice, time } from "./ui";
import { AgentAvatar, AgentBadge } from "./agent-ui";
import { ICON } from "./icons";

export function MessageItem({
  message,
  index,
  messages,
  conversationId,
  userId,
  members,
  mentionMembers,
  mentionsEnabled,
  canEdit,
  canRetract,
  showDate,
  compact,
  firstUnread,
  entryReadSeq,
  target,
  isRovingTarget,
  highlightId,
  actionMessageId,
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
  index: number;
  messages: LocalMessage[];
  conversationId: string;
  userId: string;
  members: Member[];
  mentionMembers: Member[];
  mentionsEnabled: boolean;
  canEdit: boolean;
  canRetract: boolean;
  showDate: boolean;
  compact: boolean;
  firstUnread?: string;
  entryReadSeq: number;
  target?: LocalMessage;
  isRovingTarget: boolean;
  highlightId: string;
  actionMessageId: string;
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
  const actionTabIndex =
    isRovingTarget || actionMessageId === message.id ? 0 : -1;
  return (
    <Fragment>
      {showDate && (
        <div className="date-divider">
          <span>{date(message.createdAt)}</span>
        </div>
      )}
      {message.id === firstUnread && entryReadSeq > 0 && (
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
            const next =
              messages[event.key === "ArrowUp" ? index - 1 : index + 1];
            if (next) {
              setActionMessageId("");
              setFocusedMessageId(next.id);
              focusMessage(next.id);
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
            setActionMessageId("");
            inputRef.current?.focus();
          }
        }}
        className={`message ${highlightId === message.id ? "located" : ""} ${compact ? "compact" : ""} ${message.status || ""} ${message.retractedAt ? "retracted" : ""} ${actionMessageId === message.id ? "actions-open" : ""}`}
      >
        <div className="message-avatar">
          {!compact ? (
            message.senderKind === "agent" ? (
              <AgentAvatar />
            ) : (
              <Avatar name={message.senderName} seed={message.senderId} />
            )
          ) : (
            <span>{time(message.createdAt)}</span>
          )}
        </div>
        <div className="message-content">
          {!compact && (
            <div className="message-meta">
              <b>{message.senderName}</b>
              {message.senderKind === "agent" && (
                <AgentBadge
                  ownerName={
                    members.find((member) => member.userId === message.senderId)
                      ?.ownerName
                  }
                />
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
              <button
                onClick={() => {
                  for (const attachment of message.attachments || []) {
                    void api
                      .deletePendingAttachment(conversationId, attachment.id)
                      .catch(() => {});
                  }
                  chatStore.discardLocal(
                    conversationId,
                    message.clientMessageId,
                  );
                }}
              >
                <Trash2 size={ICON.xs} />
                删除
              </button>
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
                aria-expanded={actionMessageId === message.id}
                onClick={() =>
                  setActionMessageId(
                    actionMessageId === message.id ? "" : message.id,
                  )
                }
              >
                <MoreHorizontal size={ICON.md} />
              </IconButton>
              <div className="message-actions">
                <IconButton
                  label={`回复 ${message.senderName}`}
                  tabIndex={actionTabIndex}
                  onClick={() => onReply(message.id)}
                >
                  <CornerUpLeft size={ICON.sm} />
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
}
