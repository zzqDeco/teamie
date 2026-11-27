import type { Attachment } from "@teamie/api-client";

const DB_NAME = "teamie-offline-attachments-v1";
const DB_VERSION = 1;
const STORE_NAME = "staged";

/** Matches backend sender-wide pending upload cap. */
export const MAX_STAGED_ATTACHMENTS_PER_USER = 32;
/** Matches `PENDING_ATTACHMENT_TTL_MS` (23h). */
export const STAGED_ATTACHMENT_TTL_MS = 23 * 60 * 60 * 1000;

export type StagedAttachmentSummary = {
  id: string;
  uploadKey: string;
  filename: string;
  mediaType: string;
  sizeBytes: number;
  attachment?: Attachment;
};

export type StagedAttachmentRecord = {
  id: string;
  uploadKey: string;
  userId: string;
  conversationId: string;
  /** Empty string while held in composer draft; set to clientMessageId when queued to send. */
  clientMessageId: string;
  name: string;
  type: string;
  size: number;
  lastModified: number;
  blob: Blob;
  createdAt: number;
};

export type OfflineAttachmentStoreErrorCode =
  | "storage_unavailable"
  | "capacity_exceeded"
  | "quota_exceeded";

export class OfflineAttachmentStoreError extends Error {
  code: OfflineAttachmentStoreErrorCode;
  constructor(code: OfflineAttachmentStoreErrorCode, message: string) {
    super(message);
    this.name = "OfflineAttachmentStoreError";
    this.code = code;
  }
}

export interface OfflineAttachmentBackend {
  stage(record: StagedAttachmentRecord, now?: number): Promise<void>;
  listDrafts(
    userId: string,
    conversationId: string,
    now?: number,
  ): Promise<StagedAttachmentRecord[]>;
  listForMessage(
    userId: string,
    clientMessageId: string,
    now?: number,
  ): Promise<StagedAttachmentRecord[]>;
  bindToMessage(
    userId: string,
    conversationId: string,
    ids: string[],
    clientMessageId: string,
  ): Promise<StagedAttachmentRecord[]>;
  unbindToDraft(
    userId: string,
    conversationId: string,
    clientMessageId: string,
  ): Promise<StagedAttachmentRecord[]>;
  deleteById(userId: string, id: string): Promise<void>;
  deleteForMessage(userId: string, clientMessageId: string): Promise<void>;
  deleteForConversation(userId: string, conversationId: string): Promise<void>;
  pruneToConversations(
    userId: string,
    allowedConversationIds: ReadonlySet<string>,
  ): Promise<void>;
  clearForUser(userId?: string): Promise<void>;
  clearExceptUser(userId: string): Promise<void>;
}

function isExpired(record: StagedAttachmentRecord, now: number): boolean {
  return !Number.isFinite(record.createdAt) || now - record.createdAt >= STAGED_ATTACHMENT_TTL_MS;
}

function sortByCreated(a: StagedAttachmentRecord, b: StagedAttachmentRecord) {
  return a.createdAt - b.createdAt || a.id.localeCompare(b.id);
}

function mapDomError(err: unknown): OfflineAttachmentStoreError {
  if (err instanceof OfflineAttachmentStoreError) return err;
  const name = (err as { name?: string } | null)?.name || "";
  if (name === "QuotaExceededError") {
    return new OfflineAttachmentStoreError(
      "quota_exceeded",
      "本地离线存储空间不足，请移除部分待发送文件后重试。",
    );
  }
  return new OfflineAttachmentStoreError(
    "storage_unavailable",
    "当前浏览器不支持离线暂存附件，请恢复网络后再添加。",
  );
}

function requestPromise<T>(request: IDBRequest<T>): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error || new Error("IDB request failed"));
  });
}

function transactionDone(tx: IDBTransaction): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error || new Error("IDB transaction failed"));
    tx.onabort = () => reject(tx.error || new Error("IDB transaction aborted"));
  });
}

