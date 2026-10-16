import { Fragment, useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent, KeyboardEvent } from "react";
import {
  ArrowDown,
  ArrowUp,
  Check,
  CornerUpLeft,
  Hash,
  KeyRound,
  LoaderCircle,
  MessageCircle,
  Pencil,
  RefreshCw,
  SendHorizontal,
  Trash2,
  Users,
  X,
} from "lucide-react";
import { api } from "@teamie/api-client";
import type { Agent, Conversation, Member, Message } from "@teamie/api-client";
import { useAuth } from "./auth";
import { errorText, sendMessage, useChat, useChatState } from "./chat";
import { chatStore } from "./store";
import type { LocalMessage } from "./store";
import { Avatar, Button, date, IconButton, Modal, Notice, time } from "./ui";
import { AgentAvatar, AgentBadge } from "./agent-ui";

export function conversationName(
  conversation: Conversation,
  userId: string,
  members: Member[],
) {
  if (conversation.name) return conversation.name;
  return (
    conversation.memberIds
      .filter((id) => id !== userId)
      .map(
        (id) => members.find((m) => m.userId === id)?.displayName || "团队伙伴",
      )
      .join("、") || "私聊"
  );
}
export function ConversationView({
  conversation,
  members,
  agents = [],
  onMembers,
  onMenu,
}: {
  conversation: Conversation;
  members: Member[];
  agents?: Agent[];
  onMembers: () => void;
  onMenu: () => void;
}) {
  const { user } = useAuth();
  const { data, syncError, sync, sendTyping, typing } = useChat();
  const snapshot = useChatState();
  const messages = snapshot.messages[conversation.id] || [];
  const name = conversationName(conversation, user!.id, members);
  const waitingAgents = agents.filter(
    (agent) =>
      conversation.memberIds.includes(agent.principalId) &&
      agent.enabled &&
      agent.runtimeState === "waiting_configuration",
  );
  const [loading, setLoading] = useState(true),
    [hasMore, setHasMore] = useState(false),
    [loadingOlder, setLoadingOlder] = useState(false),
    [error, setError] = useState("");
  const [body, setBody] = useState(""),
    [reply, setReply] = useState<Message | null>(null),
    [editing, setEditing] = useState<Message | null>(null),
    [editBody, setEditBody] = useState("");
  const [retracting, setRetracting] = useState<Message | null>(null),
    [actionBusy, setActionBusy] = useState(false),
    [actionError, setActionError] = useState("");
  const [atBottom, setAtBottom] = useState(true),
    [visible, setVisible] = useState(document.visibilityState === "visible");
  const scrollRef = useRef<HTMLDivElement>(null),
    inputRef = useRef<HTMLTextAreaElement>(null),
    initialScroll = useRef(true),
    lastRead = useRef(conversation.readSeq),
    lastTyping = useRef(0);
  const generation = chatStore.generation;
  const fetchLatest = useCallback(async () => {
    const token = chatStore.generation;
    setLoading(true);
    setError("");
    try {
      const page = await api.messages(conversation.id);
      if (token !== chatStore.generation) return;
      chatStore.merge(page.messages);
      setHasMore(page.hasMore);
    } catch (e) {
      if (token === chatStore.generation) setError(errorText(e));
    } finally {
      if (token === chatStore.generation) setLoading(false);
    }
  }, [conversation.id]);
  useEffect(() => {
    void fetchLatest();
    return () => {
      sendTyping(conversation.id, false);
    };
  }, [fetchLatest, sendTyping, conversation.id]);
  useEffect(() => {
    const change = () => setVisible(document.visibilityState === "visible");
    document.addEventListener("visibilitychange", change);
    return () => document.removeEventListener("visibilitychange", change);
  }, []);
  const latest = messages.filter((m) => !m.status).at(-1)?.seq || 0;
  const tailId = messages.at(-1)?.id;
  useEffect(() => {
    if (!loading && (atBottom || initialScroll.current)) {
      scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight });
      initialScroll.current = false;
    }
  }, [loading, tailId, atBottom]);
  useEffect(() => {
    if (
      !atBottom ||
      !visible ||
      loading ||
      !latest ||
      latest <= Math.max(lastRead.current, conversation.readSeq)
    )
      return;
    const timer = setTimeout(() => {
      if (generation !== chatStore.generation) return;
      void api
        .read(conversation.id, latest)
        .then((result) => {
          if (generation === chatStore.generation) {
            lastRead.current = result.readSeq;
            chatStore.markRead(conversation.id, result.readSeq);
          }
        })
        .catch(() => {
          /* Durable sync and the next read retry preserve unread state. */
        });
    }, 600);
    return () => clearTimeout(timer);
  }, [
    latest,
    atBottom,
    visible,
    loading,
    conversation.id,
    conversation.readSeq,
    generation,
  ]);
  async function older() {
    const first = messages.find((m) => !m.status)?.seq;
    if (!first) return;
    const token = chatStore.generation;
    const height = scrollRef.current?.scrollHeight || 0;
    setLoadingOlder(true);
    setError("");
    try {
      const page = await api.messages(conversation.id, first);
      if (token !== chatStore.generation) return;
      chatStore.merge(page.messages);
      setHasMore(page.hasMore);
      requestAnimationFrame(() => {
        if (scrollRef.current)
          scrollRef.current.scrollTop +=
            scrollRef.current.scrollHeight - height;
      });
    } catch (e) {
      if (token === chatStore.generation) setError(errorText(e));
    } finally {
      if (token === chatStore.generation) setLoadingOlder(false);
    }
  }
  function submit(event?: FormEvent) {
    event?.preventDefault();
    const text = body.trim();
    if (!text || text.length > 8000) return;
    const clientMessageId = crypto.randomUUID();
    const message: Message = {
      id: `local:${clientMessageId}`,
      clientMessageId,
      conversationId: conversation.id,
      body: text,
      senderId: user!.id,
      senderName: user!.displayName,
      senderKind: user!.kind,
      seq: Number.MAX_SAFE_INTEGER,
      createdAt: new Date().toISOString(),
      ...(reply ? { replyToId: reply.id } : {}),
    };
    void sendMessage(message, chatStore.generation);
    setBody("");
    setReply(null);
    setAtBottom(true);
    sendTyping(conversation.id, false);
    inputRef.current?.focus();
  }
  function keyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (
      event.key === "Enter" &&
      !event.shiftKey &&
      !event.nativeEvent.isComposing
    ) {
      event.preventDefault();
      submit();
    }
  }
  async function edit(event: FormEvent) {
    event.preventDefault();
    if (!editing || !editBody.trim()) return;
    const token = chatStore.generation;
    setActionBusy(true);
    setActionError("");
    try {
      const result = await api.edit(
        conversation.id,
        editing.id,
        editBody.trim(),
      );
      if (token !== chatStore.generation) return;
      chatStore.merge([result.message]);
      setEditing(null);
    } catch (e) {
      if (token === chatStore.generation) setActionError(errorText(e));
    } finally {
      setActionBusy(false);
    }
  }
  async function retract() {
    if (!retracting) return;
    const token = chatStore.generation;
    setActionBusy(true);
    setActionError("");
    try {
      const result = await api.retract(conversation.id, retracting.id);
      if (token !== chatStore.generation) return;
      chatStore.merge([result.message]);
      setRetracting(null);
    } catch (e) {
      if (token === chatStore.generation) setActionError(errorText(e));
    } finally {
      setActionBusy(false);
    }
  }
  const closeRetract = useCallback(() => setRetracting(null), [setRetracting]);
  const otherTyping = typing[conversation.id] || [];
  const firstUnread = messages.find(
    (m) => !m.status && m.seq > conversation.readSeq,
  )?.id;
  return (
    <section className="conversation-view">
      <header className="conversation-header">
        <IconButton
          label="打开频道列表"
          className="mobile-menu"
          onClick={onMenu}
        >
          <span className="hamburger">☰</span>
        </IconButton>
        <span className="conversation-symbol">
          {conversation.type === "channel" ? (
            <Hash size={23} />
          ) : conversation.type === "group" ? (
            <Users size={22} />
          ) : (
            <MessageCircle size={22} />
          )}
        </span>
        <div>
          <h1>{name}</h1>
          <span>
            {conversation.type === "channel"
              ? "想法在这里碰头，好事从这里开始"
              : conversation.type === "direct"
                ? "你们的专属对话"
                : "一个小组，一起向前"}
          </span>
        </div>
        <div className="conversation-header-actions">
          <IconButton label="查看会话成员" onClick={onMembers}>
            <Users size={19} />
          </IconButton>
        </div>
      </header>
      {waitingAgents.length > 0 && (
        <div className="agent-conversation-waiting" role="status">
          <KeyRound size={14} />
          <span>
            {waitingAgents.map((agent) => agent.name).join("、")}{" "}
            正在等待管理员配置模型，暂时不会生成回复。对话消息会正常保存。
          </span>
        </div>
      )}
      {syncError && (
        <div className="sync-banner" role="status">
          <RefreshCw size={14} />
          <span>{syncError}</span>
          <button onClick={sync}>立即重试</button>
        </div>
      )}
      <div
        className="message-scroll"
        ref={scrollRef}
        onScroll={(event) => {
          const el = event.currentTarget;
          setAtBottom(el.scrollHeight - el.scrollTop - el.clientHeight < 80);
        }}
      >
        <div className="conversation-start">
          <span
            className={`conversation-start-icon ${conversation.type === "channel" ? "" : "mint"}`}
          >
            {conversation.type === "channel" ? (
              <Hash size={34} />
            ) : (
              <MessageCircle size={32} />
            )}
          </span>
          <span className="eyebrow">THIS IS WHERE IT BEGINS</span>
          <h2>
            {conversation.type === "channel"
              ? `欢迎来到 # ${name}`
              : `你好，${name}`}
          </h2>
          <p>
            {conversation.type === "channel"
              ? "这里是这个频道的起点。说声你好，让对话自然发生。"
              : "把想法说出来，让好事情一起发生。"}
          </p>
        </div>
        {hasMore && (
          <button
            className="load-earlier"
            onClick={older}
            disabled={loadingOlder}
          >
            {loadingOlder ? (
              <LoaderCircle size={14} className="spin" />
            ) : (
              <ArrowUp size={14} />
            )}{" "}
            加载更早的消息
          </button>
        )}
        {loading && (
          <div className="message-loading">
            <LoaderCircle size={17} className="spin" />
            正在接上你们的对话…
          </div>
        )}
        {error && (
          <div className="message-error">
            <Notice>{error}</Notice>
            <button onClick={fetchLatest}>重新加载</button>
          </div>
        )}
        {messages.map((message, index) => {
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
          const target = message.replyToId
            ? messages.find((m) => m.id === message.replyToId)
            : undefined;
          return (
            <Fragment key={message.id}>
              {showDate && (
                <div className="date-divider">
                  <span>{date(message.createdAt)}</span>
                </div>
              )}
              {message.id === firstUnread && conversation.readSeq > 0 && (
                <div className="unread-divider">
                  <span>以下是新消息</span>
                </div>
              )}
              <article
                id={`message-${message.id}`}
                className={`message ${compact ? "compact" : ""} ${message.status || ""} ${message.retractedAt ? "retracted" : ""}`}
              >
                <div className="message-avatar">
                  {!compact ? (
                    message.senderKind === "agent" ? (
                      <AgentAvatar />
                    ) : (
                      <Avatar
                        name={message.senderName}
                        seed={message.senderId}
                      />
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
                            members.find(
                              (member) => member.userId === message.senderId,
                            )?.ownerName
                          }
                        />
                      )}
                      {message.senderId === user!.id && (
                        <span className="you-label">你</span>
                      )}
                      <time dateTime={message.createdAt}>
                        {time(message.createdAt)}
                      </time>
                    </div>
                  )}
                  {message.replyToId && (
                    <button
                      className="message-reply"
                      onClick={() => {
                        const targetElement = document.getElementById(
                          `message-${message.replyToId}`,
                        );
                        targetElement?.scrollIntoView({
                          block: "center",
                          behavior: "smooth",
                        });
                        targetElement?.animate(
                          [
                            { background: "#eee9ff" },
                            { background: "transparent" },
                          ],
                          { duration: 1600 },
                        );
                      }}
                    >
                      <CornerUpLeft size={13} />
                      <b>{target?.senderName || "较早的消息"}</b>
                      <span>
                        {target?.retractedAt
                          ? "消息已撤回"
                          : target?.body || "引用了一条较早的消息"}
                      </span>
                    </button>
                  )}
                  {editing?.id === message.id ? (
                    <form className="edit-message" onSubmit={edit}>
                      <textarea
                        autoFocus
                        value={editBody}
                        onChange={(e) => setEditBody(e.target.value)}
                        maxLength={8000}
                        required
                        aria-label="编辑消息内容"
                        onKeyDown={(e) => {
                          if (e.key === "Escape") setEditing(null);
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
                          disabled={!editBody.trim()}
                        >
                          保存修改
                        </Button>
                      </div>
                    </form>
                  ) : (
                    <p className="message-text">
                      {message.retractedAt ? "这条消息已撤回" : message.body}
                      {message.editedAt && !message.retractedAt && (
                        <span className="edited-label">（已编辑）</span>
                      )}
                    </p>
                  )}
                  {message.status === "sending" && (
                    <span className="send-status">
                      <LoaderCircle size={11} className="spin" /> 正在发送
                    </span>
                  )}
                  {message.status === "failed" && (
                    <span className="send-status failed-status">
                      {message.error || "发送失败"}
                      <button
                        onClick={() =>
                          void sendMessage(
                            message as LocalMessage,
                            chatStore.generation,
                          )
                        }
                      >
                        <RefreshCw size={11} />
                        重试
                      </button>
                    </span>
                  )}
                </div>
                {!message.retractedAt &&
                  !message.status &&
                  editing?.id !== message.id && (
                    <div className="message-actions">
                      <IconButton
                        label={`回复 ${message.senderName}`}
                        onClick={() => {
                          setReply(message);
                          inputRef.current?.focus();
                        }}
                      >
                        <CornerUpLeft size={15} />
                      </IconButton>
                      {message.senderId === user!.id &&
                        data?.capabilities["im.message.edit"] && (
                          <IconButton
                            label="编辑消息"
                            onClick={() => {
                              setEditing(message);
                              setEditBody(message.body);
                              setActionError("");
                            }}
                          >
                            <Pencil size={14} />
                          </IconButton>
                        )}
                      {message.senderId === user!.id &&
                        data?.capabilities["im.message.retract"] && (
                          <IconButton
                            label="撤回消息"
                            className="danger-hover"
                            onClick={() => {
                              setRetracting(message);
                              setActionError("");
                            }}
                          >
                            <Trash2 size={14} />
                          </IconButton>
                        )}
                    </div>
                  )}
              </article>
            </Fragment>
          );
        })}
        <div className="message-end" />
      </div>
      {!atBottom && (
        <button
          className="jump-latest"
          onClick={() => {
            setAtBottom(true);
            scrollRef.current?.scrollTo({
              top: scrollRef.current.scrollHeight,
              behavior: "smooth",
            });
          }}
        >
          <ArrowDown size={14} />
          回到最新消息
        </button>
      )}
      <div className="composer-area">
        {reply && (
          <div className="reply-preview">
            <CornerUpLeft size={15} />
            <div>
              回复 <b>{reply.senderName}</b>
              <span>{reply.retractedAt ? "消息已撤回" : reply.body}</span>
            </div>
            <IconButton label="取消回复" onClick={() => setReply(null)}>
              <X size={16} />
            </IconButton>
          </div>
        )}
        <form
          className={`composer ${reply ? "with-reply" : ""}`}
          onSubmit={submit}
        >
          <textarea
            ref={inputRef}
            value={body}
            onChange={(e) => {
              setBody(e.target.value);
              if (Date.now() - lastTyping.current > 1500) {
                sendTyping(conversation.id, !!e.target.value);
                lastTyping.current = Date.now();
              }
            }}
            onKeyDown={keyDown}
            placeholder={
              conversation.type === "channel"
                ? `发送消息到 # ${name}`
                : `发送消息给 ${name}`
            }
            maxLength={8000}
            rows={1}
            aria-label="消息内容"
          />
          <div className="composer-tools">
            <span className="composer-hint">一句你好，就是协作的开始。</span>
            {body.length > 7600 && (
              <span className="character-count">{body.length}/8000</span>
            )}
            <button
              className="send-button"
              type="submit"
              disabled={!body.trim()}
              aria-label="发送消息"
              title="发送消息 (Enter)"
            >
              <SendHorizontal size={18} />
            </button>
          </div>
        </form>
        <div className="composer-footer">
          <span>
            {otherTyping.length ? (
              `${otherTyping.map((x) => x.displayName).join("、")} 正在输入…`
            ) : (
              <>
                <span className="tiny-lock">
                  <Check size={11} />
                </span>
                只在当前对话中分享
              </>
            )}
          </span>
          <span>
            <kbd>Enter</kbd> 发送 · <kbd>Shift + Enter</kbd> 换行
          </span>
        </div>
      </div>
      {retracting && (
        <Modal
          title="撤回这条消息？"
          subtitle="撤回后，所有人都会看到一条撤回提示。"
          onClose={closeRetract}
        >
          <blockquote className="retract-quote">{retracting.body}</blockquote>
          <Notice>{actionError}</Notice>
          <div className="modal-actions">
            <Button className="secondary" onClick={closeRetract}>
              保留消息
            </Button>
            <Button className="danger" busy={actionBusy} onClick={retract}>
              确认撤回
            </Button>
          </div>
        </Modal>
      )}
    </section>
  );
}
