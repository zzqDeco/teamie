import { useContext, useRef, useState } from "react";
import type { FormEvent } from "react";
import { Link, UNSAFE_NavigationContext, useInRouterContext } from "react-router-dom";
import { Bookmark, CornerUpRight, MailWarning, Pin as PinIcon, X } from "lucide-react";
import { api } from "@teamie/api-client";
import type { Conversation, ForwardedSnapshot, Member, Mention, Message } from "@teamie/api-client";
import { AgentAvatar, AgentBadge } from "../agent-ui";
import { MessageAttachments } from "../attachments";
import { conversationName } from "../composer-drafts";
import { errorText } from "../errors";
import { ICON } from "../icons";
import { MessageBody } from "../markdown";
import { MentionInput, MentionText, trimMentionDraft } from "../mentions";
import { chatStore, pinMessageId, savedItemConversationId, savedItemMessageId } from "../store";
import type { PinnedMessage, SavedMessage } from "../store";
import { toast } from "../toast";
import { Avatar, Button, date, IconButton, Modal, Notice, time } from "../ui";

export async function toggleMessagePin(conversationId: string, message: Message, isPinned: boolean) {
  const scope = chatStore.reactionScope(conversationId);
  if (!scope) return;
  try {
    if (isPinned) {
      const res = await api.unpinMessage(conversationId, message.id);
      if (!chatStore.reactionScopeCurrent(scope)) return;
      chatStore.applyPinUpdated({
        conversationId, messageId: message.id, action: "unpinned", pinned: false, pinsVersion: res?.pinsVersion,
      });
    } else {
      const res = await api.pinMessage(conversationId, message.id);
      if (!chatStore.reactionScopeCurrent(scope)) return;
      chatStore.applyPinUpdated({
        conversationId, messageId: message.id, action: "pinned", pinned: true,
        pin: res?.pin || {
          message, pinnedBy: chatStore.getSnapshot().userId, pinnedByName: "", pinnedAt: new Date().toISOString(),
        },
        pinsVersion: res?.pinsVersion,
      });
    }
  } catch (e) {
    if (chatStore.reactionScopeCurrent(scope)) {
      toast.error(`${isPinned ? "取消置顶" : "置顶"}失败：${errorText(e)}`);
    }
  }
}

export async function toggleMessageSave(conversationId: string, message: Message, isSaved: boolean) {
  const scope = chatStore.reactionScope(conversationId);
  if (!scope) return;
  try {
    if (isSaved) {
      await api.unsaveMessage(conversationId, message.id);
      if (!chatStore.reactionScopeCurrent(scope)) return;
      chatStore.applySavedUpdated({ conversationId, messageId: message.id, action: "unsaved", saved: false });
    } else {
      const res = await api.saveMessage(conversationId, message.id);
      if (!chatStore.reactionScopeCurrent(scope)) return;
      chatStore.applySavedUpdated({
        conversationId, messageId: message.id, action: "saved", saved: true,
        savedAt: res?.saved?.savedAt || new Date().toISOString(), message,
      });
    }
  } catch (e) {
    if (chatStore.reactionScopeCurrent(scope)) {
      toast.error(`${isSaved ? "取消收藏" : "收藏"}失败：${errorText(e)}`);
    }
  }
}

export async function markMessageAsUnread(conversationId: string, message: Message) {
  const scope = chatStore.reactionScope(conversationId);
  if (!scope) return;
  const prevSnap = chatStore.getSnapshot();
  const prevManualSeq =
    prevSnap.manualUnreadByConversation?.[conversationId] ??
    prevSnap.conversations[conversationId]?.manualUnreadSeq;
  const write = chatStore.beginManualUnreadWrite(conversationId);
  chatStore.setManualUnread(conversationId, message.seq);
  try {
    const res = await api.markConversationUnread(conversationId, message.seq);
    if (!chatStore.reactionScopeCurrent(scope) || !write.isLatest()) return;
    chatStore.setManualUnread(conversationId, res?.manualUnreadSeq ?? message.seq);
  } catch (e) {
    if (!chatStore.reactionScopeCurrent(scope) || !write.isLatest()) return;
    chatStore.setManualUnread(conversationId, write.rollbackSeq(prevManualSeq));
    toast.error(`标为未读失败：${errorText(e)}`);
  } finally {
    write.end();
  }
}

