import {
  Fragment,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  useSyncExternalStore,
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
import {
  isComposingKey,
  MentionInput,
  MentionText,
  trimMentionDraft,
} from "./mentions";
import type { LocalMessage } from "./store";
import { Avatar, Button, date, IconButton, Modal, Notice, time } from "./ui";
import { AgentAvatar, AgentBadge } from "./agent-ui";
import { ConversationSearch } from "./conversation-search";

function sameMentions(a: Mention[], b: Mention[]) {
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
type ComposerDraft = {
  body: string;
  mentions: Mention[];
  replyToId: string;
};
const emptyComposerDraft: ComposerDraft = {
  body: "",
  mentions: [],
  replyToId: "",
};
let draftGeneration = chatStore.generation;
// Drafts retain reply identity, never a copied IM body, and disappear with
// their account or conversation access.
const composerDrafts = new Map<string, ComposerDraft>();
const draftListeners = new Set<() => void>();
const notifyDrafts = () => draftListeners.forEach((listener) => listener());
const subscribeDrafts = (listener: () => void) => {
  draftListeners.add(listener);
  return () => {
    draftListeners.delete(listener);
  };
};
export function clearComposerDrafts() {
  draftGeneration = chatStore.generation;
  composerDrafts.clear();
  notifyDrafts();
}
chatStore.subscribe(() => {
  if (draftGeneration !== chatStore.generation) {
    clearComposerDrafts();
    return;
  }
  const allowed = chatStore.getSnapshot().conversations;
  let changed = false;
  for (const id of composerDrafts.keys()) {
    if (!allowed[id]) changed = composerDrafts.delete(id) || changed;
  }
  if (changed) notifyDrafts();
});
function readComposerDraft(conversationId: string): ComposerDraft {
  return composerDrafts.get(conversationId) || emptyComposerDraft;
}
function writeComposerDraft(conversationId: string, draft: ComposerDraft) {
  if (
    !chatStore.getSnapshot().conversations[conversationId] ||
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
  notifyDrafts();
}
function extendHistoryBoundary(beforeSeq: number, messages: LocalMessage[]) {
  const sequences = new Set(
    messages.filter((message) => !message.status).map((message) => message.seq),
  );
  while (beforeSeq > 1 && sequences.has(beforeSeq - 1)) beforeSeq--;
  return beforeSeq;
}
export function ConversationView({
  conversation,
  members,
  onMembers,
  membersOpen,
  onMenu,
  initialMessageId = "",
  onClearMessageTarget,
}: {
  conversation: Conversation;
  members: Member[];
  onMembers: () => void;
  membersOpen: boolean;
  onMenu: () => void;
  initialMessageId?: string;
  onClearMessageTarget?: () => void;
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
  // The view remounts per conversation, so this freezes what was unread when
  // it opened; acknowledging messages must not erase the divider mid-read.
  const [entry] = useState(() => ({
    readSeq: conversation.readSeq,
    lastSeq: conversation.lastSeq,
  }));
  const entryHasUnread = entry.readSeq > 0 && entry.lastSeq > entry.readSeq;
  const unreadAnchorPending = useRef(entryHasUnread && !initialMessageId);
  const [latestLoaded, setLatestLoaded] = useState(false);
  const [seenSeq, setSeenSeq] = useState(0);
  const [unreadContext, setUnreadContext] = useState(false);
  const editPending = useRef(false);
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
  const [editing, setEditing] = useState<Message | null>(null),
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
  const userId = user!.id;
  const canReadConversation =
    snapshot.userId === userId && !!snapshot.conversations[conversation.id];
  const cachedReply = reconcileMessages(
    [],
    [...allMessages, ...(located || [])].filter(
      (message) =>
        message.conversationId === conversation.id && message.id === replyToId,
    ),
  )[0];
  const replyKey = JSON.stringify([
    generation,
    conversation.id,
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
    resolvedComposerReply?.key === replyKey && !resolvedComposerReply.signal.aborted
      ? resolvedComposerReply
      : null;
  // Reply-only reads stay local; current sync versions always win over an
  // older context or a delayed single-message response.
  const reply = canReadConversation && replyToId
    ? reconcileMessages(
        resolvedReply?.message ? [resolvedReply.message] : [],
        cachedReply ? [cachedReply] : [],
      )[0]
    : undefined;
  useEffect(() => {
    if (!canReadConversation || !replyToId) return;
    // One read belongs to the selected reply's lifetime. A cache arrival or a
    // same-account profile refresh must not discard an accepted newer version.
    const controller = new AbortController();
    const current = () =>
      !controller.signal.aborted &&
      generation === chatStore.generation &&
      chatStore.getSnapshot().userId === userId &&
      !!chatStore.getSnapshot().conversations[conversation.id] &&
      readComposerDraft(conversation.id).replyToId === replyToId;
    void api
      .message(conversation.id, replyToId, controller.signal)
      .then(({ message }) => {
        if (current())
          setResolvedComposerReply({ key: replyKey, signal: controller.signal, message });
      })
      .catch((error: unknown) => {
        if (!current()) return;
        if (error instanceof Error && "status" in error && error.status === 404) {
          writeComposerDraft(conversation.id, {
            ...readComposerDraft(conversation.id),
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
    conversation.id,
    generation,
    replyKey,
    replyToId,
    userId,
  ]);
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
    acknowledgement.current = null;
    historyBefore.current = null;
    setHasMore(false);
    setLocated(null);
    setUnreadContext(false);
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
  }, [highlightId, located, focusMessage, visible, membersOpen]);
  useLayoutEffect(() => {
    const pending = acknowledgement.current;
    if (
      !visible ||
      !pending ||
      pending.id !== highlightId ||
      pending.generation !== chatStore.generation ||
      pending.operation !== navigation.current
    )
      return;
    const target = document.getElementById(`message-${pending.id}`);
    if (
      !target ||
      !scrollRef.current?.contains(target) ||
      target.closest("[inert]")
    )
      return;
    acknowledgement.current = null;
    if (
      messages.some(
        (message) => message.id === pending.id && message.retractedAt,
      ) ||
      chatStore
        .getSnapshot()
        .messages[
          conversation.id
        ]?.some((message) => message.id === pending.id && message.retractedAt)
    )
      return;
    void api
      .read(conversation.id, pending.seq)
      .then((result) => {
        if (
          pending.generation === chatStore.generation &&
          pending.operation === navigation.current
        ) {
          lastRead.current = Math.max(lastRead.current, result.readSeq);
          chatStore.markRead(conversation.id, result.readSeq);
        }
      })
      .catch(() => {
        if (
          pending.generation === chatStore.generation &&
          pending.operation === navigation.current
        )
          setError("已读状态暂未同步");
      });
  }, [
    highlightId,
    located,
    visible,
    conversation.id,
    membersOpen,
    messages,
  ]);
  const loadContext = useCallback(
    async (id: string, readOnDisplay = false) => {
      const token = chatStore.generation;
      const operation = ++navigation.current;
      const current = () =>
        token === chatStore.generation && operation === navigation.current;
      setAtBottom(false);
      setLoading(false);
      setLoadingOlder(false);
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
        const page = await api.messagesAround(
          conversation.id,
          target.message.seq,
        );
        if (!current()) return;
        const currentTarget = chatStore
          .getSnapshot()
          .messages[conversation.id]?.find((message) => message.id === id);
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
              : errorText(e),
          );
      } finally {
        if (current()) setLocating(false);
      }
    },
    [conversation.id],
  );
  const loadUnreadContext = useCallback(async () => {
    const seq = conversation.readSeq + 1;
    const token = chatStore.generation;
    const operation = ++navigation.current;
    const current = () =>
      token === chatStore.generation && operation === navigation.current;
    unreadAnchorPending.current = false;
    setAtBottom(false);
    setLoading(false);
    setLoadingOlder(false);
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
        (chatStore.getSnapshot().messages[conversation.id] || []).filter(
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
      if (current()) setError(errorText(e));
    } finally {
      if (current()) setLocating(false);
    }
  }, [conversation.id, conversation.readSeq]);
  function returnToLatest() {
    if (initialMessageId && onClearMessageTarget) onClearMessageTarget();
    else void fetchLatest();
  }
  async function locate(id: string) {
    if (!messages.some((message) => message.id === id)) return loadContext(id);
    navigation.current++;
    acknowledgement.current = null;
    setLoading(false);
    setLoadingOlder(false);
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
    return () => {
      lastTyping.current = 0;
      sendTyping(conversation.id, false);
    };
  }, [fetchLatest, loadContext, initialMessageId, sendTyping, conversation.id]);
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
  const firstUnread =
    entry.readSeq > 0
      ? messages.find(
          (m) =>
            !m.status &&
            m.seq > entry.readSeq &&
            m.seq <= entry.lastSeq &&
            m.senderId !== userId,
        )?.id
      : undefined;
  const earliestLoaded = messages.find((m) => !m.status)?.seq || 0;
  const unreadBeforeWindow =
    entryHasUnread &&
    latestLoaded &&
    !located &&
    !locating &&
    conversation.readSeq > 0 &&
    conversation.readSeq < conversation.lastSeq &&
    earliestLoaded > conversation.readSeq + 1;
  // A message counts as seen once its bottom edge has been inside the
  // viewport; reads advance only through that point, never past unseen rows.
  const measureSeen = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    const limit = el.getBoundingClientRect().bottom + 2;
    const rows = el.querySelectorAll<HTMLElement>("article[data-seq]");
    for (let i = rows.length - 1; i >= 0; i--) {
      if (rows[i].getBoundingClientRect().bottom <= limit) {
        const seq = Number(rows[i].dataset.seq);
        setSeenSeq((old) => Math.max(old, seq));
        return;
      }
    }
  }, []);
  useEffect(() => {
    if (loading) return;
    const el = scrollRef.current;
    if (unreadAnchorPending.current && !located) {
      const anchor = firstUnread
        ? document.getElementById(`message-${firstUnread}`)
        : null;
      if (el && anchor && el.contains(anchor)) {
        unreadAnchorPending.current = false;
        initialScroll.current = false;
        el.scrollTop +=
          anchor.getBoundingClientRect().top -
          el.getBoundingClientRect().top -
          56;
        setAtBottom(el.scrollHeight - el.scrollTop - el.clientHeight < 80);
        measureSeen();
        return;
      }
      if (latestLoaded) unreadAnchorPending.current = false;
    }
    if (atBottom || initialScroll.current) {
      el?.scrollTo({ top: el.scrollHeight });
      initialScroll.current = false;
    }
    measureSeen();
  }, [loading, tailId, atBottom, firstUnread, latestLoaded, located, measureSeen]);
  const readBase = conversation.readSeq;
  // Read cursors are cumulative, so the target may only cover a gap-free run
  // of loaded messages directly after the cursor; unloaded history stays
  // unread. A never-read conversation (cursor 0) keeps the historic behaviour
  // of acknowledging what is visible, as it has no prior reading position.
  let contiguousEnd = readBase;
  if (readBase === 0) contiguousEnd = Number.MAX_SAFE_INTEGER;
  else
    for (const m of messages) {
      if (m.status || m.seq <= contiguousEnd) continue;
      if (m.seq !== contiguousEnd + 1) break;
      contiguousEnd = m.seq;
    }
  const readTarget =
    located && !unreadContext
      ? 0
      : Math.min(seenSeq, latest, contiguousEnd);
  useEffect(() => {
    if (
      !readTarget ||
      searchOpen ||
      !visible ||
      loading ||
      readTarget <= Math.max(lastRead.current, conversation.readSeq)
    )
      return;
    const timer = setTimeout(() => {
      if (generation !== chatStore.generation) return;
      void api
        .read(conversation.id, readTarget)
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
    readTarget,
    searchOpen,
    visible,
    loading,
    conversation.id,
    conversation.readSeq,
    generation,
  ]);
  async function older() {
    const boundary = historyBefore.current;
    if (!boundary || boundary.generation !== chatStore.generation || loadingOlder)
      return;
    // Sync can insert an isolated old edit into a trimmed cache. Only visible,
    // contiguous history can extend the boundary of a completed history page.
    const first = extendHistoryBoundary(boundary.seq, messages);
    const token = chatStore.generation;
    const operation = navigation.current;
    const height = scrollRef.current?.scrollHeight || 0;
    setLoadingOlder(true);
    setError("");
    try {
      const page = await api.messages(conversation.id, first);
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
      ...(replyToId ? { replyToId } : {}),
    };
    void sendMessage(message, chatStore.generation);
    writeComposerDraft(conversation.id, {
      body: "",
      mentions: [],
      replyToId: "",
    });
    if (located) returnToLatest();
    setAtBottom(true);
    lastTyping.current = 0;
    sendTyping(conversation.id, false);
    inputRef.current?.focus();
  }
  const canEdit = !!data?.capabilities["im.message.edit"];
  function startEdit(message: Message) {
    setActionMessageId("");
    setEditing(message);
    setEditBody(message.body);
    setEditMentions(message.mentions || []);
    setActionError("");
  }
  function keyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (isComposingKey(event)) return;
    if (
      event.key === "ArrowUp" &&
      !event.shiftKey &&
      !event.altKey &&
      !event.metaKey &&
      !event.ctrlKey &&
      !body &&
      !replyToId &&
      attachments.files.length === 0 &&
      !located &&
      canEdit
    ) {
      const own = [...messages]
        .reverse()
        .find(
          (m) =>
            m.senderId === userId &&
            !m.status &&
            !m.retractedAt &&
            (m.body || m.attachments?.length),
        );
      if (own) {
        event.preventDefault();
        startEdit(own);
      }
      return;
    }
    if (
      !window.matchMedia("(pointer: coarse)").matches &&
      event.key === "Enter" &&
      !event.shiftKey
    ) {
      event.preventDefault();
      submit();
    }
  }
  async function edit(event: FormEvent) {
    event.preventDefault();
    if (editPending.current) return;
    if (!editing || (!editBody.trim() && !editing.attachments?.length)) return;
    const draft = trimMentionDraft(editBody, editMentions);
    if (
      draft.body === editing.body &&
      sameMentions(draft.mentions, editing.mentions || [])
    ) {
      setEditing(null);
      return;
    }
    const token = chatStore.generation;
    editPending.current = true;
    setActionBusy(true);
    setActionError("");
    try {
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
      editPending.current = false;
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
          <button onClick={returnToLatest}>返回最新消息</button>
        </div>
      )}
      {unreadBeforeWindow && (
        <div className="unread-jump-banner" role="status">
          <span>
            {conversation.lastSeq - conversation.readSeq > 99
              ? "99+"
              : conversation.lastSeq - conversation.readSeq}{" "}
            条新消息，部分在更早的位置
          </span>
          <button onClick={() => void loadUnreadContext()}>
            <ArrowUp size={14} />
            跳到第一条未读
          </button>
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
          measureSeen();
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
              {message.id === firstUnread && (
                <div className="unread-divider">
                  <span>以下是新消息</span>
                </div>
              )}
              <article
                id={`message-${message.id}`}
                data-seq={message.status ? undefined : message.seq}
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
                          if (e.key === "Escape") {
                            setEditing(null);
                            inputRef.current?.focus();
                          } else if (
                            e.key === "Enter" &&
                            !e.shiftKey &&
                            !window.matchMedia("(pointer: coarse)").matches
                          ) {
                            e.preventDefault();
                            e.currentTarget.form?.requestSubmit();
                          }
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
                            updateComposerDraft({ replyToId: message.id });
                            inputRef.current?.focus();
                          }}
                        >
                          <CornerUpLeft size={15} />
                        </IconButton>
                        {message.senderId === user!.id && canEdit && (
                            <IconButton
                              label="编辑消息"
                              onClick={() => startEdit(message)}
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
        {replyToId && (
          <div className="reply-preview">
            <CornerUpLeft size={15} />
            <div>
              回复 <b>{reply?.senderName || "原消息"}</b>
              <span>
                {reply?.retractedAt
                  ? "消息已撤回"
                  : reply
                    ? reply.body || `${reply.attachments?.length || 0} 个文件`
                    : resolvedReply?.error || "正在加载原消息…"}
              </span>
            </div>
            <IconButton
              label="取消回复"
              onClick={() => updateComposerDraft({ replyToId: "" })}
            >
              <X size={16} />
            </IconButton>
          </div>
        )}
        <form
          className={`composer ${replyToId ? "with-reply" : ""}`}
          onSubmit={submit}
        >
          <MentionInput
            inputRef={inputRef}
            value={body}
            mentions={mentions}
            onChange={(value, mentions) => {
              updateComposerDraft({ body: value, mentions });
            }}
            members={mentionMembers}
            enabled={mentionsEnabled}
            onTyping={(value) => {
              if (!value) {
                if (lastTyping.current > 0) {
                  lastTyping.current = 0;
                  sendTyping(conversation.id, false);
                }
                return;
              }
              if (Date.now() - lastTyping.current > 1500) {
                sendTyping(conversation.id, true);
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
