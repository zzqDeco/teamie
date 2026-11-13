import { Fragment, useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import {
  ArrowUp,
  AtSign,
  Bell,
  BellOff,
  Blocks,
  Check,
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
import { errorText } from "../errors";
import { chatStore, effectiveNotificationLevel } from "../store";
import type { LocalMessage } from "../store";
import {
  formatPresenceLabel,
  presenceClassName,
  useUserPresence,
} from "../engagement-store";
import { trimMentionDraft } from "../mentions";
import { sameMentions } from "../composer-drafts";
import { Button, IconButton, Modal, ModalCloseButton, Notice } from "../ui";
import { ICON } from "../icons";
import { toast } from "../toast";
import { AgentAvailability } from "../agent-ui";
import type { Participant } from "../participants";
import { MobileNavButton } from "../shell/page-header";
import { handleRovingKeys } from "./roving-keys";
import { messagePreviewText } from "./plain-preview";
import { sameLocalDay } from "../format-datetime";
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

  useEffect(() => {
    if (!actionMessageId) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") setActionMessageId("");
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [actionMessageId]);

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

export { copyToClipboard } from "../clipboard";

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
    description: "私信、小组、@提及与回复",
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
  appsOpen = false,
  onMenu,
  onSearchOpen,
  onMembers,
  onApps,
  members,
  userId,
  canConfigureNotifications = false,
  presenceEnabled = false,
}: {
  conversation: Conversation;
  name: string;
  membersOpen: boolean;
  appsOpen?: boolean;
  onMenu: () => void;
  onSearchOpen: () => void;
  onMembers: () => void;
  onApps?: () => void;
  members?: Participant[];
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
        notifWrapRef.current?.querySelector<HTMLElement>("[aria-haspopup]")?.focus({ preventScroll: true });
      }
    };
    // Focus the checked level so arrow keys start from the current choice.
    notifWrapRef.current
      ?.querySelector<HTMLElement>('[role="menuitemradio"][aria-checked="true"]')
      ?.focus({ preventScroll: true });
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
    } catch (e) {
      if (token === chatStore.generation) toast.error(`通知设置未保存：${errorText(e)}`);
    } finally {
      setNotifBusy(false);
    }
  }

  return (
    <header className="conversation-header">
      <MobileNavButton onClick={onMenu} />
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
            ? "频道"
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
              >
                <i
                  className={`presence-dot ${presenceClassName(peerPresence)}`}
                  aria-hidden="true"
                />
                <span className="sr-only">对方状态：</span>
                {formatPresenceLabel(peerPresence)}
              </span>
            </>
          )}
          {otherMember?.kind === "agent" && <AgentAvailability enabled={otherMember.agentEnabled} />}
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
                tabIndex={-1}
                onKeyDown={(event) => {
                  handleRovingKeys(event, '[role="menuitemradio"]');
                }}
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
            role="img"
            title={`会话通知：${notificationLevelLabel(level)}`}
            aria-label={`会话通知：${notificationLevelLabel(level)}`}
          >
            {level === "none" ? (
              <BellOff size={ICON.sm} aria-hidden="true" />
            ) : (
              <AtSign size={ICON.sm} aria-hidden="true" />
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
        {conversation.type === "group" && onApps && (
          <IconButton
            label="小组应用"
            onClick={onApps}
            aria-expanded={appsOpen}
          >
            <Blocks size={ICON.lg} />
          </IconButton>
        )}
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
          <Button variant="secondary" size="sm" onClick={onInvite}>
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
      busy={actionBusy}
      onClose={onClose}
    >
      <blockquote className="retract-quote">{messagePreviewText(retracting)}</blockquote>
      <Notice>{actionError}</Notice>
      <div className="modal-actions">
        <ModalCloseButton>保留消息</ModalCloseButton>
        <Button variant="danger" busy={actionBusy} onClick={onConfirm}>
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
  const showDate = !previous || !sameLocalDay(previous.createdAt, message.createdAt);
  const compact =
    !showDate &&
    !showUnreadDivider &&
    previous?.senderId === message.senderId &&
    new Date(message.createdAt).getTime() -
      new Date(previous.createdAt).getTime() <
      5 * 60_000 &&
    !message.replyToId &&
    !previous.retractedAt;
  // Agent ownership text appears once per run of the same sender, not on
  // every time-split group head (it stays in the badge tooltip).
  const showOwner = !previous || previous.senderId !== message.senderId;
  return { showDate, compact, showOwner };
}

export const emptyMembers: Member[] = [];

export { OwnMessageReadReceipt } from "./read-receipts";

// Memoized: rows receive only primitives and referentially stable values, so a