export function createIndexedDbBackend(
  getFactory: () => IDBFactory | undefined = () =>
    typeof globalThis !== "undefined" ? globalThis.indexedDB : undefined,
): OfflineAttachmentBackend {
  let dbPromise: Promise<IDBDatabase> | null = null;

  function openDb(): Promise<IDBDatabase> {
    if (dbPromise) return dbPromise;
    const factory = getFactory();
    if (!factory || typeof factory.open !== "function") {
      return Promise.reject(
        new OfflineAttachmentStoreError(
          "storage_unavailable",
          "当前浏览器不支持离线暂存附件，请恢复网络后再添加。",
        ),
      );
    }
    dbPromise = new Promise<IDBDatabase>((resolve, reject) => {
      try {
        const req = factory.open(DB_NAME, DB_VERSION);
        req.onupgradeneeded = () => {
          const db = req.result;
          if (!db.objectStoreNames.contains(STORE_NAME)) {
            const store = db.createObjectStore(STORE_NAME, { keyPath: "id" });
            store.createIndex("byUser", "userId", { unique: false });
            store.createIndex("byUserConversation", ["userId", "conversationId"], {
              unique: false,
            });
            store.createIndex("byUserMessage", ["userId", "clientMessageId"], {
              unique: false,
            });
          }
        };
        req.onsuccess = () => {
          const db = req.result;
          db.onversionchange = () => {
            db.close();
            dbPromise = null;
          };
          resolve(db);
        };
        req.onerror = () => {
          dbPromise = null;
          reject(mapDomError(req.error));
        };
        req.onblocked = () => {
          dbPromise = null;
          reject(
            new OfflineAttachmentStoreError(
              "storage_unavailable",
              "离线附件存储正被占用，请稍后重试。",
            ),
          );
        };
      } catch (err) {
        dbPromise = null;
        reject(mapDomError(err));
      }
    });
    return dbPromise;
  }

  async function withStore<T>(
    mode: IDBTransactionMode,
    run: (store: IDBObjectStore) => Promise<T>,
  ): Promise<T> {
    const db = await openDb();
    try {
      const tx = db.transaction(STORE_NAME, mode);
      const store = tx.objectStore(STORE_NAME);
      const done = transactionDone(tx);
      const value = await run(store);
      await done;
      return value;
    } catch (err) {
      throw mapDomError(err);
    }
  }

  return {
    async stage(record, now = Date.now()) {
      await withStore("readwrite", async (store) => {
        const byUser = store.index("byUser");
        const existing = (await requestPromise(
          byUser.getAll(record.userId),
        )) as StagedAttachmentRecord[];
        let activeCount = 0;
        for (const item of existing) {
          if (isExpired(item, now)) {
            store.delete(item.id);
          } else if (item.id !== record.id) {
            activeCount += 1;
          }
        }
        if (activeCount >= MAX_STAGED_ATTACHMENTS_PER_USER) {
          throw new OfflineAttachmentStoreError(
            "capacity_exceeded",
            `离线暂存附件已达上限（${MAX_STAGED_ATTACHMENTS_PER_USER} 个），请先移除部分待发送文件。`,
          );
        }
        store.put(record);
      });
    },

    async listDrafts(userId, conversationId, now = Date.now()) {
      if (!userId || !conversationId) return [];
      try {
        return await withStore("readwrite", async (store) => {
          const idx = store.index("byUserConversation");
          const rows = (await requestPromise(
            idx.getAll([userId, conversationId]),
          )) as StagedAttachmentRecord[];
          const kept: StagedAttachmentRecord[] = [];
          for (const row of rows) {
            if (isExpired(row, now)) {
              store.delete(row.id);
            } else if (row.clientMessageId === "") {
              kept.push(row);
            }
          }
          return kept.sort(sortByCreated);
        });
      } catch {
        return [];
      }
    },

    async listForMessage(userId, clientMessageId, now = Date.now()) {
      if (!userId || !clientMessageId) return [];
      try {
        return await withStore("readwrite", async (store) => {
          const idx = store.index("byUserMessage");
          const rows = (await requestPromise(
            idx.getAll([userId, clientMessageId]),
          )) as StagedAttachmentRecord[];
          const kept: StagedAttachmentRecord[] = [];
          for (const row of rows) {
            if (isExpired(row, now)) {
              store.delete(row.id);
            } else {
              kept.push(row);
            }
          }
          return kept.sort(sortByCreated);
        });
      } catch {
        return [];
      }
    },

    async bindToMessage(userId, conversationId, ids, clientMessageId) {
      if (!userId || !ids.length || !clientMessageId) return [];
      return withStore("readwrite", async (store) => {
        const bound: StagedAttachmentRecord[] = [];
        for (const id of ids) {
          const row = (await requestPromise(
            store.get(id),
          )) as StagedAttachmentRecord | undefined;
          if (
            !row ||
            row.userId !== userId ||
            row.conversationId !== conversationId
          ) {
            continue;
          }
          const updated: StagedAttachmentRecord = { ...row, clientMessageId };
          store.put(updated);
          bound.push(updated);
        }
        return bound;
      });
    },

    async unbindToDraft(userId, conversationId, clientMessageId) {
      if (!userId || !clientMessageId) return [];
      try {
        return await withStore("readwrite", async (store) => {
          const idx = store.index("byUserMessage");
          const rows = (await requestPromise(
            idx.getAll([userId, clientMessageId]),
          )) as StagedAttachmentRecord[];
          const restored: StagedAttachmentRecord[] = [];
          for (const row of rows) {
            if (row.conversationId !== conversationId) continue;
            const updated: StagedAttachmentRecord = {
              ...row,
              clientMessageId: "",
            };
            store.put(updated);
            restored.push(updated);
          }
          return restored.sort(sortByCreated);
        });
      } catch {
        return [];
      }
    },

    async deleteById(userId, id) {
      if (!userId || !id) return;
      try {
        await withStore("readwrite", async (store) => {
          const row = (await requestPromise(
            store.get(id),
          )) as StagedAttachmentRecord | undefined;
          if (row && row.userId === userId) store.delete(id);
        });
      } catch {
        // Ignore cleanup failure when storage is unavailable.
      }
    },

    async deleteForMessage(userId, clientMessageId) {
      if (!userId || !clientMessageId) return;
      try {
        await withStore("readwrite", async (store) => {
          const idx = store.index("byUserMessage");
          const rows = (await requestPromise(
            idx.getAll([userId, clientMessageId]),
          )) as StagedAttachmentRecord[];
          for (const row of rows) store.delete(row.id);
        });
      } catch {
        // Ignore cleanup failure.
      }
    },

    async deleteForConversation(userId, conversationId) {
      if (!userId || !conversationId) return;
      try {
        await withStore("readwrite", async (store) => {
          const idx = store.index("byUserConversation");
          const rows = (await requestPromise(
            idx.getAll([userId, conversationId]),
          )) as StagedAttachmentRecord[];
          for (const row of rows) store.delete(row.id);
        });
      } catch {
        // Ignore cleanup failure.
      }
    },

    async pruneToConversations(userId, allowedConversationIds) {
      if (!userId) return;
      try {
        await withStore("readwrite", async (store) => {
          const idx = store.index("byUser");
          const rows = (await requestPromise(
            idx.getAll(userId),
          )) as StagedAttachmentRecord[];
          for (const row of rows) {
            if (!allowedConversationIds.has(row.conversationId)) {
              store.delete(row.id);
            }
          }
        });
      } catch {
        // Ignore cleanup failure.
      }
    },

    async clearForUser(userId) {
      try {
        await withStore("readwrite", async (store) => {
          if (!userId) {
            store.clear();
            return;
          }
          const idx = store.index("byUser");
          const rows = (await requestPromise(
            idx.getAll(userId),
          )) as StagedAttachmentRecord[];
          for (const row of rows) store.delete(row.id);
        });
      } catch {
        // Ignore cleanup failure.
      }
    },

    async clearExceptUser(userId) {
      try {
        await withStore("readwrite", async (store) => {
          const rows = (await requestPromise(
            store.getAll(),
          )) as StagedAttachmentRecord[];
          for (const row of rows) {
            if (row.userId !== userId) store.delete(row.id);
          }
        });
      } catch {
        // Ignore cleanup failure.
      }
    },
  };
}

