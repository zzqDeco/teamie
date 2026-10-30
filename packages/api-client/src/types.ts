import type {
  MarketplaceAppDTO, DeveloperMarketplaceAppDTO, MarketplaceEndpointVerificationDTO,
  DiscoveredMCPToolDTO, EndpointProbeCheckDTO, MarketplaceAppReviewDTO,
  CreateMarketplaceAppReq, CreateDeveloperMarketplaceAppReq, UpdateMarketplaceAppReq,
  CreatedMarketplaceAppResp, CreatedDeveloperMarketplaceAppResp,
  RotateMarketplaceAppSecretResp, RotateDeveloperMarketplaceAppSecretResp,
  DeveloperMarketplaceAppDetailResp, VerifyMarketplaceAppResp, ConversationAppSessionResp,
  AdminModelConfigResp,
  AdminModelProfileDTO,
  AdminModelProviderDTO,
  AdminUserPageResp,
  AttachmentDTO,
  AttachmentLimitsDTO,
  AttentionItemDTO,
  AttentionPageResp,
  BootstrapResp,
  ConversationDTO,
  CreateAdminModelProfileReq,
  CreateAdminModelProviderReq,
  CreatePasswordResetLinkResp,
  EventDTO,
  ListMessagesResp,
  MemberDTO,
  MemberReadReceiptDTO,
  MentionDTO,
  MessageDTO,
  ModelCatalogResp,
  ModelConfigDTO,
  PasswordResetDTO,
  PasswordResetInspectionResp,
  PresenceItemDTO,
  PublicAgentDTO,
  PublicModelOptionDTO,
  ReactionDTO,
  ReadReceiptsResp,
  ReadSeqCountDTO,
  RegistrationInviteDTO,
  RegistrationMetadataResp,
  SessionResp,
  SyncResp,
  UpdateAdminModelProfileReq,
  UpdateAdminModelProviderReq,
  UpdateAdminModelRoutingReq,
  UpdateAdminWebSearchConfigReq,
  UserDTO,
  WebSearchConfigDTO,
  WorkspaceDTO,
  WorkspaceSearchItemDTO,
  WorkspaceSearchPageResp,
} from "./generated";

export * from "./generated";

export type User = Omit<UserDTO, "kind" | "platformRole"> & {
  kind: "human" | "agent";
  platformRole: "admin" | "user";
};
export type Session = Omit<SessionResp, "user"> & { user: User };
export type RegistrationPolicy = Omit<RegistrationMetadataResp, "mode"> & {
  mode: "invite";
};
export type RegistrationInvite = RegistrationInviteDTO;
export type CreatedRegistrationInvite = {
  invite: Pick<RegistrationInvite, "id" | "createdAt" | "expiresAt"> & {
    code: string;
  };
  url: string;
};
export type PlatformUsersPage = Omit<AdminUserPageResp, "users"> & {
  users: User[];
};
export type PasswordResetLink = PasswordResetDTO;
export type CreatedPasswordResetLink = CreatePasswordResetLinkResp;
export type PasswordResetInspection = PasswordResetInspectionResp;
export type Workspace = WorkspaceDTO;
export type NotificationLevel = "all" | "mentions" | "none";
export type Conversation = Omit<
  ConversationDTO,
  "type" | "canManageMembers" | "notificationLevel"
> & {
  type: "channel" | "direct" | "group";
  canManageMembers?: boolean;
  notificationLevel?: NotificationLevel;
};
export type Member = Omit<MemberDTO, "role" | "kind"> & {
  role: "owner" | "member";
  kind: "human" | "agent";
  ownerId?: string;
  ownerName?: string;
};
export type Agent = PublicAgentDTO;
export type AgentProfileInput = {
  name: string;
  description?: string;
  workspaceIds: string[];
  enabled?: boolean;
  modelId?: string;
};
export type WebSearchGatewayConfig = WebSearchConfigDTO;
export type AdminModelProvider = AdminModelProviderDTO;
export type AdminModelProfile = AdminModelProfileDTO;
export type PublicModelOption = PublicModelOptionDTO;
export type ModelCatalog = ModelCatalogResp;
export type CreateModelProviderInput = CreateAdminModelProviderReq;
export type UpdateModelProviderInput = Omit<UpdateAdminModelProviderReq, "id">;
export type CreateModelProfileInput = CreateAdminModelProfileReq;
export type UpdateModelProfileInput = Omit<UpdateAdminModelProfileReq, "id">;
export type ModelGatewayConfig = Omit<
  ModelConfigDTO,
  "contextWindowTokens" | "webSearch"
