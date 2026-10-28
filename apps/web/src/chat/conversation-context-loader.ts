import { useCallback, useEffect, useLayoutEffect } from "react";
import type { Dispatch, MutableRefObject, RefObject, SetStateAction } from "react";
import { api } from "@teamie/api-client";
import type { Conversation, Message } from "@teamie/api-client";
import { errorText } from "../errors";
import { chatStore, reconcileMessages } from "../store";
import type { LocalMessage } from "../store";

export function useConversationContextLoader({
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
  fetchedLatestRef,
  acknowledgementRef,
  historyBeforeRef,
  initialScrollRef,
  navigationRef,
  scrollRef,
}: {
  conversation: Conversation;
  messages: LocalMessage[];
  located: LocalMessage[] | null;
  readSeq: number;
  initialMessageId: string;
  onClearMessageTarget?: () => void;
  visible: boolean;
  membersOpen: boolean;
  highlightId: string;
  setHighlightId: (id: string) => void;
  setEntry: (entry: { readSeq: number; lastSeq: number } | null) => void;
  setAnchorPending: (v: boolean) => void;
  setHasMore: (v: boolean) => void;
  setLatestLoaded: (v: boolean) => void;
  setLocated: Dispatch<SetStateAction<LocalMessage[] | null>>;
  setUnreadContext: (v: boolean) => void;
  setLocating: (v: boolean) => void;
  setLoadingOlder: (v: boolean) => void;
  setLoadingNewer: (v: boolean) => void;
  setAtBottom: (v: boolean) => void;
  setLoading: (v: boolean) => void;
  setError: (err: string) => void;
  setResolvedTargets: Dispatch<SetStateAction<Record<string, Message>>>;
  fetchedLatestRef: MutableRefObject<boolean>;
  acknowledgementRef: MutableRefObject<{
    id: string;
    seq: number;
    generation: number;
    operation: number;
  } | null>;
  historyBeforeRef: MutableRefObject<{ seq: number; generation: number } | null>;
  initialScrollRef: MutableRefObject<boolean>;
  navigationRef: MutableRefObject<number>;
  scrollRef: RefObject<HTMLDivElement | null>;
}) {
  const fetchLatest = useCallback(async () => {
    const token = chatStore.generation;
    const reactionScope = chatStore.reactionScope(conversation.id);
    const operation = ++navigationRef.current;
    const hasConfirmed = (chatStore.getSnapshot().messages[conversation.id] || []).some(
      (m) => !m.status,
    );
    if (fetchedLatestRef.current) {
      const current = chatStore.getSnapshot().conversations[conversation.id];
      if (current) {
        setEntry({ readSeq: current.readSeq, lastSeq: current.lastSeq });
        setAnchorPending(current.lastSeq > current.readSeq);
      }
    }
    fetchedLatestRef.current = true;
    acknowledgementRef.current = null;
    historyBeforeRef.current = null;
    setHasMore(false);
    setLocated(null);
    setUnreadContext(false);
    setHighlightId("");
    setLocating(false);
    setLoadingOlder(false);
    setLoadingNewer(false);
    initialScrollRef.current = true;
    setAtBottom(true);
    setLoading(!hasConfirmed);
    setError("");
    try {
      const page = await api.messages(conversation.id);
      if (token !== chatStore.generation || operation !== navigationRef.current || (reactionScope && !chatStore.reactionScopeCurrent(reactionScope))) return;
      historyBeforeRef.current = page.messages[0]
        ? { seq: page.messages[0].seq, generation: token }
        : null;
      chatStore.merge(page.messages);
      setHasMore(page.hasMore);
      setLatestLoaded(true);
    } catch (e) {
      if (token === chatStore.generation && operation === navigationRef.current)
        setError(errorText(e));
    } finally {
      if (token === chatStore.generation && operation === navigationRef.current)
        setLoading(false);
    }
  }, [
    acknowledgementRef,
    conversation.id,
    fetchedLatestRef,
    historyBeforeRef,
    initialScrollRef,
    navigationRef,
    setAnchorPending,
    setAtBottom,
    setEntry,
    setError,
    setHasMore,
    setHighlightId,
    setLatestLoaded,
    setLoading,
    setLoadingNewer,
    setLoadingOlder,
    setLocated,
    setLocating,
    setUnreadContext,
  ]);

  const focusMessage = useCallback(
    (id: string) => {
      const target = document.getElementById(`message-${id}`);
      if (!target || !scrollRef.current?.contains(target) || target.closest("[inert]")) return;
      target.scrollIntoView({
        block: "center",
        behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "instant" : "smooth",
      });
      target.focus({ preventScroll: true });
    },
    [scrollRef],
  );

  useLayoutEffect(() => {
    if (highlightId) focusMessage(highlightId);
  }, [highlightId, focusMessage, visible, membersOpen]);

  const loadContext = useCallback(
    async (id: string, readOnDisplay = false) => {
      const token = chatStore.generation;
      const reactionScope = chatStore.reactionScope(conversation.id);
      const operation = ++navigationRef.current;
      const current = () =>
        token === chatStore.generation && operation === navigationRef.current && (!reactionScope || chatStore.reactionScopeCurrent(reactionScope));
      setAtBottom(false);
      setLoading(false);
      setLoadingOlder(false);
      setLoadingNewer(false);
      setLocating(true);
      acknowledgementRef.current = null;
      historyBeforeRef.current = null;
      setHasMore(false);
      initialScrollRef.current = false;
      setError("");
      try {
        const target = await api.message(conversation.id, id);
        if (!current()) return;
        chatStore.ingestMessageReactions([target.message], reactionScope);
        setResolvedTargets((old) => ({
          ...old,
          [target.message.id]: old[target.message.id]
            ? reconcileMessages([old[target.message.id]], [target.message])[0]
            : target.message,
        }));
        const page = await api.messagesAround(conversation.id, target.message.seq);
        if (!current()) return;
        chatStore.ingestMessageReactions(page.messages, reactionScope);
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
          acknowledgementRef.current = { id, seq: resolved.seq, generation: token, operation };
        setLocated(context);
        setUnreadContext(false);
        historyBeforeRef.current = context[0] ? { seq: context[0].seq, generation: token } : null;
        setHasMore((historyBeforeRef.current?.seq ?? 1) > 1);
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
    [
      acknowledgementRef,
      conversation.id,
      historyBeforeRef,
      initialScrollRef,
      navigationRef,
      setAtBottom,
      setError,
      setHasMore,
      setHighlightId,
      setLoading,
      setLoadingNewer,
      setLoadingOlder,
      setLocated,
      setLocating,
      setResolvedTargets,
      setUnreadContext,
    ],
  );

  const loadUnreadContext = useCallback(async () => {
    const seq = readSeq + 1;
    const token = chatStore.generation;
    const reactionScope = chatStore.reactionScope(conversation.id);
    const operation = ++navigationRef.current;
    const current = () =>
      token === chatStore.generation && operation === navigationRef.current && (!reactionScope || chatStore.reactionScopeCurrent(reactionScope));
    setAnchorPending(false);
    setAtBottom(false);
    setLoading(false);
    setLoadingOlder(false);
    setLoadingNewer(false);
    setLocating(true);
    acknowledgementRef.current = null;
    historyBeforeRef.current = null;
    setHasMore(false);
    initialScrollRef.current = false;
    setError("");
    try {
      const page = await api.messagesAround(conversation.id, seq);
      if (!current()) return;
      chatStore.ingestMessageReactions(page.messages, reactionScope);
      const context = reconcileMessages(
        page.messages,
        (chatStore.getSnapshot().messages[conversation.id] || []).filter((message) =>
          page.messages.some((m) => m.id === message.id),
        ),
      );
      setLocated(context);
      setUnreadContext(true);
      historyBeforeRef.current = context[0] ? { seq: context[0].seq, generation: token } : null;
      setHasMore((historyBeforeRef.current?.seq ?? 1) > 1);
      const target = context.find((m) => !m.status && m.seq >= seq);
      if (target) setHighlightId(target.id);
    } catch (e) {
      if (current()) setError(errorText(e));
    } finally {
      if (current()) setLocating(false);
    }
  }, [
    acknowledgementRef,
    conversation.id,
    historyBeforeRef,
    initialScrollRef,
    navigationRef,
    readSeq,
    setAnchorPending,
    setAtBottom,
    setError,
    setHasMore,
    setHighlightId,
    setLoading,
    setLoadingNewer,
    setLoadingOlder,
    setLocated,
    setLocating,
    setUnreadContext,
  ]);

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
    navigationRef.current++;
    acknowledgementRef.current = null;
    setLoading(false);
    setLoadingOlder(false);
    setLoadingNewer(false);
    setLocating(false);
    initialScrollRef.current = false;
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

  return {
    fetchLatest,
    focusMessage,
    loadContext,
    loadUnreadContext,
    returnToLatest,
    locate,
  };
}
