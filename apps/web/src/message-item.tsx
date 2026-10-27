import { Fragment, memo, useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent, RefObject } from "react";
import {
  ArrowLeft,
  ArrowUp,
  AtSign,
  Bell,
  BellOff,
  Check,
  Copy,
  CornerUpLeft,
  Hash,
  Link2,
  LoaderCircle,
  MessageCircle,
  MoreHorizontal,
  Pencil,
  PencilLine,
  RefreshCw,
  Search,
  SmilePlus,
  Trash2,
  Users,
} from "lucide-react";
import { api } from "@teamie/api-client";
import type {
  Conversation,
  Member,
  Mention,
  Message,
  NotificationLevel,
} from "@teamie/api-client";
import { errorText, sendMessage } from "./chat";
import { chatStore, effectiveNotificationLevel } from "./store";
import type { LocalMessage } from "./store";
import {
  COMMON_REACTION_EMOJIS,
  computeMessageReadSummary,
  formatPresenceLabel,
  useConversationReceipts,
  useUserPresence,
} from "./engagement-store";
import { MentionInput, trimMentionDraft } from "./mentions";
import { restoreComposerDraft, sameMentions } from "./composer-drafts";
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

/** Absolute deep link that opens a conversation located at one message. */
export function messageLink(
  workspaceId: string,
  conversationId: string,
  messageId: string,
) {
  return `${window.location.origin}/app/w/${encodeURIComponent(workspaceId)}/c/${encodeURIComponent(conversationId)}?message=${encodeURIComponent(messageId)}`;
}

async function copyToClipboard(text: string) {
  if (!navigator.clipboard?.writeText) throw new Error("clipboard unavailable");
  await navigator.clipboard.writeText(text);
}

function discardFailedMessage(conversationId: string, message: LocalMessage) {
  for (const attachment of message.attachments || []) {
    void api
      .deletePendingAttachment(conversationId, attachment.id)
      .catch(() => {});
  }
  chatStore.discardLocal(conversationId, message.clientMessageId);
}

const NOTIFICATION_LEVEL_OPTIONS: ReadonlyArray<{
  value: NotificationLevel;
  label: string;
  description: string;
}> = [
  {
    value: "all",
    label: "全部消息",
    description: "接收所有新消息提醒与未读徽标",
  },
  {
    value: "mentions",
    label: "仅 @提及与回复",
    description: "仅在被 @提及或回复时显示徽标与提醒",
  },
  {
    value: "none",
    label: "静音",
    description: "隐藏未读徽标、工作空间红点与桌面通知",
  },
];

function notificationLevelLabel(level: NotificationLevel): string {
  if (level === "mentions") return "仅 @提及与回复";
  if (level === "none") return "静音";
  return "全部消息";
}

