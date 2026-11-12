import { useEffect, useState } from "react";
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
// Composer keystrokes update the in-memory draft immediately (so remounts read
// the latest text) but serialize to localStorage (per user, so drafts survive
// closing the tab) only after typing pauses.
// Every reset path cancels the pending write so a previous account's or a
// revoked conversation's draft can never be persisted afterwards.
const DRAFT_PERSIST_DELAY_MS = 300;
let persistTimer: ReturnType<typeof setTimeout> | undefined;
// Bumped whenever drafts are dropped wholesale (account change, logout) or a
// single conversation's draft is pruned. Composer text writes carry the epoch
// they were loaded under and are ignored once it is stale.
let draftEpoch = 0;
const conversationEpochs = new Map<string, number>();
const textRevisions = new Map<string, number>();

function cancelPendingPersist() {
  if (persistTimer === undefined) return;
  clearTimeout(persistTimer);
  persistTimer = undefined;
}

export const subscribeDrafts = (listener: () => void) => {
  draftListeners.add(listener);
  return () => {
    draftListeners.delete(listener);
  };
};

// Conversations whose draft has text: drives the sidebar 「草稿」 marker. It
// has its own listeners so keystrokes never notify timeline subscribers; it
// only changes when a draft becomes empty/non-empty.
let draftPresence: ReadonlySet<string> = new Set();
const presenceListeners = new Set<() => void>();
function refreshDraftPresence() {
  const next = new Set<string>();
  for (const [id, draft] of composerDrafts) if (draft.body.trim()) next.add(id);
  if (next.size === draftPresence.size && [...next].every((id) => draftPresence.has(id))) return;
  draftPresence = next;
  presenceListeners.forEach((listener) => listener());
}
export function subscribeDraftPresence(listener: () => void) {
  presenceListeners.add(listener);
  return () => {
    presenceListeners.delete(listener);
  };
}
/** Stable snapshot of conversation ids that currently hold draft text. */
export const readDraftPresence = () => draftPresence;

function removeStoredDrafts(userId: string) {
  try {
    localStorage.removeItem(draftStorageKey(userId));
    sessionStorage.removeItem(draftStorageKey(userId));
  } catch {
    /* Ignore storage access errors. */
  }
}

function persistSessionDrafts(userId: string) {
  if (!userId) return;
  try {
    if (composerDrafts.size === 0)
      localStorage.removeItem(draftStorageKey(userId));
    else
      localStorage.setItem(
        draftStorageKey(userId),
        JSON.stringify(Object.fromEntries(composerDrafts)),
      );
  } catch {
    /* Drafts remain usable in memory when localStorage is full or blocked. */
  }
}

/** Reads the user's drafts; one-time migration from the old sessionStorage key. */
function readStoredDrafts(userId: string) {
  const key = draftStorageKey(userId);
  const stored = localStorage.getItem(key);
  if (stored) return stored;
  const legacy = sessionStorage.getItem(key);
  if (legacy) {
    try {
      localStorage.setItem(key, legacy);
    } catch {
      /* Still usable in memory for this tab. */
    }
    sessionStorage.removeItem(key);
  }
  return legacy;
}

function loadSessionDrafts(userId: string) {
  if (!userId) return;
  try {
    const raw = readStoredDrafts(userId);
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
    /* Corrupted or unavailable storage falls back to in-memory drafts. */
  }
}

if (draftUserId) loadSessionDrafts(draftUserId);
refreshDraftPresence();

/** Writes any debounced composer text to localStorage now. */
export function flushComposerDrafts() {
  if (persistTimer === undefined) return;
  cancelPendingPersist();
  persistSessionDrafts(draftUserId);
}

if (typeof window !== "undefined") {
  window.addEventListener("pagehide", flushComposerDrafts);
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "hidden") flushComposerDrafts();
  });
}

export function clearComposerDrafts() {
  const previousUser = draftUserId;
  const currentUser = chatStore.getSnapshot().userId;
  cancelPendingPersist();
  draftEpoch++;
  conversationEpochs.clear();
  if (previousUser) removeStoredDrafts(previousUser);
  if (currentUser) removeStoredDrafts(currentUser);
  draftUserId = currentUser;
  composerDrafts.clear();
  refreshDraftPresence();
  notifyDrafts();
}

