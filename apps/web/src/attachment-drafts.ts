import { api } from "@teamie/api-client";
import type { Attachment } from "@teamie/api-client";
import { errorText } from "./errors";
import { chatStore } from "./store";
import type { LocalMessage } from "./store";
import { attachmentTransport } from "./chat/attachment-upload";
import {
  clearStagedAttachmentsForUser,
  deleteStagedAttachment,
  listStagedDraftAttachments,
  pruneStagedAttachmentsToConversations,
  stagedRecordToFile,
  unbindStagedAttachmentsToDraft,
} from "./pwa/offline-attachments";

/** Uploads run at most this many at a time per conversation (FIFO). */
export const UPLOAD_CONCURRENCY = 2;

export type DraftFile = {
  id: string;
  uploadKey: string;
  file: File;
  state: "staged" | "uploading" | "ready" | "failed";
  attachment?: Attachment;
  error?: string;
  /** Epoch ms when the server accepted the upload (pending files expire). */
  uploadedAt?: number;
  /** Upload progress 0–100 while `uploading` (undefined until bytes flow). */
  progress?: number;
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
  userId: string;
  conversationId: string;
  /** Session the uploads belong to; remote deletes never cross sessions. */
  session: string;
  files: DraftFile[];
  controllers: Map<string, AbortController>;
  /** File ids waiting for an upload slot, in FIFO order. */
  waiting: string[];
};

const entries = new Map<string, Entry>();
const listeners = new Set<() => void>();
const EMPTY: DraftFile[] = [];
let draftUserId = chatStore.getSnapshot().userId;
let lastPrunedSignature = "";

const keyFor = (conversationId: string, userId = chatStore.getSnapshot().userId) =>
  `${userId}\u0000${conversationId}`;
const notify = () => listeners.forEach((listener) => listener());
const live = (entry: Entry) => entries.get(entry.key) === entry;

