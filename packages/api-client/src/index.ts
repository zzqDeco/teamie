import { createMarketplaceClient } from "./marketplace";
import {
  IDL_ROUTES, type AgentListResp, type AgentResp, type AttachmentResp,
  type ChangePasswordReq, type CreateAgentReq, type CreateWorkspaceReq,
  type GetInviteResp, type InspectPasswordResetReq, type ListInvitesResp,
  type LoginReq, type MarkReadResp, type NotificationLevelResp,
  type ReactionsResp, type RegisterReq, type ResetPasswordReq,
  type SendMessageReq, type TestAdminModelConfigResp, type UpdateProfileReq,
  type WorkspacePresenceResp,
} from "./generated";
import {
  normalizePlatformUsersPage, publicAgent, publicAgentProfile,
  publicModelCatalog, publicModelGateway,
} from "./normalizers";
import { executeTransportRequest, part, routePath } from "./transport";
import type {
  AgentProfileInput, AttentionPage, Bootstrap, Conversation,
  CreatedPasswordResetLink, CreatedRegistrationInvite, CreateModelProfileInput,
  Member, Mention, Message, MessagePage, ModelCatalog, ModelGateway,
  ModelRoutingInput, NotificationLevel, PasswordResetInspection,
  PlatformUsersPage, PresenceItem, ReadReceipts, RegistrationPolicy, Session,
  SyncPage, UpdateModelProfileInput, User, WebSearchInput, Workspace,
  WorkspaceSearchPage,
} from "./types";

export * from "./generated";
export * from "./types";
export * from "./transport";
export * from "./normalizers";