> & {
  contextWindowTokens?: number;
  webSearch?: WebSearchGatewayConfig;
};
export type ModelGateway = Omit<AdminModelConfigResp, "config" | "status"> & {
  config: ModelGatewayConfig;
  status: "unconfigured" | "ready" | "disabled";
};
export type WebSearchInput = UpdateAdminWebSearchConfigReq;
export type ModelRoutingInput = UpdateAdminModelRoutingReq;
export type Attachment = AttachmentDTO;
export type AttachmentLimits = AttachmentLimitsDTO;
export const defaultAttachmentLimits: AttachmentLimits = {
  maxFileBytes: 4 * 1024 * 1024,
  maxFilesPerMessage: 8,
};
export type Mention = MentionDTO;
export type Reaction = ReactionDTO;
export type ReadSeqCount = ReadSeqCountDTO;
export type MemberReadReceipt = MemberReadReceiptDTO;
export type ReadReceipts = ReadReceiptsResp;
export type PresenceState = "online" | "away" | "offline";
export type PresenceItem = Omit<PresenceItemDTO, "state"> & {
  state: "online" | "away";
};
export type Message = Omit<MessageDTO, "senderKind"> & {
  senderKind: "human" | "agent";
};
export type Bootstrap = Omit<
  BootstrapResp,
  "user" | "workspaces" | "conversations" | "attachmentLimits"
> & {
  user: User;
  workspaces: Workspace[];
  conversations: Conversation[];
  attachmentLimits?: AttachmentLimits;
};
export type SyncEvent = Omit<
  EventDTO,
  "type" | "message" | "conversation" | "notificationLevel"
> & {
  type:
    | "message.created"
    | "message.updated"
    | "message.retracted"
    | "conversation.created"
    | "membership.changed"
    | "read.updated"
    | "reaction.updated"
    | "preference.updated";
  message?: Message;
  conversation?: Conversation;
  notificationLevel?: NotificationLevel;
};
export type SyncPage = Omit<SyncResp, "events"> & {
  events: SyncEvent[];
};
export type MessagePage = Omit<ListMessagesResp, "messages"> & {
  messages: Message[];
};
export type AttentionItem = Omit<
  AttentionItemDTO,
  "message" | "conversation" | "reasons"
> & {
  message: Message;
  conversation: Pick<Conversation, "id" | "workspaceId" | "name" | "type">;
  reasons: ("mention" | "reply")[];
};
export type AttentionPage = Omit<
  AttentionPageResp,
  "items" | "channelUnreadCount"
> & {
  items: AttentionItem[];
  channelUnreadCount?: number;
};
export type WorkspaceSearchItem = Omit<
  WorkspaceSearchItemDTO,
  "message" | "conversation"
> & {
  message: Message;
  conversation: Pick<Conversation, "id" | "workspaceId" | "name" | "type">;
};
export type WorkspaceSearchPage = Omit<WorkspaceSearchPageResp, "items"> & {
  items: WorkspaceSearchItem[];
};

export type MarketplaceApp = MarketplaceAppDTO;
export type DeveloperMarketplaceApp = DeveloperMarketplaceAppDTO;
export type MarketplaceEndpointVerification = MarketplaceEndpointVerificationDTO;
export type DiscoveredMCPTool = DiscoveredMCPToolDTO;
export type EndpointProbeCheck = EndpointProbeCheckDTO;
export type MarketplaceAppReview = MarketplaceAppReviewDTO;
export type CreateMarketplaceAppInput = CreateMarketplaceAppReq;
export type CreateDeveloperMarketplaceAppInput = CreateDeveloperMarketplaceAppReq;
export type UpdateMarketplaceAppInput = Omit<UpdateMarketplaceAppReq, "id">;
export type CreatedMarketplaceApp = CreatedMarketplaceAppResp;
export type CreatedDeveloperMarketplaceApp = CreatedDeveloperMarketplaceAppResp;
export type RotatedMarketplaceAppSecret = RotateMarketplaceAppSecretResp;
export type RotatedDeveloperMarketplaceAppSecret = RotateDeveloperMarketplaceAppSecretResp;
export type DeveloperMarketplaceAppDetail = DeveloperMarketplaceAppDetailResp;
export type VerifiedMarketplaceApp = VerifyMarketplaceAppResp;
export type ConversationAppSession = ConversationAppSessionResp;
