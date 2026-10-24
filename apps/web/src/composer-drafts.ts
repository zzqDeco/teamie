import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import type { RefObject } from "react";
import { api } from "@teamie/api-client";
import type {
  Conversation,
  Member,
  Mention,
  Message,
} from "@teamie/api-client";
import { chatStore, reconcileMessages } from "./store";
import type { LocalMessage } from "./store";

export type ComposerDraft = {
  body: string;
  mentions: Mention[];
  replyToId: string;
};

const emptyComposerDraft: ComposerDraft = {
  body: "",
  mentions: [],
  replyToId: "",
};

const draftStorageKey = (userId: string) => `teamie.drafts.v1:${userId}`;
let draftUserId = chatStore.getSnapshot().userId;
// Drafts retain reply identity, never a copied IM body, and disappear with
// their account or conversation access.
const composerDrafts = new Map<string, ComposerDraft>();
const draftListeners = new Set<() => void>();
const notifyDrafts = () => draftListeners.forEach((listener) => listener());

export const subscribeDrafts = (listener: () => void) => {
  draftListeners.add(listener);
  return () => {
    draftListeners.delete(listener);
  };
};

function persistSessionDrafts(userId: string) {
  if (!userId) return;
  try {
    if (composerDrafts.size === 0)
      sessionStorage.removeItem(draftStorageKey(userId));
    else
      sessionStorage.setItem(
        draftStorageKey(userId),
        JSON.stringify(Object.fromEntries(composerDrafts)),
      );
  } catch {
    /* Drafts remain usable in memory when sessionStorage is full or blocked. */
  }
}

function loadSessionDrafts(userId: string) {
  if (!userId) return;
  try {
    const raw = sessionStorage.getItem(draftStorageKey(userId));
    if (!raw) return;
    const parsed = JSON.parse(raw) as Record<string, ComposerDraft>;
    if (!parsed || typeof parsed !== "object") return;
    for (const [id, entry] of Object.entries(parsed)) {
      if (
        entry &&
        typeof entry.body === "string" &&
        Array.isArray(entry.mentions) &&
        typeof entry.replyToId === "string" &&
        (entry.body || entry.mentions.length > 0 || entry.replyToId)
      ) {
        composerDrafts.set(id, {
          body: entry.body,
          mentions: entry.mentions,
          replyToId: entry.replyToId,
        });
      }
    }
  } catch {
    /* Corrupted or unavailable sessionStorage falls back to in-memory drafts. */
  }
}

if (draftUserId) loadSessionDrafts(draftUserId);

export function clearComposerDrafts() {
  const previousUser = draftUserId;
  const currentUser = chatStore.getSnapshot().userId;
  try {
    if (previousUser) sessionStorage.removeItem(draftStorageKey(previousUser));
    if (currentUser) sessionStorage.removeItem(draftStorageKey(currentUser));
  } catch {
    /* Ignore storage access errors. */
  }
  draftUserId = currentUser;
  composerDrafts.clear();
  notifyDrafts();
}

chatStore.subscribe(() => {
  const snap = chatStore.getSnapshot();
  let userChanged = false;
  if (snap.userId !== draftUserId) {
    try {
      if (draftUserId) sessionStorage.removeItem(draftStorageKey(draftUserId));
    } catch {
      /* Ignore storage access errors. */
    }
    draftUserId = snap.userId;
    composerDrafts.clear();
    if (draftUserId) loadSessionDrafts(draftUserId);
    userChanged = true;
  }
  const allowed = snap.conversations;
  let pruned = false;
  if (chatStore.hasAuthoritativeConversations) {
    for (const id of composerDrafts.keys()) {
      if (!allowed[id]) pruned = composerDrafts.delete(id) || pruned;
    }
    if (pruned) persistSessionDrafts(draftUserId);
  }
  if (userChanged || pruned) notifyDrafts();
});

export function readComposerDraft(conversationId: string): ComposerDraft {
  return composerDrafts.get(conversationId) || emptyComposerDraft;
}

