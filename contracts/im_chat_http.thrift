namespace go im.chat

// Public HTTP wire contract for the Teamie Chat service.
// Kept independent from any Agent runtime or Eino types.

struct UserDTO {
  1: required string id (api.body="id")
  2: required string email (api.body="email")
  3: required string displayName (api.body="displayName")
  4: required string kind (api.body="kind")
  5: required string platformRole (api.body="platformRole")
  6: required string createdAt (api.body="createdAt")
}

struct WorkspaceDTO {
  1: required string id (api.body="id")
  2: required string name (api.body="name")
  3: required string createdBy (api.body="createdBy")
  4: required string createdAt (api.body="createdAt")
}

struct InviteWorkspaceDTO {
  1: required string id (api.body="id")
  2: required string name (api.body="name")
}

struct MemberDTO {
  1: required string userId (api.body="userId")
  2: required string displayName (api.body="displayName")
  3: required string role (api.body="role")
  4: required string kind (api.body="kind")
}

struct ConversationDTO {
  1: required string id (api.body="id")
  2: required string workspaceId (api.body="workspaceId")
  3: required string type (api.body="type")
  4: required string name (api.body="name")
  5: required list<string> memberIds (api.body="memberIds")
  6: required i64 lastSeq (api.body="lastSeq")
  7: required i64 readSeq (api.body="readSeq")
  8: required string updatedAt (api.body="updatedAt")
  9: optional string createdBy (api.body="createdBy")
  10: required bool canManageMembers (api.body="canManageMembers")
}

struct AttachmentDTO {
  1: required string id (api.body="id")
  2: required string filename (api.body="filename")
  3: required string mediaType (api.body="mediaType")
  4: required i64 sizeBytes (api.body="sizeBytes")
  5: required string sha256 (api.body="sha256")
}

struct AttachmentLimitsDTO {
  1: required i64 maxFileBytes (api.body="maxFileBytes")
  2: required i64 maxFilesPerMessage (api.body="maxFilesPerMessage")
}

struct MentionDTO {
  1: required string userId (api.body="userId")
  2: required i64 start (api.body="start")
  3: required i64 end (api.body="end")
}

struct MessageDTO {
  1: required string id (api.body="id")
  2: required string conversationId (api.body="conversationId")
  3: required string senderId (api.body="senderId")
  4: required string senderName (api.body="senderName")
  5: required string senderKind (api.body="senderKind")
  6: required string clientMessageId (api.body="clientMessageId")
  7: required i64 seq (api.body="seq")
  8: required string body (api.body="body")
  9: optional string replyToId (api.body="replyToId")
  10: required string createdAt (api.body="createdAt")
  11: optional string editedAt (api.body="editedAt")
  12: optional string retractedAt (api.body="retractedAt")
  13: optional list<AttachmentDTO> attachments (api.body="attachments")
  14: optional list<MentionDTO> mentions (api.body="mentions")
}

struct EventDTO {
  1: required string id (api.body="id")
  2: required string type (api.body="type")
  3: optional string conversationId (api.body="conversationId")
  4: optional string workspaceId (api.body="workspaceId")
  5: optional MessageDTO message (api.body="message")
  6: optional ConversationDTO conversation (api.body="conversation")
  7: optional i64 readSeq (api.body="readSeq")
  8: required string createdAt (api.body="createdAt")
}

struct AttentionConversationDTO {
  1: required string id (api.body="id")
  2: required string workspaceId (api.body="workspaceId")
  3: required string name (api.body="name")
  4: required string type (api.body="type")
}

struct AttentionItemDTO {
  1: required MessageDTO message (api.body="message")
  2: required AttentionConversationDTO conversation (api.body="conversation")
  3: required string workspaceName (api.body="workspaceName")
  4: required list<string> reasons (api.body="reasons")
  5: required bool isRead (api.body="isRead")
}

struct WorkspaceSearchItemDTO {
  1: required MessageDTO message (api.body="message")
  2: required AttentionConversationDTO conversation (api.body="conversation")
}

struct EmptyResp {}

struct GetBootstrapReq {}

struct BootstrapResp {
  1: required UserDTO user (api.body="user")
  2: required list<WorkspaceDTO> workspaces (api.body="workspaces")
  3: required list<ConversationDTO> conversations (api.body="conversations")
  4: required map<string, bool> capabilities (api.body="capabilities")
  5: required string flagsRevision (api.body="flagsRevision")
  6: required string syncCursor (api.body="syncCursor")
  7: required AttachmentLimitsDTO attachmentLimits (api.body="attachmentLimits")
}

