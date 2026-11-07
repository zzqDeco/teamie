import { api } from "@teamie/api-client";
import type { Attachment } from "@teamie/api-client";
import { errorText } from "./errors";
import { chatStore } from "./store";

export type DraftFile = {
  id: string;
  uploadKey: string;
  file: File;
  state: "uploading" | "ready" | "failed";
  attachment?: Attachment;
  error?: string;
  /** Epoch ms when the server accepted the upload (pending files expire). */
  uploadedAt?: number;
};

/**
 * Pending uploads are private to the uploader and expire on the server 24 h
 * after upload. Drafts older than this margin are dropped locally instead of
 * failing at send time.
 */
export const PENDING_ATTACHMENT_TTL_MS = 23 * 60 * 60 * 1000;
const LOGOUT_CLEANUP_TIMEOUT_MS = 3000;

type Entry = {
  key: string;
  conversationId: string;
  /** Session the uploads belong to; remote deletes never cross sessions. */
  session: string;
  files: DraftFile[];
  controllers: Map<string, AbortController>;
  queue: Promise<void>;
};

// Module-level so pending files survive conversation switches and remounts
// (deep links re-key the conversation view). Keyed by user + conversation.
// Like composer text drafts they disappear with their account or access;
// unlike text they hold File objects, so they live in memory only.
const entries = new Map<string, Entry>();
const listeners = new Set<() => void>();
const EMPTY: DraftFile[] = [];
let draftUserId = chatStore.getSnapshot().userId;

const keyFor = (conversationId: string) =>
  `${chatStore.getSnapshot().userId}\u0000${conversationId}`;
const notify = () => listeners.forEach((listener) => listener());
const live = (entry: Entry) => entries.get(entry.key) === entry;

function entryFor(conversationId: string) {
  const key = keyFor(conversationId);
  let entry = entries.get(key);
  if (!entry) {
    entry = {
      key,
      conversationId,
      session: api.csrfToken,
      files: [],
      controllers: new Map(),
      queue: Promise.resolve(),
    };
    entries.set(key, entry);
  }
  return entry;
}

function update(entry: Entry, files: DraftFile[]) {
  if (!live(entry)) return;
  entry.files = files;
  if (files.length === 0 && entry.controllers.size === 0)
    entries.delete(entry.key);
  notify();
}

function deleteRemote(entry: Entry, attachmentId: string) {
  if (entry.session !== api.csrfToken) return Promise.resolve();
  return api
    .deletePendingAttachment(entry.conversationId, attachmentId)
    .catch(() => {});
}

/** Removes an entry; optionally deletes its uploaded files on the server. */
function dropEntry(entry: Entry, remote: boolean) {
  if (!live(entry)) return [];
  entries.delete(entry.key);
  entry.controllers.forEach((controller) => controller.abort());
  entry.controllers.clear();
  return remote
    ? entry.files.flatMap((file) =>
        file.attachment ? [deleteRemote(entry, file.attachment.id)] : [],
      )
    : [];
}

chatStore.subscribe(() => {
  const snapshot = chatStore.getSnapshot();
  let changed = false;
  if (snapshot.userId !== draftUserId) {
    // Account switch/logout: the old session can no longer delete its files;
    // the server expires them. Never issue requests under the new account.
    draftUserId = snapshot.userId;
    for (const entry of [...entries.values()]) dropEntry(entry, false);
    changed = true;
  } else if (chatStore.hasAuthoritativeConversations) {
    for (const entry of [...entries.values()]) {
      if (!snapshot.conversations[entry.conversationId]) {
        dropEntry(entry, true);
        changed = true;
      }
    }
  }
  if (changed) notify();
});

