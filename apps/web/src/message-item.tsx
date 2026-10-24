import { Fragment, useCallback, useRef, useState } from "react";
import type { FormEvent, RefObject } from "react";
import {
  ArrowLeft,
  ArrowUp,
  CornerUpLeft,
  Hash,
  LoaderCircle,
  MessageCircle,
  MoreHorizontal,
  Pencil,
  RefreshCw,
  Search,
  Trash2,
  Users,
} from "lucide-react";
import { api } from "@teamie/api-client";
import type {
  Conversation,
  Member,
  Mention,
  Message,
} from "@teamie/api-client";
import { errorText, sendMessage } from "./chat";
import { chatStore, reconcileMessages } from "./store";
import type { LocalMessage } from "./store";
import { MentionInput, trimMentionDraft } from "./mentions";
import { sameMentions } from "./composer-drafts";
import { MessageAttachments } from "./attachments";
import { MessageBody } from "./markdown";
import { Avatar, Button, date, IconButton, Modal, Notice, time } from "./ui";
import { AgentAvatar, AgentBadge } from "./agent-ui";
import { ICON } from "./icons";

export function useMessageActions(conversationId: string) {
  const editPending = useRef(false);
  const [actionMessageId, setActionMessageId] = useState("");
  const [editing, setEditing] = useState<Message | null>(null);
  const [editBody, setEditBody] = useState("");
  const [editMentions, setEditMentions] = useState<Mention[]>([]);
  const [retracting, setRetracting] = useState<Message | null>(null);
  const [actionBusy, setActionBusy] = useState(false);
  const [actionError, setActionError] = useState("");

  const startEdit = useCallback((message: Message) => {
    setActionMessageId("");
    setEditing(message);
    setEditBody(message.body);
    setEditMentions(message.mentions || []);
    setActionError("");
  }, []);

  const edit = useCallback(
    async (event: FormEvent) => {
      event.preventDefault();
      if (editPending.current) return;
      if (!editing || (!editBody.trim() && !editing.attachments?.length))
        return;
      const draft = trimMentionDraft(editBody, editMentions);
      if (
        draft.body === editing.body &&
        sameMentions(draft.mentions, editing.mentions || [])
      ) {
        setEditing(null);
        return;
      }
      const token = chatStore.generation;
      editPending.current = true;
      setActionBusy(true);
      setActionError("");
      try {
        const result = await api.edit(
          conversationId,
          editing.id,
          draft.body,
          draft.mentions,
        );
        if (token !== chatStore.generation) return;
        chatStore.merge([result.message]);
        setEditing(null);
      } catch (e) {
        if (token === chatStore.generation) setActionError(errorText(e));
      } finally {
        editPending.current = false;
        setActionBusy(false);
      }
    },
    [conversationId, editBody, editMentions, editing],
  );

  const retract = useCallback(async () => {
    if (!retracting) return;
    const token = chatStore.generation;
    setActionBusy(true);
    setActionError("");
    try {
      const result = await api.retract(conversationId, retracting.id);
      if (token !== chatStore.generation) return;
      chatStore.merge([result.message]);
      setRetracting(null);
    } catch (e) {
      if (token === chatStore.generation) setActionError(errorText(e));
    } finally {
      setActionBusy(false);
    }
  }, [conversationId, retracting]);

  const closeRetract = useCallback(() => setRetracting(null), []);
  const requestRetract = useCallback((msg: Message) => {
    setActionMessageId("");
    setRetracting(msg);
    setActionError("");
  }, []);

  return {
    actionMessageId,
    setActionMessageId,
    editing,
    setEditing,
    editBody,
    setEditBody,
    editMentions,
    setEditMentions,
    retracting,
    actionBusy,
    actionError,
    startEdit,
    edit,
    retract,
    closeRetract,
    requestRetract,
  };
}

export function ConversationHeader({
  conversation,
  name,
  membersOpen,
  onMenu,
  onSearchOpen,
  onMembers,
}: {
  conversation: Conversation;
  name: string;
  membersOpen: boolean;
  onMenu: () => void;
  onSearchOpen: () => void;
  onMembers: () => void;
}) {
  return (
    <header className="conversation-header">
      <IconButton
        label="打开对话列表"
        className="mobile-menu"
        onClick={onMenu}
      >
        <ArrowLeft size={ICON.lg} />
      </IconButton>
      <span className="conversation-symbol">
        {conversation.type === "channel" ? (
          <Hash size={ICON.xl} />
        ) : conversation.type === "group" ? (
          <Users size={ICON.lg} />
        ) : (
          <MessageCircle size={ICON.lg} />
        )}
      </span>
      <div>
        <h1>{name}</h1>
        <span>
          {conversation.type === "channel"
            ? "团队频道"
            : conversation.type === "direct"
              ? "私信"
              : "小组对话"}
        </span>
      </div>
      <div className="conversation-header-actions">
        <IconButton
          label="搜索会话"
          onClick={onSearchOpen}
          aria-haspopup="dialog"
        >
          <Search size={ICON.lg} />
        </IconButton>
        <IconButton
          label="查看会话成员"
          onClick={onMembers}
          aria-expanded={membersOpen}
        >
          <Users size={ICON.lg} />
        </IconButton>
      </div>
    </header>
  );
}

