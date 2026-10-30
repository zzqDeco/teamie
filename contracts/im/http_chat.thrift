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
  11: optional string notificationLevel (api.body="notificationLevel")
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

struct ReactionDTO {
  1: required string emoji (api.body="emoji")
  2: required i64 count (api.body="count")
  3: required list<string> userIds (api.body="userIds")
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
  15: optional list<ReactionDTO> reactions (api.body="reactions")
  16: optional string reactionsVersion (api.body="reactionsVersion")
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
  9: optional string messageId (api.body="messageId")
  10: optional list<ReactionDTO> reactions (api.body="reactions")
  11: optional string notificationLevel (api.body="notificationLevel")
  12: optional string reactionsVersion (api.body="reactionsVersion")
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
  5: optional string idempotencyKey (api.header="Idempotency-Key", go.tag="json:\"-\"")
}

struct ConversationResp {
  1: required ConversationDTO conversation (api.body="conversation")
}

struct AddConversationMemberReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: required string userId (api.body="userId")
  3: optional string idempotencyKey (api.header="Idempotency-Key", go.tag="json:\"-\"")
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

struct PutReactionReq {
  1: required string id (api.path="id")
  2: required string messageId (api.path="messageId")
  3: required string emoji (api.path="emoji")
}

struct DeleteReactionReq {
  1: required string id (api.path="id")
  2: required string messageId (api.path="messageId")
  3: required string emoji (api.path="emoji")
}

struct ReactionsResp {
  1: required list<ReactionDTO> reactions (api.body="reactions")
  2: required string reactionsVersion (api.body="reactionsVersion")
}

struct ReadSeqCountDTO {
  1: required i64 readSeq (api.body="readSeq")
  2: required i64 count (api.body="count")
}

struct MemberReadReceiptDTO {
  1: required string userId (api.body="userId")
  2: required i64 readSeq (api.body="readSeq")
}

struct GetReadReceiptsReq {
  1: required string id (api.path="id")
}

struct ReadReceiptsResp {
  1: required string conversationId (api.body="conversationId")
  2: required i64 eligibleCount (api.body="eligibleCount")
  3: required list<ReadSeqCountDTO> readSeqCounts (api.body="readSeqCounts")
  4: optional list<MemberReadReceiptDTO> members (api.body="members")
}

struct SetNotificationLevelReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: required string level (api.body="level")
}