export function ConversationHeader({
  conversation,
  name,
  membersOpen,
  onMenu,
  onSearchOpen,
  onMembers,
  members,
  userId,
  canConfigureNotifications = false,
  presenceEnabled = false,
}: {
  conversation: Conversation;
  name: string;
  membersOpen: boolean;
  onMenu: () => void;
  onSearchOpen: () => void;
  onMembers: () => void;
  members?: Member[];
  userId?: string;
  canConfigureNotifications?: boolean;
  presenceEnabled?: boolean;
}) {
  const [notifOpen, setNotifOpen] = useState(false);
  const [notifBusy, setNotifBusy] = useState(false);
  const notifWrapRef = useRef<HTMLDivElement>(null);
  const level = effectiveNotificationLevel(conversation);
  const otherMember =
    conversation.type === "direct" && members && userId
      ? members.find(
          (m) =>
            m.userId !== userId && conversation.memberIds.includes(m.userId),
        )
      : undefined;
  const otherHumanId =
    otherMember?.kind === "human" ? otherMember.userId : undefined;
  const peerPresence = useUserPresence(
    otherHumanId,
    !!presenceEnabled && !!otherHumanId,
  );

  useEffect(() => {
    setNotifOpen(false);
  }, [conversation.id]);

  useEffect(() => {
    if (!notifOpen) return;
    const onPointerDown = (event: PointerEvent) => {
      if (
        event.target instanceof Node &&
        !notifWrapRef.current?.contains(event.target)
      ) {
        setNotifOpen(false);
      }
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.stopPropagation();
        setNotifOpen(false);
      }
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown, true);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown, true);
    };
  }, [notifOpen]);

  async function selectNotificationLevel(next: NotificationLevel) {
    if (notifBusy) return;
    setNotifOpen(false);
    if (next === level) return;
    const token = chatStore.generation;
    setNotifBusy(true);
    try {
      const result = await api.setNotificationLevel(conversation.id, next);
      if (token === chatStore.generation) {
        chatStore.setNotificationLevel(
          conversation.id,
          result.notificationLevel,
        );
      }
    } catch {
      // Keep current level if request fails
    } finally {
      setNotifBusy(false);
    }
  }

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
          {presenceEnabled && otherHumanId && (
            <span
              className={`header-presence presence-${peerPresence}`}
              aria-label={`对方状态：${formatPresenceLabel(peerPresence)}`}
            >
              {" · "}
              <i
                className={`presence-dot presence-${peerPresence}`}
                aria-hidden="true"
              />
              {formatPresenceLabel(peerPresence)}
            </span>
          )}
        </span>
      </div>
      <div className="conversation-header-actions">
        {canConfigureNotifications ? (
          <div className="notification-level-control" ref={notifWrapRef}>
            <IconButton
              label={`会话通知设置（当前：${notificationLevelLabel(level)}）`}
              aria-haspopup="menu"
              aria-expanded={notifOpen}
              disabled={notifBusy}
              onClick={() => setNotifOpen((open) => !open)}
            >
              {level === "none" ? (
                <BellOff size={ICON.lg} />
              ) : level === "mentions" ? (
                <AtSign size={ICON.lg} />
              ) : (
                <Bell size={ICON.lg} />
              )}
            </IconButton>
            {notifOpen && (
              <div
                className="notification-level-menu"
                role="menu"
                aria-label="会话通知级别"
              >
                {NOTIFICATION_LEVEL_OPTIONS.map((option) => {
                  const active = level === option.value;
                  return (
                    <button
                      key={option.value}
                      type="button"
                      role="menuitemradio"
                      aria-checked={active}
                      className={`notification-level-option ${active ? "active" : ""}`}
                      onClick={() => void selectNotificationLevel(option.value)}
                    >
                      <div className="notification-level-option-text">
                        <b>{option.label}</b>
                        <span>{option.description}</span>
                      </div>
                      {active && <Check size={ICON.sm} aria-hidden="true" />}
                    </button>
                  );
                })}
              </div>
            )}
          </div>
        ) : level !== "all" ? (
          <span
            className="notification-level-readonly"
            title={`会话通知：${notificationLevelLabel(level)}`}
            aria-label={`会话通知：${notificationLevelLabel(level)}`}
          >
            {level === "none" ? (
              <BellOff size={ICON.sm} />
            ) : (
              <AtSign size={ICON.sm} />
            )}
          </span>
        ) : null}
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

/** Date divider and compact grouping relative to the previous row. */
export function messageGrouping(
  message: LocalMessage,
  previous: LocalMessage | undefined,
  showUnreadDivider = false,
) {
  const showDate =
    !previous ||
    new Date(previous.createdAt).toDateString() !==
      new Date(message.createdAt).toDateString();
  const compact =
    !showDate &&
    !showUnreadDivider &&
    previous?.senderId === message.senderId &&
    new Date(message.createdAt).getTime() -
      new Date(previous.createdAt).getTime() <
      5 * 60_000 &&
    !message.replyToId &&
    !previous.retractedAt;
  return { showDate, compact };
}

const emptyMembers: Member[] = [];

