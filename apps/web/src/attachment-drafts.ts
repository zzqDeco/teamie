import { api, ApiError, defaultAttachmentLimits } from "@teamie/api-client";
import type { Attachment } from "@teamie/api-client";
import { errorText, isNetworkError } from "./errors";
import { chatStore } from "./store";
import type { LocalMessage } from "./store";
import { attachmentTransport } from "./chat/attachment-upload";
import {
  bindStagedAttachmentsToMessage,
  clearStagedAttachmentsForUser,
  deleteStagedAttachment,
  listStagedDraftAttachments,
  markStagedMessageQueued,
  OfflineAttachmentStoreError,
  pruneStagedAttachmentsToConversations,
  reconcileOrphanedStagedAttachments,
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
  /** True while a durable copy of this draft's blob resides in IndexedDB. */
  stagedOffline?: boolean;
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
const pendingStagingCounts = new Map<string, number>();
const EMPTY: DraftFile[] = [];
let draftUserId = chatStore.getSnapshot().userId;
let lastPrunedSignature = "";
let reconciledGeneration = -1;

const keyFor = (conversationId: string, userId = chatStore.getSnapshot().userId) =>
  `${userId}\u0000${conversationId}`;
const notify = () => listeners.forEach((listener) => listener());
const live = (entry: Entry) => entries.get(entry.key) === entry;

export function beginOfflineAttachmentStaging(
  conversationId: string,
  userId = chatStore.getSnapshot().userId,
): void {
  const key = keyFor(conversationId, userId);
  pendingStagingCounts.set(key, (pendingStagingCounts.get(key) ?? 0) + 1);
  notify();
}

export function endOfflineAttachmentStaging(
  conversationId: string,
  userId = chatStore.getSnapshot().userId,
): void {
  const key = keyFor(conversationId, userId);
  const next = (pendingStagingCounts.get(key) ?? 0) - 1;
  if (next <= 0) pendingStagingCounts.delete(key);
  else pendingStagingCounts.set(key, next);
  notify();
}

export function hasPendingOfflineStaging(conversationId: string): boolean {
  return (pendingStagingCounts.get(keyFor(conversationId)) ?? 0) > 0;
}

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
    reconciledGeneration = -1;
    pendingStagingCounts.clear();
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
              stagedOffline: false,
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
    if (live(entry) && !controller.signal.aborted) {
      const stagedInterrupted =
        file.stagedOffline &&
        ((e instanceof ApiError && e.code === "offline_unverified") ||
          isNetworkError(e));
      const onlineUploadFailure =
        stagedInterrupted &&
        !api.sessionUnverified &&
        (typeof navigator === "undefined" || navigator.onLine !== false);
      update(
        entry,
        entry.files.map((f) =>
          f.id === id
            ? stagedInterrupted
              ? {
                  ...f,
                  state: "staged",
                  error: onlineUploadFailure ? errorText(e) : undefined,
                  progress: undefined,
                }
              : { ...f, state: "failed", error: errorText(e), progress: undefined }
            : f,
        ),
      );
    }
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
  const existingIds = new Set(entry.files.map((f) => f.id));
  const additions = files.filter((f) => !existingIds.has(f.id));
  if (!additions.length) return;
  update(entry, [...entry.files, ...additions]);
  for (const file of additions) {
    if (file.state === "uploading") queueAttachmentUpload(conversationId, file.id);
  }
}

/**
 * Hydrates offline-staged composer draft blobs from IndexedDB for one conversation,
 * respecting `maxFilesPerMessage` and leaving any excess records in IndexedDB for
 * recovery after files are sent or removed. Returns the count of preserved excess files.
 */