export class TeamieClient {
  csrfToken = "";
  onUnauthorized?: () => void;
  onForbidden?: () => void;
  onSessionChanged?: () => void;
  #request<T>(
    path: string,
    options: RequestInit = {},
    responseType: "json" | "blob" = "json",
    authentication: "session" | "capability" = "session",
  ): Promise<T> {
    return executeTransportRequest<T>(this, path, options, responseType, authentication);
  }
  session = () => this.#request<Session>(IDL_ROUTES.GetSession.clientPath);
  registration = () =>
    this.#request<RegistrationPolicy>(IDL_ROUTES.GetRegistrationMetadata.clientPath);
  registrationInvites = () =>
    this.#request<ListInvitesResp>(IDL_ROUTES.ListInvites.clientPath);
  createRegistrationInvite = () =>
    this.#request<CreatedRegistrationInvite>(IDL_ROUTES.CreateInvite.clientPath, {
      method: IDL_ROUTES.CreateInvite.method,
      body: JSON.stringify({}),
    });
  revokeRegistrationInvite = (id: string) =>
    this.#request<void>(routePath(IDL_ROUTES.RevokeInvite, { id }), {
      method: IDL_ROUTES.RevokeInvite.method,
    });
  platformUsers = async (
    options: { cursor?: string; limit?: number; q?: string } = {},
  ): Promise<PlatformUsersPage> => {
    const params = new URLSearchParams();
    if (options.cursor) params.set("cursor", options.cursor);
    if (options.limit !== undefined) params.set("limit", String(options.limit));
    if (options.q) params.set("q", options.q);
    return normalizePlatformUsersPage(
      await this.#request<PlatformUsersPage>(
        `${IDL_ROUTES.ListAdminUsers.clientPath}${params.size ? `?${params}` : ""}`,
      ),
    );
  };
  createPasswordResetLink = (userId: string) =>
    this.#request<CreatedPasswordResetLink>(
      routePath(IDL_ROUTES.CreatePasswordResetLink, { id: userId }),
      { method: IDL_ROUTES.CreatePasswordResetLink.method, body: JSON.stringify({}) },
    );
  inspectPasswordReset = (token: string) =>
    this.#request<PasswordResetInspection>(
      IDL_ROUTES.InspectPasswordReset.clientPath,
      {
        method: IDL_ROUTES.InspectPasswordReset.method,
        body: JSON.stringify({ token } satisfies InspectPasswordResetReq),
      },
      "json",
      "capability",
    );
  resetPassword = (token: string, newPassword: string) =>
    this.#request<void>(
      IDL_ROUTES.ResetPassword.clientPath,
      {
        method: IDL_ROUTES.ResetPassword.method,
        body: JSON.stringify({ token, newPassword } satisfies ResetPasswordReq),
      },
      "json",
      "capability",
    );
  agents = async () => ({
    agents: (
      await this.#request<AgentListResp>(IDL_ROUTES.ListOwnedAgents.clientPath)
    ).agents.map((agent) => publicAgent(agent)),
  });
  agent = async (id: string) => ({
    agent: publicAgent(
      (await this.#request<AgentResp>(routePath(IDL_ROUTES.GetAgentProfile, { id }))).agent,
    ),
  });
  createAgent = async (profile: AgentProfileInput, idempotencyKey: string) => ({
    agent: publicAgent(
      (
        await this.#request<AgentResp>(IDL_ROUTES.CreateAgent.clientPath, {
          method: IDL_ROUTES.CreateAgent.method,
          headers: { "Idempotency-Key": idempotencyKey },
          body: JSON.stringify(publicAgentProfile(profile) satisfies CreateAgentReq),
        })
      ).agent,
    ),
  });
  updateAgent = async (id: string, profile: Partial<AgentProfileInput>) => ({
    agent: publicAgent(
      (
        await this.#request<AgentResp>(routePath(IDL_ROUTES.UpdateAgent, { id }), {
          method: IDL_ROUTES.UpdateAgent.method,
          body: JSON.stringify(publicAgentProfile(profile)),
        })
      ).agent,
    ),
  });
  deleteAgent = (id: string) =>
    this.#request<void>(routePath(IDL_ROUTES.DeleteAgent, { id }), {
      method: IDL_ROUTES.DeleteAgent.method,
    });
  workspaceAgents = async (id: string) => ({
    agents: (
      await this.#request<AgentListResp>(routePath(IDL_ROUTES.ListWorkspaceAgents, { id }))
    ).agents.map((agent) => publicAgent(agent, id)),
  });
  modelCatalog = async () =>
    publicModelCatalog(
      await this.#request<ModelCatalog>(IDL_ROUTES.GetModelCatalog.clientPath),
    );
  modelGateway = async () =>
    publicModelGateway(
      await this.#request<ModelGateway>(IDL_ROUTES.GetAdminModelConfig.clientPath),
    );
  saveWebSearch = async (input: WebSearchInput) =>
    publicModelGateway(
      await this.#request<ModelGateway>(IDL_ROUTES.UpdateAdminWebSearchConfig.clientPath, {
        method: IDL_ROUTES.UpdateAdminWebSearchConfig.method,
        body: JSON.stringify(input),
      }),
    );
  saveModelRouting = async (input: ModelRoutingInput) =>
    publicModelGateway(
      await this.#request<ModelGateway>(IDL_ROUTES.UpdateAdminModelRouting.clientPath, {
        method: IDL_ROUTES.UpdateAdminModelRouting.method,
        body: JSON.stringify(input),
      }),
    );
  testModelGateway = () =>
    this.#request<TestAdminModelConfigResp>(IDL_ROUTES.TestAdminModelConfig.clientPath, {
      method: IDL_ROUTES.TestAdminModelConfig.method,
      body: JSON.stringify({}),
    });
  createModelProfile = async (input: CreateModelProfileInput) =>
    publicModelGateway(
      await this.#request<ModelGateway>(IDL_ROUTES.CreateAdminModelProfile.clientPath, {
        method: IDL_ROUTES.CreateAdminModelProfile.method,
        body: JSON.stringify(input),
      }),
    );
  updateModelProfile = async (id: string, input: UpdateModelProfileInput) =>
    publicModelGateway(
      await this.#request<ModelGateway>(
        routePath(IDL_ROUTES.UpdateAdminModelProfile, { id }),
        { method: IDL_ROUTES.UpdateAdminModelProfile.method, body: JSON.stringify(input) },
      ),
    );
  deleteModelProfile = async (id: string) =>
    publicModelGateway(
      await this.#request<ModelGateway>(
        routePath(IDL_ROUTES.DeleteAdminModelProfile, { id }),
        { method: IDL_ROUTES.DeleteAdminModelProfile.method },
      ),
    );
  testModelProfile = (id: string) =>
    this.#request<TestAdminModelConfigResp>(
      routePath(IDL_ROUTES.TestAdminModelProfile, { id }),
      { method: IDL_ROUTES.TestAdminModelProfile.method, body: JSON.stringify({}) },
    );
  login = (email: string, password: string) =>
    this.#request<Session>(IDL_ROUTES.Login.clientPath, {
      method: IDL_ROUTES.Login.method,
      body: JSON.stringify({ email, password } satisfies LoginReq),
    });
  register = (data: RegisterReq) =>
    this.#request<Session>(IDL_ROUTES.Register.clientPath, {
      method: IDL_ROUTES.Register.method,
      body: JSON.stringify(data),
    });
  logout = () =>
    this.#request<void>(IDL_ROUTES.Logout.clientPath, {
      method: IDL_ROUTES.Logout.method,
    });
  changePassword = (currentPassword: string, newPassword: string) =>
    this.#request<void>(IDL_ROUTES.ChangePassword.clientPath, {
      method: IDL_ROUTES.ChangePassword.method,
      body: JSON.stringify({
        currentPassword,
        newPassword,
      } satisfies ChangePasswordReq),
    });
  profile = (displayName: string) =>
    this.#request<{ user: User }>(IDL_ROUTES.UpdateProfile.clientPath, {
      method: IDL_ROUTES.UpdateProfile.method,
      body: JSON.stringify({ displayName } satisfies UpdateProfileReq),
    });
  bootstrap = () =>
    this.#request<Bootstrap>(IDL_ROUTES.GetBootstrap.clientPath);
  createWorkspace = (name: string) =>
    this.#request<{ workspace: Workspace; conversation: Conversation }>(
      IDL_ROUTES.CreateWorkspace.clientPath,
      {
        method: IDL_ROUTES.CreateWorkspace.method,
        body: JSON.stringify({ name } satisfies CreateWorkspaceReq),
      },
    );
  members = (id: string) =>
    this.#request<{ members: Member[] }>(
      routePath(IDL_ROUTES.ListWorkspaceMembers, { id }),
    );
  createInvite = (id: string) =>
    this.#request<{ code: string; expiresAt: string }>(
      routePath(IDL_ROUTES.CreateWorkspaceInvite, { id }),
      { method: IDL_ROUTES.CreateWorkspaceInvite.method },
    );
  invite = (code: string) =>
    this.#request<GetInviteResp>(routePath(IDL_ROUTES.GetInvite, { code }));
  acceptInvite = (code: string) =>
    this.#request<{ workspace: Workspace; conversations: Conversation[] }>(
      routePath(IDL_ROUTES.AcceptInvite, { code }),
      { method: IDL_ROUTES.AcceptInvite.method },
    );
  removeMember = (workspaceId: string, userId: string) =>
    this.#request<void>(
      routePath(IDL_ROUTES.RemoveWorkspaceMember, { id: workspaceId, userId }),
      { method: IDL_ROUTES.RemoveWorkspaceMember.method },
    );
  conversations = (id: string) =>
    this.#request<{ conversations: Conversation[] }>(
      routePath(IDL_ROUTES.ListConversations, { id }),
    );
  addConversationMember = (
    id: string,
    userId: string,
    idempotencyKey?: string,
  ) =>
    this.#request<{ conversation: Conversation }>(
      routePath(IDL_ROUTES.AddConversationMember, { id }),
      {
        method: IDL_ROUTES.AddConversationMember.method,
        ...(idempotencyKey
          ? { headers: { "Idempotency-Key": idempotencyKey } }
          : {}),
        body: JSON.stringify({ userId }),
      },
    );
  removeConversationMember = (id: string, userId: string) =>
    this.#request<{ conversation: Conversation }>(
      routePath(IDL_ROUTES.RemoveConversationMember, { id, userId }),
      { method: IDL_ROUTES.RemoveConversationMember.method },
    );
  createConversation = (
    id: string,
    data: { type: Conversation["type"]; name?: string; memberIds?: string[] },
    idempotencyKey?: string,
  ) =>
    this.#request<{ conversation: Conversation }>(
      routePath(IDL_ROUTES.CreateConversation, { id }),
      {
        method: IDL_ROUTES.CreateConversation.method,
        ...(idempotencyKey
          ? { headers: { "Idempotency-Key": idempotencyKey } }
          : {}),
        body: JSON.stringify(data),
      },
    );
  messages = (id: string, beforeSeq?: number) =>
    this.#request<MessagePage>(
      `${routePath(IDL_ROUTES.ListMessages, { id })}?limit=50${beforeSeq ? `&beforeSeq=${beforeSeq}` : ""}`,
    );
  attention = (
    options: { cursor?: string; limit?: number; unreadOnly?: boolean } = {},
    signal?: AbortSignal,
  ) => {
    const params = new URLSearchParams();
    if (options.cursor) params.set("cursor", options.cursor);
    if (options.limit !== undefined) params.set("limit", String(options.limit));
    if (options.unreadOnly !== undefined)
      params.set("unreadOnly", String(options.unreadOnly));
    return this.#request<AttentionPage>(
      `${IDL_ROUTES.GetAttention.clientPath}${params.size ? `?${params}` : ""}`,
      { signal },
    );
  };
  searchMessages = (
    id: string,
    options: { q?: string; content?: "files"; beforeSeq?: number },
    signal?: AbortSignal,
  ) => {
    const params = new URLSearchParams({ limit: "50" });
    if (options.q) params.set("q", options.q);
    if (options.content) params.set("content", options.content);
    if (options.beforeSeq !== undefined)
      params.set("beforeSeq", String(options.beforeSeq));
    return this.#request<MessagePage>(
      `${routePath(IDL_ROUTES.ListMessages, { id })}?${params}`,
      { signal },
    );
  };
  searchWorkspaceMessages = (
    workspaceId: string,
    options: { q: string; cursor?: string; limit?: number },
    signal?: AbortSignal,
  ) => {
    const params = new URLSearchParams({ q: options.q });
    if (options.cursor) params.set("cursor", options.cursor);
    if (options.limit !== undefined) params.set("limit", String(options.limit));
    return this.#request<WorkspaceSearchPage>(
      `${routePath(IDL_ROUTES.SearchWorkspaceMessages, { id: workspaceId })}?${params}`,
      { signal },
    );
  };
  message = (conversationId: string, id: string, signal?: AbortSignal) =>
    this.#request<{ message: Message }>(
      routePath(IDL_ROUTES.GetMessage, { id: conversationId, messageId: id }),
      { signal },
    );
  messagesAround = (id: string, seq: number) =>
    this.#request<MessagePage>(
      `${routePath(IDL_ROUTES.ListMessages, { id })}?limit=50&aroundSeq=${seq}`,
    );
  send = (id: string, data: Omit<SendMessageReq, "id">) =>
    this.#request<{ message: Message }>(
      routePath(IDL_ROUTES.SendMessage, { id }),
      {
        method: IDL_ROUTES.SendMessage.method,
        body: JSON.stringify(data),
      },
    );
  uploadAttachment = (
    conversationId: string,
    file: File,
    signal?: AbortSignal,
    idempotencyKey?: string,
  ) => {
    const form = new FormData();
    form.append("file", file, file.name);
    return this.#request<AttachmentResp>(
      routePath(IDL_ROUTES.UploadAttachment, { id: conversationId }),
      {
        method: IDL_ROUTES.UploadAttachment.method,
        body: form,
        signal,
        ...(idempotencyKey
          ? { headers: { "Idempotency-Key": idempotencyKey } }
          : {}),
      },
    );
  };
  attachmentUpload = (conversationId: string, uploadKey: string) =>
    this.#request<AttachmentResp>(
      routePath(IDL_ROUTES.GetAttachmentUpload, {
        id: conversationId,
        uploadKey,
      }),
    );
  deletePendingAttachment = (conversationId: string, attachmentId: string) =>
    this.#request<void>(
      routePath(IDL_ROUTES.DeletePendingAttachment, {
        id: conversationId,
        attachmentId,
      }),
      { method: IDL_ROUTES.DeletePendingAttachment.method },
    );
  attachmentContent = (
    conversationId: string,
    attachmentId: string,
    signal?: AbortSignal,
  ) =>
    this.#request<Blob>(
      routePath(IDL_ROUTES.DownloadAttachment, {
        id: conversationId,
        attachmentId,
      }),
      { signal },
      "blob",
    );
  edit = (
    conversationId: string,
    id: string,
    body: string,
    mentions?: Mention[],
  ) =>
    this.#request<{ message: Message }>(
      routePath(IDL_ROUTES.EditMessage, { id: conversationId, messageId: id }),
      {
        method: IDL_ROUTES.EditMessage.method,
        body: JSON.stringify({
          body,
          ...(mentions?.length ? { mentions } : {}),
        }),
      },
    );
  retract = (conversationId: string, id: string) =>
    this.#request<{ message: Message }>(
      routePath(IDL_ROUTES.RetractMessage, {
        id: conversationId,
        messageId: id,
      }),
      { method: IDL_ROUTES.RetractMessage.method },
    );
  read = (id: string, seq: number) =>
    this.#request<MarkReadResp>(routePath(IDL_ROUTES.MarkRead, { id }), {
      method: IDL_ROUTES.MarkRead.method,
      body: JSON.stringify({ seq }),
    });
  putReaction = (conversationId: string, messageId: string, emoji: string) =>
    this.#request<ReactionsResp>(
      routePath(IDL_ROUTES.PutReaction, {
        id: conversationId,
        messageId,
        emoji,
      }),
      {
        method: IDL_ROUTES.PutReaction.method,
      },
    );
  deleteReaction = (conversationId: string, messageId: string, emoji: string) =>
    this.#request<ReactionsResp>(
      routePath(IDL_ROUTES.DeleteReaction, {
        id: conversationId,
        messageId,
        emoji,
      }),
      {
        method: IDL_ROUTES.DeleteReaction.method,
      },
    );
  readReceipts = (conversationId: string, signal?: AbortSignal) =>
    this.#request<ReadReceipts>(
      routePath(IDL_ROUTES.GetReadReceipts, { id: conversationId }),
      { signal },
    );
  setNotificationLevel = (
    conversationId: string,
    level: NotificationLevel,
  ) =>
    this.#request<
      Omit<NotificationLevelResp, "notificationLevel"> & {
        notificationLevel: NotificationLevel;
      }
    >(routePath(IDL_ROUTES.SetNotificationLevel, { id: conversationId }), {
      method: IDL_ROUTES.SetNotificationLevel.method,
      body: JSON.stringify({ level }),
    });
  workspacePresence = (workspaceId: string, signal?: AbortSignal) =>
    this.#request<
      Omit<WorkspacePresenceResp, "items"> & { items: PresenceItem[] }
    >(routePath(IDL_ROUTES.GetWorkspacePresence, { id: workspaceId }), {
      signal,
    });
  #marketplace = createMarketplaceClient(this);
  adminMarketplaceApps = this.#marketplace.adminMarketplaceApps;
  createAdminMarketplaceApp = this.#marketplace.createAdminMarketplaceApp;
  updateAdminMarketplaceApp = this.#marketplace.updateAdminMarketplaceApp;
  rotateAdminMarketplaceAppSecret = this.#marketplace.rotateAdminMarketplaceAppSecret;
  deleteAdminMarketplaceApp = this.#marketplace.deleteAdminMarketplaceApp;
  marketplaceCatalog = this.#marketplace.marketplaceCatalog;
  workspaceApps = this.#marketplace.workspaceApps;
  installWorkspaceApp = this.#marketplace.installWorkspaceApp;
  uninstallWorkspaceApp = this.#marketplace.uninstallWorkspaceApp;
  conversationApps = this.#marketplace.conversationApps;
  createConversationAppSession = this.#marketplace.createConversationAppSession;
  sync = (cursor: string) =>
    this.#request<SyncPage>(
      `${IDL_ROUTES.Sync.clientPath}?cursor=${part(cursor)}&limit=100`,
    );
}
export const api = new TeamieClient();