export function writeComposerDraft(
  conversationId: string,
  draft: ComposerDraft,
) {
  const snap = chatStore.getSnapshot();
  if (
    (chatStore.hasAuthoritativeConversations &&
      !snap.conversations[conversationId]) ||
    (!draft.body && draft.mentions.length === 0 && !draft.replyToId)
  ) {
    composerDrafts.delete(conversationId);
  } else {
    composerDrafts.set(conversationId, {
      body: draft.body,
      mentions: [...draft.mentions],
      replyToId: draft.replyToId,
    });
  }
  persistSessionDrafts(snap.userId);
  notifyDrafts();
}

export function sameMentions(a: Mention[], b: Mention[]) {
  return (
    a.length === b.length &&
    a.every(
      (m, i) =>
        m.userId === b[i].userId && m.start === b[i].start && m.end === b[i].end,
    )
  );
}

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

export function extendHistoryBoundary(
  beforeSeq: number,
  messages: LocalMessage[],
) {
  const sequences = new Set(
    messages.filter((message) => !message.status).map((message) => message.seq),
  );
  while (beforeSeq > 1 && sequences.has(beforeSeq - 1)) beforeSeq--;
  return beforeSeq;
}

export function useComposerReply({
  conversationId,
  initialMessageId,
  replyToId,
  userId,
  canReadConversation,
  allMessages,
  located,
}: {
  conversationId: string;
  initialMessageId: string;
  replyToId: string;
  userId: string;
  canReadConversation: boolean;
  allMessages: LocalMessage[];
  located: LocalMessage[] | null;
}) {
  const generation = chatStore.generation;
  const cachedReply = reconcileMessages(
    [],
    [...allMessages, ...(located || [])].filter(
      (message) =>
        message.conversationId === conversationId && message.id === replyToId,
    ),
  )[0];
  const replyKey = JSON.stringify([
    generation,
    conversationId,
    initialMessageId,
    replyToId,
  ]);
  const [resolvedComposerReply, setResolvedComposerReply] = useState<{
    key: string;
    signal: AbortSignal;
    message?: Message;
    error?: string;
  } | null>(null);
  const resolvedReply =
    resolvedComposerReply?.key === replyKey &&
    !resolvedComposerReply.signal.aborted
      ? resolvedComposerReply
      : null;
  const reply =
    canReadConversation && replyToId
      ? reconcileMessages(
          resolvedReply?.message ? [resolvedReply.message] : [],
          cachedReply ? [cachedReply] : [],
        )[0]
      : undefined;

  useEffect(() => {
    if (!canReadConversation || !replyToId) return;
    const controller = new AbortController();
    const current = () =>
      !controller.signal.aborted &&
      generation === chatStore.generation &&
      chatStore.getSnapshot().userId === userId &&
      !!chatStore.getSnapshot().conversations[conversationId] &&
      readComposerDraft(conversationId).replyToId === replyToId;
    void api
      .message(conversationId, replyToId, controller.signal)
      .then(({ message }) => {
        if (current())
          setResolvedComposerReply({
            key: replyKey,
            signal: controller.signal,
            message,
          });
      })
      .catch((error: unknown) => {
        if (!current()) return;
        if (
          error instanceof Error &&
          "status" in error &&
          error.status === 404
        ) {
          writeComposerDraft(conversationId, {
            ...readComposerDraft(conversationId),
            replyToId: "",
          });
          return;
        }
        setResolvedComposerReply({
          key: replyKey,
          signal: controller.signal,
          error: "原消息暂时无法加载",
        });
      });
    return () => controller.abort();
  }, [
    canReadConversation,
    conversationId,
    generation,
    replyKey,
    replyToId,
    userId,
  ]);

  return { reply, replyError: resolvedReply?.error };
}