export function ForwardedMessageCard({
  forwarded,
  conversationId,
  workspaceId,
  userId,
  onLocateSource,
  onJumpToSource,
  onLocateMessage,
}: {
  forwarded: ForwardedSnapshot & { conversationId?: string; messageId?: string };
  conversationId: string;
  workspaceId?: string;
  userId: string;
  onLocateSource?: (conversationId: string, messageId: string) => void;
  onJumpToSource?: (conversationId: string, messageId: string) => void;
  onLocateMessage?: (messageId: string) => void;
}) {
  const inRouter = useInRouterContext();
  const navCtx = useContext(UNSAFE_NavigationContext);
  const sourceConvId =
    forwarded.sourceConversationId || forwarded.conversationId || "";
  const sourceMsgId =
    forwarded.sourceMessageId || forwarded.messageId || "";
  const targetConv = sourceConvId
    ? chatStore.getSnapshot().conversations[sourceConvId]
    : undefined;
  const canJump = Boolean(
    sourceConvId &&
      sourceMsgId &&
      targetConv &&
      (onJumpToSource ||
        onLocateSource ||
        (sourceConvId === conversationId ? onLocateMessage : true)),
  );
  function handleJumpToSource() {
    if (onJumpToSource) {
      onJumpToSource(sourceConvId, sourceMsgId);
      return;
    }
    if (onLocateSource) {
      onLocateSource(sourceConvId, sourceMsgId);
      return;
    }
    if (sourceConvId === conversationId) {
      onLocateMessage?.(sourceMsgId);
      return;
    }
    const wsId =
      targetConv?.workspaceId ||
      workspaceId ||
      chatStore.getSnapshot().conversations[conversationId]?.workspaceId ||
      "";
    if (!wsId) return;
    const path = `/app/w/${encodeURIComponent(wsId)}/c/${encodeURIComponent(sourceConvId)}?message=${encodeURIComponent(sourceMsgId)}`;
    if (inRouter && navCtx?.navigator) {
      navCtx.navigator.push(path);
    } else if (typeof window !== "undefined") {
      window.history.pushState(null, "", path);
    }
  }
  return (
    <div className="message-forwarded-card" data-testid="forwarded-card">
      <div className="message-forwarded-header">
        <CornerUpRight size={ICON.xs} />
        <span className="message-forwarded-prefix">转发自</span>
        <b className="message-forwarded-sender">{forwarded.senderName}</b>
        {forwarded.senderKind === "agent" && <AgentBadge />}
        {forwarded.createdAt && (
          <time dateTime={forwarded.createdAt}>{time(forwarded.createdAt)}</time>
        )}
        {canJump && (
          <button
            type="button"
            className="message-forwarded-jump"
            onClick={handleJumpToSource}
          >
            查看原消息
          </button>
        )}
      </div>
      {forwarded.body && (
        <div className="message-forwarded-body">
          <MessageBody body={forwarded.body} userId={userId} />
        </div>
      )}
      {!!forwarded.attachments?.length && (
        <div className="message-forwarded-attachments">
          <MessageAttachments
            conversationId={conversationId}
            attachments={forwarded.attachments}
          />
        </div>
      )}
    </div>
  );
}