export function ConversationBanners({
  syncError,
  onSync,
  located,
  locating,
  onReturnToLatest,
  unreadBeforeWindow,
  unreadCount,
  onJumpFirstUnread,
  onMarkLatestRead,
}: {
  syncError: string;
  onSync: () => void;
  located: boolean;
  locating: boolean;
  onReturnToLatest: () => void;
  unreadBeforeWindow: boolean;
  unreadCount: number;
  onJumpFirstUnread: () => void;
  onMarkLatestRead: () => void;
}) {
  return (
    <Fragment>
      {syncError && (
        <div className="sync-banner" role="status">
          <RefreshCw size={ICON.xs} />
          <span>{syncError}</span>
          <button onClick={onSync}>立即重试</button>
        </div>
      )}
      {(located || locating) && (
        <div className="located-banner" role="status">
          <span>{locating ? "正在定位消息…" : "消息上下文"}</span>
          <button onClick={onReturnToLatest}>返回最新消息</button>
        </div>
      )}
      {unreadBeforeWindow && (
        <div className="unread-jump-banner" role="status">
          <span>
            {unreadCount > 99 ? "99+" : unreadCount} 条新消息，部分在更早的位置
          </span>
          <span className="unread-jump-actions">
            <button onClick={onJumpFirstUnread}>
              <ArrowUp size={ICON.xs} />
              跳到第一条未读
            </button>
            <button onClick={onMarkLatestRead}>全部标为已读</button>
          </span>
        </div>
      )}
    </Fragment>
  );
}

export function ConversationEmptyStart({
  conversation,
  name,
  isWorkspaceOwner,
  onInvite,
}: {
  conversation: Conversation;
  name: string;
  isWorkspaceOwner?: boolean;
  onInvite?: () => void;
}) {
  return (
    <div className="conversation-start">
      <span
        className={`conversation-start-icon ${conversation.type === "channel" ? "" : "mint"}`}
      >
        {conversation.type === "channel" ? (
          <Hash size={ICON.xl} />
        ) : (
          <MessageCircle size={ICON.xl} />
        )}
      </span>
      <h2>
        {conversation.type === "channel"
          ? `欢迎来到 # ${name}`
          : `你好，${name}`}
      </h2>
      <p>还没有消息，打个招呼吧。</p>
      {conversation.type === "channel" &&
        (isWorkspaceOwner && onInvite ? (
          <Button className="secondary small-button" onClick={onInvite}>
            邀请成员加入
          </Button>
        ) : isWorkspaceOwner === false ? (
          <span className="conversation-invite-hint">请联系空间创建者邀请</span>
        ) : null)}
    </div>
  );
}

export function RetractMessageModal({
  retracting,
  actionError,
  actionBusy,
  onClose,
  onConfirm,
}: {
  retracting: Message;
  actionError: string;
  actionBusy: boolean;
  onClose: () => void;
  onConfirm: () => void;
}) {
  return (
    <Modal
      title="撤回这条消息？"
      subtitle="撤回后，所有人都会看到一条撤回提示。"
      onClose={onClose}
    >
      <blockquote className="retract-quote">{retracting.body}</blockquote>
      <Notice>{actionError}</Notice>
      <div className="modal-actions">
        <Button className="secondary" onClick={onClose}>
          保留消息
        </Button>
        <Button className="danger" busy={actionBusy} onClick={onConfirm}>
          确认撤回
        </Button>
      </div>
    </Modal>
  );
}

export function MessageItem({
  message,
  index,
  messages,
  allMessages,
  resolvedTargets,
  conversationId,
  userId,
  members,
  mentionMembers,
  mentionsEnabled,
  canEdit,
  canRetract,
  firstUnread,
  entryReadSeq,
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
  allMessages: LocalMessage[];
  resolvedTargets: Record<string, Message>;
  conversationId: string;
  userId: string;
  members: Member[];
  mentionMembers: Member[];
  mentionsEnabled: boolean;
  canEdit: boolean;
  canRetract: boolean;
  firstUnread?: string;
  entryReadSeq: number;
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
  const previous = messages[index - 1];
  const showDate =
    !previous ||
    new Date(previous.createdAt).toDateString() !==
      new Date(message.createdAt).toDateString();
  const compact =
    !showDate &&
    previous?.senderId === message.senderId &&
    new Date(message.createdAt).getTime() -
      new Date(previous.createdAt).getTime() <
      5 * 60_000 &&
    !message.replyToId &&
    !previous.retractedAt;
  const replyCandidates: LocalMessage[] = [];
  if (message.replyToId) {
    const cachedTarget = resolvedTargets[message.replyToId];
    if (cachedTarget && cachedTarget.conversationId === conversationId)
      replyCandidates.push(cachedTarget);
    const storeTarget = allMessages.find((m) => m.id === message.replyToId);
    if (storeTarget) replyCandidates.push(storeTarget);
    const viewTarget = messages.find((m) => m.id === message.replyToId);
    if (viewTarget) replyCandidates.push(viewTarget);
  }
  const target = replyCandidates.length
    ? reconcileMessages([replyCandidates[0]], replyCandidates.slice(1))[0]
    : undefined;
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
