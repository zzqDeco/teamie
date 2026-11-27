import { api, ApiError } from "@teamie/api-client";
import type { Attachment } from "@teamie/api-client";
import type { LocalMessage, StagedAttachmentSummary } from "../collaboration-store";
import { errorText, isNetworkError } from "../errors";
import { chatStore } from "../store";
import {
  deleteStagedAttachment,
  listStagedMessageAttachments,
  stagedRecordToFile,
} from "./offline-attachments";

function isPermanentUploadError(error: unknown): boolean {
  if (!(error instanceof ApiError)) return false;
  if (error.code === "offline_unverified") return false;
  return error.status !== 429 && error.status !== 401 && error.status < 500;
}

export type StagedUploadOutcome =
  | { ok: true; attachments: Attachment[]; stagedAttachments: StagedAttachmentSummary[] }
  | { ok: false; aborted?: boolean };

/**
 * Serially uploads any staged IndexedDB attachments bound to a queued message
 * using their stable `uploadKey` (`Idempotency-Key`), persisting each uploaded
 * descriptor immediately and deleting its local blob once accepted by the server.
 */
export async function resolveStagedAttachmentsForSend(
  message: LocalMessage,
  generation: number,
): Promise<StagedUploadOutcome> {
  const staged = message.stagedAttachments;
  if (!staged?.length) {
    return { ok: true, attachments: message.attachments || [], stagedAttachments: [] };
  }

  const nextStaged = staged.map((item) => ({ ...item }));
  const needsUpload = nextStaged.some((item) => !item.attachment);
  const recordsById = new Map<string, Awaited<ReturnType<typeof listStagedMessageAttachments>>[number]>();

  if (needsUpload) {
    const records = await listStagedMessageAttachments(
      message.senderId,
      message.clientMessageId,
    );
    if (chatStore.generation !== generation) return { ok: false, aborted: true };
    for (const rec of records) recordsById.set(rec.id, rec);
  }

  for (let i = 0; i < nextStaged.length; i++) {
    if (chatStore.generation !== generation) return { ok: false, aborted: true };
    const item = nextStaged[i];
    if (item.attachment) continue;

    const record = recordsById.get(item.id);
    if (!record) {
      chatStore.merge([
        {
          ...message,
          stagedAttachments: nextStaged,
          status: "failed",
          permanent: true,
          error: "离线暂存的附件已过期或丢失，请删除后重新添加。",
        },
      ]);
      return { ok: false };
    }

    try {
      const response = await api.uploadAttachment(
        message.conversationId,
        stagedRecordToFile(record),
        undefined,
        item.uploadKey,
      );
      if (chatStore.generation !== generation) return { ok: false, aborted: true };
      nextStaged[i] = { ...item, attachment: response.attachment };
      chatStore.merge([
        {
          ...message,
          stagedAttachments: nextStaged,
          status: "sending",
          error: undefined,
          permanent: undefined,
        },
      ]);
      void deleteStagedAttachment(message.senderId, item.id);
    } catch (error) {
      if (chatStore.generation !== generation) return { ok: false, aborted: true };
      const offlineNow =
        (error instanceof ApiError && error.code === "offline_unverified") ||
        (isNetworkError(error) &&
          typeof navigator !== "undefined" &&
          navigator.onLine === false);
      if (offlineNow) {
        chatStore.merge([
          {
            ...message,
            stagedAttachments: nextStaged,
            status: "queued",
            error: undefined,
            permanent: false,
          },
        ]);
        return { ok: false };
      }
      const permanent = isPermanentUploadError(error);
      chatStore.merge([
        {
          ...message,
          stagedAttachments: nextStaged,
          status: "failed",
          error: errorText(error),
          permanent,
        },
      ]);
      return { ok: false };
    }
  }

  const combinedAttachments: Attachment[] = [
    ...(message.attachments || []),
    ...nextStaged.map((item) => item.attachment!),
  ];
  return {
    ok: true,
    attachments: combinedAttachments,
    stagedAttachments: nextStaged,
  };
}
