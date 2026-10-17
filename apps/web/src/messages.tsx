import {
  Fragment,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import type { FormEvent, KeyboardEvent } from "react";
import {
  ArrowDown,
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
  SendHorizontal,
  Trash2,
  Users,
  X,
} from "lucide-react";
import { defaultAttachmentLimits } from "@teamie/api-client";
import {
  AttachmentDraft,
  AttachmentPicker,
  MessageAttachments,
  useAttachmentDraft,
} from "./attachments";
import { api } from "@teamie/api-client";
import type {
  Conversation,
  Member,
  Message,
  Mention,
} from "@teamie/api-client";
import { useAuth } from "./auth";
import { errorText, sendMessage, useChat, useChatState } from "./chat";
import { chatStore, reconcileMessages } from "./store";
import { MentionInput, MentionText, trimMentionDraft } from "./mentions";
import type { LocalMessage } from "./store";
import { Avatar, Button, date, IconButton, Modal, Notice, time } from "./ui";
import { AgentAvatar, AgentBadge } from "./agent-ui";
import { ConversationSearch } from "./conversation-search";

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
  onMembers,
  membersOpen,
  onMenu,
}: {
  conversation: Conversation;
  members: Member[];
  onMembers: () => void;
  membersOpen: boolean;
  onMenu: () => void;
}) {
  const { user } = useAuth();
  const { data, syncError, sync, sendTyping, typing } = useChat();
  const attachments = useAttachmentDraft(
    conversation.id,
    data?.attachmentLimits || defaultAttachmentLimits,
  );
  const uploadsEnabled = !!data?.capabilities["im.attachments.upload"];
  const attachmentBlocked =
    attachments.blocked || (attachments.files.length > 0 && !uploadsEnabled);
  const snapshot = useChatState();
  const allMessages = snapshot.messages[conversation.id] || [];
  const [located, setLocated] = useState<LocalMessage[] | null>(null);
  const [resolvedTargets, setResolvedTargets] = useState<
    Record<string, Message>
  >({});
  const messages = located
    ? reconcileMessages(
        located,
        allMessages.filter((m) => located.some((l) => l.id === m.id)),
      )
    : allMessages;
  const [highlightId, setHighlightId] = useState("");
  const [locating, setLocating] = useState(false);
  const [searchOpen, setSearchOpen] = useState(false);
  const closeSearch = useCallback(() => setSearchOpen(false), []);
  const navigation = useRef(0);
  const [mentions, setMentions] = useState<Mention[]>([]);
  const [editMentions, setEditMentions] = useState<Mention[]>([]);
  const mentionMembers = members.filter((member) =>
    conversation.memberIds.includes(member.userId),
  );
  const mentionsEnabled = !!data?.capabilities["im.message.mentions"];
  const name = conversationName(conversation, user!.id, members);
  const [actionMessageId, setActionMessageId] = useState("");
  const [loading, setLoading] = useState(
      () =>
        !(chatStore.getSnapshot().messages[conversation.id] || []).some(
          (m) => !m.status,
        ),
    ),
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
  useEffect(
    () => () => {
      navigation.current++;
    },
    [conversation.id],
  );
  const fetchLatest = useCallback(async () => {
    const token = chatStore.generation;
    const operation = ++navigation.current;
    const hasConfirmed = (
      chatStore.getSnapshot().messages[conversation.id] || []
    ).some((m) => !m.status);
    setLocated(null);
    setHighlightId("");
    setLocating(false);
    setLoadingOlder(false);
    initialScroll.current = true;
    setAtBottom(true);
    setLoading(!hasConfirmed);
    setError("");
    try {
      const page = await api.messages(conversation.id);
      if (token !== chatStore.generation || operation !== navigation.current)
        return;
      chatStore.merge(page.messages);
      setHasMore(page.hasMore);
    } catch (e) {
      if (token === chatStore.generation && operation === navigation.current)
        setError(errorText(e));
    } finally {
      if (token === chatStore.generation && operation === navigation.current)
        setLoading(false);
    }
  }, [conversation.id]);
  const focusMessage = useCallback((id: string) => {
    const target = document.getElementById(`message-${id}`);
    if (!target || !scrollRef.current?.contains(target)) return;
    target.scrollIntoView({
      block: "center",
      behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches
        ? "instant"
        : "smooth",
    });
    target.focus({ preventScroll: true });
  }, []);
  useLayoutEffect(() => {
    if (highlightId) focusMessage(highlightId);
  }, [highlightId, located, focusMessage]);
  async function locate(id: string) {
    const token = chatStore.generation;
    const operation = ++navigation.current;
    const current = () =>
      token === chatStore.generation && operation === navigation.current;
    setLoading(false);
    setLoadingOlder(false);
    setLocating(false);
    initialScroll.current = false;
    setError("");
    if (messages.some((m) => m.id === id)) {
      setHighlightId(id);
      focusMessage(id);
      const el = scrollRef.current;
      if (el) {
        setAtBottom(
          !located && el.scrollHeight - el.scrollTop - el.clientHeight < 80,
        );
      }
      return;
    }
    setAtBottom(false);
    setLocating(true);
    try {
      const target = await api.message(conversation.id, id);
      if (!current()) return;
      setResolvedTargets((old) => ({
        ...old,
        [target.message.id]: old[target.message.id]
          ? reconcileMessages([old[target.message.id]], [target.message])[0]
          : target.message,
      }));
      const page = await api.messagesAround(
        conversation.id,
        target.message.seq,
      );
      if (!current()) return;
      const context = reconcileMessages(page.messages, [target.message]);
      const resolved =
        context.find((m) => m.id === target.message.id) || target.message;
      setResolvedTargets((old) => ({
        ...old,
        [resolved.id]: old[resolved.id]
          ? reconcileMessages([old[resolved.id]], [resolved])[0]
          : resolved,
      }));
      setLocated(context);
      setHasMore((context[0]?.seq || 1) > 1);
      setHighlightId(id);
    } catch (e) {
      if (current())
        setError(
          e instanceof Error && "status" in e && e.status === 404
            ? "原消息已不可访问"
            : errorText(e),
        );
    } finally {
      if (current()) setLocating(false);
    }
  }
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
  useEffect(() => {
    const input = inputRef.current;
    if (!input) return;
    input.style.height = "auto";
    input.style.height = `${input.scrollHeight}px`;
  }, [body]);
  useEffect(() => {
    const scroll = scrollRef.current;
    if (!scroll || !atBottom) return;
    const observer = new ResizeObserver(() => {
      scroll.scrollTop = scroll.scrollHeight;
    });
    observer.observe(scroll);
    return () => observer.disconnect();
  }, [atBottom]);
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
      searchOpen ||
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
    searchOpen,
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
    const operation = navigation.current;
    const height = scrollRef.current?.scrollHeight || 0;
    setLoadingOlder(true);
    setError("");
    try {
      const page = await api.messages(conversation.id, first);
      if (token !== chatStore.generation || operation !== navigation.current)
        return;
      if (located)
        setLocated((old) =>
          old ? reconcileMessages(old, page.messages) : old,
        );
      else chatStore.merge(page.messages);
      setHasMore(page.hasMore);
      requestAnimationFrame(() => {
        if (
          token === chatStore.generation &&
          operation === navigation.current &&
          scrollRef.current
        )
          scrollRef.current.scrollTop +=
            scrollRef.current.scrollHeight - height;
      });
    } catch (e) {
      if (token === chatStore.generation && operation === navigation.current)
        setError(errorText(e));
    } finally {
      if (token === chatStore.generation && operation === navigation.current)
        setLoadingOlder(false);
    }
  }
  function submit(event?: FormEvent) {
    event?.preventDefault();
    const draft = trimMentionDraft(body, mentions);
    const text = draft.body;
    if (
      (!text && !attachments.ready) ||
      text.length > 8000 ||
      attachmentBlocked
    )
      return;
    const files = attachments.consume();
    const clientMessageId = crypto.randomUUID();
    const message: Message = {
      id: `local:${clientMessageId}`,
      clientMessageId,
      conversationId: conversation.id,
      body: text,
      ...(draft.mentions.length ? { mentions: draft.mentions } : {}),
      ...(files.length ? { attachments: files } : {}),
      senderId: user!.id,
      senderName: user!.displayName,
      senderKind: user!.kind,
      seq: Number.MAX_SAFE_INTEGER,
      createdAt: new Date().toISOString(),
      ...(reply ? { replyToId: reply.id } : {}),
    };
    void sendMessage(message, chatStore.generation);
    setBody("");
    setMentions([]);
    if (located) void fetchLatest();
    setReply(null);
    setAtBottom(true);
    sendTyping(conversation.id, false);
    inputRef.current?.focus();
  }
  function keyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (
      !window.matchMedia("(pointer: coarse)").matches &&
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
    if (!editing || (!editBody.trim() && !editing.attachments?.length)) return;
    const token = chatStore.generation;
    setActionBusy(true);
    setActionError("");
    try {
      const draft = trimMentionDraft(editBody, editMentions);
      const result = await api.edit(
        conversation.id,
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
    <section
      className="conversation-view"
      onKeyDown={(event) => {
        if (event.key === "Escape") setActionMessageId("");
      }}
    >
      <header className="conversation-header">
        <IconButton
          label="打开对话列表"
          className="mobile-menu"
          onClick={onMenu}
        >
          <ArrowLeft size={21} />
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
              ? "团队频道"
              : conversation.type === "direct"
                ? "私信"
                : "小组对话"}
          </span>
        </div>
        <div className="conversation-header-actions">
          <IconButton
            label="搜索会话"
            onClick={() => setSearchOpen(true)}
            aria-haspopup="dialog"
          >
            <Search size={19} />
          </IconButton>
          <IconButton
            label="查看会话成员"
            onClick={onMembers}
            aria-expanded={membersOpen}
          >
            <Users size={19} />
          </IconButton>
        </div>
      </header>
      {searchOpen && (
        <ConversationSearch
          key={`${user!.id}:${conversation.id}`}
          conversationId={conversation.id}
          onClose={closeSearch}
          onLocate={(id) => {
            const token = chatStore.generation;
            const operation = navigation.current;
            setSearchOpen(false);
            requestAnimationFrame(() => {
              if (
                token === chatStore.generation &&
                operation === navigation.current
              )
                void locate(id);
            });
          }}
        />
      )}
      {syncError && (
        <div className="sync-banner" role="status">
          <RefreshCw size={14} />
          <span>{syncError}</span>
          <button onClick={sync}>立即重试</button>
        </div>
      )}
      {(located || locating) && (
        <div className="located-banner" role="status">
          <span>{locating ? "正在定位消息…" : "消息上下文"}</span>
          <button onClick={() => void fetchLatest()}>返回最新消息</button>
        </div>
      )}
      <div
        className="message-scroll"
        ref={scrollRef}
        onScroll={(event) => {
          const el = event.currentTarget;
          setAtBottom(
            !located && el.scrollHeight - el.scrollTop - el.clientHeight < 80,
          );
        }}
      >
        {!loading && !messages.length && !error && (
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
            <h2>
              {conversation.type === "channel"
                ? `欢迎来到 # ${name}`
                : `你好，${name}`}
            </h2>
            <p>还没有消息，打个招呼吧。</p>
          </div>
        )}
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
            加载消息…
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
          const replyCandidates: LocalMessage[] = [];
          if (message.replyToId) {
            const cachedTarget = resolvedTargets[message.replyToId];
            if (cachedTarget && cachedTarget.conversationId === conversation.id)
              replyCandidates.push(cachedTarget);
            const storeTarget = allMessages.find(
              (m) => m.id === message.replyToId,
            );
            if (storeTarget) replyCandidates.push(storeTarget);
            const viewTarget = messages.find((m) => m.id === message.replyToId);
            if (viewTarget) replyCandidates.push(viewTarget);
          }
          const target = replyCandidates.length
            ? reconcileMessages(
                [replyCandidates[0]],
                replyCandidates.slice(1),
              )[0]
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
                tabIndex={-1}
                className={`message ${highlightId === message.id ? "located" : ""} ${compact ? "compact" : ""} ${message.status || ""} ${message.retractedAt ? "retracted" : ""} ${actionMessageId === message.id ? "actions-open" : ""}`}
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
                      onClick={() => void locate(message.replyToId!)}
                    >
                      <CornerUpLeft size={13} />
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
                    <form className="edit-message" onSubmit={edit}>
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
                          disabled={
                            !editBody.trim() && !editing.attachments?.length
                          }
                        >
                          保存修改
                        </Button>
                      </div>
                    </form>
                  ) : (
                    <p className="message-text">
                      {message.retractedAt ? (
                        "这条消息已撤回"
                      ) : (
                        <MentionText
                          body={message.body}
                          mentions={message.mentions}
                          userId={user!.id}
                        />
                      )}
                      {message.editedAt && !message.retractedAt && (
                        <span className="edited-label">（已编辑）</span>
                      )}
                    </p>
                  )}
                  {!message.retractedAt && !!message.attachments?.length && (
                    <MessageAttachments
                      conversationId={conversation.id}
                      attachments={message.attachments}
                    />
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
                    <Fragment>
                      <IconButton
                        label={`消息操作：${message.senderName}`}
                        className="message-more-button"
                        aria-expanded={actionMessageId === message.id}
                        onClick={() =>
                          setActionMessageId(
                            actionMessageId === message.id ? "" : message.id,
                          )
                        }
                      >
                        <MoreHorizontal size={18} />
                      </IconButton>
                      <div className="message-actions">
                        <IconButton
                          label={`回复 ${message.senderName}`}
                          onClick={() => {
                            setActionMessageId("");
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
                                setActionMessageId("");
                                setEditing(message);
                                setEditBody(message.body);
                                setEditMentions(message.mentions || []);
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
                                setActionMessageId("");
                                setRetracting(message);
                                setActionError("");
                              }}
                            >
                              <Trash2 size={14} />
                            </IconButton>
                          )}
                      </div>
                    </Fragment>
                  )}
              </article>
            </Fragment>
          );
        })}
        <div className="message-end" />
      </div>
      {!atBottom && !located && !locating && (
        <button
          className="jump-latest"
          onClick={() => {
            if (located) {
              void fetchLatest();
              return;
            }
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
        <AttachmentDraft
          files={attachments.files}
          error={
            attachments.error ||
            (attachments.files.length && !uploadsEnabled
              ? "文件上传已关闭，请移除待发送文件"
              : "")
          }
          onRemove={attachments.remove}
          onRetry={attachments.retry}
        />
        {reply && (
          <div className="reply-preview">
            <CornerUpLeft size={15} />
            <div>
              回复 <b>{reply.senderName}</b>
              <span>
                {reply.retractedAt
                  ? "消息已撤回"
                  : reply.body || `${reply.attachments?.length || 0} 个文件`}
              </span>
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
          <MentionInput
            inputRef={inputRef}
            value={body}
            mentions={mentions}
            onChange={(value, mentions) => {
              setBody(value);
              setMentions(mentions);
            }}
            members={mentionMembers}
            enabled={mentionsEnabled}
            onTyping={(value) => {
              if (Date.now() - lastTyping.current > 1500) {
                sendTyping(conversation.id, !!value);
                lastTyping.current = Date.now();
              }
            }}
            onKeyDown={keyDown}
            placeholder={
              conversation.type === "channel"
                ? `发送消息到 # ${name}`
                : `发送消息给 ${name}`
            }
            aria-label="消息内容"
          />
          <div className="composer-tools">
            {uploadsEnabled && (
              <AttachmentPicker
                onSelect={attachments.add}
                disabled={
                  attachments.files.length >=
                  (data?.attachmentLimits || defaultAttachmentLimits)
                    .maxFilesPerMessage
                }
                limit={
                  (data?.attachmentLimits || defaultAttachmentLimits)
                    .maxFileBytes
                }
              />
            )}
            {body.length > 7600 && (
              <span className="character-count">{body.length}/8000</span>
            )}
            <button
              className="send-button"
              type="submit"
              disabled={
                (!body.trim() && !attachments.ready) || attachmentBlocked
              }
              aria-label="发送消息"
              title="发送消息"
            >
              <SendHorizontal size={18} />
            </button>
          </div>
        </form>
        <div className="composer-footer">
          <span>
            {otherTyping.length
              ? `${otherTyping.map((x) => x.displayName).join("、")} 正在输入…`
              : ""}
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
