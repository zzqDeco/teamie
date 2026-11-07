import { IDL_ROUTES } from "./generated";
import { executeTransportRequest, routePath } from "./transport";
import type { HttpTransportState } from "./transport";
import type {
  AnnouncementsPage,
  ConversationSavedIds,
  CreateAnnouncementInput,
  CreatedAnnouncement,
  DeletePinResult,
  MarkAnnouncementsReadResult,
  MarkUnreadResult,
  PinsList,
  PutPinResult,
  SavedPage,
  SaveMessageResult,
} from "./types";

export function createCollaborationClient(state: HttpTransportState) {
  const request = <T>(
    path: string,
    options?: RequestInit,
    responseType: "json" | "blob" = "json",
  ): Promise<T> => executeTransportRequest<T>(state, path, options, responseType);

  const attachmentThumbnailUrl = (conversationId: string, attachmentId: string) =>
    routePath(IDL_ROUTES.DownloadAttachmentThumbnail, {
      id: conversationId,
      attachmentId,
    });

  return {
    pinMessage: (conversationId: string, messageId: string) =>
      request<PutPinResult>(
        routePath(IDL_ROUTES.PutPin, { id: conversationId, messageId }),
        { method: IDL_ROUTES.PutPin.method },
      ),
    unpinMessage: (conversationId: string, messageId: string) =>
      request<DeletePinResult>(
        routePath(IDL_ROUTES.DeletePin, { id: conversationId, messageId }),
        { method: IDL_ROUTES.DeletePin.method },
      ),
    listPins: (conversationId: string, signal?: AbortSignal) =>
      request<PinsList>(routePath(IDL_ROUTES.ListPins, { id: conversationId }), {
        signal,
      }),
    saveMessage: (conversationId: string, messageId: string) =>
      request<SaveMessageResult>(
        routePath(IDL_ROUTES.SaveMessage, { id: conversationId, messageId }),
        { method: IDL_ROUTES.SaveMessage.method },
      ),
    unsaveMessage: (conversationId: string, messageId: string) =>
      request<void>(
        routePath(IDL_ROUTES.UnsaveMessage, { id: conversationId, messageId }),
        { method: IDL_ROUTES.UnsaveMessage.method },
      ),
    listSavedMessages: (
      options: { cursor?: string; limit?: number } = {},
      signal?: AbortSignal,
    ) => {
      const params = new URLSearchParams();
      if (options.cursor) params.set("cursor", options.cursor);
      if (options.limit !== undefined)
        params.set("limit", String(options.limit));
      return request<SavedPage>(
        `${IDL_ROUTES.ListSaved.clientPath}${params.size ? `?${params}` : ""}`,
        { signal },
      );
    },
    listConversationSavedMessages: (
      conversationId: string,
      signal?: AbortSignal,
    ) =>
      request<ConversationSavedIds>(
        routePath(IDL_ROUTES.ListConversationSaved, { id: conversationId }),
        { signal },
      ),
    markConversationUnread: (conversationId: string, seq: number) =>
      request<MarkUnreadResult>(
        routePath(IDL_ROUTES.MarkUnread, { id: conversationId }),
        {
          method: IDL_ROUTES.MarkUnread.method,
          body: JSON.stringify({ seq }),
        },
      ),
    clearConversationUnread: (conversationId: string) =>
      request<void>(routePath(IDL_ROUTES.ClearUnread, { id: conversationId }), {
        method: IDL_ROUTES.ClearUnread.method,
      }),
    attachmentThumbnailUrl,
    attachmentThumbnail: (
      conversationId: string,
      attachmentId: string,
      signal?: AbortSignal,
    ) =>
      request<Blob>(
        attachmentThumbnailUrl(conversationId, attachmentId),
        { signal },
        "blob",
      ),
    announcements: (
      options: { beforeSeq?: number; limit?: number } = {},
      signal?: AbortSignal,
    ) => {
      const params = new URLSearchParams();
      if (options.beforeSeq !== undefined)
        params.set("beforeSeq", String(options.beforeSeq));
      if (options.limit !== undefined)
        params.set("limit", String(options.limit));
      return request<AnnouncementsPage>(
        `${IDL_ROUTES.ListAnnouncements.clientPath}${params.size ? `?${params}` : ""}`,
        { signal },
      );
    },
    markAnnouncementsRead: (seq: number) =>
      request<MarkAnnouncementsReadResult>(
        IDL_ROUTES.MarkAnnouncementsRead.clientPath,
        {
          method: IDL_ROUTES.MarkAnnouncementsRead.method,
          body: JSON.stringify({ seq }),
        },
      ),
    createAnnouncement: (input: CreateAnnouncementInput) =>
      request<CreatedAnnouncement>(IDL_ROUTES.CreateAnnouncement.clientPath, {
        method: IDL_ROUTES.CreateAnnouncement.method,
        body: JSON.stringify(input),
      }),
    deleteAnnouncement: (id: string) =>
      request<void>(routePath(IDL_ROUTES.DeleteAnnouncement, { id }), {
        method: IDL_ROUTES.DeleteAnnouncement.method,
      }),
  };
}