function entryFor(conversationId: string) {
  const userId = chatStore.getSnapshot().userId;
  const key = keyFor(conversationId, userId);
  let entry = entries.get(key);
  if (!entry) {
    entry = {
      key,
      userId,
      conversationId,
      session: api.csrfToken,
      files: [],
      controllers: new Map(),
      waiting: [],
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
  entry.waiting = [];
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
    const previousUserId = draftUserId;
    draftUserId = snapshot.userId;
    lastPrunedSignature = "";
    for (const entry of [...entries.values()]) dropEntry(entry, false);
    if (previousUserId) {
      void clearStagedAttachmentsForUser(previousUserId);
    }
    changed = true;
  } else if (chatStore.hasAuthoritativeConversations && snapshot.userId) {
    const allowedIds = Object.keys(snapshot.conversations).sort();
    const signature = `${snapshot.userId}\u0000${allowedIds.join(",")}`;
    for (const entry of [...entries.values()]) {
      if (!snapshot.conversations[entry.conversationId]) {
        dropEntry(entry, true);
        changed = true;
      }
    }
    if (signature !== lastPrunedSignature) {
      lastPrunedSignature = signature;
      void pruneStagedAttachmentsToConversations(
        snapshot.userId,
        new Set(allowedIds),
      );
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
  // Refresh session binding if entry was originally created while offline.
  if (!entry.session && api.csrfToken) entry.session = api.csrfToken;
  update(
    entry,
    entry.files.map((f) =>
      f.id === id ? { ...f, state: "uploading", error: undefined, progress: undefined } : f,
    ),
  );
  let lastPercent = -1;
  const onProgress = (fraction: number) => {
    const percent = Math.round(fraction * 100);
    if (percent === lastPercent || !live(entry) || controller.signal.aborted) return;
    lastPercent = percent;
    update(
      entry,
      entry.files.map((f) => (f.id === id && f.state === "uploading" ? { ...f, progress: percent } : f)),
    );
  };
  try {
    const result = await attachmentTransport.upload(
      entry.conversationId,
      file.file,
      controller.signal,
      file.uploadKey,
      onProgress,
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
              progress: undefined,
            }
          : f,
      ),
    );
    if (entry.userId) {
      void deleteStagedAttachment(entry.userId, id);
    }
  } catch (e) {
    if (live(entry) && !controller.signal.aborted)
      update(
        entry,
        entry.files.map((f) =>
          f.id === id ? { ...f, state: "failed", error: errorText(e), progress: undefined } : f,
        ),
      );
  } finally {
    entry.controllers.delete(id);
    if (live(entry) && entry.files.length === 0 && entry.controllers.size === 0)
      entries.delete(entry.key);
    pumpUploads(entry);
  }
}

function pumpUploads(entry: Entry) {
  while (live(entry) && entry.controllers.size < UPLOAD_CONCURRENCY && entry.waiting.length) {
    const id = entry.waiting.shift()!;
    if (entry.files.some((f) => f.id === id) && !entry.controllers.has(id)) void upload(entry, id);
  }
}

/**
 * Uploads run up to UPLOAD_CONCURRENCY at a time per conversation and keep
 * going after the composer unmounts; their results land in the store and
 * reappear on return.
 */
export function queueAttachmentUpload(conversationId: string, id: string) {
  const entry = entries.get(keyFor(conversationId));
  if (!entry || entry.waiting.includes(id) || entry.controllers.has(id)) return;
  entry.waiting.push(id);
  pumpUploads(entry);
}

export function addAttachmentDraftFiles(
  conversationId: string,
  files: DraftFile[],
) {
  if (!files.length) return;
  const entry = entryFor(conversationId);
  update(entry, [...entry.files, ...files]);
  for (const file of files) {
    if (file.state === "uploading") queueAttachmentUpload(conversationId, file.id);
  }
}

/** Hydrates offline-staged composer draft blobs from IndexedDB for one conversation. */
export async function hydrateStagedAttachmentDrafts(
  conversationId: string,
  autoUpload = false,
): Promise<void> {
  const snapshot = chatStore.getSnapshot();
  const userId = snapshot.userId;
  const generation = chatStore.generation;
  if (!userId || !conversationId) return;
  const records = await listStagedDraftAttachments(userId, conversationId);
  if (
    !records.length ||
    chatStore.generation !== generation ||
    chatStore.getSnapshot().userId !== userId
  ) {
    return;
  }
  const entry = entryFor(conversationId);
  const existingIds = new Set(entry.files.map((f) => f.id));
  const additions: DraftFile[] = [];
  for (const rec of records) {
    if (existingIds.has(rec.id)) continue;
    additions.push({
      id: rec.id,
      uploadKey: rec.uploadKey,
      file: stagedRecordToFile(rec),
      state: autoUpload ? "uploading" : "staged",
    });
  }
  if (!additions.length) return;
  update(entry, [...entry.files, ...additions]);
  if (autoUpload) {
    for (const file of additions) queueAttachmentUpload(conversationId, file.id);
  }
}

/** Promotes all in-memory `"staged"` draft files to active uploads once online. */
export function promoteStagedAttachmentDrafts(): void {
  if (
    api.sessionUnverified ||
    (typeof navigator !== "undefined" && navigator.onLine === false)
  ) {
    return;
  }
  const currentUserId = chatStore.getSnapshot().userId;
  if (!currentUserId) return;
  for (const entry of [...entries.values()]) {
    if (entry.userId !== currentUserId) continue;
    const stagedIds = entry.files
      .filter((f) => f.state === "staged")
      .map((f) => f.id);
    if (!stagedIds.length) continue;
    if (!entry.session && api.csrfToken) entry.session = api.csrfToken;
    for (const id of stagedIds) queueAttachmentUpload(entry.conversationId, id);
  }
}

/** Restores a queued offline message's attachments back into the composer draft. */
export async function restoreMessageAttachmentsToDraft(
  conversationId: string,
  message: LocalMessage,
  offline: boolean,
): Promise<void> {
  const userId = chatStore.getSnapshot().userId;
  const generation = chatStore.generation;
  if (!userId) return;
  const restoredRecords = message.stagedAttachments?.length
    ? await unbindStagedAttachmentsToDraft(
        userId,
        conversationId,
        message.clientMessageId,
      )
    : [];
  if (chatStore.generation !== generation || chatStore.getSnapshot().userId !== userId) {
    return;
  }
  const byId = new Map(restoredRecords.map((r) => [r.id, r]));
  const entry = entryFor(conversationId);
  const existingIds = new Set(entry.files.map((f) => f.id));
  const additions: DraftFile[] = [];

  for (const att of message.attachments || []) {
    if (existingIds.has(att.id)) continue;
    existingIds.add(att.id);
    additions.push({
      id: att.id,
      uploadKey: crypto.randomUUID(),
      file: new File([new Uint8Array(Math.max(1, Math.min(1, att.sizeBytes)))], att.filename, {
        type: att.mediaType,
      }),
      state: "ready",
      attachment: att,
      uploadedAt: Date.now(),
    });
  }

  for (const staged of message.stagedAttachments || []) {
    if (existingIds.has(staged.id)) continue;
    if (staged.attachment) {
      existingIds.add(staged.id);
      additions.push({
        id: staged.id,
        uploadKey: staged.uploadKey,
        file: new File([new Uint8Array(1)], staged.filename, {
          type: staged.mediaType,
        }),
        state: "ready",
        attachment: staged.attachment,
        uploadedAt: Date.now(),
      });
      continue;
    }
    const rec = byId.get(staged.id);
    if (!rec) continue;
    existingIds.add(rec.id);
    additions.push({
      id: rec.id,
      uploadKey: rec.uploadKey,
      file: stagedRecordToFile(rec),
      state: offline ? "staged" : "uploading",
    });
  }

  if (!additions.length) return;
  update(entry, [...entry.files, ...additions]);
  if (!offline) {
    for (const file of additions) {
      if (file.state === "uploading") queueAttachmentUpload(conversationId, file.id);
    }
  }
}

/** The user removed a file: abort its upload, delete server pending upload and local IDB blob. */
export function removeAttachmentDraftFile(conversationId: string, id: string) {
  const entry = entries.get(keyFor(conversationId));
  if (!entry) return;
  const file = entry.files.find((f) => f.id === id);
  entry.controllers.get(id)?.abort();
  entry.waiting = entry.waiting.filter((waiting) => waiting !== id);
  update(
    entry,
    entry.files.filter((f) => f.id !== id),
  );
  if (file?.attachment) void deleteRemote(entry, file.attachment.id);
  if (entry.userId) void deleteStagedAttachment(entry.userId, id);
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

/** Consumes already-uploaded descriptors and (optionally filtered) offline-staged files for send. */
export function consumeAttachmentDraftForSend(
  conversationId: string,
  allowedStagedIds?: Set<string>,
): {
  uploaded: Attachment[];
  staged: DraftFile[];
} {
  const entry = entries.get(keyFor(conversationId));
  if (!entry) return { uploaded: [], staged: [] };
  const uploaded: Attachment[] = [];
  const staged: DraftFile[] = [];
  const remaining: DraftFile[] = [];
  for (const file of entry.files) {
    if (file.state === "ready" && file.attachment) {
      uploaded.push(file.attachment);
    } else if (
      file.state === "staged" &&
      (!allowedStagedIds || allowedStagedIds.has(file.id))
    ) {
      staged.push(file);
    } else {
      remaining.push(file);
    }
  }
  update(entry, remaining);
  return { uploaded, staged };
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
 * Logout: abort uploads, delete every pending file while the session can still
 * authorize it, and wipe offline-staged IndexedDB blobs for this account.
 */
export async function discardAttachmentDrafts() {
  const currentUserId = draftUserId || chatStore.getSnapshot().userId || undefined;
  const deletes = [...entries.values()].flatMap((entry) =>
    dropEntry(entry, true),
  );
  notify();
  const tasks: Promise<unknown>[] = [
    ...deletes,
    clearStagedAttachmentsForUser(currentUserId),
  ];
  let timer: ReturnType<typeof setTimeout> | undefined;
  await Promise.race([
    Promise.allSettled(tasks),
    new Promise<void>((resolve) => {
      timer = setTimeout(resolve, LOGOUT_CLEANUP_TIMEOUT_MS);
    }),
  ]);
  clearTimeout(timer);
}