export function ForwardMessageModal({
  message,
  currentConversation,
  conversations,
  members,
  userId,
  mentionsEnabled,
  allowBroadcastMentions = false,
  isWorkspaceOwner,
  allowHereMention = true,
  onClose,
}: {
  message: Message;
  currentConversation: Conversation;
  conversations: Conversation[];
  members: Member[];
  userId: string;
  mentionsEnabled: boolean;
  allowBroadcastMentions?: boolean;
  isWorkspaceOwner?: boolean;
  allowHereMention?: boolean;
  onClose: () => void;
}) {
  const sameWorkspace = conversations.filter(
    (c) => c.workspaceId === currentConversation.workspaceId,
  );
  const targetOptions =
    sameWorkspace.length > 0 ? sameWorkspace : [currentConversation];
  const [targetId, setTargetId] = useState(
    targetOptions[0]?.id || currentConversation.id,
  );
  const [comment, setComment] = useState("");
  const [mentions, setMentions] = useState<Mention[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const pendingAttemptRef = useRef<{
    key: string;
    clientMessageId: string;
  } | null>(null);

  const targetConv =
    targetOptions.find((c) => c.id === targetId) || currentConversation;
  const targetMembers = members.filter((m) =>
    targetConv.memberIds.includes(m.userId),
  );

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!targetId || busy) return;
    const scope = chatStore.reactionScope(targetId);
    if (!scope) return;
    setBusy(true);
    setError("");
    try {
      const trimmed = trimMentionDraft(comment, mentions);
      const attemptKey = `${message.id}:${targetId}:${comment}`;
      let clientMessageId: string;
      if (pendingAttemptRef.current?.key === attemptKey) {
        clientMessageId = pendingAttemptRef.current.clientMessageId;
      } else {
        clientMessageId = crypto.randomUUID();
        pendingAttemptRef.current = { key: attemptKey, clientMessageId };
      }
      const response = await api.send(targetId, {
        clientMessageId,
        body: trimmed.body,
        ...(trimmed.mentions.length ? { mentions: trimmed.mentions } : {}),
        forwardOf: {
          conversationId: message.conversationId,
          messageId: message.id,
        },
      });
      if (!chatStore.reactionScopeCurrent(scope)) return;
      if (response?.message) chatStore.merge([response.message]);
      onClose();
    } catch (e) {
      if (chatStore.reactionScopeCurrent(scope)) setError(errorText(e));
    } finally {
      if (chatStore.reactionScopeCurrent(scope)) setBusy(false);
    }
  }

  return (
    <Modal
      title="转发消息"
      subtitle="选择同一工作空间内的目标对话，并可附上留言。"
      onClose={onClose}
    >
      <form className="forward-modal-form" onSubmit={(e) => void submit(e)}>
        <label className="forward-field">
          <span>目标对话</span>
          <select
            aria-label="目标对话"
            value={targetId}
            onChange={(e) => {
              setTargetId(e.target.value);
              setMentions([]);
            }}
          >
            {targetOptions.map((conv) => (
              <option key={conv.id} value={conv.id}>
                {conv.type === "channel" ? "# " : ""}
                {conversationName(conv, userId, members)}
              </option>
            ))}
          </select>
        </label>
        <div className="forward-preview-box">
          <div className="forward-preview-meta">
            <b>{message.senderName}</b>
            {message.senderKind === "agent" && <AgentBadge />}
            <time dateTime={message.createdAt}>{time(message.createdAt)}</time>
          </div>
          <p className="forward-preview-snippet">
            {message.body ||
              (message.attachments?.length
                ? `[${message.attachments.length} 个文件]`
                : message.forwarded
                  ? `[转发自 ${message.forwarded.senderName}]`
                  : "")}
          </p>
        </div>
        <div className="forward-field">
          <span>附加留言（可选）</span>
          <MentionInput
            value={comment}
            mentions={mentions}
            onChange={(body, nextMentions) => {
              setComment(body);
              setMentions(nextMentions);
            }}
            members={targetMembers}
            enabled={mentionsEnabled}
            allowBroadcastMentions={
              allowBroadcastMentions &&
              targetConv.type !== "direct" &&
              (targetConv as { kind?: string }).kind !== "dm" &&
              (targetConv.type !== "channel" ||
                (isWorkspaceOwner ??
                  members.some((m) => m.userId === userId && m.role === "owner")))
            }
            allowHereMention={allowHereMention}
            placeholder="添加留言（可选）"
            aria-label="转发留言"
          />
        </div>
        <Notice>{error}</Notice>
        <div className="modal-actions">
          <Button type="button" variant="secondary" onClick={onClose}>
            取消
          </Button>
          <Button type="submit" busy={busy} disabled={!targetId}>
            确认转发
          </Button>
        </div>
      </form>
    </Modal>
  );
}

