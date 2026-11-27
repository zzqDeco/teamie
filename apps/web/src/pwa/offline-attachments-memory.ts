import {
  MAX_STAGED_ATTACHMENTS_PER_USER,
  OfflineAttachmentStoreError,
  isStagedAttachmentExpired,
  sortStagedAttachments,
} from "./offline-attachments";
import type {
  OfflineAttachmentBackend,
  StagedAttachmentRecord,
} from "./offline-attachments";

/** In-memory test backend mirroring IndexedDB semantics without browser IDB. */
export function createMemoryOfflineAttachmentBackend(): OfflineAttachmentBackend {
  const rows = new Map<string, StagedAttachmentRecord>();
  return {
    async stage(record, now = Date.now()) {
      let active = 0;
      for (const item of [...rows.values()]) {
        if (item.userId !== record.userId) continue;
        if (isStagedAttachmentExpired(item, now)) rows.delete(item.id);
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
        if (isStagedAttachmentExpired(item, now)) rows.delete(item.id);
        else if (item.clientMessageId === "") result.push({ ...item });
      }
      return result.sort(sortStagedAttachments);
    },
    async listForMessage(userId, clientMessageId, now = Date.now()) {
      const result: StagedAttachmentRecord[] = [];
      for (const item of [...rows.values()]) {
        if (item.userId !== userId || item.clientMessageId !== clientMessageId) continue;
        if (isStagedAttachmentExpired(item, now)) rows.delete(item.id);
        else result.push({ ...item });
      }
      return result.sort(sortStagedAttachments);
    },
    async bindToMessage(userId, conversationId, ids, clientMessageId) {
      const candidates: StagedAttachmentRecord[] = [];
      for (const id of ids) {
        const item = rows.get(id);
        if (
          !item ||
          item.userId !== userId ||
          item.conversationId !== conversationId ||
          (item.clientMessageId !== "" && item.clientMessageId !== clientMessageId)
        ) {
          return [];
        }
        candidates.push(item);
      }
      const bound: StagedAttachmentRecord[] = [];
      for (const item of candidates) {
        const next = { ...item, clientMessageId };
        rows.set(item.id, next);
        bound.push({ ...next });
      }
      return bound;
    },
    async unbindToDraft(userId, conversationId, clientMessageId, requiredIds, now) {
      const liveRows = [...rows.values()].filter(
        (item) =>
          item.userId === userId &&
          item.conversationId === conversationId &&
          item.clientMessageId === clientMessageId &&
          (now === undefined || !isStagedAttachmentExpired(item, now)),
      );
      if (requiredIds?.length) {
        const liveIds = new Set(liveRows.map((item) => item.id));
        if (requiredIds.some((id) => !liveIds.has(id))) {
          throw new OfflineAttachmentStoreError(
            "missing_attachment",
            "部分离线暂存附件已过期或丢失，无法恢复编辑，请取消发送后重新添加。",
          );
        }
      }
      const restored: StagedAttachmentRecord[] = [];
      for (const item of liveRows) {
        const next = { ...item, clientMessageId: "" };
        rows.set(item.id, next);
        restored.push({ ...next });
      }
      return restored.sort(sortStagedAttachments);
    },
    async reconcileOrphanedBindings(
      userId,
      activeClientMessageIds,
      now = Date.now(),
    ) {
      const recovered: StagedAttachmentRecord[] = [];
      for (const item of [...rows.values()]) {
        if (item.userId !== userId) continue;
        if (isStagedAttachmentExpired(item, now)) {
          rows.delete(item.id);
        } else if (
          item.clientMessageId !== "" &&
          !activeClientMessageIds.has(item.clientMessageId)
        ) {
          const next = { ...item, clientMessageId: "" };
          rows.set(item.id, next);
          recovered.push({ ...next });
        }
      }
      return recovered.sort(sortStagedAttachments);
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