chatStore.subscribe(() => {
  const snap = chatStore.getSnapshot();
  let userChanged = false;
  if (snap.userId !== draftUserId) {
    cancelPendingPersist();
    draftEpoch++;
    conversationEpochs.clear();
    // Account isolation: another account never sees (or inherits) these drafts.
    if (draftUserId) removeStoredDrafts(draftUserId);
    draftUserId = snap.userId;
    composerDrafts.clear();
    if (draftUserId) loadSessionDrafts(draftUserId);
    userChanged = true;
  }
  const allowed = snap.conversations;
  let pruned = false;
  if (chatStore.hasAuthoritativeConversations) {
    for (const id of composerDrafts.keys()) {
      if (!allowed[id] && composerDrafts.delete(id)) {
        pruned = true;
        conversationEpochs.set(id, (conversationEpochs.get(id) ?? 0) + 1);
      }
    }
    if (pruned) {
      cancelPendingPersist();
      persistSessionDrafts(draftUserId);
    }
  }
  if (userChanged || pruned) {
    refreshDraftPresence();
    notifyDrafts();
  }
});

export function readComposerDraft(conversationId: string): ComposerDraft {
  return composerDrafts.get(conversationId) || emptyComposerDraft;
}

/**
 * Identifies the draft generation a composer was loaded under. It changes only
 * when drafts are reset (account change, logout) or the conversation's draft
 * is pruned after access removal.
 */
/**
 * Changes when the stored draft text of a conversation is replaced from
 * outside the composer (e.g. restoring a failed message for editing). The
 * mounted composer reloads its local text when this changes.
 */
export function composerTextRevision(conversationId: string) {
  return textRevisions.get(conversationId) ?? 0;
}

export function composerDraftEpoch(conversationId: string) {
  return `${draftUserId}\u0000${draftEpoch}\u0000${conversationEpochs.get(conversationId) ?? 0}`;
}

function draftAllowed(conversationId: string, draft: ComposerDraft) {
  return !(
    (chatStore.hasAuthoritativeConversations &&
      !chatStore.getSnapshot().conversations[conversationId]) ||
    (!draft.body && draft.mentions.length === 0 && !draft.replyToId)
  );
}

export function writeComposerDraft(
  conversationId: string,
  draft: ComposerDraft,
) {
  const snap = chatStore.getSnapshot();
  if (!draftAllowed(conversationId, draft)) {
    composerDrafts.delete(conversationId);
  } else {
    composerDrafts.set(conversationId, {
      body: draft.body,
      mentions: [...draft.mentions],
      replyToId: draft.replyToId,
    });
  }
  cancelPendingPersist();
  persistSessionDrafts(snap.userId);
  refreshDraftPresence();
  notifyDrafts();
}

/**
 * Moves the sender's own undelivered text back into the composer so it can be
 * fixed and resent. Any in-progress draft text is kept and the restored text
 * is appended after it; mention offsets are shifted to match.
 */
export function restoreComposerDraft(
  conversationId: string,
  restored: Pick<Message, "body" | "mentions" | "replyToId">,
) {
  const existing = readComposerDraft(conversationId);
  const separator = existing.body && restored.body ? "\n" : "";
  const offset = existing.body.length + separator.length;
  // The mounted composer keeps its text in local state; bumping the revision
  // (announced by the write's notification) makes it reload the restored
  // draft in place, keeping the same textarea and its focus.
  textRevisions.set(conversationId, (textRevisions.get(conversationId) ?? 0) + 1);
  writeComposerDraft(conversationId, {
    body: existing.body + separator + restored.body,
    mentions: [
      ...existing.mentions,
      ...(restored.mentions || []).map((mention) => ({
        ...mention,
        start: mention.start + offset,
        end: mention.end + offset,
      })),
    ],
    replyToId: restored.replyToId || existing.replyToId,
  });
}

/**
 * Records composer text typed under `epoch`. The in-memory draft updates
 * immediately without notifying subscribers (only the owning composer shows
 * the text); localStorage is written once typing pauses or on flush.
 */
export function writeComposerText(
  conversationId: string,
  epoch: string,
  body: string,
  mentions: Mention[],
) {
  if (epoch !== composerDraftEpoch(conversationId)) return;
  const draft = {
    body,
    mentions,
    replyToId: readComposerDraft(conversationId).replyToId,
  };
  if (!draftAllowed(conversationId, draft)) composerDrafts.delete(conversationId);
  else composerDrafts.set(conversationId, { ...draft, mentions: [...mentions] });
  refreshDraftPresence();
  cancelPendingPersist();
  persistTimer = setTimeout(() => {
    persistTimer = undefined;
    persistSessionDrafts(draftUserId);
  }, DRAFT_PERSIST_DELAY_MS);
}

export function sameMentions(a: Mention[], b: Mention[]) {
  return (
    a.length === b.length &&
    a.every(
      (m, i) =>
        m.userId === b[i].userId &&
        m.kind === b[i].kind &&
        m.start === b[i].start &&
        m.end === b[i].end,
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
        (id) => members.find((m) => m.userId === id)?.displayName || "成员",
      )
      .join("、") || "私信"
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