function OwnMessageReadReceipt({
  conversationId,
  conversationType,
  seq,
  userId,
  senderReadSeq,
  allMembers,
  isLatestOwn,
}: {
  conversationId: string;
  conversationType: Conversation["type"];
  seq: number;
  userId: string;
  senderReadSeq: number;
  allMembers: Member[];
  isLatestOwn: boolean;
}) {
  const receipts = useConversationReceipts(conversationId, true);
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLDivElement>(null);
  const summary = computeMessageReadSummary({
    receipts,
    seq,
    senderId: userId,
    senderReadSeq,
    conversationType,
    members: allMembers,
  });

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent) => {
      if (
        event.target instanceof Node &&
        !wrapRef.current?.contains(event.target)
      ) {
        setOpen(false);
      }
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.stopPropagation();
        setOpen(false);
      }
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown, true);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown, true);
    };
  }, [open]);

  if (!summary || !summary.label) return null;

  const visibilityClass = isLatestOwn ? "persistent" : "on-hover";
  const statusClass = summary.readCount > 0 ? "is-read" : "is-unread";

  if (conversationType === "group") {
    return (
      <div
        className={`message-read-receipt-wrap ${visibilityClass}`}
        ref={wrapRef}
      >
        <button
          type="button"
          className={`message-read-receipt is-interactive ${statusClass}`}
          aria-expanded={open}
          aria-label={`已读回执：${summary.label}`}
          onClick={() => setOpen((current) => !current)}
        >
          {summary.label}
        </button>
        {open && (
          <div
            className="read-receipt-popover"
            role="dialog"
            aria-label="群聊已读详情"
          >
            <div className="read-receipt-section">
              <b>已读 ({summary.readMembers.length})</b>
              {summary.readMembers.length > 0 ? (
                <ul>
                  {summary.readMembers.map((member) => (
                    <li key={member.userId}>{member.displayName}</li>
                  ))}
                </ul>
              ) : (
                <span>暂无成员已读</span>
              )}
            </div>
            <div className="read-receipt-section">
              <b>未读 ({summary.unreadMembers.length})</b>
              {summary.unreadMembers.length > 0 ? (
                <ul>
                  {summary.unreadMembers.map((member) => (
                    <li key={member.userId}>{member.displayName}</li>
                  ))}
                </ul>
              ) : (
                <span>全部成员已读</span>
              )}
            </div>
          </div>
        )}
      </div>
    );
  }

  return (
    <span
      className={`message-read-receipt ${visibilityClass} ${statusClass}`}
      aria-label={`已读状态：${summary.label}`}
    >
      {summary.label}
    </span>
  );
}

// Memoized: rows receive only primitives and referentially stable values, so a
// timeline re-render touches just the rows whose own inputs changed.
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

  useEffect(() => {
    if (!copyFeedback) return;
    const timer = window.setTimeout(() => setCopyFeedback(null), 2400);
    return () => window.clearTimeout(timer);
  }, [copyFeedback]);

  useEffect(() => {
    if (!pickerOpen) return;
    const onPointerDown = (event: PointerEvent) => {
      if (
        event.target instanceof Node &&
        !pickerWrapRef.current?.contains(event.target)
      ) {
        setPickerOpen(false);
      }
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.stopPropagation();
        setPickerOpen(false);
      }
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown, true);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown, true);
    };
  }, [pickerOpen]);

  async function toggleReaction(emoji: string) {
    if (!canReact || reactionBusy || message.retractedAt || message.status) {
      return;
    }
    setPickerOpen(false);
    const existing = message.reactions?.find((item) => item.emoji === emoji);
    const reactedByMe = !!existing?.userIds.includes(userId);
    const token = chatStore.generation;
    setReactionBusy(true);
    try {
      const result = reactedByMe
        ? await api.deleteReaction(conversationId, message.id, emoji)
        : await api.putReaction(conversationId, message.id, emoji);
      if (token === chatStore.generation) {
        chatStore.setReactions(conversationId, message.id, result.reactions);
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
                      disabled={!canReact || reactionBusy}
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
                      <div
                        className="reaction-picker-popover"
                        role="menu"
                        aria-label="选择表情回应"
                      >
                        {COMMON_REACTION_EMOJIS.map((emoji) => {
                          const active =
                            !!message.reactions
                              ?.find((item) => item.emoji === emoji)
                              ?.userIds.includes(userId);
                          return (
                            <button
                              key={emoji}
                              type="button"
                              role="menuitem"
                              className={`reaction-picker-emoji ${active ? "active" : ""}`}
                              aria-label={`表情回应 ${emoji}`}
                              onClick={() => void toggleReaction(emoji)}
                            >
                              {emoji}
                            </button>
                          );
                        })}
                      </div>
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
