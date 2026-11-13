import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { Dispatch, MutableRefObject, RefObject, SetStateAction } from "react";
import { api } from "@teamie/api-client";
import type { Conversation } from "@teamie/api-client";
import { errorText } from "../errors";
import { chatStore, reconcileMessages } from "../store";
import type { LocalMessage } from "../store";
import { extendHistoryBoundary } from "../composer-drafts";
import { subscribeBodyCollapse } from "./collapsible-body";

type Anchor = { height: number; top: number; token: number; operation: number };

/**
 * Older/newer page loading for the timeline. Older pages keep the reader's
 * viewport on the same message: the scroll position is compensated in a
 * layout effect right after the prepended rows commit (the scroller has
 * `overflow-anchor: none`, so native anchoring cannot double-compensate).
 * A top sentinel observed with IntersectionObserver loads older pages
 * automatically; the visible button remains the fallback and a11y entry.
 */
export function useHistoryPaging({
  conversation,
  messages,
  located,
  unreadContext,
  latest,
  hasMore,
  loading,
  locating,
  loadingOlder,
  setLoadingOlder,
  loadingNewer,
  setLoadingNewer,
  setHasMore,
  setLocated,
  setError,
  historyBeforeRef,
  navigationRef,
  initialScrollRef,
  scrollRef,
}: {
  conversation: Conversation;
  messages: LocalMessage[];
  located: LocalMessage[] | null;
  unreadContext: boolean;
  latest: number;
  hasMore: boolean;
  loading: boolean;
  locating: boolean;
  loadingOlder: boolean;
  setLoadingOlder: Dispatch<SetStateAction<boolean>>;
  loadingNewer: boolean;
  setLoadingNewer: Dispatch<SetStateAction<boolean>>;
  setHasMore: Dispatch<SetStateAction<boolean>>;
  setLocated: Dispatch<SetStateAction<LocalMessage[] | null>>;
  setError: Dispatch<SetStateAction<string>>;
  historyBeforeRef: MutableRefObject<{ seq: number; generation: number } | null>;
  navigationRef: MutableRefObject<number>;
  initialScrollRef: MutableRefObject<boolean>;
  scrollRef: RefObject<HTMLDivElement | null>;
}) {
  const [anchorTick, setAnchorTick] = useState(0);
  const [olderFailedScope, setOlderFailedScope] = useState<string | null>(null);
  const anchor = useRef<Anchor | null>(null);
  const sentinelRef = useRef<HTMLDivElement>(null);
  // Navigation id of the in-flight older page; a navigation reset frees it.
  const busy = useRef<number | null>(null);

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
  }, [navigationRef, scrollRef]);

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
      busy.current === navigationRef.current
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
        setError(errorText(e));
        setOlderFailedScope(scopeKey);
      }
    } finally {
      if (current()) {
        busy.current = null;
        setLoadingOlder(false);
      }
    }
  }

  async function newer() {
    if (!located || !unreadContext || loadingNewer || loadingOlder || locating) return;
    const token = chatStore.generation;
    const reactionScope = chatStore.reactionScope(conversation.id);
    const operation = navigationRef.current;
    const current = () =>
      token === chatStore.generation &&
      operation === navigationRef.current &&
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

  const olderRef = useRef(older);
  useLayoutEffect(() => {
    olderRef.current = older;
  });
  const loadOlder = useCallback(() => olderRef.current(), []);
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

  return { older: loadOlder, newer, sentinelRef };
}