struct CreateWorkspaceReq {
  1: required string name (api.body="name")
}

struct CreateWorkspaceResp {
  1: required WorkspaceDTO workspace (api.body="workspace")
  2: required ConversationDTO conversation (api.body="conversation")
}

struct ListWorkspaceMembersReq {
  1: required string id (api.path="id")
}

struct ListWorkspaceMembersResp {
  1: required list<MemberDTO> members (api.body="members")
}

struct CreateWorkspaceInviteReq {
  1: required string id (api.path="id")
}

struct CreateWorkspaceInviteResp {
  1: required string code (api.body="code")
  2: required string expiresAt (api.body="expiresAt")
  3: required string url (api.body="url")
}

struct GetInviteReq {
  1: required string code (api.path="code")
}

struct GetInviteResp {
  1: required InviteWorkspaceDTO workspace (api.body="workspace")
  2: required string expiresAt (api.body="expiresAt")
}

struct AcceptInviteReq {
  1: required string code (api.path="code")
}

struct AcceptInviteResp {
  1: required WorkspaceDTO workspace (api.body="workspace")
  2: required list<ConversationDTO> conversations (api.body="conversations")
}

struct RemoveWorkspaceMemberReq {
  1: required string id (api.path="id")
  2: required string userId (api.path="userId")
}

struct ListConversationsReq {
  1: required string id (api.path="id")
}

struct ListConversationsResp {
  1: required list<ConversationDTO> conversations (api.body="conversations")
}

struct CreateConversationReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: required string type (api.body="type")
  3: optional string name (api.body="name")
  4: optional list<string> memberIds (api.body="memberIds")
}

struct ConversationResp {
  1: required ConversationDTO conversation (api.body="conversation")
}

struct AddConversationMemberReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: required string userId (api.body="userId")
}

struct RemoveConversationMemberReq {
  1: required string id (api.path="id")
  2: required string userId (api.path="userId")
}

struct ListMessagesReq {
  1: required string id (api.path="id")
  2: optional string beforeSeq (api.query="beforeSeq")
  3: optional string aroundSeq (api.query="aroundSeq")
  4: optional string limit (api.query="limit")
  5: optional string q (api.query="q")
  6: optional string content (api.query="content")
}

struct ListMessagesResp {
  1: required list<MessageDTO> messages (api.body="messages")
  2: required bool hasMore (api.body="hasMore")
}

struct GetMessageReq {
  1: required string id (api.path="id")
  2: required string messageId (api.path="messageId")
}

struct MessageResp {
  1: required MessageDTO message (api.body="message")
}

struct SendMessageReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: required string clientMessageId (api.body="clientMessageId")
  3: required string body (api.body="body")
  4: optional string replyToId (api.body="replyToId")
  5: optional list<string> attachmentIds (api.body="attachmentIds")
  6: optional list<MentionDTO> mentions (api.body="mentions")
}

struct EditMessageReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: required string messageId (api.path="messageId", go.tag="json:\"-\"")
  3: required string body (api.body="body")
  4: optional list<MentionDTO> mentions (api.body="mentions")
}

struct RetractMessageReq {
  1: required string id (api.path="id")
  2: required string messageId (api.path="messageId")
}

struct MarkReadReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: required i64 seq (api.body="seq")
}

struct MarkReadResp {
  1: required i64 readSeq (api.body="readSeq")
}

struct SearchWorkspaceMessagesReq {
  1: required string id (api.path="id")
  2: optional string q (api.query="q")
  3: optional string cursor (api.query="cursor")
  4: optional string limit (api.query="limit")
}

struct WorkspaceSearchPageResp {
  1: required list<WorkspaceSearchItemDTO> items (api.body="items")
  2: optional string cursor (api.body="cursor")
}

struct GetAttentionReq {
  1: optional string cursor (api.query="cursor")
  2: optional string limit (api.query="limit")
  3: optional string unreadOnly (api.query="unreadOnly")
}

struct AttentionPageResp {
  1: required list<AttentionItemDTO> items (api.body="items")
  2: optional string cursor (api.body="cursor")
  3: required i64 unreadCount (api.body="unreadCount")
  4: required i64 channelUnreadCount (api.body="channelUnreadCount")
}

