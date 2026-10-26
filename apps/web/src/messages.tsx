import {
  useCallback, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore,
} from "react";
import { ArrowDown, ArrowUp, LoaderCircle } from "lucide-react";
import { api, defaultAttachmentLimits } from "@teamie/api-client";
import type { Conversation, Member, Message } from "@teamie/api-client";
import { useAttachmentDraft } from "./attachments";
import { useAuth } from "./auth";
import { errorText, useChat, useChatState } from "./chat";
import { chatStore, reconcileMessages } from "./store";
import type { LocalMessage } from "./store";
import { MessageBody } from "./markdown";
export { MessageBody };
import { Notice } from "./ui";
import { ConversationSearch } from "./conversation-search";
import { ICON } from "./icons";
import {
  clearComposerDrafts, conversationName, extendHistoryBoundary,
  readComposerDraft, subscribeDrafts, useComposerReply, writeComposerDraft,
} from "./composer-drafts";
import type { ComposerDraft } from "./composer-drafts";
export { clearComposerDrafts, conversationName, extendHistoryBoundary };
import {
  ConversationBanners, ConversationEmptyStart, ConversationHeader,
  MessageItem, RetractMessageModal, useMessageActions,
} from "./message-item";
import { MessageComposer } from "./message-composer";

export function ConversationView({
  conversation, members, onMembers, membersOpen, onMenu,
  initialMessageId = "", onClearMessageTarget, isWorkspaceOwner, onInvite,
}: {
  conversation: Conversation; members: Member[]; onMembers: () => void;
  membersOpen: boolean; onMenu: () => void; initialMessageId?: string;
  onClearMessageTarget?: () => void; isWorkspaceOwner?: boolean; onInvite?: () => void;
}) {
  const { user } = useAuth();
  const { data, syncError, sync, sendTyping, typing } = useChat();
  const attachmentLimits = data?.attachmentLimits || defaultAttachmentLimits;
  const attachments = useAttachmentDraft(conversation.id, attachmentLimits);
  const uploadsEnabled = !!data?.capabilities["im.attachments.upload"];
  const attachmentBlocked =
    attachments.blocked || (attachments.files.length > 0 && !uploadsEnabled);
  const snapshot = useChatState();
  const allMessages = snapshot.messages[conversation.id] || [];
  const readSeq = Math.max(
    conversation.readSeq,
    snapshot.conversations[conversation.id]?.readSeq ?? 0,
  );
  const authoritative = data
    ? data.conversations.find((c) => c.id === conversation.id) || conversation
    : undefined;
  const [entry, setEntry] = useState<{ readSeq: number; lastSeq: number } | null>(null);
  const [anchorPending, setAnchorPending] = useState(false);
  if (!entry && authoritative) {
    const frozen = {
      readSeq: Math.max(authoritative.readSeq, readSeq),
      lastSeq: Math.max(authoritative.lastSeq, conversation.lastSeq),
    };
    setEntry(frozen);
    setAnchorPending(!initialMessageId && frozen.lastSeq > frozen.readSeq);
  }
  const [latestLoaded, setLatestLoaded] = useState(false);
  const [unreadContext, setUnreadContext] = useState(false);
  const fetchedLatest = useRef(false);
  const [located, setLocated] = useState<LocalMessage[] | null>(null);
  const [resolvedTargets, setResolvedTargets] = useState<Record<string, Message>>({});
  const messages = located
    ? reconcileMessages(located, allMessages.filter((m) => located.some((l) => l.id === m.id)))
    : allMessages;
  const [highlightId, setHighlightId] = useState("");
  const [focusedMessageId, setFocusedMessageId] = useState("");
  const [locating, setLocating] = useState(false);
  const [searchOpen, setSearchOpen] = useState(false);
  const closeSearch = useCallback(() => setSearchOpen(false), []);
  const navigation = useRef(0);
  const historyBefore = useRef<{ seq: number; generation: number } | null>(null);
  const acknowledgement = useRef<{
    id: string; seq: number; generation: number; operation: number;
  } | null>(null);
  const { body, mentions, replyToId } = useSyncExternalStore(
    subscribeDrafts,
    useCallback(() => readComposerDraft(conversation.id), [conversation.id]),
  );
  function updateComposerDraft(patch: Partial<ComposerDraft>) {
    writeComposerDraft(conversation.id, { ...readComposerDraft(conversation.id), ...patch });
  }
  const mentionMembers = members.filter((member) => conversation.memberIds.includes(member.userId));
  const mentionsEnabled = !!data?.capabilities["im.message.mentions"];
  const name = conversationName(conversation, user!.id, members);
  const {
    actionMessageId, setActionMessageId, editing, setEditing, editBody, setEditBody,
    editMentions, setEditMentions, retracting, actionBusy, actionError,
    startEdit, edit, retract, closeRetract, requestRetract,
  } = useMessageActions(conversation.id);
  const [loading, setLoading] = useState(
      () => !(chatStore.getSnapshot().messages[conversation.id] || []).some((m) => !m.status),
    ),
    [hasMore, setHasMore] = useState(false),
    [loadingOlder, setLoadingOlder] = useState(false),
    [loadingNewer, setLoadingNewer] = useState(false),
    [error, setError] = useState("");
  const [atBottom, setAtBottom] = useState(true),
    [visible, setVisible] = useState(document.visibilityState === "visible");
  const scrollRef = useRef<HTMLDivElement>(null),
    inputRef = useRef<HTMLTextAreaElement>(null),
    initialScroll = useRef(true),
    lastRead = useRef(readSeq);
  const userId = user!.id;
  const generation = chatStore.generation;
  const canReadConversation =
    snapshot.userId === userId && !!snapshot.conversations[conversation.id];
  const { reply, replyError } = useComposerReply({
    conversationId: conversation.id, initialMessageId, replyToId, userId,
    canReadConversation, allMessages, located,
  });

  useEffect(() => () => { navigation.current++; }, [conversation.id]);
  const fetchLatest = useCallback(async () => {
    const token = chatStore.generation;
    const operation = ++navigation.current;
    const hasConfirmed = (chatStore.getSnapshot().messages[conversation.id] || []).some(
      (m) => !m.status,
    );
    if (fetchedLatest.current) {
      const current = chatStore.getSnapshot().conversations[conversation.id];
      if (current) {
        setEntry({ readSeq: current.readSeq, lastSeq: current.lastSeq });
        setAnchorPending(current.lastSeq > current.readSeq);
      }
    }
    fetchedLatest.current = true;
    acknowledgement.current = null;
    historyBefore.current = null;
    setHasMore(false);
    setLocated(null);
    setUnreadContext(false);
    setHighlightId("");
    setLocating(false);
    setLoadingOlder(false);
    setLoadingNewer(false);
    initialScroll.current = true;
    setAtBottom(true);
    setLoading(!hasConfirmed);
    setError("");
    try {
      const page = await api.messages(conversation.id);
      if (token !== chatStore.generation || operation !== navigation.current) return;
      historyBefore.current = page.messages[0]
        ? { seq: page.messages[0].seq, generation: token }
        : null;
      chatStore.merge(page.messages);
      setHasMore(page.hasMore);
      setLatestLoaded(true);
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
    if (!target || !scrollRef.current?.contains(target) || target.closest("[inert]")) return;
    target.scrollIntoView({
      block: "center",
      behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "instant" : "smooth",
    });
    target.focus({ preventScroll: true });
  }, []);

  useLayoutEffect(() => {
    if (highlightId) focusMessage(highlightId);
  }, [highlightId, focusMessage, visible, membersOpen]);

  const loadContext = useCallback(
    async (id: string, readOnDisplay = false) => {
      const token = chatStore.generation;
      const operation = ++navigation.current;
      const current = () =>
        token === chatStore.generation && operation === navigation.current;
      setAtBottom(false);
      setLoading(false);
      setLoadingOlder(false);
      setLoadingNewer(false);
      setLocating(true);
      acknowledgement.current = null;
      historyBefore.current = null;
      setHasMore(false);
      initialScroll.current = false;
      setError("");
      try {
        const target = await api.message(conversation.id, id);
        if (!current()) return;
        setResolvedTargets((old) => ({
          ...old,
          [target.message.id]: old[target.message.id]
            ? reconcileMessages([old[target.message.id]], [target.message])[0]
            : target.message,
        }));
        const page = await api.messagesAround(conversation.id, target.message.seq);
        if (!current()) return;
        const currentTarget = chatStore
          .getSnapshot()
          .messages[conversation.id]?.find((message) => message.id === id);
        const context = reconcileMessages(page.messages, [
          target.message,
          ...(currentTarget ? [currentTarget] : []),
        ]);
        const resolved = context.find((m) => m.id === target.message.id) || target.message;
        setResolvedTargets((old) => ({
          ...old,
          [resolved.id]: old[resolved.id]
            ? reconcileMessages([old[resolved.id]], [resolved])[0]
            : resolved,
        }));
        if (readOnDisplay && !resolved.retractedAt)
          acknowledgement.current = { id, seq: resolved.seq, generation: token, operation };
        setLocated(context);
        setUnreadContext(false);
        historyBefore.current = context[0] ? { seq: context[0].seq, generation: token } : null;
        setHasMore((historyBefore.current?.seq ?? 1) > 1);
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
    },
    [conversation.id],
  );

  const loadUnreadContext = useCallback(async () => {
    const seq = readSeq + 1;
    const token = chatStore.generation;
    const operation = ++navigation.current;
    const current = () =>
      token === chatStore.generation && operation === navigation.current;
    setAnchorPending(false);
    setAtBottom(false);
    setLoading(false);
    setLoadingOlder(false);
    setLoadingNewer(false);
    setLocating(true);
    acknowledgement.current = null;
    historyBefore.current = null;
    setHasMore(false);
    initialScroll.current = false;
    setError("");
    try {
      const page = await api.messagesAround(conversation.id, seq);
      if (!current()) return;
      const context = reconcileMessages(
        page.messages,
        (chatStore.getSnapshot().messages[conversation.id] || []).filter((message) =>
          page.messages.some((m) => m.id === message.id),
        ),
      );
      setLocated(context);
      setUnreadContext(true);
      historyBefore.current = context[0] ? { seq: context[0].seq, generation: token } : null;
      setHasMore((historyBefore.current?.seq ?? 1) > 1);
      const target = context.find((m) => !m.status && m.seq >= seq);
      if (target) setHighlightId(target.id);
    } catch (e) {
      if (current()) setError(errorText(e));
    } finally {
      if (current()) setLocating(false);
    }
  }, [conversation.id, readSeq]);

  function returnToLatest() {
    const current = chatStore.getSnapshot().conversations[conversation.id];
    if (current) {
      setEntry({ readSeq: current.readSeq, lastSeq: current.lastSeq });
      setAnchorPending(current.lastSeq > current.readSeq);
    }
    if (initialMessageId && onClearMessageTarget) onClearMessageTarget();
    else void fetchLatest();
  }

  async function locate(id: string) {
    if (!messages.some((message) => message.id === id)) return loadContext(id);
    navigation.current++;
    acknowledgement.current = null;
    setLoading(false);
    setLoadingOlder(false);
    setLoadingNewer(false);
    setLocating(false);
    initialScroll.current = false;
    setError("");
    setHighlightId(id);
    focusMessage(id);
    const el = scrollRef.current;
    if (el) setAtBottom(!located && el.scrollHeight - el.scrollTop - el.clientHeight < 80);
  }

  useEffect(() => {
    if (initialMessageId) void loadContext(initialMessageId, true);
    else void fetchLatest();
  }, [fetchLatest, loadContext, initialMessageId]);

  const latest = messages.filter((m) => !m.status).at(-1)?.seq || 0;
  const tailId = messages.at(-1)?.id;
  const firstUnread =
    entry && entry.lastSeq > entry.readSeq
      ? messages.find(
          (m) => !m.status && m.seq > entry.readSeq && m.seq <= entry.lastSeq && m.senderId !== userId,
        )?.id
      : undefined;
  const nextUnreadSeq = readSeq + 1;
  const unreadBeforeWindow =
    latestLoaded && !located && !locating && conversation.lastSeq > readSeq &&
    !messages.some((m) => !m.status && m.seq === nextUnreadSeq) &&
    messages.some((m) => !m.status && m.seq > nextUnreadSeq);

  const seenRows = useRef(new Set<number>());
  const [readCandidate, setReadCandidate] = useState(0);
  const ackState = useRef({ allowed: true, blocked: false });
  const dwell = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const readFailures = useRef(0);
  useEffect(() => {
    ackState.current = {
      allowed: !located || unreadContext,
      blocked: searchOpen || !visible || loading,
    };
  }, [located, unreadContext, searchOpen, visible, loading]);
  const scheduleSeen = useCallback(() => {
    clearTimeout(dwell.current);
    dwell.current = setTimeout(() => {
      const el = scrollRef.current;
      if (!el || !ackState.current.allowed || ackState.current.blocked) return;
      const root = el.getBoundingClientRect();
      for (const row of el.querySelectorAll<HTMLElement>("article[data-seq]")) {
        const rect = row.getBoundingClientRect();
        const inside = rect.top >= root.top - 1 && rect.bottom <= root.bottom + 1;
        const visibleHeight = Math.min(rect.bottom, root.bottom) - Math.max(rect.top, root.top);
        const tallAndCovering =
          rect.height > root.height * 0.8 && visibleHeight >= root.height * 0.5 && visibleHeight > 0;
        if (inside || tallAndCovering || row.dataset.own === "true")
          seenRows.current.add(Number(row.dataset.seq));
      }
      const base = chatStore.getSnapshot().conversations[conversation.id]?.readSeq ?? 0;
      let end = base;
      while (seenRows.current.has(end + 1)) end++;
      setReadCandidate(end);
    }, 400);
  }, [conversation.id]);
  useEffect(() => () => clearTimeout(dwell.current), []);

  useEffect(() => {
    const change = () => setVisible(document.visibilityState === "visible");
    document.addEventListener("visibilitychange", change);
    return () => document.removeEventListener("visibilitychange", change);
  }, []);
  useEffect(() => {
    const scroll = scrollRef.current;
    if (!scroll || !atBottom || located || locating) return;
    const observer = new ResizeObserver(() => { scroll.scrollTop = scroll.scrollHeight; });
    observer.observe(scroll);
    return () => observer.disconnect();
  }, [atBottom, located, locating]);
  const lastTailId = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (loading) return;
    const el = scrollRef.current;
    const tailChanged = tailId !== lastTailId.current;
    lastTailId.current = tailId;
    if (anchorPending && !located) {
      const anchor = firstUnread ? document.getElementById(`message-${firstUnread}`) : null;
      if (el && anchor && el.contains(anchor)) {
        setAnchorPending(false);
        initialScroll.current = false;
        el.scrollTop += anchor.getBoundingClientRect().top - el.getBoundingClientRect().top - 56;
        setAtBottom(el.scrollHeight - el.scrollTop - el.clientHeight < 80);
        scheduleSeen();
        return;
      }
      if (latestLoaded) setAnchorPending(false);
      else return;
    }
    if (!located && !locating && (initialScroll.current || (atBottom && tailChanged))) {
      el?.scrollTo({ top: el.scrollHeight });
      initialScroll.current = false;
    }
    scheduleSeen();
  }, [
    loading, tailId, atBottom, anchorPending, firstUnread, latestLoaded,
    located, locating, visible, searchOpen, readSeq, scheduleSeen,
  ]);
  useLayoutEffect(() => {
    const pending = acknowledgement.current;
    if (
      !visible || !pending || pending.id !== highlightId ||
      pending.generation !== chatStore.generation || pending.operation !== navigation.current
    )
      return;
    const target = document.getElementById(`message-${pending.id}`);
    if (!target || !scrollRef.current?.contains(target) || target.closest("[inert]")) return;
    acknowledgement.current = null;
    if (
      messages.some((m) => m.id === pending.id && m.retractedAt) ||
      chatStore.getSnapshot().messages[conversation.id]?.some((m) => m.id === pending.id && m.retractedAt)
    )
      return;
    void api
      .read(conversation.id, pending.seq)
      .then((result) => {
        if (pending.generation === chatStore.generation && pending.operation === navigation.current) {
          lastRead.current = Math.max(lastRead.current, result.readSeq);
          chatStore.markRead(conversation.id, result.readSeq);
        }
      })
      .catch(() => {
        if (pending.generation === chatStore.generation && pending.operation === navigation.current)
          setError("已读状态暂未同步");
      });
  }, [conversation.id, highlightId, located, membersOpen, messages, visible]);
  useEffect(() => {
    const nextRead = Math.min(readCandidate, latest);
    if (!nextRead || nextRead <= Math.max(lastRead.current, readSeq) || generation !== chatStore.generation)
      return;
    void api
      .read(conversation.id, nextRead)
      .then((result) => {
        if (generation === chatStore.generation) {
          readFailures.current = 0;
          lastRead.current = Math.max(lastRead.current, result.readSeq);
          chatStore.markRead(conversation.id, result.readSeq);
        }
      })
      .catch(() => {
        if (generation === chatStore.generation) {
          setReadCandidate(0);
          if (readFailures.current < 1) {
            readFailures.current++;
            scheduleSeen();
          }
        }
      });
  }, [conversation.id, generation, latest, readCandidate, readSeq, scheduleSeen]);
  const markLatestRead = useCallback(() => {
    if (!latest || latest <= readSeq) return;
    const token = chatStore.generation;
    void api
      .read(conversation.id, latest)
      .then((result) => {
        if (token === chatStore.generation) {
          lastRead.current = Math.max(lastRead.current, result.readSeq);
          chatStore.markRead(conversation.id, result.readSeq);
        }
      })
      .catch((e) => {
        if (token === chatStore.generation) setError(errorText(e));
      });
  }, [conversation.id, latest, readSeq]);

  async function older() {
    const boundary = historyBefore.current;
    if (!boundary || boundary.generation !== chatStore.generation || loadingOlder) return;
    const first = extendHistoryBoundary(boundary.seq, messages);
    const token = chatStore.generation;
    const operation = navigation.current;
    const height = scrollRef.current?.scrollHeight || 0;
    setLoadingOlder(true);
    setError("");
    try {
      const page = await api.messages(conversation.id, first);
      if (token !== chatStore.generation || operation !== navigation.current) return;
      historyBefore.current = page.messages[0]
        ? { seq: page.messages[0].seq, generation: token }
        : null;
      if (located) setLocated((old) => (old ? reconcileMessages(old, page.messages) : old));
      else chatStore.merge(page.messages);
      setHasMore(page.hasMore);
      requestAnimationFrame(() => {
        if (token === chatStore.generation && operation === navigation.current && scrollRef.current)
          scrollRef.current.scrollTop += scrollRef.current.scrollHeight - height;
      });
    } catch (e) {
      if (token === chatStore.generation && operation === navigation.current) setError(errorText(e));
    } finally {
      if (token === chatStore.generation && operation === navigation.current) setLoadingOlder(false);
    }
  }
  async function newer() {
    if (!located || !unreadContext || loadingNewer || loadingOlder || locating) return;
    const token = chatStore.generation;
    const operation = navigation.current;
    const current = () =>
      token === chatStore.generation &&
      operation === navigation.current &&
      !!chatStore.getSnapshot().conversations[conversation.id];
    setLoadingNewer(true);
    setError("");
    try {
      const page = await api.messagesAround(conversation.id, latest + 1);
      if (!current()) return;
      if (!page.messages.some((message) => message.seq > latest)) {
        setError("后续消息暂未加载，请重试");
        return;
      }
      setLocated((old) => (old ? reconcileMessages(old, page.messages) : old));
    } catch (e) {
      if (current()) setError(errorText(e));
    } finally {
      if (current()) setLoadingNewer(false);
    }
  }
  const canEdit = !!data?.capabilities["im.message.edit"];
  const canRetract = !!data?.capabilities["im.message.retract"];
  const otherTyping = typing[conversation.id] || [];
  const rovingMessageId =
    (focusedMessageId && messages.some((m) => m.id === focusedMessageId) ? focusedMessageId : undefined) ||
    (highlightId && messages.some((m) => m.id === highlightId) ? highlightId : undefined) ||
    messages.at(-1)?.id ||
    "";

  return (
    <section
      className="conversation-view"
      onKeyDown={(event) => { if (event.key === "Escape") setActionMessageId(""); }}
    >
      <ConversationHeader
        conversation={conversation} name={name} membersOpen={membersOpen}
        onMenu={onMenu} onSearchOpen={() => setSearchOpen(true)} onMembers={onMembers}
      />
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
              if (token === chatStore.generation && operation === navigation.current)
                void locate(id);
            });
          }}
        />
      )}
      <ConversationBanners
        syncError={syncError} onSync={sync} located={!!located} locating={locating}
        onReturnToLatest={returnToLatest} unreadBeforeWindow={unreadBeforeWindow}
        unreadCount={conversation.lastSeq - readSeq}
        onJumpFirstUnread={() => void loadUnreadContext()} onMarkLatestRead={markLatestRead}
      />
      <div
        className="message-scroll"
        ref={scrollRef}
        onScroll={(event) => {
          const el = event.currentTarget;
          setAtBottom(!located && el.scrollHeight - el.scrollTop - el.clientHeight < 80);
          readFailures.current = 0;
          scheduleSeen();
        }}
      >
        {!loading && !messages.length && !error && (
          <ConversationEmptyStart
            conversation={conversation} name={name}
            isWorkspaceOwner={isWorkspaceOwner} onInvite={onInvite}
          />
        )}
        {hasMore && (
          <button className="load-earlier" onClick={older} disabled={loadingOlder || loadingNewer}>
            {loadingOlder ? <LoaderCircle size={ICON.xs} className="spin" /> : <ArrowUp size={ICON.xs} />}{" "}
            加载更早的消息
          </button>
        )}
        {loading && (
          <div className="message-loading">
            <LoaderCircle size={ICON.md} className="spin" />
            加载消息…
          </div>
        )}
        {error && (
          <div className="message-error">
            <Notice>{error}</Notice>
            <button onClick={fetchLatest}>重新加载</button>
          </div>
        )}
        {messages.map((message, index) => (
          <MessageItem
            key={message.id} message={message} index={index} messages={messages}
            allMessages={allMessages} resolvedTargets={resolvedTargets}
            workspaceId={conversation.workspaceId}
            conversationId={conversation.id} userId={userId} members={members}
            mentionMembers={mentionMembers} mentionsEnabled={mentionsEnabled}
            canEdit={canEdit} canRetract={canRetract} firstUnread={firstUnread}
            entryReadSeq={entry?.readSeq ?? 0} isRovingTarget={message.id === rovingMessageId}
            highlightId={highlightId} actionMessageId={actionMessageId} editing={editing}
            editBody={editBody} editMentions={editMentions} actionBusy={actionBusy}
            actionError={actionError} inputRef={inputRef}
            setFocusedMessageId={setFocusedMessageId} setActionMessageId={setActionMessageId}
            focusMessage={focusMessage} locate={locate} onEditSubmit={edit}
            setEditBody={setEditBody} setEditMentions={setEditMentions}
            setEditing={setEditing} startEdit={startEdit}
            onReply={(id) => {
              setActionMessageId("");
              updateComposerDraft({ replyToId: id });
              inputRef.current?.focus();
            }}
            onRetractRequest={requestRetract}
          />
        ))}
        {located && unreadContext && latest < conversation.lastSeq && (
          <button
            className="load-earlier load-later"
            onClick={() => void newer()}
            disabled={loadingNewer || loadingOlder || locating}
          >
            {loadingNewer ? <LoaderCircle size={ICON.xs} className="spin" /> : <ArrowDown size={ICON.xs} />}{" "}
            加载后续消息
          </button>
        )}
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
            scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight, behavior: "smooth" });
          }}
        >
          <ArrowDown size={ICON.xs} />
          回到最新消息
        </button>
      )}
      <MessageComposer
        conversation={conversation} user={user!} name={name} body={body}
        mentions={mentions} replyToId={replyToId} reply={reply} replyError={replyError}
        attachments={attachments} attachmentLimits={attachmentLimits}
        uploadsEnabled={uploadsEnabled} attachmentBlocked={attachmentBlocked}
        mentionMembers={mentionMembers} mentionsEnabled={mentionsEnabled}
        otherTyping={otherTyping} inputRef={inputRef} editing={editing}
        located={!!located} latestLoaded={latestLoaded} latest={latest}
        canEdit={canEdit} messages={messages} sendTyping={sendTyping}
        onChangeDraft={(value, nextMentions) =>
          updateComposerDraft({ body: value, mentions: nextMentions })
        }
        onClearReply={() => updateComposerDraft({ replyToId: "" })}
        onStartEdit={startEdit}
        onSent={() => {
          if (located) returnToLatest();
          setAtBottom(true);
        }}
      />
      {retracting && (
        <RetractMessageModal
          retracting={retracting} actionError={actionError} actionBusy={actionBusy}
          onClose={closeRetract} onConfirm={() => void retract()}
        />
      )}
    </section>
  );
}