struct NotificationLevelResp {
  1: required string conversationId (api.body="conversationId")
  2: required string notificationLevel (api.body="notificationLevel")
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

struct AttentionUnreadDTO {
  1: required string conversationId (api.body="conversationId")
  2: required i64 unreadCount (api.body="unreadCount")
}

struct AttentionPageResp {
  1: required list<AttentionItemDTO> items (api.body="items")
  2: optional string cursor (api.body="cursor")
  3: required i64 unreadCount (api.body="unreadCount")
  4: required i64 channelUnreadCount (api.body="channelUnreadCount")
  5: optional list<AttentionUnreadDTO> attentionUnreadByConversation (api.body="attentionUnreadByConversation", go.tag="json:\"attentionUnreadByConversation\"")
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

struct MarketplaceAppDTO {
  1: required string id (api.body="id")
  2: required string slug (api.body="slug")
  3: required string name (api.body="name")
  4: required string description (api.body="description")
  5: required string webUrl (api.body="webUrl")
  6: required string mcpUrl (api.body="mcpUrl")
  7: required bool enabled (api.body="enabled")
  8: required string createdAt (api.body="createdAt")
  9: required string updatedAt (api.body="updatedAt")
  10: optional string publisherName (api.body="publisherName")
  11: optional bool isOfficial (api.body="isOfficial")
  12: optional bool servingDraft (api.body="servingDraft")
  13: optional i32 publishedVersion (api.body="publishedVersion")
}

struct DiscoveredMCPToolDTO {
  1: required string name (api.body="name")
  2: required string exposedName (api.body="exposedName")
  3: required string description (api.body="description")
  4: optional string inputSchemaJson (api.body="inputSchemaJson")
}

struct EndpointProbeCheckDTO {
  1: required string stage (api.body="stage")
  2: required bool ok (api.body="ok")
  3: required string code (api.body="code")
  4: required string summary (api.body="summary")
}

struct MarketplaceEndpointVerificationDTO {
  1: required bool ok (api.body="ok")
  2: required string verifiedAt (api.body="verifiedAt")
  3: required string webUrl (api.body="webUrl")
  4: required string mcpUrl (api.body="mcpUrl")
  5: optional string mcpServerName (api.body="mcpServerName")
  6: optional string mcpServerVersion (api.body="mcpServerVersion")
  7: optional string mcpProtocolVersion (api.body="mcpProtocolVersion")
  8: required list<EndpointProbeCheckDTO> checks (api.body="checks")
  9: required list<DiscoveredMCPToolDTO> tools (api.body="tools")
}

struct MarketplaceAppReviewDTO {
  1: required string id (api.body="id")
  2: required string appId (api.body="appId")
  3: required i32 revision (api.body="revision")
  4: required string action (api.body="action")
  5: required string actorId (api.body="actorId")
  6: optional string actorName (api.body="actorName")
  7: required string note (api.body="note")
  8: required string webUrl (api.body="webUrl")
  9: required string mcpUrl (api.body="mcpUrl")
  10: required string createdAt (api.body="createdAt")
}

struct DeveloperMarketplaceAppDTO {
  1: required string id (api.body="id")
  2: required string slug (api.body="slug")
  3: required string name (api.body="name")
  4: required string description (api.body="description")
  5: required string webUrl (api.body="webUrl")
  6: required string mcpUrl (api.body="mcpUrl")
  7: required bool enabled (api.body="enabled")
  8: required string createdAt (api.body="createdAt")
  9: required string updatedAt (api.body="updatedAt")
  10: optional string ownerId (api.body="ownerId")
  11: optional string ownerDisplayName (api.body="ownerDisplayName")
  12: required bool isOfficial (api.body="isOfficial")
  13: required bool isPublished (api.body="isPublished")
  14: required i32 publishedVersion (api.body="publishedVersion")
  15: required string reviewStatus (api.body="reviewStatus")
  16: required string draftName (api.body="draftName")
  17: required string draftDescription (api.body="draftDescription")
  18: required string draftWebUrl (api.body="draftWebUrl")
  19: required string draftMcpUrl (api.body="draftMcpUrl")
  20: required bool hasDraftChanges (api.body="hasDraftChanges")
  21: required i32 submissionRevision (api.body="submissionRevision")
  22: required string submissionNote (api.body="submissionNote")
  23: optional string submittedAt (api.body="submittedAt")
  24: optional string reviewedAt (api.body="reviewedAt")
  25: required string reviewNote (api.body="reviewNote")
  26: required bool adminSuspended (api.body="adminSuspended")
  27: required string suspendedReason (api.body="suspendedReason")
  28: optional string lastVerifiedAt (api.body="lastVerifiedAt")
  29: optional MarketplaceEndpointVerificationDTO verificationReport (api.body="verificationReport")
}

struct MarketplaceAppResp {
  1: required MarketplaceAppDTO app (api.body="app")
}

struct CreatedMarketplaceAppResp {
  1: required MarketplaceAppDTO app (api.body="app")
  2: required string signingSecret (api.body="signingSecret")
}

struct RotateMarketplaceAppSecretResp {
  1: required MarketplaceAppDTO app (api.body="app")
  2: required string signingSecret (api.body="signingSecret")
}

struct MarketplaceAppListResp {
  1: required list<MarketplaceAppDTO> apps (api.body="apps")
}

struct DeveloperMarketplaceAppResp {
  1: required DeveloperMarketplaceAppDTO app (api.body="app")
}

struct DeveloperMarketplaceAppDetailResp {
  1: required DeveloperMarketplaceAppDTO app (api.body="app")
  2: required list<MarketplaceAppReviewDTO> reviews (api.body="reviews")
}

struct DeveloperMarketplaceAppListResp {
  1: required list<DeveloperMarketplaceAppDTO> apps (api.body="apps")
}

struct CreatedDeveloperMarketplaceAppResp {
  1: required DeveloperMarketplaceAppDTO app (api.body="app")
  2: required string signingSecret (api.body="signingSecret")
}

struct RotateDeveloperMarketplaceAppSecretResp {
  1: required DeveloperMarketplaceAppDTO app (api.body="app")
  2: required string signingSecret (api.body="signingSecret")
}

struct VerifyMarketplaceAppResp {
  1: required DeveloperMarketplaceAppDTO app (api.body="app")
  2: required MarketplaceEndpointVerificationDTO verification (api.body="verification")
}

struct CreateMarketplaceAppReq {
  1: required string slug (api.body="slug")
  2: required string name (api.body="name")
  3: optional string description (api.body="description")
  4: required string webUrl (api.body="webUrl")
  5: required string mcpUrl (api.body="mcpUrl")
  6: optional bool enabled (api.body="enabled")
}

struct CreateDeveloperMarketplaceAppReq {
  1: required string slug (api.body="slug")
  2: required string name (api.body="name")
  3: optional string description (api.body="description")
  4: optional string webUrl (api.body="webUrl")
  5: optional string mcpUrl (api.body="mcpUrl")
}

struct UpdateMarketplaceAppReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: optional string slug (api.body="slug")
  3: optional string name (api.body="name")
  4: optional string description (api.body="description")
  5: optional string webUrl (api.body="webUrl")
  6: optional string mcpUrl (api.body="mcpUrl")
  7: optional bool enabled (api.body="enabled")
}

struct SubmitDeveloperMarketplaceAppReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: optional string note (api.body="note")
}

struct ReviewAdminMarketplaceAppReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: required i32 expectedRevision (api.body="expectedRevision")
  3: optional string note (api.body="note")
}

struct RejectAdminMarketplaceAppReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: required i32 expectedRevision (api.body="expectedRevision")
  3: required string note (api.body="note")
}

struct SuspendAdminMarketplaceAppReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: required string reason (api.body="reason")
}

struct MarketplaceAppIDReq {
  1: required string id (api.path="id")
}

struct InstallWorkspaceAppReq {
  1: required string id (api.path="id", go.tag="json:\"-\"")
  2: required string appId (api.body="appId")
}

struct UninstallWorkspaceAppReq {
  1: required string id (api.path="id")
  2: required string appId (api.path="appId")
}

struct CreateConversationAppSessionReq {
  1: required string id (api.path="id")
  2: required string appId (api.path="appId")
}

struct ConversationAppSessionResp {
  1: required MarketplaceAppDTO app (api.body="app")
  2: required string token (api.body="token")
  3: required string expiresAt (api.body="expiresAt")
}

service ChatHTTPService {
  BootstrapResp GetBootstrap(1: GetBootstrapReq req) (api.get="/api/v1/bootstrap")
  CreateWorkspaceResp CreateWorkspace(1: CreateWorkspaceReq req) (api.post="/api/v1/workspaces", api.status="201")
  ListWorkspaceMembersResp ListWorkspaceMembers(1: ListWorkspaceMembersReq req) (api.get="/api/v1/workspaces/:id/members")
  CreateWorkspaceInviteResp CreateWorkspaceInvite(1: CreateWorkspaceInviteReq req) (api.post="/api/v1/workspaces/:id/invites", api.status="201")
  GetInviteResp GetInvite(1: GetInviteReq req) (api.get="/api/v1/invites/:code")
  AcceptInviteResp AcceptInvite(1: AcceptInviteReq req) (api.post="/api/v1/invites/:code/accept")
  EmptyResp RemoveWorkspaceMember(1: RemoveWorkspaceMemberReq req) (api.delete="/api/v1/workspaces/:id/members/:userId")
  ListConversationsResp ListConversations(1: ListConversationsReq req) (api.get="/api/v1/workspaces/:id/conversations")
  ConversationResp CreateConversation(1: CreateConversationReq req) (api.post="/api/v1/workspaces/:id/conversations", api.status="201")
  ConversationResp AddConversationMember(1: AddConversationMemberReq req) (api.post="/api/v1/conversations/:id/members")
  ConversationResp RemoveConversationMember(1: RemoveConversationMemberReq req) (api.delete="/api/v1/conversations/:id/members/:userId")
  ListMessagesResp ListMessages(1: ListMessagesReq req) (api.get="/api/v1/conversations/:id/messages")
  MessageResp GetMessage(1: GetMessageReq req) (api.get="/api/v1/conversations/:id/messages/:messageId")
  MessageResp SendMessage(1: SendMessageReq req) (api.post="/api/v1/conversations/:id/messages", api.status="201")
  MessageResp EditMessage(1: EditMessageReq req) (api.patch="/api/v1/conversations/:id/messages/:messageId")
  MessageResp RetractMessage(1: RetractMessageReq req) (api.delete="/api/v1/conversations/:id/messages/:messageId")
  ReactionsResp PutReaction(1: PutReactionReq req) (api.put="/api/v1/conversations/:id/messages/:messageId/reactions/:emoji")
  ReactionsResp DeleteReaction(1: DeleteReactionReq req) (api.delete="/api/v1/conversations/:id/messages/:messageId/reactions/:emoji")
  MarkReadResp MarkRead(1: MarkReadReq req) (api.post="/api/v1/conversations/:id/read")
  ReadReceiptsResp GetReadReceipts(1: GetReadReceiptsReq req) (api.get="/api/v1/conversations/:id/read-receipts")
  NotificationLevelResp SetNotificationLevel(1: SetNotificationLevelReq req) (api.put="/api/v1/conversations/:id/notification-level")
  WorkspaceSearchPageResp SearchWorkspaceMessages(1: SearchWorkspaceMessagesReq req) (api.get="/api/v1/workspaces/:id/messages/search")
  AttentionPageResp GetAttention(1: GetAttentionReq req) (api.get="/api/v1/attention")
  SyncResp Sync(1: SyncReq req) (api.get="/api/v1/sync")
  AttachmentResp UploadAttachment(1: UploadAttachmentReq req) (api.post="/api/v1/conversations/:id/attachments")
  AttachmentResp GetAttachmentUpload(1: GetAttachmentUploadReq req) (api.get="/api/v1/conversations/:id/attachment-uploads/:uploadKey")
  EmptyResp DeletePendingAttachment(1: DeletePendingAttachmentReq req) (api.delete="/api/v1/conversations/:id/attachments/:attachmentId")
  DownloadAttachmentResp DownloadAttachment(1: DownloadAttachmentReq req) (api.get="/api/v1/conversations/:id/attachments/:attachmentId/content")
  DeveloperMarketplaceAppListResp ListDeveloperMarketplaceApps(1: GetBootstrapReq req) (api.get="/api/v1/developer/apps")
  CreatedDeveloperMarketplaceAppResp CreateDeveloperMarketplaceApp(1: CreateDeveloperMarketplaceAppReq req) (api.post="/api/v1/developer/apps", api.status="201")
  DeveloperMarketplaceAppDetailResp GetDeveloperMarketplaceApp(1: MarketplaceAppIDReq req) (api.get="/api/v1/developer/apps/:id")
  DeveloperMarketplaceAppResp UpdateDeveloperMarketplaceApp(1: UpdateMarketplaceAppReq req) (api.patch="/api/v1/developer/apps/:id")
  VerifyMarketplaceAppResp VerifyDeveloperMarketplaceApp(1: MarketplaceAppIDReq req) (api.post="/api/v1/developer/apps/:id/verify")
  DeveloperMarketplaceAppDetailResp SubmitDeveloperMarketplaceApp(1: SubmitDeveloperMarketplaceAppReq req) (api.post="/api/v1/developer/apps/:id/submit")
  DeveloperMarketplaceAppDetailResp WithdrawDeveloperMarketplaceApp(1: MarketplaceAppIDReq req) (api.post="/api/v1/developer/apps/:id/withdraw")
  RotateDeveloperMarketplaceAppSecretResp RotateDeveloperMarketplaceAppSecret(1: MarketplaceAppIDReq req) (api.post="/api/v1/developer/apps/:id/rotate-secret")
  EmptyResp DeleteDeveloperMarketplaceApp(1: MarketplaceAppIDReq req) (api.delete="/api/v1/developer/apps/:id")
  DeveloperMarketplaceAppListResp ListAdminMarketplaceApps(1: GetBootstrapReq req) (api.get="/api/v1/admin/marketplace/apps")
  DeveloperMarketplaceAppDetailResp GetAdminMarketplaceApp(1: MarketplaceAppIDReq req) (api.get="/api/v1/admin/marketplace/apps/:id")
  CreatedMarketplaceAppResp CreateAdminMarketplaceApp(1: CreateMarketplaceAppReq req) (api.post="/api/v1/admin/marketplace/apps", api.status="201")
  MarketplaceAppResp UpdateAdminMarketplaceApp(1: UpdateMarketplaceAppReq req) (api.patch="/api/v1/admin/marketplace/apps/:id")
  VerifyMarketplaceAppResp VerifyAdminMarketplaceApp(1: MarketplaceAppIDReq req) (api.post="/api/v1/admin/marketplace/apps/:id/verify")
  DeveloperMarketplaceAppDetailResp ApproveAdminMarketplaceApp(1: ReviewAdminMarketplaceAppReq req) (api.post="/api/v1/admin/marketplace/apps/:id/approve")
  DeveloperMarketplaceAppDetailResp RejectAdminMarketplaceApp(1: RejectAdminMarketplaceAppReq req) (api.post="/api/v1/admin/marketplace/apps/:id/reject")
  DeveloperMarketplaceAppDetailResp SuspendAdminMarketplaceApp(1: SuspendAdminMarketplaceAppReq req) (api.post="/api/v1/admin/marketplace/apps/:id/suspend")
  DeveloperMarketplaceAppDetailResp UnsuspendAdminMarketplaceApp(1: MarketplaceAppIDReq req) (api.post="/api/v1/admin/marketplace/apps/:id/unsuspend")
  RotateMarketplaceAppSecretResp RotateAdminMarketplaceAppSecret(1: MarketplaceAppIDReq req) (api.post="/api/v1/admin/marketplace/apps/:id/rotate-secret")
  EmptyResp DeleteAdminMarketplaceApp(1: MarketplaceAppIDReq req) (api.delete="/api/v1/admin/marketplace/apps/:id")
  MarketplaceAppListResp ListMarketplaceCatalog(1: GetBootstrapReq req) (api.get="/api/v1/marketplace/catalog")
  MarketplaceAppListResp ListWorkspaceApps(1: ListWorkspaceMembersReq req) (api.get="/api/v1/workspaces/:id/apps")
  MarketplaceAppResp InstallWorkspaceApp(1: InstallWorkspaceAppReq req) (api.post="/api/v1/workspaces/:id/apps", api.status="201")
  EmptyResp UninstallWorkspaceApp(1: UninstallWorkspaceAppReq req) (api.delete="/api/v1/workspaces/:id/apps/:appId")
  MarketplaceAppListResp ListConversationApps(1: GetReadReceiptsReq req) (api.get="/api/v1/conversations/:id/apps")
  ConversationAppSessionResp CreateConversationAppSession(1: CreateConversationAppSessionReq req) (api.post="/api/v1/conversations/:id/apps/:appId/session")
}
