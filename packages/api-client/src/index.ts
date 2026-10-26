import {
  IDL_ROUTES,
  type AdminModelConfigResp,
  type AdminUserPageResp,
  type AgentListResp,
  type AgentResp,
  type AttachmentDTO,
  type AttachmentLimitsDTO,
  type AttachmentResp,
  type AttentionItemDTO,
  type AttentionPageResp,
  type BootstrapResp,
  type ChangePasswordReq,
  type ConversationDTO,
  type CreateAgentReq,
  type CreatePasswordResetLinkResp,
  type CreateWorkspaceReq,
  type EventDTO,
  type GetInviteResp,
  type InspectPasswordResetReq,
  type ListInvitesResp,
  type ListMessagesResp,
  type LoginReq,
  type MarkReadResp,
  type MemberDTO,
  type MentionDTO,
  type MessageDTO,
  type ModelConfigDTO,
  type PasswordResetDTO,
  type PasswordResetInspectionResp,
  type PublicAgentDTO,
  type RegisterReq,
  type RegistrationInviteDTO,
  type RegistrationMetadataResp,
  type ResetPasswordReq,
  type SendMessageReq,
  type SessionResp,
  type SyncResp,
  type TestAdminModelConfigResp,
  type UpdateAdminModelConfigReq,
  type UpdateProfileReq,
  type UserDTO,
  type WebSearchConfigDTO,
  type WorkspaceDTO,
  type WorkspaceSearchItemDTO,
  type WorkspaceSearchPageResp,
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
export type Conversation = Omit<
  ConversationDTO,
  "type" | "canManageMembers"
> & {
  type: "channel" | "direct" | "group";
  canManageMembers?: boolean;
};
export type Member = Omit<MemberDTO, "role" | "kind"> & {
  role: "owner" | "member";
  kind: "human" | "agent";
  ownerId?: string;
  ownerName?: string;
};
export type Agent = PublicAgentDTO;
function publicAgent(value: Agent, workspaceId?: string): Agent {
  return {
    id: value.id,
    principalId: value.principalId,
    ownerId: value.ownerId,
    ownerName: value.ownerName,
    name: value.name,
    description: value.description,
    workspaceIds: value.workspaceIds.filter(
      (id) => workspaceId === undefined || id === workspaceId,
    ),
    enabled: value.enabled,
    createdAt: value.createdAt,
    updatedAt: value.updatedAt,
  };
}
export type AgentProfileInput = {
  name: string;
  description?: string;
  workspaceIds: string[];
  enabled?: boolean;
};
function publicAgentProfile(
  value: Partial<AgentProfileInput>,
): Partial<AgentProfileInput> {
  return {
    ...(value.name === undefined ? {} : { name: value.name }),
    ...(value.description === undefined
      ? {}
      : { description: value.description }),
    ...(value.workspaceIds === undefined
      ? {}
      : { workspaceIds: [...value.workspaceIds] }),
    ...(value.enabled === undefined ? {} : { enabled: value.enabled }),
  };
}
export type WebSearchGatewayConfig = WebSearchConfigDTO;
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
export type ModelGatewayInput = Omit<
  UpdateAdminModelConfigReq,
  "clearApiKey" | "clearWebSearchApiKey"
> & {
  clearApiKey?: boolean;
  clearWebSearchApiKey?: boolean;
};
function publicModelGateway(value: ModelGateway): ModelGateway {
  // Project only public metadata even if a server accidentally adds a secret field.
  return {
    config: {
      baseUrl: value.config.baseUrl,
      model: value.config.model,
      hasApiKey: value.config.hasApiKey,
      enabled: value.config.enabled,
      configured: value.config.configured,
      updatedAt: value.config.updatedAt,
      ...(value.config.contextWindowTokens === undefined
        ? {}
        : { contextWindowTokens: value.config.contextWindowTokens }),
      ...(value.config.webSearch === undefined
        ? {}
        : {
            webSearch: {
              provider: value.config.webSearch.provider,
              hasApiKey: value.config.webSearch.hasApiKey,
              enabled: value.config.webSearch.enabled,
              configured: value.config.webSearch.configured,
            },
          }),
    },
    status: value.status,
  };
}
export type Attachment = AttachmentDTO;
export type AttachmentLimits = AttachmentLimitsDTO;
export const defaultAttachmentLimits: AttachmentLimits = {
  maxFileBytes: 4 * 1024 * 1024,
  maxFilesPerMessage: 8,
};
export type Mention = MentionDTO;
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
  "type" | "message" | "conversation"
> & {
  type:
    | "message.created"
    | "message.updated"
    | "message.retracted"
    | "conversation.created"
    | "membership.changed"
    | "read.updated";
  message?: Message;
  conversation?: Conversation;
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
export class ApiError extends Error {
  status: number;
  code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}
const part = encodeURIComponent;
function routePath(
  route: { clientPath: string },
  params?: Record<string, string>,
): string {
  if (!params) return route.clientPath;
  return route.clientPath.replace(/:([a-zA-Z0-9_]+)/g, (_, key: string) =>
    part(params[key] ?? ""),
  );
}
export class TeamieClient {
  csrfToken = "";
  onUnauthorized?: () => void;
  onForbidden?: () => void;
  onSessionChanged?: () => void;
  async #request<T>(
    path: string,
    options: RequestInit = {},
    responseType: "json" | "blob" = "json",
    authentication: "session" | "capability" = "session",
  ): Promise<T> {
    const sessionToken = this.csrfToken;
    const headers = new Headers(options.headers);
    if (options.body && !(options.body instanceof FormData))
      headers.set("Content-Type", "application/json");
    // Bind protected reads to the same browser session as writes. Session
    // discovery deliberately remains unbound so a changed cookie can recover.
    if (
      authentication === "session" &&
      path !== IDL_ROUTES.GetSession.clientPath &&
      sessionToken
    )
      headers.set("X-CSRF-Token", sessionToken);
    const response = await fetch(`/api/v1${path}`, {
      ...options,
      headers,
      credentials: authentication === "session" ? "same-origin" : "omit",
      cache: "no-store",
    });
    if (!response.ok) {
      const data = await response.json().catch(() => ({}));
      const current = authentication === "session" && sessionToken === this.csrfToken;
      const sessionChanged = data.error?.code === "session_changed" ||
        response.status === 403 && ["csrf_failed", "invalid_csrf"].includes(data.error?.code);
      if (current && sessionChanged) this.onSessionChanged?.();
      if (
        authentication === "session" &&
        sessionToken === this.csrfToken &&
        response.status === 401 &&
        !sessionChanged &&
        !path.startsWith(IDL_ROUTES.Login.clientPath) &&
        !path.startsWith(IDL_ROUTES.Register.clientPath)
      )
        this.onUnauthorized?.();
      if (
        authentication === "session" &&
        sessionToken === this.csrfToken &&
        response.status === 403 &&
        /\/(conversations|workspaces|sync)/.test(path) &&
        !sessionChanged
      )
        this.onForbidden?.();
      throw new ApiError(
        response.status,
        data.error?.code ?? "request_failed",
        data.error?.message ?? `请求失败 (${response.status})`,
      );
    }
    const value = response.status === 204 ? undefined : responseType === "blob"
      ? await response.blob() : await response.json();
    if (response.status !== 204 && authentication === "session" && !/^\/auth\/(?:login|register|session)$/.test(path) && sessionToken !== this.csrfToken)
      throw new ApiError(409, "stale_session", "账号已变化，请重试。");
    return value as T;
  }
  session = () => this.#request<Session>(IDL_ROUTES.GetSession.clientPath);
  registration = () =>
    this.#request<RegistrationPolicy>(
      IDL_ROUTES.GetRegistrationMetadata.clientPath,
    );
  registrationInvites = () =>
    this.#request<ListInvitesResp>(IDL_ROUTES.ListInvites.clientPath);
  createRegistrationInvite = () =>
    this.#request<CreatedRegistrationInvite>(
      IDL_ROUTES.CreateInvite.clientPath,
      {
        method: IDL_ROUTES.CreateInvite.method,
        body: JSON.stringify({}),
      },
    );
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
    const result = await this.#request<PlatformUsersPage>(
      `${IDL_ROUTES.ListAdminUsers.clientPath}${params.size ? `?${params}` : ""}`,
    );
    return {
      users: result.users.map((user) => ({
        id: user.id,
        email: user.email,
        displayName: user.displayName,
        kind: user.kind,
        platformRole: user.platformRole,
        createdAt: user.createdAt,
      })),
      ...(result.cursor ? { cursor: result.cursor } : {}),
    };
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
      (
        await this.#request<AgentResp>(
          routePath(IDL_ROUTES.GetAgentProfile, { id }),
        )
      ).agent,
    ),
  });
  createAgent = async (profile: AgentProfileInput) => ({
    agent: publicAgent(
      (
        await this.#request<AgentResp>(IDL_ROUTES.CreateAgent.clientPath, {
          method: IDL_ROUTES.CreateAgent.method,
          body: JSON.stringify(
            publicAgentProfile(profile) satisfies CreateAgentReq,
          ),
        })
      ).agent,
    ),
  });
  updateAgent = async (id: string, profile: Partial<AgentProfileInput>) => ({
    agent: publicAgent(
      (
        await this.#request<AgentResp>(
          routePath(IDL_ROUTES.UpdateAgent, { id }),
          {
            method: IDL_ROUTES.UpdateAgent.method,
            body: JSON.stringify(publicAgentProfile(profile)),
          },
        )
      ).agent,
    ),
  });
  deleteAgent = (id: string) =>
    this.#request<void>(routePath(IDL_ROUTES.DeleteAgent, { id }), {
      method: IDL_ROUTES.DeleteAgent.method,
    });
  workspaceAgents = async (id: string) => ({
    agents: (
      await this.#request<AgentListResp>(
        routePath(IDL_ROUTES.ListWorkspaceAgents, { id }),
      )
    ).agents.map((agent) => publicAgent(agent, id)),
  });
  modelGateway = async () =>
    publicModelGateway(
      await this.#request<ModelGateway>(
        IDL_ROUTES.GetAdminModelConfig.clientPath,
      ),
    );
  saveModelGateway = async (config: ModelGatewayInput) =>
    publicModelGateway(
      await this.#request<ModelGateway>(
        IDL_ROUTES.UpdateAdminModelConfig.clientPath,
        {
          method: IDL_ROUTES.UpdateAdminModelConfig.method,
          body: JSON.stringify(config),
        },
      ),
    );
  testModelGateway = () =>
    this.#request<TestAdminModelConfigResp>(
      IDL_ROUTES.TestAdminModelConfig.clientPath,
      { method: IDL_ROUTES.TestAdminModelConfig.method, body: JSON.stringify({}) },
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
  attachmentContent = (conversationId: string, attachmentId: string) =>
    this.#request<Blob>(
      routePath(IDL_ROUTES.DownloadAttachment, {
        id: conversationId,
        attachmentId,
      }),
      {},
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
  sync = (cursor: string) =>
    this.#request<SyncPage>(
      `${IDL_ROUTES.Sync.clientPath}?cursor=${part(cursor)}&limit=100`,
    );
}
export const api = new TeamieClient();
