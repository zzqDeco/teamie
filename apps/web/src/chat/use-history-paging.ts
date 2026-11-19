import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { Dispatch, MutableRefObject, RefObject, SetStateAction } from "react";
import { api } from "@teamie/api-client";
import type { Conversation } from "@teamie/api-client";
import { errorText } from "../errors";
import { chatStore, reconcileMessages } from "../store";
import type { LocalMessage } from "../store";
import { extendHistoryBoundary } from "../composer-drafts";
import { subscribeBodyCollapse } from "./collapsible-body";
import type { LocatedScrollState } from "./conversation-context-loader";

type Anchor = { height: number; top: number; token: number; operation: number };
type PagingError = { scope: string; direction: "older" | "newer"; message: string };

/**
 * Older/newer page loading for the timeline. Older pages keep the reader's
 * viewport on the same message: the scroll position is compensated in a
 * layout effect right after the prepended rows commit (the scroller has
 * `overflow-anchor: none`, so native anchoring cannot double-compensate).
 * Top and bottom sentinels observed with IntersectionObserver load pages
 * automatically; visible buttons remain the fallback and a11y entry.
 */
export function useHistoryPaging({
  conversation,
  messages,
  located,
  unreadContext,
  latestLoaded,
  latest,
  currentLastSeq,
  hasMore,
  loading,
  locating,
  loadingOlder,
  setLoadingOlder,
  loadingNewer,
  setLoadingNewer,
  setHasMore,
  setLatestLoaded,
  setLocated,
  setUnreadContext,
  setAnchorPending,
  setHighlightId,
  initialMessageId,
  onClearMessageTarget,
  onRetryFallback,
  error,
  setError,
  historyBeforeRef,
  navigationRef,
  initialScrollRef,
  fetchedLatestRef,
  skipNextFetchLatestRef,
  locatedScrollSettledRef,
  scrollRef,
}: {
  conversation: Conversation;
  messages: LocalMessage[];
  located: LocalMessage[] | null;
  unreadContext: boolean;
  latestLoaded: boolean;
  latest: number;
  currentLastSeq: number;
  hasMore: boolean;
  loading: boolean;
  locating: boolean;
  loadingOlder: boolean;
  setLoadingOlder: Dispatch<SetStateAction<boolean>>;
  loadingNewer: boolean;
  setLoadingNewer: Dispatch<SetStateAction<boolean>>;
  setHasMore: Dispatch<SetStateAction<boolean>>;
  setLatestLoaded: Dispatch<SetStateAction<boolean>>;
  setLocated: Dispatch<SetStateAction<LocalMessage[] | null>>;
  setUnreadContext: Dispatch<SetStateAction<boolean>>;
  setAnchorPending: Dispatch<SetStateAction<boolean>>;
  setHighlightId: Dispatch<SetStateAction<string>>;
  initialMessageId: string;
  onClearMessageTarget?: () => void;
  onRetryFallback: () => void;
  error: string;
  setError: Dispatch<SetStateAction<string>>;
  historyBeforeRef: MutableRefObject<{ seq: number; generation: number } | null>;
  navigationRef: MutableRefObject<number>;
  initialScrollRef: MutableRefObject<boolean>;
  fetchedLatestRef: MutableRefObject<boolean>;
  skipNextFetchLatestRef: MutableRefObject<string | null>;
  locatedScrollSettledRef: MutableRefObject<LocatedScrollState | null>;
  scrollRef: RefObject<HTMLDivElement | null>;
}) {
  const [anchorTick, setAnchorTick] = useState(0);
  const [olderFailedScope, setOlderFailedScope] = useState<string | null>(null);
  const [newerFailedScope, setNewerFailedScope] = useState<string | null>(null);
  const [newerArmedScope, setNewerArmedScope] = useState<string | null>(null);
  const [pagingError, setPagingError] = useState<PagingError | null>(null);
  const anchor = useRef<Anchor | null>(null);
  const sentinelRef = useRef<HTMLDivElement>(null);
  const bottomSentinelRef = useRef<HTMLDivElement>(null);
  // Navigation id of in-flight pages; a navigation reset frees them.
  const busy = useRef<number | null>(null);
  const newerBusyRef = useRef<number | null>(null);

  const applyAnchor = useCallback(() => {
    const pending = anchor.current;
    const el = scrollRef.current;
    if (!pending) return;
    if (
      !el ||
      pending.token !== chatStore.generation ||
      pending.operation !== navigationRef.current
    ) {
      anchor.current = null;
      return;
    }
    el.scrollTop = Math.max(0, pending.top + (el.scrollHeight - pending.height));
    if (locatedScrollSettledRef.current) {
      locatedScrollSettledRef.current.lastScrollTop = el.scrollTop;
    }
  }, [locatedScrollSettledRef, navigationRef, scrollRef]);

  useLayoutEffect(() => {
    applyAnchor();
    const settled = anchor.current;
    if (!settled) return;
    const timer = setTimeout(() => {
      if (anchor.current === settled) anchor.current = null;
    }, 0);
    return () => clearTimeout(timer);
  }, [applyAnchor, messages, anchorTick]);

  useLayoutEffect(() => subscribeBodyCollapse(applyAnchor), [applyAnchor]);

  async function older() {
    const boundary = historyBeforeRef.current;
    if (
      !boundary ||
      boundary.generation !== chatStore.generation ||
      busy.current === navigationRef.current ||
      newerBusyRef.current === navigationRef.current ||
      loadingNewer
    )
      return;
    const first = extendHistoryBoundary(boundary.seq, messages);
    const token = chatStore.generation;
    const reactionScope = chatStore.reactionScope(conversation.id);
    const operation = navigationRef.current;
    const scopeKey = `${token}:${operation}`;
    const current = () => token === chatStore.generation && operation === navigationRef.current;
    busy.current = operation;
    setOlderFailedScope(null);
    setPagingError(null);
    setLoadingOlder(true);
    setError("");
    try {
      const page = await api.messages(conversation.id, first);
      if (!current() || (reactionScope && !chatStore.reactionScopeCurrent(reactionScope))) return;
      historyBeforeRef.current = page.messages[0]
        ? { seq: page.messages[0].seq, generation: token }
        : null;
      const el = scrollRef.current;
      anchor.current = el
        ? { height: el.scrollHeight, top: el.scrollTop, token, operation }
        : null;
      chatStore.ingestMessageReactions(page.messages, reactionScope);
      if (located) setLocated((old) => (old ? reconcileMessages(old, page.messages) : old));
      else chatStore.merge(page.messages);
      setHasMore(page.hasMore);
      setAnchorTick((value) => value + 1);
    } catch (e) {
      if (current()) {
        const message = errorText(e);
        setError(message);
        setOlderFailedScope(scopeKey);
        setPagingError({ scope: scopeKey, direction: "older", message });
      }
    } finally {
      if (current()) {
        busy.current = null;
        setLoadingOlder(false);
      }
    }
  }

  async function newer() {
    if (
      !located ||
      latest >= currentLastSeq ||
      busy.current === navigationRef.current ||
      newerBusyRef.current === navigationRef.current ||
      loadingNewer ||
      loadingOlder ||
      locating
    )
      return;
    const token = chatStore.generation;
    const reactionScope = chatStore.reactionScope(conversation.id);
    const operation = navigationRef.current;
    const scopeKey = `${token}:${operation}`;
    const current = () =>
      token === chatStore.generation &&
      operation === navigationRef.current &&
      (!reactionScope || chatStore.reactionScopeCurrent(reactionScope));
    newerBusyRef.current = operation;
    setNewerFailedScope(null);
    setPagingError(null);
    setLoadingNewer(true);
    setError("");
    try {
      const page = await api.messagesAround(conversation.id, latest + 1);
      if (!current()) return;
      if (!page.messages.some((message) => message.seq > latest)) {
        const message = "后续消息暂未加载，请重试";
        setError(message);
        setNewerFailedScope(scopeKey);
        setPagingError({ scope: scopeKey, direction: "newer", message });
        return;
      }
      chatStore.ingestMessageReactions(page.messages, reactionScope);
      const snap = chatStore.getSnapshot();
      const freshStoreMessages = snap.messages[conversation.id] || [];
      const freshStoreConv = snap.conversations[conversation.id];
      const freshStoreLatestSeq = freshStoreMessages.reduce(
        (max, m) => (m.status ? max : Math.max(max, m.seq)),
        0,
      );
      const mergedLocated = reconcileMessages(located, page.messages);
      const confirmedMerged = mergedLocated.filter((m) => !m.status);
      const mergedTailSeq = confirmedMerged.at(-1)?.seq || 0;
      const mergedMinSeq = confirmedMerged[0]?.seq || 0;
      const freshLastSeq = Math.max(
        conversation.lastSeq,
        freshStoreConv?.lastSeq ?? 0,
        freshStoreLatestSeq,
        mergedTailSeq,
      );
      const reachedLatestSeq = freshLastSeq > 0 && mergedTailSeq >= freshLastSeq;
      const storeContiguousStart =
        latestLoaded &&
        freshStoreMessages.length > 0 &&
        freshStoreLatestSeq > 0 &&
        freshStoreLatestSeq >= freshLastSeq
          ? extendHistoryBoundary(freshStoreLatestSeq, freshStoreMessages)
          : 0;
      const reachedStoreWindow =
        !unreadContext &&
        storeContiguousStart > 0 &&
        mergedTailSeq >= storeContiguousStart - 1;
      if (reachedLatestSeq || reachedStoreWindow) {
        chatStore.merge(mergedLocated);
        const mergedStore = chatStore.getSnapshot().messages[conversation.id] || [];
        const mergedStoreLatest = mergedStore.reduce(
          (max, m) => (m.status ? max : Math.max(max, m.seq)),
          0,
        );
        const contiguousMin =
          mergedStoreLatest > 0
            ? extendHistoryBoundary(mergedStoreLatest, mergedStore)
            : mergedMinSeq;
        const nextBeforeSeq =
          mergedMinSeq > 0 && contiguousMin > 0
            ? Math.min(mergedMinSeq, contiguousMin)
            : mergedMinSeq || contiguousMin;
        historyBeforeRef.current =
          nextBeforeSeq > 0 ? { seq: nextBeforeSeq, generation: token } : null;
        setHasMore(nextBeforeSeq > 1);
        setLatestLoaded(true);
        fetchedLatestRef.current = true;
        initialScrollRef.current = false;
        setAnchorPending(false);
        locatedScrollSettledRef.current = null;
        setLocated(null);
        setUnreadContext(false);
        setHighlightId("");
        if (initialMessageId && onClearMessageTarget) {
          skipNextFetchLatestRef.current = conversation.id;
          onClearMessageTarget();
        }
        return;
      }
      setLocated((old) => (old ? reconcileMessages(old, page.messages) : old));
    } catch (e) {
      if (current()) {
        const message = errorText(e);
        setError(message);
        setNewerFailedScope(scopeKey);
        setPagingError({ scope: scopeKey, direction: "newer", message });
      }
    } finally {
      if (current()) {
        newerBusyRef.current = null;
        setLoadingNewer(false);
      }
    }
  }

  const olderRef = useRef(older);
  const newerRef = useRef(newer);
  useLayoutEffect(() => {
    olderRef.current = older;
    newerRef.current = newer;
  });
  const loadOlder = useCallback(() => olderRef.current(), []);
  const loadNewer = useCallback(() => newerRef.current(), []);
  const retryError = useCallback(() => {
    const currentScope = `${chatStore.generation}:${navigationRef.current}`;
    if (pagingError && pagingError.scope === currentScope && pagingError.message === error) {
      if (pagingError.direction === "older") {
        void olderRef.current();
        return;
      }
      void newerRef.current();
      return;
    }
    void onRetryFallback();
  }, [error, navigationRef, onRetryFallback, pagingError]);

  const autoLoad =
    hasMore &&
    !loading &&
    !locating &&
    !loadingOlder &&
    !loadingNewer;

  // Re-created after each page so a sentinel that is still visible (short
  // pages) reports again and keeps filling the viewport until `hasMore` ends.
  useEffect(() => {
    const sentinel = sentinelRef.current;
    const root = scrollRef.current;
    const currentScope = `${chatStore.generation}:${navigationRef.current}`;
    if (
      !autoLoad ||
      olderFailedScope === currentScope ||
      !sentinel ||
      !root ||
      typeof IntersectionObserver === "undefined"
    )
      return;
    const observer = new IntersectionObserver(
      (entries) => {
        // Never during the initial bottom anchoring of a freshly opened view.
        if (initialScrollRef.current) return;
        if (entries.some((entry) => entry.isIntersecting)) void loadOlder();
      },
      { root, rootMargin: "200px 0px 0px 0px" },
    );
    observer.observe(sentinel);
    return () => observer.disconnect();
  }, [
    autoLoad,
    conversation.id,
    initialScrollRef,
    loadOlder,
    messages.length,
    navigationRef,
    olderFailedScope,
    scrollRef,
  ]);

  // Arm bottom auto-load only after initial located target focus has settled
  // and the user actively scrolls downward in the located view.
  useEffect(() => {
    const root = scrollRef.current;
    if (!located || latest >= currentLastSeq || locating || !root) return;
    const armIfSettled = () => {
      const scope = `${chatStore.generation}:${navigationRef.current}`;
      const settled = locatedScrollSettledRef.current;
      if (!settled || settled.scope !== scope) return;
      setNewerArmedScope((prev) => (prev === scope ? prev : scope));
    };
    const onScroll = () => {
      const scope = `${chatStore.generation}:${navigationRef.current}`;
      const settled = locatedScrollSettledRef.current;
      if (!settled || settled.scope !== scope) return;
      const top = root.scrollTop;
      if (top > settled.lastScrollTop) {
        settled.lastScrollTop = top;
        setNewerArmedScope((prev) => (prev === scope ? prev : scope));
      } else if (top < settled.lastScrollTop) {
        settled.lastScrollTop = top;
      }
    };
    const onWheel = (event: WheelEvent) => {
      if (event.deltaY > 0) armIfSettled();
    };
    root.addEventListener("scroll", onScroll, { passive: true });
    root.addEventListener("wheel", onWheel, { passive: true });
    return () => {
      root.removeEventListener("scroll", onScroll);
      root.removeEventListener("wheel", onWheel);
    };
  }, [
    conversation.id,
    currentLastSeq,
    latest,
    located,
    locatedScrollSettledRef,
    locating,
    navigationRef,
    scrollRef,
  ]);

  const bottomAutoLoad =
    located !== null &&
    latest < currentLastSeq &&
    !loading &&
    !locating &&
    !loadingOlder &&
    !loadingNewer;

  useEffect(() => {
    const sentinel = bottomSentinelRef.current;
    const root = scrollRef.current;
    const currentScope = `${chatStore.generation}:${navigationRef.current}`;
    if (
      !bottomAutoLoad ||
      newerArmedScope !== currentScope ||
      newerFailedScope === currentScope ||
      !sentinel ||
      !root ||
      typeof IntersectionObserver === "undefined"
    )
      return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (
          initialScrollRef.current ||
          `${chatStore.generation}:${navigationRef.current}` !== currentScope
        )
          return;
        if (entries.some((entry) => entry.isIntersecting)) void loadNewer();
      },
      { root, rootMargin: "0px 0px 200px 0px" },
    );
    observer.observe(sentinel);
    return () => observer.disconnect();
  }, [
    bottomAutoLoad,
    conversation.id,
    initialScrollRef,
    loadNewer,
    messages.length,
    navigationRef,
    newerArmedScope,
    newerFailedScope,
    scrollRef,
  ]);

  return { older: loadOlder, newer: loadNewer, sentinelRef, bottomSentinelRef, retryError };
}