export function useReadWatermarks({
  conversationId,
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
  navigationRef,
  acknowledgementRef,
  lastReadRef,
  onError,
}: {
  conversationId: string;
  readSeq: number;
  latest: number;
  located: LocalMessage[] | null;
  unreadContext: boolean;
  searchOpen: boolean;
  visible: boolean;
  loading: boolean;
  membersOpen: boolean;
  highlightId: string;
  messages: LocalMessage[];
  scrollRef: RefObject<HTMLDivElement | null>;
  navigationRef: RefObject<number>;
  acknowledgementRef: RefObject<{
    id: string;
    seq: number;
    generation: number;
    operation: number;
  } | null>;
  lastReadRef: RefObject<number>;
  onError: (msg: string) => void;
}) {
  const seenRows = useRef(new Set<number>());
  const [readCandidate, setReadCandidate] = useState(0);
  const ackState = useRef({ allowed: true, blocked: false });
  const dwell = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const readFailures = useRef(0);
  const generation = chatStore.generation;

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
        const inside =
          rect.top >= root.top - 1 && rect.bottom <= root.bottom + 1;
        const visibleHeight =
          Math.min(rect.bottom, root.bottom) - Math.max(rect.top, root.top);
        const tallAndCovering =
          rect.height > root.height * 0.8 &&
          visibleHeight >= root.height * 0.5 &&
          visibleHeight > 0;
        if (inside || tallAndCovering || row.dataset.own === "true")
          seenRows.current.add(Number(row.dataset.seq));
      }
      const base =
        chatStore.getSnapshot().conversations[conversationId]?.readSeq ?? 0;
      let end = base;
      while (seenRows.current.has(end + 1)) end++;
      setReadCandidate(end);
    }, 400);
  }, [conversationId, scrollRef]);

  useEffect(() => () => clearTimeout(dwell.current), []);

  useLayoutEffect(() => {
    const pending = acknowledgementRef.current;
    if (
      !visible ||
      !pending ||
      pending.id !== highlightId ||
      pending.generation !== chatStore.generation ||
      pending.operation !== navigationRef.current
    )
      return;
    const target = document.getElementById(`message-${pending.id}`);
    if (
      !target ||
      !scrollRef.current?.contains(target) ||
      target.closest("[inert]")
    )
      return;
    acknowledgementRef.current = null;
    if (
      messages.some((m) => m.id === pending.id && m.retractedAt) ||
      chatStore
        .getSnapshot()
        .messages[conversationId]?.some(
          (m) => m.id === pending.id && m.retractedAt,
        )
    )
      return;
    void api
      .read(conversationId, pending.seq)
      .then((result) => {
        if (
          pending.generation === chatStore.generation &&
          pending.operation === navigationRef.current
        ) {
          lastReadRef.current = Math.max(lastReadRef.current, result.readSeq);
          chatStore.markRead(conversationId, result.readSeq);
        }
      })
      .catch(() => {
        if (
          pending.generation === chatStore.generation &&
          pending.operation === navigationRef.current
        )
          onError("已读状态暂未同步");
      });
  }, [
    acknowledgementRef,
    conversationId,
    highlightId,
    lastReadRef,
    located,
    membersOpen,
    messages,
    navigationRef,
    onError,
    scrollRef,
    visible,
  ]);

  useEffect(() => {
    const nextRead = Math.min(readCandidate, latest);
    if (
      !nextRead ||
      nextRead <= Math.max(lastReadRef.current, readSeq) ||
      generation !== chatStore.generation
    )
      return;
    void api
      .read(conversationId, nextRead)
      .then((result) => {
        if (generation === chatStore.generation) {
          readFailures.current = 0;
          lastReadRef.current = Math.max(lastReadRef.current, result.readSeq);
          chatStore.markRead(conversationId, result.readSeq);
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
  }, [
    conversationId,
    generation,
    lastReadRef,
    latest,
    readCandidate,
    readSeq,
    scheduleSeen,
  ]);

  const resetReadFailures = useCallback(() => {
    readFailures.current = 0;
  }, []);

  const markLatestRead = useCallback(() => {
    if (!latest || latest <= readSeq) return;
    const token = chatStore.generation;
    void api
      .read(conversationId, latest)
      .then((result) => {
        if (token === chatStore.generation) {
          lastReadRef.current = Math.max(lastReadRef.current, result.readSeq);
          chatStore.markRead(conversationId, result.readSeq);
        }
      })
      .catch((e) => {
        if (token === chatStore.generation) {
          onError(e instanceof Error ? e.message : "标记已读失败");
        }
      });
  }, [conversationId, latest, readSeq, lastReadRef, onError]);

  return { scheduleSeen, resetReadFailures, markLatestRead };
}

export async function runLoadOlder({
  conversationId,
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
  formatError,
}: {
  conversationId: string;
  messages: LocalMessage[];
  located: LocalMessage[] | null;
  loadingOlder: boolean;
  historyBefore: RefObject<{ seq: number; generation: number } | null>;
  navigation: RefObject<number>;
  scrollRef: RefObject<HTMLDivElement | null>;
  setLoadingOlder: (v: boolean) => void;
  setError: (v: string) => void;
  setLocated: (
    updater: (old: LocalMessage[] | null) => LocalMessage[] | null,
  ) => void;
  setHasMore: (v: boolean) => void;
  formatError: (e: unknown) => string;
}) {
  const boundary = historyBefore.current;
  if (
    !boundary ||
    boundary.generation !== chatStore.generation ||
    loadingOlder
  )
    return;
  const first = extendHistoryBoundary(boundary.seq, messages);
  const token = chatStore.generation;
  const operation = navigation.current;
  const height = scrollRef.current?.scrollHeight || 0;
  setLoadingOlder(true);
  setError("");
  try {
    const page = await api.messages(conversationId, first);
    if (token !== chatStore.generation || operation !== navigation.current)
      return;
    historyBefore.current = page.messages[0]
      ? { seq: page.messages[0].seq, generation: token }
      : null;
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
      setError(formatError(e));
  } finally {
    if (token === chatStore.generation && operation === navigation.current)
      setLoadingOlder(false);
  }
}

export async function runLoadNewer({
  conversationId,
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
  formatError,
}: {
  conversationId: string;
  latest: number;
  located: LocalMessage[] | null;
  unreadContext: boolean;
  loadingNewer: boolean;
  loadingOlder: boolean;
  locating: boolean;
  navigation: RefObject<number>;
  setLoadingNewer: (v: boolean) => void;
  setError: (v: string) => void;
  setLocated: (
    updater: (old: LocalMessage[] | null) => LocalMessage[] | null,
  ) => void;
  formatError: (e: unknown) => string;
}) {
  if (!located || !unreadContext || loadingNewer || loadingOlder || locating)
    return;
  const token = chatStore.generation;
  const operation = navigation.current;
  const current = () =>
    token === chatStore.generation &&
    operation === navigation.current &&
    !!chatStore.getSnapshot().conversations[conversationId];
  setLoadingNewer(true);
  setError("");
  try {
    const page = await api.messagesAround(conversationId, latest + 1);
    if (!current()) return;
    if (!page.messages.some((message) => message.seq > latest)) {
      setError("后续消息暂未加载，请重试");
      return;
    }
    setLocated((old) => (old ? reconcileMessages(old, page.messages) : old));
  } catch (e) {
    if (current()) setError(formatError(e));
  } finally {
    if (current()) setLoadingNewer(false);
  }
}

export async function runLoadContext({
  conversationId,
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
  formatError,
}: {
  conversationId: string;
  id: string;
  readOnDisplay: boolean;
  navigation: RefObject<number>;
  acknowledgement: RefObject<{
    id: string;
    seq: number;
    generation: number;
    operation: number;
  } | null>;
  historyBefore: RefObject<{ seq: number; generation: number } | null>;
  setAtBottom: (v: boolean) => void;
  setLoading: (v: boolean) => void;
  setLoadingOlder: (v: boolean) => void;
  setLoadingNewer: (v: boolean) => void;
  setLocating: (v: boolean) => void;
  setHasMore: (v: boolean) => void;
  initialScroll: RefObject<boolean>;
  setError: (v: string) => void;
  setResolvedTargets: (
    updater: (old: Record<string, Message>) => Record<string, Message>,
  ) => void;
  setLocated: (v: LocalMessage[] | null) => void;
  setUnreadContext: (v: boolean) => void;
  setHighlightId: (v: string) => void;
  formatError: (e: unknown) => string;
}) {
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
    const target = await api.message(conversationId, id);
    if (!current()) return;
    setResolvedTargets((old) => ({
      ...old,
      [target.message.id]: old[target.message.id]
        ? reconcileMessages([old[target.message.id]], [target.message])[0]
        : target.message,
    }));
    const page = await api.messagesAround(conversationId, target.message.seq);
    if (!current()) return;
    const currentTarget = chatStore
      .getSnapshot()
      .messages[conversationId]?.find((message) => message.id === id);
    const context = reconcileMessages(page.messages, [
      target.message,
      ...(currentTarget ? [currentTarget] : []),
    ]);
    const resolved =
      context.find((m) => m.id === target.message.id) || target.message;
    setResolvedTargets((old) => ({
      ...old,
      [resolved.id]: old[resolved.id]
        ? reconcileMessages([old[resolved.id]], [resolved])[0]
        : resolved,
    }));
    if (readOnDisplay && !resolved.retractedAt)
      acknowledgement.current = {
        id,
        seq: resolved.seq,
        generation: token,
        operation,
      };
    setLocated(context);
    setUnreadContext(false);
    historyBefore.current = context[0]
      ? { seq: context[0].seq, generation: token }
      : null;
    setHasMore((historyBefore.current?.seq ?? 1) > 1);
    setHighlightId(id);
  } catch (e) {
    if (current())
      setError(
        e instanceof Error && "status" in e && e.status === 404
          ? "原消息已不可访问"
          : formatError(e),
      );
  } finally {
    if (current()) setLocating(false);
  }
}

export async function runLoadUnreadContext({
  conversationId,
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
  formatError,
}: {
  conversationId: string;
  readSeq: number;
  navigation: RefObject<number>;
  acknowledgement: RefObject<{
    id: string;
    seq: number;
    generation: number;
    operation: number;
  } | null>;
  historyBefore: RefObject<{ seq: number; generation: number } | null>;
  setAnchorPending: (v: boolean) => void;
  setAtBottom: (v: boolean) => void;
  setLoading: (v: boolean) => void;
  setLoadingOlder: (v: boolean) => void;
  setLoadingNewer: (v: boolean) => void;
  setLocating: (v: boolean) => void;
  setHasMore: (v: boolean) => void;
  initialScroll: RefObject<boolean>;
  setError: (v: string) => void;
  setLocated: (v: LocalMessage[] | null) => void;
  setUnreadContext: (v: boolean) => void;
  setHighlightId: (v: string) => void;
  formatError: (e: unknown) => string;
}) {
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
    const page = await api.messagesAround(conversationId, seq);
    if (!current()) return;
    const context = reconcileMessages(
      page.messages,
      (chatStore.getSnapshot().messages[conversationId] || []).filter(
        (message) => page.messages.some((m) => m.id === message.id),
      ),
    );
    setLocated(context);
    setUnreadContext(true);
    historyBefore.current = context[0]
      ? { seq: context[0].seq, generation: token }
      : null;
    setHasMore((historyBefore.current?.seq ?? 1) > 1);
    const target = context.find((m) => !m.status && m.seq >= seq);
    if (target) setHighlightId(target.id);
  } catch (e) {
    if (current()) setError(formatError(e));
  } finally {
    if (current()) setLocating(false);
  }
}

