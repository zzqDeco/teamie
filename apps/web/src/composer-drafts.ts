import type { Conversation, Member, Mention } from "@teamie/api-client";
import { chatStore } from "./store";
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
