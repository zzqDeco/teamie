import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { MutableRefObject, RefObject } from "react";
import { api } from "@teamie/api-client";
import type { Conversation } from "@teamie/api-client";
import { errorText } from "../errors";
import { chatStore } from "../store";
import type { LocalMessage } from "../store";

export function useConversationReadTracking({
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
  initialScrollRef,
  acknowledgementRef,
  navigationRef,
  lastReadRef,
  setError,
}: {
  conversation: Conversation;
  messages: LocalMessage[];
  located: LocalMessage[] | null;
  unreadContext: boolean;
  searchOpen: boolean;
  visible: boolean;
  setVisible: (v: boolean) => void;
  loading: boolean;
  atBottom: boolean;
  setAtBottom: (v: boolean) => void;
  locating: boolean;
  anchorPending: boolean;
  setAnchorPending: (v: boolean) => void;
  firstUnread: string | undefined;
  latestLoaded: boolean;
  tailId: string | undefined;
  readSeq: number;
  latest: number;
  generation: number;
  highlightId: string;
  membersOpen: boolean;
  scrollRef: RefObject<HTMLDivElement | null>;
  contentRef: RefObject<HTMLDivElement | null>;
  initialScrollRef: MutableRefObject<boolean>;
  acknowledgementRef: MutableRefObject<{
    id: string;
    seq: number;
    generation: number;
    operation: number;
  } | null>;
  navigationRef: MutableRefObject<number>;
  lastReadRef: MutableRefObject<number>;
  setError: (err: string) => void;
}) {
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
  // Rows near the viewport, tracked by IntersectionObserver so the dwell check
  // measures only those instead of every rendered row. Without IO (jsdom,
  // old engines) the check falls back to scanning all rows.
  const rowVisibility = useRef<{
    observer: IntersectionObserver;
    observed: Set<HTMLElement>;
    near: Set<HTMLElement>;
    unreported: Set<HTMLElement>;
  } | null>(null);
  const scheduleSeen = useCallback(() => {
    clearTimeout(dwell.current);
    dwell.current = setTimeout(() => {
      const el = scrollRef.current;
      if (!el || !ackState.current.allowed || ackState.current.blocked) return;
      const root = el.getBoundingClientRect();
      const tracked = rowVisibility.current;
      // The exact visibility test below is unchanged; IO only narrows the
      // candidates to a superset of rows that can pass it.
      const candidates = tracked
        ? [...tracked.near, ...tracked.unreported]
        : el.querySelectorAll<HTMLElement>("article[data-seq]");
      for (const row of candidates) {
        if (!row.dataset.seq || !el.contains(row)) continue;
        const rect = row.getBoundingClientRect();
        const inside = rect.top >= root.top - 1 && rect.bottom <= root.bottom + 1;
        const visibleHeight = Math.min(rect.bottom, root.bottom) - Math.max(rect.top, root.top);
        const tallAndCovering =
          rect.height > root.height * 0.8 && visibleHeight >= root.height * 0.5 && visibleHeight > 0;
        if (inside || tallAndCovering || row.dataset.own === "true")
          seenRows.current.add(Number(row.dataset.seq));
      }
      for (const row of el.querySelectorAll<HTMLElement>('article[data-seq][data-own="true"]'))
        seenRows.current.add(Number(row.dataset.seq));
      const base = chatStore.getSnapshot().conversations[conversation.id]?.readSeq ?? 0;
      let end = base;
      while (seenRows.current.has(end + 1)) end++;
      setReadCandidate(end);
    }, 400);
  }, [conversation.id, scrollRef]);
  useEffect(() => () => clearTimeout(dwell.current), []);
  useEffect(() => {
    const scroll = scrollRef.current;
    if (!scroll || typeof IntersectionObserver === "undefined") return;
    const near = new Set<HTMLElement>();
    const unreported = new Set<HTMLElement>();
    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          const row = entry.target as HTMLElement;
          unreported.delete(row);
          if (entry.isIntersecting) near.add(row);
          else near.delete(row);
        }
      },
      // The margin keeps the candidate set a strict superset of rows within
      // the 1px tolerance of the exact check.
      { root: scroll, rootMargin: "64px 0px" },
    );
    rowVisibility.current = { observer, observed: new Set(), near, unreported };
    return () => {
      observer.disconnect();
      rowVisibility.current = null;
    };
  }, [scrollRef]);
  useEffect(() => {
    const tracked = rowVisibility.current;
    const scroll = scrollRef.current;
    if (!tracked || !scroll) return;
    for (const row of tracked.observed) {
      if (row.isConnected) continue;
      tracked.observer.unobserve(row);
      tracked.observed.delete(row);
      tracked.near.delete(row);
      tracked.unreported.delete(row);
    }
    for (const row of scroll.querySelectorAll<HTMLElement>("article")) {
      if (tracked.observed.has(row)) continue;
      tracked.observed.add(row);
      // Until IO reports a new row it is measured directly.
      tracked.unreported.add(row);
      tracked.observer.observe(row);
    }
  }, [messages, scrollRef]);

  useEffect(() => {
    const change = () => setVisible(document.visibilityState === "visible");
    document.addEventListener("visibilitychange", change);
    return () => document.removeEventListener("visibilitychange", change);
  }, [setVisible]);
  useEffect(() => {
    const scroll = scrollRef.current;
    if (!scroll || !atBottom || located || locating) return;
    const observer = new ResizeObserver(() => { scroll.scrollTop = scroll.scrollHeight; });
    // The viewport shrinking (composer growth) and the content growing (edits,
    // late KaTeX/font/image layout) both keep a view that was at bottom pinned.
    observer.observe(scroll);
    if (contentRef.current) observer.observe(contentRef.current);
    return () => observer.disconnect();
  }, [atBottom, contentRef, located, locating, scrollRef]);
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
        initialScrollRef.current = false;
        el.scrollTop += anchor.getBoundingClientRect().top - el.getBoundingClientRect().top - 56;
        setAtBottom(el.scrollHeight - el.scrollTop - el.clientHeight < 80);
        scheduleSeen();
        return;
      }
      if (latestLoaded) setAnchorPending(false);
      else return;
    }
    if (!located && !locating && (initialScrollRef.current || (atBottom && tailChanged))) {
      el?.scrollTo({ top: el.scrollHeight });
      initialScrollRef.current = false;
    }
    scheduleSeen();
  }, [
    loading, tailId, atBottom, anchorPending, firstUnread, latestLoaded,
    located, locating, visible, searchOpen, readSeq, scheduleSeen,
    initialScrollRef, scrollRef, setAnchorPending, setAtBottom,
  ]);
  useLayoutEffect(() => {
    const pending = acknowledgementRef.current;
    if (
      !visible || !pending || pending.id !== highlightId ||
      pending.generation !== chatStore.generation || pending.operation !== navigationRef.current
    )
      return;
    const target = document.getElementById(`message-${pending.id}`);
    if (!target || !scrollRef.current?.contains(target) || target.closest("[inert]")) return;
    acknowledgementRef.current = null;
    if (
      messages.some((m) => m.id === pending.id && m.retractedAt) ||
      chatStore.getSnapshot().messages[conversation.id]?.some((m) => m.id === pending.id && m.retractedAt)
    )
      return;
    void api
      .read(conversation.id, pending.seq)
      .then((result) => {
        if (pending.generation === chatStore.generation && pending.operation === navigationRef.current) {
          lastReadRef.current = Math.max(lastReadRef.current, result.readSeq);
          chatStore.markRead(conversation.id, result.readSeq);
        }
      })
      .catch(() => {
        if (pending.generation === chatStore.generation && pending.operation === navigationRef.current)
          setError("已读状态暂未同步");
      });
  }, [
    acknowledgementRef, conversation.id, highlightId, lastReadRef,
    located, membersOpen, messages, navigationRef, scrollRef, setError, visible,
  ]);
  useEffect(() => {
    const nextRead = Math.min(readCandidate, latest);
    if (!nextRead || nextRead <= Math.max(lastReadRef.current, readSeq) || generation !== chatStore.generation)
      return;
    void api
      .read(conversation.id, nextRead)
      .then((result) => {
        if (generation === chatStore.generation) {
          readFailures.current = 0;
          lastReadRef.current = Math.max(lastReadRef.current, result.readSeq);
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
  }, [conversation.id, generation, lastReadRef, latest, readCandidate, readSeq, scheduleSeen]);
  const markLatestRead = useCallback(() => {
    if (!latest || latest <= readSeq) return;
    const token = chatStore.generation;
    void api
      .read(conversation.id, latest)
      .then((result) => {
        if (token === chatStore.generation) {
          lastReadRef.current = Math.max(lastReadRef.current, result.readSeq);
          chatStore.markRead(conversation.id, result.readSeq);
        }
      })
      .catch((e) => {
        if (token === chatStore.generation) setError(errorText(e));
      });
  }, [conversation.id, lastReadRef, latest, readSeq, setError]);
  const resetReadFailures = useCallback(() => {
    readFailures.current = 0;
  }, []);
  return { scheduleSeen, markLatestRead, resetReadFailures };
}