export function PinnedMessagesBar({
  conversationId,
  pins,
  onLocate,
}: {
  conversationId: string;
  pins: PinnedMessage[];
  canPin?: boolean;
  onLocate: (messageId: string) => void;
}) {
  const [open, setOpen] = useState(false);
  if (!pins.length) return null;
  const latestPin = pins[0];
  const latestMsg = latestPin.message;
  const latestSnippet =
    latestMsg?.body ||
    (latestMsg?.attachments?.length
      ? `[${latestMsg.attachments.length} 个文件]`
      : latestMsg?.forwarded
        ? `[转发自 ${latestMsg.forwarded.senderName}]`
        : "查看置顶消息");

  return (
    <div className="pinned-messages-bar">
      <div className="pinned-messages-summary">
        <button
          type="button"
          className="pinned-messages-toggle"
          aria-expanded={open}
          onClick={() => setOpen((v) => !v)}
        >
          <PinIcon size={ICON.xs} />
          <span className="pinned-messages-count">
            置顶消息 ({pins.length})
          </span>
          <span className="pinned-messages-latest">
            {latestMsg?.senderName ? `${latestMsg.senderName}: ` : ""}
            {latestSnippet}
          </span>
        </button>
      </div>
      {open && (
        <div
          className="pinned-messages-panel"
          role="region"
          aria-label="置顶消息列表"
        >
          <div className="pinned-messages-panel-header">
            <b>置顶消息 ({pins.length})</b>
            <IconButton label="关闭置顶列表" onClick={() => setOpen(false)}>
              <X size={ICON.sm} />
            </IconButton>
          </div>
          <ul className="pinned-messages-list">
            {pins.map((pin) => {
              const msgId = pinMessageId(pin);
              const msg = pin.message;
              const snippet =
                msg?.body ||
                (msg?.attachments?.length
                  ? `[${msg.attachments.length} 个文件]`
                  : msg?.forwarded
                    ? `[转发自 ${msg.forwarded.senderName}]`
                    : "置顶消息");
              return (
                <li key={msgId} className="pinned-message-item">
                  <button
                    type="button"
                    className="pinned-message-jump"
                    onClick={() => {
                      setOpen(false);
                      onLocate(msgId);
                    }}
                  >
                    <div className="pinned-message-meta">
                      <b>{msg?.senderName || pin.pinnedByName || "成员"}</b>
                      {msg?.senderKind === "agent" && <AgentBadge />}
                      <time dateTime={msg?.createdAt || pin.pinnedAt}>
                        {time(msg?.createdAt || pin.pinnedAt)}
                      </time>
                    </div>
                    <p className="pinned-message-snippet">{snippet}</p>
                  </button>
                  <button
                    type="button"
                    className="pinned-message-unpin"
                    aria-label={`取消置顶 ${msg?.senderName || ""}`.trim()}
                    onClick={() =>
                      void toggleMessagePin(
                        conversationId,
                        msg || {
                          id: msgId,
                          clientMessageId: msgId,
                          conversationId,
                          senderId: pin.pinnedBy,
                          senderName: pin.pinnedByName,
                          senderKind: "human",
                          seq: 0,
                          body: "",
                          createdAt: pin.pinnedAt,
                        },
                        true,
                      )
                    }
                  >
                    取消置顶
                  </button>
                </li>
              );
            })}
          </ul>
        </div>
      )}
    </div>
  );
}

export function ManualUnreadBanner({
  unreadCount,
  onMarkRead,
}: {
  unreadCount: number;
  onMarkRead: () => void;
}) {
  return (
    <div className="unread-jump-banner manual-unread-banner" role="status">
      <span>
        已标记为未读
        {unreadCount > 0 ? `（${unreadCount} 条未读消息）` : ""}
      </span>
      <span className="unread-jump-actions">
        <button type="button" onClick={onMarkRead}>
          标为已读
        </button>
      </span>
    </div>
  );
}

export function MessageCollaborationIndicators({
  isPinned,
  isSaved,
}: {
  isPinned: boolean;
  isSaved: boolean;
}) {
  if (!isPinned && !isSaved) return null;
  return (
    <span className="message-collab-indicators">
      {isPinned && (
        <span className="message-pin-badge">
          <PinIcon size={ICON.xs} /> 已置顶
        </span>
      )}
      {isSaved && (
        <span className="message-saved-badge">
          <Bookmark size={ICON.xs} /> 已收藏
        </span>
      )}
    </span>
  );
}

export function MessageCollaborationActions({
  conversationId,
  message,
  actionTabIndex,
  isPinned,
  isSaved,
  canForward,
  canPin,
  canSave,
  canMarkUnread,
  onForward,
}: {
  conversationId: string;
  message: Message;
  actionTabIndex: number;
  isPinned: boolean;
  isSaved: boolean;
  canForward: boolean;
  canPin: boolean;
  canSave: boolean;
  canMarkUnread: boolean;
  onForward: (message: Message) => void;
}) {
  return (
    <>
      {canForward && (
        <IconButton
          label="转发"
          tabIndex={actionTabIndex}
          onClick={() => onForward(message)}
        >
          <CornerUpRight size={ICON.xs} />
        </IconButton>
      )}
      {(canPin || isPinned) && (
        <IconButton
          label={isPinned ? "取消置顶" : "置顶"}
          tabIndex={actionTabIndex}
          onClick={() =>
            void toggleMessagePin(conversationId, message, isPinned)
          }
        >
          <PinIcon size={ICON.xs} />
        </IconButton>
      )}
      {(canSave || isSaved) && (
        <IconButton
          label={isSaved ? "取消收藏" : "收藏"}
          tabIndex={actionTabIndex}
          onClick={() =>
            void toggleMessageSave(conversationId, message, isSaved)
          }
        >
          <Bookmark size={ICON.xs} />
        </IconButton>
      )}
      {canMarkUnread && (
        <IconButton
          label="标为未读"
          tabIndex={actionTabIndex}
          onClick={() => void markMessageAsUnread(conversationId, message)}
        >
          <MailWarning size={ICON.xs} />
        </IconButton>
      )}
    </>
  );
}