/** In-memory test backend mirroring IndexedDB semantics without browser IDB. */
export function createMemoryOfflineAttachmentBackend(): OfflineAttachmentBackend {
  const rows = new Map<string, StagedAttachmentRecord>();
  return {
    async stage(record, now = Date.now()) {
      let active = 0;
      for (const item of [...rows.values()]) {
        if (item.userId !== record.userId) continue;
        if (isExpired(item, now)) rows.delete(item.id);
        else if (item.id !== record.id) active += 1;
      }
      if (active >= MAX_STAGED_ATTACHMENTS_PER_USER) {
        throw new OfflineAttachmentStoreError(
          "capacity_exceeded",
          `离线暂存附件已达上限（${MAX_STAGED_ATTACHMENTS_PER_USER} 个），请先移除部分待发送文件。`,
        );
      }
      rows.set(record.id, { ...record });
    },
    async listDrafts(userId, conversationId, now = Date.now()) {
      const result: StagedAttachmentRecord[] = [];
      for (const item of [...rows.values()]) {
        if (item.userId !== userId || item.conversationId !== conversationId) continue;
        if (isExpired(item, now)) rows.delete(item.id);
        else if (item.clientMessageId === "") result.push({ ...item });
      }
      return result.sort(sortByCreated);
    },
    async listForMessage(userId, clientMessageId, now = Date.now()) {
      const result: StagedAttachmentRecord[] = [];
      for (const item of [...rows.values()]) {
        if (item.userId !== userId || item.clientMessageId !== clientMessageId) continue;
        if (isExpired(item, now)) rows.delete(item.id);
        else result.push({ ...item });
      }
      return result.sort(sortByCreated);
    },
    async bindToMessage(userId, conversationId, ids, clientMessageId) {
      const bound: StagedAttachmentRecord[] = [];
      for (const id of ids) {
        const item = rows.get(id);
        if (!item || item.userId !== userId || item.conversationId !== conversationId) continue;
        const next = { ...item, clientMessageId };
        rows.set(id, next);
        bound.push({ ...next });
      }
      return bound;
    },
    async unbindToDraft(userId, conversationId, clientMessageId) {
      const restored: StagedAttachmentRecord[] = [];
      for (const item of [...rows.values()]) {
        if (
          item.userId !== userId ||
          item.conversationId !== conversationId ||
          item.clientMessageId !== clientMessageId
        )
          continue;
        const next = { ...item, clientMessageId: "" };
        rows.set(item.id, next);
        restored.push({ ...next });
      }
      return restored.sort(sortByCreated);
    },
    async deleteById(userId, id) {
      const item = rows.get(id);
      if (item && item.userId === userId) rows.delete(id);
    },
    async deleteForMessage(userId, clientMessageId) {
      for (const item of [...rows.values()]) {
        if (item.userId === userId && item.clientMessageId === clientMessageId) {
          rows.delete(item.id);
        }
      }
    },
    async deleteForConversation(userId, conversationId) {
      for (const item of [...rows.values()]) {
        if (item.userId === userId && item.conversationId === conversationId) {
          rows.delete(item.id);
        }
      }
    },
    async pruneToConversations(userId, allowedConversationIds) {
      for (const item of [...rows.values()]) {
        if (item.userId === userId && !allowedConversationIds.has(item.conversationId)) {
          rows.delete(item.id);
        }
      }
    },
    async clearForUser(userId) {
      if (!userId) {
        rows.clear();
        return;
      }
      for (const item of [...rows.values()]) {
        if (item.userId === userId) rows.delete(item.id);
      }
    },
    async clearExceptUser(userId) {
      for (const item of [...rows.values()]) {
        if (item.userId !== userId) rows.delete(item.id);
      }
    },
  };
}

