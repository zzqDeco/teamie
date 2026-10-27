import { Fragment, useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import {
  ArrowLeft,
  ArrowUp,
  AtSign,
  Bell,
  BellOff,
  Check,
  CheckCheck,
  Hash,
  MessageCircle,
  RefreshCw,
  Search,
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
import { errorText } from "../chat";
import { chatStore, effectiveNotificationLevel } from "../store";
import type { LocalMessage } from "../store";
import {
  computeMessageReadSummary,
  formatPresenceLabel,
  presenceClassName,
  useConversationReceipts,
  useUserPresence,
} from "../engagement-store";
import { trimMentionDraft } from "../mentions";
import { sameMentions } from "../composer-drafts";
import { Button, IconButton, Modal, Notice } from "../ui";
import { ICON } from "../icons";

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

export async function copyToClipboard(text: string) {
  if (!navigator.clipboard?.writeText) throw new Error("clipboard unavailable");
  await navigator.clipboard.writeText(text);
}

export function discardFailedMessage(conversationId: string, message: LocalMessage) {
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
    description: "显示全部未读，提醒相关消息",
  },
  {
    value: "mentions",
    label: "相关消息",
    description: "私聊、群聊、@提及与回复",
  },
  {
    value: "none",
    label: "静音",
    description: "隐藏未读徽标、工作空间红点与桌面通知",
  },
];

function notificationLevelLabel(level: NotificationLevel): string {
  if (level === "mentions") return "相关消息";
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
            <>
              <span className="header-presence-sep" aria-hidden="true">
                ·
              </span>
              <span
                className={`header-presence ${presenceClassName(peerPresence)}`}
                aria-label={`对方状态：${formatPresenceLabel(peerPresence)}`}
              >
                <i
                  className={`presence-dot ${presenceClassName(peerPresence)}`}
                  aria-hidden="true"
                />
                {formatPresenceLabel(peerPresence)}
              </span>
            </>
          )}
        </span>
      </div>
      <div className="conversation-header-actions">
        {canConfigureNotifications ? (
          <div className="notification-level-control" ref={notifWrapRef}>
            <IconButton
              label={`会话通知设置（当前：${notificationLevelLabel(level)}）`}
              className={level !== "all" ? "is-customized" : undefined}
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

export const emptyMembers: Member[] = [];

export function OwnMessageReadReceipt({
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
          {summary.readCount > 0 ? (
            <CheckCheck size={ICON.xs} aria-hidden="true" />
          ) : (
            <Check size={ICON.xs} aria-hidden="true" />
          )}
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
      {summary.readCount > 0 ? (
        <CheckCheck size={ICON.xs} aria-hidden="true" />
      ) : (
        <Check size={ICON.xs} aria-hidden="true" />
      )}
      {summary.label}
    </span>
  );
}

// Memoized: rows receive only primitives and referentially stable values, so a