export function SavedMessagesList({
  items,
  loading,
  error,
  userId,
  hasMore = false,
  loadingMore = false,
  onLoadMore,
}: {
  items: SavedMessage[];
  loading: boolean;
  error: string;
  userId: string;
  canSave?: boolean;
  hasMore?: boolean;
  loadingMore?: boolean;
  onLoadMore?: () => void;
}) {
  const snapConvs = chatStore.getSnapshot().conversations;
  const hasConvs = Object.keys(snapConvs).length > 0;
  const visibleItems = items.filter((item) => {
    const cid = savedItemConversationId(item);
    return !hasConvs || Boolean(snapConvs[cid]);
  });
  return (
    <div className="attention-scroll" aria-busy={loading}>
      {loading && !visibleItems.length && <p className="attention-empty" role="status">正在加载…</p>}
      {error && <Notice>{error}</Notice>}
      {!loading && !error && !visibleItems.length && <p className="attention-empty">暂无收藏消息</p>}
      <ol className="attention-list" aria-label="收藏消息">
        {visibleItems.map((item) => {
          const msgId = savedItemMessageId(item);
          const convId = savedItemConversationId(item);
          const msg = item.message;
          const conv = item.conversation;
          const workspaceId = conv?.workspaceId || snapConvs[convId]?.workspaceId || "";
          return (
            <li key={msgId} className="attention-saved-row">
              <Link
                className="attention-item"
                to={`/app/w/${encodeURIComponent(workspaceId)}/c/${encodeURIComponent(convId)}?message=${encodeURIComponent(msgId)}`}
              >
                {msg?.senderKind === "agent" ? (
                  <AgentAvatar />
                ) : (
                  <Avatar name={msg?.senderName || "成员"} seed={msg?.senderId || msgId} />
                )}
                <div className="attention-item-content">
                  <div className="attention-item-meta">
                    <b>{msg?.senderName || "成员"}</b>
                    {msg?.senderKind === "agent" && <AgentBadge />}
                    <span>
                      <Bookmark size={ICON.xs} /> 已收藏
                    </span>
                    <time dateTime={msg?.createdAt || item.savedAt}>
                      {date(msg?.createdAt || item.savedAt)} {time(msg?.createdAt || item.savedAt)}
                    </time>
                  </div>
                  <p className="attention-item-body">
                    {msg?.body ? (
                      <MentionText body={msg.body} mentions={msg.mentions} userId={userId} />
                    ) : msg?.forwarded ? (
                      `[转发自 ${msg.forwarded.senderName}] ${msg.forwarded.body || ""}`
                    ) : (
                      `${msg?.attachments?.length || 0} 个文件`
                    )}
                  </p>
                  <span className="attention-source">
                    {item.workspaceName ? `${item.workspaceName} / ` : ""}
                    {conv?.name || "对话"}
                  </span>
                </div>
              </Link>
              <button
                type="button"
                className="attention-unsave-button"
                aria-label="取消收藏"
                onClick={() =>
                  void toggleMessageSave(
                    convId,
                    msg || {
                      id: msgId,
                      clientMessageId: msgId,
                      conversationId: convId,
                      senderId: "",
                      senderName: "",
                      senderKind: "human",
                      seq: 0,
                      body: "",
                      createdAt: item.savedAt,
                    },
                    true,
                  )
                }
              >
                取消收藏
              </button>
            </li>
          );
        })}
      </ol>
      {hasMore && onLoadMore && (
        <Button
          className="secondary attention-more"
          busy={loadingMore}
          disabled={loading || loadingMore}
          onClick={onLoadMore}
        >
          加载更多
        </Button>
      )}
    </div>
  );
}
