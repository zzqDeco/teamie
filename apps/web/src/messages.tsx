import {
  useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore,
} from "react";
import type { DragEvent, FormEvent } from "react";
import { ArrowDown, ArrowUp, LoaderCircle } from "lucide-react";
import { api, defaultAttachmentLimits } from "@teamie/api-client";
import type { Conversation, Member, Mention, Message } from "@teamie/api-client";
import { useAttachmentDraft } from "./attachments";
import { useAuth } from "./auth";
import { errorText, useChat, useChatState } from "./chat";
import { chatStore, reconcileMessages } from "./store";
import type { LocalMessage } from "./store";
import { projectReactionList } from "./reactions";
import { MessageBody } from "./markdown";
export { MessageBody };
import { Notice } from "./ui";
import { ConversationSearch } from "./conversation-search";
import { ICON } from "./icons";
import {
  clearComposerDrafts, composerDraftEpoch, conversationName, extendHistoryBoundary,
  readComposerDraft, subscribeDrafts, useComposerReply, writeComposerDraft,
} from "./composer-drafts";
import type { ComposerDraft } from "./composer-drafts";
export { clearComposerDrafts, conversationName, extendHistoryBoundary };
import {
  ConversationBanners, ConversationEmptyStart, ConversationHeader,
  MessageItem, messageGrouping, RetractMessageModal, useMessageActions,
} from "./message-item";
import { MessageComposer } from "./message-composer";
import { useConversationReceiptsSync } from "./engagement-store";
import { useConversationReadTracking } from "./chat/conversation-read-tracking";
import { useConversationContextLoader } from "./chat/conversation-context-loader";

const noMessages: LocalMessage[] = [];
const noMembers: Member[] = [];
const noMentions: Mention[] = [];

