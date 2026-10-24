import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import { ArrowDown, ArrowUp, LoaderCircle } from "lucide-react";
import { defaultAttachmentLimits } from "@teamie/api-client";
import type {
  Conversation,
  Member,
  Message,
} from "@teamie/api-client";
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
  clearComposerDrafts,
  conversationName,
  extendHistoryBoundary,
  readComposerDraft,
  runFetchLatest,
  runLoadContext,
  runLoadNewer,
  runLoadOlder,
  runLoadUnreadContext,
  subscribeDrafts,
  useComposerReply,
  useConversationScrollEffect,
  useReadWatermarks,
  writeComposerDraft,
} from "./composer-drafts";
import type { ComposerDraft } from "./composer-drafts";
export { clearComposerDrafts, conversationName, extendHistoryBoundary };
import {
  ConversationBanners,
  ConversationEmptyStart,
  ConversationHeader,
  MessageItem,
  RetractMessageModal,
  useMessageActions,
} from "./message-item";
import { MessageComposer } from "./message-composer";

export function ConversationView({
  conversation,
  members,
  onMembers,
  membersOpen,
  onMenu,
  initialMessageId = "",
  onClearMessageTarget,
  isWorkspaceOwner,
  onInvite,
}: {
  conversation: Conversation;
  members: Member[];
  onMembers: () => void;
  membersOpen: boolean;
  onMenu: () => void;
  initialMessageId?: string;
  onClearMessageTarget?: () => void;
  isWorkspaceOwner?: boolean;
  onInvite?: () => void;
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
  const [entry, setEntry] = useState<{ readSeq: number; lastSeq: number } | null>(
    null,
  );
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
  const [focusedMessageId, setFocusedMessageId] = useState("");
  const [locating, setLocating] = useState(false);
  const [searchOpen, setSearchOpen] = useState(false);
  const closeSearch = useCallback(() => setSearchOpen(false), []);
  const navigation = useRef(0);
  const historyBefore = useRef<{ seq: number; generation: number } | null>(null);
  const acknowledgement = useRef<{
    id: string;
    seq: number;
    generation: number;
    operation: number;
  } | null>(null);
  const { body, mentions, replyToId } = useSyncExternalStore(
    subscribeDrafts,
    useCallback(() => readComposerDraft(conversation.id), [conversation.id]),
  );
  function updateComposerDraft(patch: Partial<ComposerDraft>) {
    writeComposerDraft(conversation.id, {
      ...readComposerDraft(conversation.id),
      ...patch,
    });
  }
  const mentionMembers = members.filter((member) =>
    conversation.memberIds.includes(member.userId),
  );
  const mentionsEnabled = !!data?.capabilities["im.message.mentions"];
  const name = conversationName(conversation, user!.id, members);
  const {
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
  } = useMessageActions(conversation.id);
  const [loading, setLoading] = useState(
      () =>
        !(chatStore.getSnapshot().messages[conversation.id] || []).some(
          (m) => !m.status,
        ),
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
  const canReadConversation =
    snapshot.userId === userId && !!snapshot.conversations[conversation.id];
  const { reply, replyError } = useComposerReply({
    conversationId: conversation.id,
    initialMessageId,
    replyToId,
    userId,
    canReadConversation,
    allMessages,
    located,
  });

  useEffect(
    () => () => {
      navigation.current++;
    },
    [conversation.id],
  );
  const fetchLatest = useCallback(
    () =>
      runFetchLatest({
        conversationId: conversation.id,
        fetchedLatest,
        navigation,
        acknowledgement,
        historyBefore,
        initialScroll,
        setEntry,
        setAnchorPending,
        setHasMore,
        setLocated,
        setUnreadContext,
        setHighlightId,
        setLocating,
        setLoadingOlder,
        setLoadingNewer,
        setAtBottom,
        setLoading,
        setError,
        setLatestLoaded,
        formatError: errorText,
      }),
    [conversation.id],
  );

  const focusMessage = useCallback((id: string) => {
    const target = document.getElementById(`message-${id}`);
    if (
      !target ||
      !scrollRef.current?.contains(target) ||
      target.closest("[inert]")
    )
      return;
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
  }, [highlightId, focusMessage, visible, membersOpen]);

  const loadContext = useCallback(
    (id: string, readOnDisplay = false) =>
      runLoadContext({
        conversationId: conversation.id,
        id,
        readOnDisplay,
        navigation,
        acknowledgement,
        historyBefore,
        setAtBottom,
        setLoading,
        setLoadingOlder,
        setLoadingNewer,
        setLocating,
        setHasMore,
        initialScroll,
        setError,
        setResolvedTargets,
        setLocated,
        setUnreadContext,
        setHighlightId,
        formatError: errorText,
      }),
    [conversation.id],
  );

  const loadUnreadContext = useCallback(
    () =>
      runLoadUnreadContext({
        conversationId: conversation.id,
        readSeq,
        navigation,
        acknowledgement,
        historyBefore,
        setAnchorPending,
        setAtBottom,
        setLoading,
        setLoadingOlder,
        setLoadingNewer,
        setLocating,
        setHasMore,
        initialScroll,
        setError,
        setLocated,
        setUnreadContext,
        setHighlightId,
        formatError: errorText,
      }),
    [conversation.id, readSeq],
  );

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
    if (el) {
      setAtBottom(
        !located && el.scrollHeight - el.scrollTop - el.clientHeight < 80,
      );
    }
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
          (m) =>
            !m.status &&
            m.seq > entry.readSeq &&
            m.seq <= entry.lastSeq &&
            m.senderId !== userId,
        )?.id
      : undefined;
  const nextUnreadSeq = readSeq + 1;
  const unreadBeforeWindow =
    latestLoaded &&
    !located &&
    !locating &&
    conversation.lastSeq > readSeq &&
    !messages.some((m) => !m.status && m.seq === nextUnreadSeq) &&
    messages.some((m) => !m.status && m.seq > nextUnreadSeq);

  const { scheduleSeen, resetReadFailures, markLatestRead } = useReadWatermarks({
    conversationId: conversation.id,
    readSeq,
    latest,
    located,
    unreadContext,
    searchOpen,
    visible,
    loading,
    membersOpen,
    highlightId,
    messages,
    scrollRef,
    navigationRef: navigation,
    acknowledgementRef: acknowledgement,
    lastReadRef: lastRead,
    onError: setError,
  });

  useConversationScrollEffect({
    loading,
    tailId,
    atBottom,
    anchorPending,
    firstUnread,
    latestLoaded,
    located,
    locating,
    visible,
    searchOpen,
    readSeq,
    scrollRef,
    initialScroll,
    setAnchorPending,
    setAtBottom,
    setVisible,
    scheduleSeen,
  });

  const older = () =>
    runLoadOlder({
      conversationId: conversation.id,
      messages,
      located,
      loadingOlder,
      historyBefore,
      navigation,
      scrollRef,
      setLoadingOlder,
      setError,
      setLocated,
      setHasMore,
      formatError: errorText,
    });

  const newer = () =>
    runLoadNewer({
      conversationId: conversation.id,
      latest,
      located,
      unreadContext,
      loadingNewer,
      loadingOlder,
      locating,
      navigation,
      setLoadingNewer,
      setError,
      setLocated,
      formatError: errorText,
    });

  const canEdit = !!data?.capabilities["im.message.edit"];
  const canRetract = !!data?.capabilities["im.message.retract"];
  const otherTyping = typing[conversation.id] || [];
  const rovingMessageId =
    (focusedMessageId && messages.some((m) => m.id === focusedMessageId)
      ? focusedMessageId
      : undefined) ||
    (highlightId && messages.some((m) => m.id === highlightId)
      ? highlightId
      : undefined) ||
    messages.at(-1)?.id ||
    "";

  return (
    <section
      className="conversation-view"
      onKeyDown={(event) => {
        if (event.key === "Escape") setActionMessageId("");
      }}
    >
      <ConversationHeader
        conversation={conversation}
        name={name}
        membersOpen={membersOpen}
        onMenu={onMenu}
        onSearchOpen={() => setSearchOpen(true)}
        onMembers={onMembers}
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
              if (
                token === chatStore.generation &&
                operation === navigation.current
              )
                void locate(id);
            });
          }}
        />
      )}
      <ConversationBanners
        syncError={syncError}
        onSync={sync}
        located={!!located}
        locating={locating}
        onReturnToLatest={returnToLatest}
        unreadBeforeWindow={unreadBeforeWindow}
        unreadCount={conversation.lastSeq - readSeq}
        onJumpFirstUnread={() => void loadUnreadContext()}
        onMarkLatestRead={markLatestRead}
      />
      <div
        className="message-scroll"
        ref={scrollRef}
        onScroll={(event) => {
          const el = event.currentTarget;
          setAtBottom(
            !located && el.scrollHeight - el.scrollTop - el.clientHeight < 80,
          );
          resetReadFailures();
          scheduleSeen();
        }}
      >
        {!loading && !messages.length && !error && (
          <ConversationEmptyStart
            conversation={conversation}
            name={name}
            isWorkspaceOwner={isWorkspaceOwner}
            onInvite={onInvite}
          />
        )}
        {hasMore && (
          <button
            className="load-earlier"
            onClick={older}
            disabled={loadingOlder || loadingNewer}
          >
            {loadingOlder ? (
              <LoaderCircle size={ICON.xs} className="spin" />
            ) : (
              <ArrowUp size={ICON.xs} />
            )}{" "}
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
            key={message.id}
            message={message}
            index={index}
            messages={messages}
            allMessages={allMessages}
            resolvedTargets={resolvedTargets}
            conversationId={conversation.id}
            userId={userId}
            members={members}
            mentionMembers={mentionMembers}
            mentionsEnabled={mentionsEnabled}
            canEdit={canEdit}
            canRetract={canRetract}
            firstUnread={firstUnread}
            entryReadSeq={entry?.readSeq ?? 0}
            isRovingTarget={message.id === rovingMessageId}
            highlightId={highlightId}
            actionMessageId={actionMessageId}
            editing={editing}
            editBody={editBody}
            editMentions={editMentions}
            actionBusy={actionBusy}
            actionError={actionError}
            inputRef={inputRef}
            setFocusedMessageId={setFocusedMessageId}
            setActionMessageId={setActionMessageId}
            focusMessage={focusMessage}
            locate={locate}
            onEditSubmit={edit}
            setEditBody={setEditBody}
            setEditMentions={setEditMentions}
            setEditing={setEditing}
            startEdit={startEdit}
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
            {loadingNewer ? (
              <LoaderCircle size={ICON.xs} className="spin" />
            ) : (
              <ArrowDown size={ICON.xs} />
            )}{" "}
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
            scrollRef.current?.scrollTo({
              top: scrollRef.current.scrollHeight,
              behavior: "smooth",
            });
          }}
        >
          <ArrowDown size={ICON.xs} />
          回到最新消息
        </button>
      )}
      <MessageComposer
        conversation={conversation}
        user={user!}
        name={name}
        body={body}
        mentions={mentions}
        replyToId={replyToId}
        reply={reply}
        replyError={replyError}
        attachments={attachments}
        attachmentLimits={attachmentLimits}
        uploadsEnabled={uploadsEnabled}
        attachmentBlocked={attachmentBlocked}
        mentionMembers={mentionMembers}
        mentionsEnabled={mentionsEnabled}
        otherTyping={otherTyping}
        inputRef={inputRef}
        editing={editing}
        located={!!located}
        latestLoaded={latestLoaded}
        latest={latest}
        canEdit={canEdit}
        messages={messages}
        sendTyping={sendTyping}
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
          retracting={retracting}
          actionError={actionError}
          actionBusy={actionBusy}
          onClose={closeRetract}
          onConfirm={() => void retract()}
        />
      )}
    </section>
  );
}