struct SyncReq {
  1: optional string cursor (api.query="cursor")
  2: optional string limit (api.query="limit")
}

struct SyncResp {
  1: required list<EventDTO> events (api.body="events")
  2: required string cursor (api.body="cursor")
  3: required bool hasMore (api.body="hasMore")
}

struct UploadAttachmentReq {
  1: required string id (api.path="id")
  2: optional string idempotencyKey (api.header="Idempotency-Key")
  3: required string filename
  4: required binary content
}

struct AttachmentResp {
  1: required AttachmentDTO attachment (api.body="attachment")
}

struct GetAttachmentUploadReq {
  1: required string id (api.path="id")
  2: required string uploadKey (api.path="uploadKey")
}

struct DeletePendingAttachmentReq {
  1: required string id (api.path="id")
  2: required string attachmentId (api.path="attachmentId")
}

struct DownloadAttachmentReq {
  1: required string id (api.path="id")
  2: required string attachmentId (api.path="attachmentId")
}

struct DownloadAttachmentResp {
  1: required AttachmentDTO attachment
  2: required binary content
}

service ChatHTTPService {
  BootstrapResp GetBootstrap(1: GetBootstrapReq req) (api.get="/api/v1/bootstrap")
  CreateWorkspaceResp CreateWorkspace(1: CreateWorkspaceReq req) (api.post="/api/v1/workspaces")
  ListWorkspaceMembersResp ListWorkspaceMembers(1: ListWorkspaceMembersReq req) (api.get="/api/v1/workspaces/:id/members")
  CreateWorkspaceInviteResp CreateWorkspaceInvite(1: CreateWorkspaceInviteReq req) (api.post="/api/v1/workspaces/:id/invites")
  GetInviteResp GetInvite(1: GetInviteReq req) (api.get="/api/v1/invites/:code")
  AcceptInviteResp AcceptInvite(1: AcceptInviteReq req) (api.post="/api/v1/invites/:code/accept")
  EmptyResp RemoveWorkspaceMember(1: RemoveWorkspaceMemberReq req) (api.delete="/api/v1/workspaces/:id/members/:userId")
  ListConversationsResp ListConversations(1: ListConversationsReq req) (api.get="/api/v1/workspaces/:id/conversations")
  ConversationResp CreateConversation(1: CreateConversationReq req) (api.post="/api/v1/workspaces/:id/conversations")
  ConversationResp AddConversationMember(1: AddConversationMemberReq req) (api.post="/api/v1/conversations/:id/members")
  ConversationResp RemoveConversationMember(1: RemoveConversationMemberReq req) (api.delete="/api/v1/conversations/:id/members/:userId")
  ListMessagesResp ListMessages(1: ListMessagesReq req) (api.get="/api/v1/conversations/:id/messages")
  MessageResp GetMessage(1: GetMessageReq req) (api.get="/api/v1/conversations/:id/messages/:messageId")
  MessageResp SendMessage(1: SendMessageReq req) (api.post="/api/v1/conversations/:id/messages")
  MessageResp EditMessage(1: EditMessageReq req) (api.patch="/api/v1/conversations/:id/messages/:messageId")
  MessageResp RetractMessage(1: RetractMessageReq req) (api.delete="/api/v1/conversations/:id/messages/:messageId")
  MarkReadResp MarkRead(1: MarkReadReq req) (api.post="/api/v1/conversations/:id/read")
  WorkspaceSearchPageResp SearchWorkspaceMessages(1: SearchWorkspaceMessagesReq req) (api.get="/api/v1/workspaces/:id/messages/search")
  AttentionPageResp GetAttention(1: GetAttentionReq req) (api.get="/api/v1/attention")
  SyncResp Sync(1: SyncReq req) (api.get="/api/v1/sync")
  AttachmentResp UploadAttachment(1: UploadAttachmentReq req) (api.post="/api/v1/conversations/:id/attachments")
  AttachmentResp GetAttachmentUpload(1: GetAttachmentUploadReq req) (api.get="/api/v1/conversations/:id/attachment-uploads/:uploadKey")
  EmptyResp DeletePendingAttachment(1: DeletePendingAttachmentReq req) (api.delete="/api/v1/conversations/:id/attachments/:attachmentId")
  DownloadAttachmentResp DownloadAttachment(1: DownloadAttachmentReq req) (api.get="/api/v1/conversations/:id/attachments/:attachmentId/content")
}