export function ConversationView({
  conversation, members, onMembers, membersOpen, onMenu,
  initialMessageId = "", onClearMessageTarget, isWorkspaceOwner, onInvite,
}: {
  conversation: Conversation; members: Member[]; onMembers: () => void;
  membersOpen: boolean; onMenu: () => void; initialMessageId?: string;
  onClearMessageTarget?: () => void; isWorkspaceOwner?: boolean; onInvite?: () => void;
}) {
  const { user } = useAuth();
  const { data, syncError, sync, sendTyping } = useChat();
  const attachmentLimits = data?.attachmentLimits || defaultAttachmentLimits;
  const attachments = useAttachmentDraft(conversation.id, attachmentLimits);
  const uploadsEnabled = !!data?.capabilities["im.attachments.upload"];
  const attachmentBlocked =
    attachments.blocked || (attachments.files.length > 0 && !uploadsEnabled);
  const snapshot = useChatState();
  const allMessages = snapshot.messages[conversation.id] || noMessages;
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
  const messages = useMemo(() => {
    const ids = located ? new Set(located.map((l) => l.id)) : null;
    const base = !located
      ? allMessages
      : reconcileMessages(located, allMessages.filter((m) => ids!.has(m.id)));
    return projectReactionList(base, snapshot.reactionStates);
  }, [located, allMessages, snapshot.reactionStates]);
  const [highlightId, setHighlightId] = useState("");
  const [focusedMessageId, setFocusedMessageId] = useState("");
  const [locating, setLocating] = useState(false);
  const [searchOpen, setSearchOpen] = useState(false);
  const [draggingFiles, setDraggingFiles] = useState(false);
  const closeSearch = useCallback(() => setSearchOpen(false), []);
  const navigation = useRef(0);
  const historyBefore = useRef<{ seq: number; generation: number } | null>(null);
  const acknowledgement = useRef<{
    id: string; seq: number; generation: number; operation: number;
  } | null>(null);
  // Composer text is owned by MessageComposer; the view subscribes only to the
  // reply reference and the draft epoch so keystrokes never re-render it.
  const replyToId = useSyncExternalStore(
    subscribeDrafts,
    useCallback(() => readComposerDraft(conversation.id).replyToId, [conversation.id]),
  );
  const draftEpoch = useSyncExternalStore(
    subscribeDrafts,
    useCallback(() => composerDraftEpoch(conversation.id), [conversation.id]),
  );
  const updateComposerDraft = useCallback((patch: Partial<ComposerDraft>) => {
    writeComposerDraft(conversation.id, { ...readComposerDraft(conversation.id), ...patch });
  }, [conversation.id]);
  const mentionMembers = useMemo(
    () => members.filter((member) => conversation.memberIds.includes(member.userId)),
    [members, conversation.memberIds],
  );
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
    contentRef = useRef<HTMLDivElement>(null),
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
  useEffect(() => {
    setDraggingFiles(false);
  }, [conversation.id, uploadsEnabled]);
  const {
    fetchLatest,
    focusMessage,
    loadUnreadContext,
    returnToLatest,
    locate,
  } = useConversationContextLoader({
    conversation,
    messages,
    located,
    readSeq,
    initialMessageId,
    onClearMessageTarget,
    visible,
    membersOpen,
    highlightId,
    setHighlightId,
    setEntry,
    setAnchorPending,
    setHasMore,
    setLatestLoaded,
    setLocated,
    setUnreadContext,
    setLocating,
    setLoadingOlder,
    setLoadingNewer,
    setAtBottom,
    setLoading,
    setError,
    setResolvedTargets,
    fetchedLatestRef: fetchedLatest,
    acknowledgementRef: acknowledgement,
    historyBeforeRef: historyBefore,
    initialScrollRef: initialScroll,
    navigationRef: navigation,
    scrollRef,
  });
  const latest = messages.filter((m) => !m.status).at(-1)?.seq || 0;
  const currentLastSeq = Math.max(
    conversation.lastSeq,
    snapshot.conversations[conversation.id]?.lastSeq ?? 0,
    latest,
  );
  const [leftBottomSeq, setLeftBottomSeq] = useState(readSeq);
  const focusedConversationRef = useRef("");
  useEffect(() => {
    if (
      loading ||
      initialMessageId ||
      located ||
      locating ||
      focusedConversationRef.current === conversation.id
    )
      return;
    focusedConversationRef.current = conversation.id;
    if (
      !window.matchMedia?.("(pointer: coarse)").matches &&
      !inputRef.current?.closest("[inert]") &&
      !document.querySelector(".modal-backdrop")
    ) {
      inputRef.current?.focus({ preventScroll: true });
    }
  }, [conversation.id, initialMessageId, loading, located, locating]);
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

  const { scheduleSeen, markLatestRead, resetReadFailures } = useConversationReadTracking({
    conversation,
    messages,
    located,
    unreadContext,
    searchOpen,
    visible,
    setVisible,
    loading,
    atBottom,
    setAtBottom,
    locating,
    anchorPending,
    setAnchorPending,
    firstUnread,
    latestLoaded,
    tailId,
    readSeq,
    latest,
    generation,
    highlightId,
    membersOpen,
    scrollRef,
    contentRef,
    initialScrollRef: initialScroll,
    acknowledgementRef: acknowledgement,
    navigationRef: navigation,
    lastReadRef: lastRead,
    setError,
  });
  async function older() {
    const boundary = historyBefore.current;
    if (!boundary || boundary.generation !== chatStore.generation || loadingOlder) return;
    const first = extendHistoryBoundary(boundary.seq, messages);
    const token = chatStore.generation;
    const reactionScope = chatStore.reactionScope(conversation.id);
    const operation = navigation.current;
    const height = scrollRef.current?.scrollHeight || 0;
    setLoadingOlder(true);
    setError("");
    try {
      const page = await api.messages(conversation.id, first);
      if (token !== chatStore.generation || operation !== navigation.current || (reactionScope && !chatStore.reactionScopeCurrent(reactionScope))) return;
      historyBefore.current = page.messages[0]
        ? { seq: page.messages[0].seq, generation: token }
        : null;
      chatStore.ingestMessageReactions(page.messages, reactionScope);
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
    const reactionScope = chatStore.reactionScope(conversation.id);
    const operation = navigation.current;
    const current = () =>
      token === chatStore.generation &&
      operation === navigation.current &&
      chatStore.reactionScopeCurrent(reactionScope);
    setLoadingNewer(true);
    setError("");
    try {
      const page = await api.messagesAround(conversation.id, latest + 1);
      if (!current()) return;
      if (!page.messages.some((message) => message.seq > latest)) {
        setError("后续消息暂未加载，请重试");
        return;
      }
      chatStore.ingestMessageReactions(page.messages, reactionScope);
      setLocated((old) => (old ? reconcileMessages(old, page.messages) : old));
    } catch (e) {
      if (current()) setError(errorText(e));
    } finally {
      if (current()) setLoadingNewer(false);
    }
  }
  const canEdit = !!data?.capabilities["im.message.edit"];
  const canRetract = !!data?.capabilities["im.message.retract"];
  const canReact = !!data?.capabilities["im.reactions.create"];
  const canViewReceipts = !!data?.capabilities["im.read_receipts.view"];
  const canConfigureNotifications = !!data?.capabilities["im.notifications.configure"];
  const presenceEnabled = !!data?.capabilities["im.presence.publish"];
  const latestOwnMessageId = useMemo(() => {
    for (let i = messages.length - 1; i >= 0; i--) {
      const m = messages[i];
      if (!m.status && !m.retractedAt && m.senderId === userId) {
        return m.id;
      }
    }
    return "";
  }, [messages, userId]);
  useConversationReceiptsSync(conversation.id, canViewReceipts, !!latestOwnMessageId);
  // Stable handlers keep memoized rows from re-rendering when unrelated view
  // state changes; the refs always hold the latest closures.
  const locateRef = useRef(locate);
  const editRef = useRef(edit);
  useLayoutEffect(() => {
    locateRef.current = locate;
    editRef.current = edit;
  });
  const locateStable = useCallback((id: string) => locateRef.current(id), []);
  const submitEdit = useCallback((event: FormEvent) => editRef.current(event), []);
  const replyToMessage = useCallback((id: string) => {
    setActionMessageId("");
    updateComposerDraft({ replyToId: id });
    inputRef.current?.focus();
  }, [setActionMessageId, updateComposerDraft]);
  const clearReply = useCallback(
    () => updateComposerDraft({ replyToId: "" }),
    [updateComposerDraft],
  );
  // Grouping and reply targets derive from neighbours/other rows; computing
  // them once per message list (with id maps) keeps each row's props primitive
  // or referentially stable and avoids an O(n²) reply lookup.
  const rows = useMemo(() => {
    const firstById = (list: LocalMessage[]) => {
      const map = new Map<string, LocalMessage>();
      for (const m of list) if (!map.has(m.id)) map.set(m.id, m);
      return map;
    };
    const storeById = firstById(allMessages);
    const viewById = messages === allMessages ? storeById : firstById(messages);
    return messages.map((message, index) => {
      let replyTarget: LocalMessage | undefined;
      if (message.replyToId) {
        const candidates: LocalMessage[] = [];
        const cachedTarget = resolvedTargets[message.replyToId];
        if (cachedTarget && cachedTarget.conversationId === conversation.id)
          candidates.push(cachedTarget);
        const storeTarget = storeById.get(message.replyToId);
        if (storeTarget) candidates.push(storeTarget);
        const viewTarget = viewById.get(message.replyToId);
        if (viewTarget) candidates.push(viewTarget);
        replyTarget = candidates.length
          ? reconcileMessages([candidates[0]], candidates.slice(1))[0]
          : undefined;
      }
      return {
        ...messageGrouping(message, messages[index - 1], message.id === firstUnread),
        previousId: messages[index - 1]?.id,
        nextId: messages[index + 1]?.id,
        replyTarget,
      };
    });
  }, [allMessages, conversation.id, firstUnread, messages, resolvedTargets]);
  const unreadBaselineSeq = Math.max(readSeq, leftBottomSeq);
  const unreadWhileScrolledUp = useMemo(
    () =>
      messages.filter(
        (m) =>
          !m.status &&
          !m.retractedAt &&
          m.seq > unreadBaselineSeq &&
          m.senderId !== userId,
      ),
    [messages, unreadBaselineSeq, userId],
  );
  const jumpUnreadCount =
    unreadWhileScrolledUp.length +
    Math.max(0, currentLastSeq - Math.max(latest, unreadBaselineSeq));
  const hasUnreadMention = useMemo(
    () =>
      messages.some((m, index) => {
        if (m.status || m.retractedAt || m.seq <= unreadBaselineSeq || m.senderId === userId)
          return false;
        if (m.mentions?.some((mention) => mention.userId === userId)) return true;
        const target = rows[index]?.replyTarget;
        return !!target && !target.retractedAt && target.senderId === userId;
      }),
    [messages, rows, unreadBaselineSeq, userId],
  );
  const hasFileTransfer = (event: DragEvent<HTMLElement>) =>
    Array.from(event.dataTransfer?.types || []).includes("Files") ||
    (event.dataTransfer?.files?.length ?? 0) > 0;
  const agentOwners = useMemo(() => {
    const owners = new Map<string, string | undefined>();
    for (const member of members)
      if (!owners.has(member.userId)) owners.set(member.userId, member.ownerName);
    return owners;
  }, [members]);
  const rovingMessageId =
    (focusedMessageId && messages.some((m) => m.id === focusedMessageId) ? focusedMessageId : undefined) ||
    (highlightId && messages.some((m) => m.id === highlightId) ? highlightId : undefined) ||
    messages.at(-1)?.id ||
    "";

  return (
    <section
      className="conversation-view"
      onKeyDown={(event) => { if (event.key === "Escape") setActionMessageId(""); }}
      onDragEnter={(event) => {
        if (!uploadsEnabled || !hasFileTransfer(event)) return;
        event.preventDefault();
        setDraggingFiles(true);
      }}
      onDragOver={(event) => {
        if (!uploadsEnabled || !hasFileTransfer(event)) return;
        event.preventDefault();
        if (event.dataTransfer) event.dataTransfer.dropEffect = "copy";
        if (!draggingFiles) setDraggingFiles(true);
      }}
      onDragLeave={(event) => {
        const next = event.relatedTarget as Node | null;
        if (next && event.currentTarget.contains(next)) return;
        setDraggingFiles(false);
      }}
      onDrop={(event) => {
        setDraggingFiles(false);
        if (!uploadsEnabled || !hasFileTransfer(event)) return;
        event.preventDefault();
        const files = Array.from(event.dataTransfer?.files || []);
        if (files.length > 0) {
          attachments.add(files);
          inputRef.current?.focus({ preventScroll: true });
        }
      }}
    >
      {uploadsEnabled && draggingFiles && (
        <div
          className="conversation-drop-overlay"
          role="region"
          aria-label="释放文件以上传到当前对话"
        >
          <span>释放文件以上传到当前对话</span>
        </div>
      )}
      <ConversationHeader
        conversation={conversation} name={name} membersOpen={membersOpen}
        onMenu={onMenu} onSearchOpen={() => setSearchOpen(true)} onMembers={onMembers}
        members={mentionMembers} userId={userId}
        canConfigureNotifications={canConfigureNotifications}
        presenceEnabled={presenceEnabled}
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
          const bottom = !located && el.scrollHeight - el.scrollTop - el.clientHeight < 80;
          if (atBottom || bottom) {
            setLeftBottomSeq((prev) => Math.max(prev, readSeq, currentLastSeq));
          }
          setAtBottom(bottom);
          resetReadFailures();
          scheduleSeen();
        }}
      >
        <div className="message-scroll-content" ref={contentRef}>
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
          {messages.map((message, index) => {
            const row = rows[index];
            const isEditing = editing?.id === message.id;
            return (
              <MessageItem
                key={message.id} message={message}
                showDate={row.showDate} compact={row.compact}
                previousId={row.previousId} nextId={row.nextId} replyTarget={row.replyTarget}
                workspaceId={conversation.workspaceId}
                conversationId={conversation.id}
                conversationType={conversation.type}
                userId={userId}
                agentOwnerName={
                  message.senderKind === "agent" ? agentOwners.get(message.senderId) : undefined
                }
                mentionMembers={isEditing ? mentionMembers : noMembers}
                allMembers={mentionMembers}
                mentionsEnabled={mentionsEnabled}
                canEdit={canEdit} canRetract={canRetract}
                canReact={canReact}
                canViewReceipts={canViewReceipts}
                isLatestOwn={message.id === latestOwnMessageId}
                senderReadSeq={message.senderId === userId ? readSeq : 0}
                showUnreadDivider={message.id === firstUnread && (entry?.readSeq ?? 0) > 0}
                isRovingTarget={message.id === rovingMessageId}
                highlighted={highlightId === message.id}
                actionsOpen={actionMessageId === message.id}
                editing={isEditing ? editing : null}
                editBody={isEditing ? editBody : ""}
                editMentions={isEditing ? editMentions : noMentions}
                actionBusy={isEditing && actionBusy}
                actionError={isEditing ? actionError : ""}
                inputRef={inputRef}
                setFocusedMessageId={setFocusedMessageId} setActionMessageId={setActionMessageId}
                focusMessage={focusMessage} locate={locateStable} onEditSubmit={submitEdit}
                setEditBody={setEditBody} setEditMentions={setEditMentions}
                setEditing={setEditing} startEdit={startEdit}
                onReply={replyToMessage}
                onRetractRequest={requestRetract}
              />
            );
          })}
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
      </div>
      {!atBottom && !located && !locating && (
        <button
          className={`jump-latest${jumpUnreadCount > 0 ? " has-unread" : ""}${hasUnreadMention ? " has-mention" : ""}`}
          onClick={() => {
            setLeftBottomSeq((prev) => Math.max(prev, readSeq, currentLastSeq));
            setAtBottom(true);
            scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight, behavior: "smooth" });
          }}
        >
          <ArrowDown size={ICON.xs} />
          {jumpUnreadCount > 0
            ? `${hasUnreadMention ? "@ " : ""}${jumpUnreadCount} 条新消息`
            : "回到最新消息"}
        </button>
      )}
      <MessageComposer
        key={`${conversation.id}\u0000${draftEpoch}`} draftEpoch={draftEpoch}
        conversation={conversation} user={user!} name={name}
        replyToId={replyToId} reply={reply} replyError={replyError}
        attachments={attachments} attachmentLimits={attachmentLimits}
        uploadsEnabled={uploadsEnabled} attachmentBlocked={attachmentBlocked}
        mentionMembers={mentionMembers} mentionsEnabled={mentionsEnabled}
        inputRef={inputRef} editing={editing}
        located={!!located} latestLoaded={latestLoaded} latest={latest}
        canEdit={canEdit} messages={messages} sendTyping={sendTyping}
        onClearReply={clearReply}
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