export function subscribeAttachmentDrafts(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function readAttachmentDraft(conversationId: string): DraftFile[] {
  return entries.get(keyFor(conversationId))?.files ?? EMPTY;
}

async function upload(entry: Entry, id: string) {
  const file = entry.files.find((f) => f.id === id);
  if (!live(entry) || !file) return;
  const controller = new AbortController();
  entry.controllers.set(id, controller);
  update(
    entry,
    entry.files.map((f) =>
      f.id === id ? { ...f, state: "uploading", error: undefined } : f,
    ),
  );
  try {
    const result = await api.uploadAttachment(
      entry.conversationId,
      file.file,
      controller.signal,
      file.uploadKey,
    );
    if (!live(entry) || !entry.files.some((f) => f.id === id)) {
      // Removed or cleared while in flight: the late upload is orphaned.
      void deleteRemote(entry, result.attachment.id);
      return;
    }
    update(
      entry,
      entry.files.map((f) =>
        f.id === id
          ? {
              ...f,
              state: "ready",
              attachment: result.attachment,
              uploadedAt: Date.now(),
            }
          : f,
      ),
    );
  } catch (e) {
    if (live(entry) && !controller.signal.aborted)
      update(
        entry,
        entry.files.map((f) =>
          f.id === id ? { ...f, state: "failed", error: errorText(e) } : f,
        ),
      );
  } finally {
    entry.controllers.delete(id);
    if (live(entry) && entry.files.length === 0 && entry.controllers.size === 0)
      entries.delete(entry.key);
  }
}

/**
 * Uploads run serially per conversation and keep going after the composer
 * unmounts; their results land in the store and reappear on return.
 */
export function queueAttachmentUpload(conversationId: string, id: string) {
  const entry = entries.get(keyFor(conversationId));
  if (!entry) return;
  entry.queue = entry.queue.then(async () => {
    if (live(entry) && entry.files.some((f) => f.id === id))
      await upload(entry, id);
  });
}

export function addAttachmentDraftFiles(
  conversationId: string,
  files: DraftFile[],
) {
  if (!files.length) return;
  const entry = entryFor(conversationId);
  update(entry, [...entry.files, ...files]);
  for (const file of files) queueAttachmentUpload(conversationId, file.id);
}

/** The user removed a file: abort its upload and delete it server-side. */
export function removeAttachmentDraftFile(conversationId: string, id: string) {
  const entry = entries.get(keyFor(conversationId));
  if (!entry) return;
  const file = entry.files.find((f) => f.id === id);
  entry.controllers.get(id)?.abort();
  update(
    entry,
    entry.files.filter((f) => f.id !== id),
  );
  if (file?.attachment) void deleteRemote(entry, file.attachment.id);
}

/** A send links the ready descriptors; nothing is deleted server-side. */
export function consumeAttachmentDraft(conversationId: string): Attachment[] {
  const entry = entries.get(keyFor(conversationId));
  if (!entry) return [];
  const attachments = entry.files.flatMap((f) =>
    f.attachment ? [f.attachment] : [],
  );
  update(entry, []);
  return attachments;
}

/** Drops ready files whose pending upload has (nearly) expired server-side. */
export function pruneExpiredAttachmentDrafts(
  conversationId: string,
  now = Date.now(),
) {
  const entry = entries.get(keyFor(conversationId));
  if (!entry) return 0;
  const kept = entry.files.filter(
    (f) => !f.uploadedAt || now - f.uploadedAt < PENDING_ATTACHMENT_TTL_MS,
  );
  const expired = entry.files.length - kept.length;
  if (expired) update(entry, kept);
  return expired;
}

/**
 * Logout: abort uploads and delete every pending file while the session can
 * still authorize it. Bounded so a slow network never blocks signing out.
 */
export async function discardAttachmentDrafts() {
  const deletes = [...entries.values()].flatMap((entry) =>
    dropEntry(entry, true),
  );
  notify();
  if (!deletes.length) return;
  let timer: ReturnType<typeof setTimeout> | undefined;
  await Promise.race([
    Promise.allSettled(deletes),
    new Promise<void>((resolve) => {
      timer = setTimeout(resolve, LOGOUT_CLEANUP_TIMEOUT_MS);
    }),
  ]);
  clearTimeout(timer);
}