export async function runFetchLatest({
  conversationId,
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
  formatError,
}: {
  conversationId: string;
  fetchedLatest: RefObject<boolean>;
  navigation: RefObject<number>;
  acknowledgement: RefObject<{
    id: string;
    seq: number;
    generation: number;
    operation: number;
  } | null>;
  historyBefore: RefObject<{ seq: number; generation: number } | null>;
  initialScroll: RefObject<boolean>;
  setEntry: (v: { readSeq: number; lastSeq: number }) => void;
  setAnchorPending: (v: boolean) => void;
  setHasMore: (v: boolean) => void;
  setLocated: (v: LocalMessage[] | null) => void;
  setUnreadContext: (v: boolean) => void;
  setHighlightId: (v: string) => void;
  setLocating: (v: boolean) => void;
  setLoadingOlder: (v: boolean) => void;
  setLoadingNewer: (v: boolean) => void;
  setAtBottom: (v: boolean) => void;
  setLoading: (v: boolean) => void;
  setError: (v: string) => void;
  setLatestLoaded: (v: boolean) => void;
  formatError: (e: unknown) => string;
}) {
  const token = chatStore.generation;
  const operation = ++navigation.current;
  const hasConfirmed = (
    chatStore.getSnapshot().messages[conversationId] || []
  ).some((m) => !m.status);
  if (fetchedLatest.current) {
    const current = chatStore.getSnapshot().conversations[conversationId];
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
    const page = await api.messages(conversationId);
    if (token !== chatStore.generation || operation !== navigation.current)
      return;
    historyBefore.current = page.messages[0]
      ? { seq: page.messages[0].seq, generation: token }
      : null;
    chatStore.merge(page.messages);
    setHasMore(page.hasMore);
    setLatestLoaded(true);
  } catch (e) {
    if (token === chatStore.generation && operation === navigation.current)
      setError(formatError(e));
  } finally {
    if (token === chatStore.generation && operation === navigation.current)
      setLoading(false);
  }
}