export async function hydrateStagedAttachmentDrafts(
  conversationId: string,
  autoUpload = false,
  maxFilesPerMessage = defaultAttachmentLimits.maxFilesPerMessage,
): Promise<number> {
  const snapshot = chatStore.getSnapshot();
  const userId = snapshot.userId;
  const generation = chatStore.generation;
  if (!userId || !conversationId) return 0;
  if (reconciledGeneration !== generation) {
    const activeIds = new Set<string>();
    for (const list of Object.values(snapshot.messages)) {
      for (const msg of list) {
        if (
          msg.status === "queued" ||
          msg.status === "sending" ||
          msg.status === "failed"
        ) {
          activeIds.add(msg.clientMessageId);
        }
      }
    }
    await reconcileOrphanedStagedAttachments(userId, activeIds);
    if (
      chatStore.generation !== generation ||
      chatStore.getSnapshot().userId !== userId
    ) {
      return 0;
    }
    reconciledGeneration = generation;
  }
  const records = await listStagedDraftAttachments(userId, conversationId);
  if (
    !records.length ||
    chatStore.generation !== generation ||
    chatStore.getSnapshot().userId !== userId
  ) {
    return 0;
  }
  const entry = entryFor(conversationId);
  const existingIds = new Set(entry.files.map((f) => f.id));
  const novelRecords = records.filter((rec) => !existingIds.has(rec.id));
  if (!novelRecords.length) return 0;
  const remainingSlots = Math.max(0, maxFilesPerMessage - entry.files.length);
  const toLoad = novelRecords.slice(0, remainingSlots);
  const overflowCount = novelRecords.length - toLoad.length;
  if (!toLoad.length) return overflowCount;
  const additions: DraftFile[] = toLoad.map((rec) => ({
    id: rec.id,
    uploadKey: rec.uploadKey,
    file: stagedRecordToFile(rec),
    state: autoUpload ? "uploading" : "staged",
    stagedOffline: true,
  }));
  update(entry, [...entry.files, ...additions]);
  if (autoUpload) {
    for (const file of additions) queueAttachmentUpload(conversationId, file.id);
  }
  return overflowCount;
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
  maxFilesPerMessage = defaultAttachmentLimits.maxFilesPerMessage,
): Promise<void> {
  const userId = chatStore.getSnapshot().userId;
  const generation = chatStore.generation;
  if (!userId) return;

  const checkCapacity = () => {
    const existingIds = new Set(
      readAttachmentDraft(conversationId).map((f) => f.id),
    );
    const incomingIds = new Set<string>();
    for (const att of message.attachments || []) {
      if (!existingIds.has(att.id)) incomingIds.add(att.id);
    }
    for (const staged of message.stagedAttachments || []) {
      if (!existingIds.has(staged.id)) incomingIds.add(staged.id);
    }
    if (existingIds.size + incomingIds.size > maxFilesPerMessage) {
      throw new OfflineAttachmentStoreError(
        "capacity_exceeded",
        `输入框已有 ${existingIds.size} 个待发送文件，恢复后将超过每条消息最多 ${maxFilesPerMessage} 个文件的上限。`,
      );
    }
  };

  checkCapacity();

  const requiredStagedIds = (message.stagedAttachments || [])
    .filter((staged) => !staged.attachment)
    .map((staged) => staged.id);

  const restoredRecords = message.stagedAttachments?.length
    ? await unbindStagedAttachmentsToDraft(
        userId,
        conversationId,
        message.clientMessageId,
        requiredStagedIds,
        Date.now(),
      )
    : [];
  if (chatStore.generation !== generation || chatStore.getSnapshot().userId !== userId) {
    return;
  }

  try {
    checkCapacity();
  } catch (err) {
    if (restoredRecords.length) {
      void bindStagedAttachmentsToMessage(
        userId,
        conversationId,
        restoredRecords.map((r) => r.id),
        message.clientMessageId,
      )
        .then((rebound) => {
          if (rebound.length) {
            markStagedMessageQueued(userId, message.clientMessageId);
          }
        })
        .catch(() => {});
    }
    throw err;
  }

  const byId = new Map(restoredRecords.map((r) => [r.id, r]));
  for (const reqId of requiredStagedIds) {
    if (!byId.has(reqId)) {
      if (restoredRecords.length) {
        void bindStagedAttachmentsToMessage(
          userId,
          conversationId,
          restoredRecords.map((r) => r.id),
          message.clientMessageId,
        )
          .then((rebound) => {
            if (rebound.length) {
              markStagedMessageQueued(userId, message.clientMessageId);
            }
          })
          .catch(() => {});
      }
      throw new OfflineAttachmentStoreError(
        "missing_attachment",
        "部分离线暂存附件已过期或丢失，无法恢复编辑，请取消发送后重新添加。",
      );
    }
  }

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
    const rec = byId.get(staged.id)!;
    existingIds.add(rec.id);
    additions.push({
      id: rec.id,
      uploadKey: rec.uploadKey,
      file: stagedRecordToFile(rec),
      state: offline ? "staged" : "uploading",
      stagedOffline: true,
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
export function removeAttachmentDraftFile(
  conversationId: string,
  id: string,
): Promise<void> {
  const entry = entries.get(keyFor(conversationId));
  if (!entry) return Promise.resolve();
  const file = entry.files.find((f) => f.id === id);
  entry.controllers.get(id)?.abort();
  entry.waiting = entry.waiting.filter((waiting) => waiting !== id);
  update(
    entry,
    entry.files.filter((f) => f.id !== id),
  );
  if (file?.attachment) void deleteRemote(entry, file.attachment.id);
  return entry.userId ? deleteStagedAttachment(entry.userId, id) : Promise.resolve();
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

/** Consumes already-uploaded descriptors and (optionally filtered) offline-staged files in original draft order. */
export function consumeAttachmentDraftForSend(
  conversationId: string,
  allowedStagedIds?: Set<string>,
): {
  uploaded: Attachment[];
  staged: DraftFile[];
  ordered: DraftFile[];
} {
  const entry = entries.get(keyFor(conversationId));
  if (!entry) return { uploaded: [], staged: [], ordered: [] };
  const uploaded: Attachment[] = [];
  const staged: DraftFile[] = [];
  const ordered: DraftFile[] = [];
  const remaining: DraftFile[] = [];
  for (const file of entry.files) {
    if (file.state === "ready" && file.attachment) {
      uploaded.push(file.attachment);
      ordered.push(file);
    } else if (
      file.state === "staged" &&
      (!allowedStagedIds || allowedStagedIds.has(file.id))
    ) {
      staged.push(file);
      ordered.push(file);
    } else {
      remaining.push(file);
    }
  }
  update(entry, remaining);
  return { uploaded, staged, ordered };
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
  pendingStagingCounts.clear();
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