let activeBackend: OfflineAttachmentBackend = createIndexedDbBackend();

export function setOfflineAttachmentBackendForTest(
  backend: OfflineAttachmentBackend | null,
): void {
  activeBackend = backend ?? createIndexedDbBackend();
}

export function stagedRecordToFile(record: StagedAttachmentRecord): File {
  return new File([record.blob], record.name, {
    type: record.type || "application/octet-stream",
    lastModified: record.lastModified || record.createdAt,
  });
}

export function stageOfflineAttachment(
  record: StagedAttachmentRecord,
  now?: number,
): Promise<void> {
  return activeBackend.stage(record, now);
}

export function listStagedDraftAttachments(
  userId: string,
  conversationId: string,
  now?: number,
): Promise<StagedAttachmentRecord[]> {
  return activeBackend.listDrafts(userId, conversationId, now);
}

export function listStagedMessageAttachments(
  userId: string,
  clientMessageId: string,
  now?: number,
): Promise<StagedAttachmentRecord[]> {
  return activeBackend.listForMessage(userId, clientMessageId, now);
}

export function bindStagedAttachmentsToMessage(
  userId: string,
  conversationId: string,
  ids: string[],
  clientMessageId: string,
): Promise<StagedAttachmentRecord[]> {
  return activeBackend.bindToMessage(userId, conversationId, ids, clientMessageId);
}

export function unbindStagedAttachmentsToDraft(
  userId: string,
  conversationId: string,
  clientMessageId: string,
): Promise<StagedAttachmentRecord[]> {
  return activeBackend.unbindToDraft(userId, conversationId, clientMessageId);
}

export function deleteStagedAttachment(
  userId: string,
  id: string,
): Promise<void> {
  return activeBackend.deleteById(userId, id);
}

export function deleteStagedAttachmentsForMessage(
  userId: string,
  clientMessageId: string,
): Promise<void> {
  return activeBackend.deleteForMessage(userId, clientMessageId);
}

export function deleteStagedAttachmentsForConversation(
  userId: string,
  conversationId: string,
): Promise<void> {
  return activeBackend.deleteForConversation(userId, conversationId);
}

export function pruneStagedAttachmentsToConversations(
  userId: string,
  allowedConversationIds: ReadonlySet<string>,
): Promise<void> {
  return activeBackend.pruneToConversations(userId, allowedConversationIds);
}

export function clearStagedAttachmentsForUser(userId?: string): Promise<void> {
  return activeBackend.clearForUser(userId);
}

export function clearStagedAttachmentsExceptUser(userId: string): Promise<void> {
  return activeBackend.clearExceptUser(userId);
}