export function useConversationScrollEffect({
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
  initialScrollRef,
  setAnchorPending,
  setAtBottom,
  setVisible,
  scheduleSeen,
}: {
  loading: boolean;
  tailId: string | undefined;
  atBottom: boolean;
  anchorPending: boolean;
  firstUnread: string | undefined;
  latestLoaded: boolean;
  located: LocalMessage[] | null;
  locating: boolean;
  visible: boolean;
  searchOpen: boolean;
  readSeq: number;
  scrollRef: RefObject<HTMLDivElement | null>;
  initialScrollRef: RefObject<boolean>;
  setAnchorPending: (v: boolean) => void;
  setAtBottom: (v: boolean) => void;
  setVisible: (v: boolean) => void;
  scheduleSeen: () => void;
}) {
  useEffect(() => {
    const change = () => setVisible(document.visibilityState === "visible");
    document.addEventListener("visibilitychange", change);
    return () => document.removeEventListener("visibilitychange", change);
  }, [setVisible]);

  useEffect(() => {
    const scroll = scrollRef.current;
    if (!scroll || !atBottom || located || locating) return;
    const observer = new ResizeObserver(() => {
      scroll.scrollTop = scroll.scrollHeight;
    });
    observer.observe(scroll);
    return () => observer.disconnect();
  }, [atBottom, located, locating, scrollRef]);

  const lastTailId = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (loading) return;
    const el = scrollRef.current;
    const tailChanged = tailId !== lastTailId.current;
    lastTailId.current = tailId;
    if (anchorPending && !located) {
      const anchor = firstUnread
        ? document.getElementById(`message-${firstUnread}`)
        : null;
      if (el && anchor && el.contains(anchor)) {
        setAnchorPending(false);
        initialScrollRef.current = false;
        el.scrollTop +=
          anchor.getBoundingClientRect().top -
          el.getBoundingClientRect().top -
          56;
        setAtBottom(el.scrollHeight - el.scrollTop - el.clientHeight < 80);
        scheduleSeen();
        return;
      }
      if (latestLoaded) setAnchorPending(false);
      else return;
    }
    if (
      !located &&
      !locating &&
      (initialScrollRef.current || (atBottom && tailChanged))
    ) {
      el?.scrollTo({ top: el.scrollHeight });
      initialScrollRef.current = false;
    }
    scheduleSeen();
  }, [
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
    initialScrollRef,
    setAnchorPending,
    setAtBottom,
    scheduleSeen,
  ]);
}
