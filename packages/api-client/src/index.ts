export type User = {
  id: string;
  email: string;
  displayName: string;
  kind: "human" | "agent";
  platformRole: "admin" | "user";
  createdAt: string;
};
export type Session = { user: User; csrfToken: string };
export type RegistrationPolicy = { mode: "invite"; bootstrapRequired: boolean };
export type RegistrationInvite = {
  id: string;
  createdAt: string;
  expiresAt: string;
  usedAt?: string;
  revokedAt?: string;
  usedBy?: string;
};
export type CreatedRegistrationInvite = {
  invite: Pick<RegistrationInvite, "id" | "createdAt" | "expiresAt"> & {
    code: string;
  };
  url: string;
};
export type PlatformUsersPage = { users: User[]; cursor?: string };
export type PasswordResetLink = {
  id: string;
  createdAt: string;
  expiresAt: string;
};
export type CreatedPasswordResetLink = {
  reset: PasswordResetLink;
  url: string;
};
export type PasswordResetInspection = {
  account: Pick<User, "id" | "displayName"> & { emailMasked: string };
  expiresAt: string;
};
export type Workspace = {
  id: string;
  name: string;
  createdBy: string;
  createdAt: string;
};
export type Conversation = {
  id: string;
  workspaceId: string;
  type: "channel" | "direct" | "group";
  name: string;
  memberIds: string[];
  lastSeq: number;
  readSeq: number;
  updatedAt: string;
  createdBy?: string;
  canManageMembers?: boolean;
};
export type Member = {
  userId: string;
  displayName: string;
  role: "owner" | "member";
  kind: "human" | "agent";
  ownerId?: string;
  ownerName?: string;
};
export type Agent = {
  id: string;
  principalId: string;
  ownerId: string;
  ownerName: string;
  name: string;
  description: string;
  workspaceIds: string[];
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
};
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
export type ModelGatewayConfig = {
  baseUrl: string;
  model: string;
  hasApiKey: boolean;
  enabled: boolean;
  configured: boolean;
  updatedAt: string;
  contextWindowTokens?: number;
};
export type ModelGateway = {
  config: ModelGatewayConfig;
  status: "unconfigured" | "ready" | "disabled";
};
export type ModelGatewayInput = {
  baseUrl: string;
  model: string;
  apiKey?: string;
  enabled: boolean;
  clearApiKey?: boolean;
  contextWindowTokens?: number;
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
    },
    status: value.status,
  };
}
export type Attachment = {
  id: string;
  filename: string;
  mediaType: string;
  sizeBytes: number;
  sha256: string;
};
export type AttachmentLimits = {
  maxFileBytes: number;
  maxFilesPerMessage: number;
};
export const defaultAttachmentLimits: AttachmentLimits = {
  maxFileBytes: 4 * 1024 * 1024,
  maxFilesPerMessage: 8,
};
export type Message = {
  id: string;
  conversationId: string;
  senderId: string;
  senderName: string;
  senderKind: "human" | "agent";
  clientMessageId: string;
  seq: number;
  body: string;
  replyToId?: string;
  createdAt: string;
  editedAt?: string;
  retractedAt?: string;
  attachments?: Attachment[];
};
export type Bootstrap = {
  user: User;
  workspaces: Workspace[];
  conversations: Conversation[];
  capabilities: Record<string, boolean>;
  flagsRevision: string;
  syncCursor: string;
  attachmentLimits?: AttachmentLimits;
};
export type SyncEvent = {
  id: string;
  type:
    | "message.created"
    | "message.updated"
    | "message.retracted"
    | "conversation.created"
    | "membership.changed"
    | "read.updated";
  conversationId?: string;
  workspaceId?: string;
  message?: Message;
  conversation?: Conversation;
  readSeq?: number;
  createdAt: string;
};
export type SyncPage = {
  events: SyncEvent[];
  cursor: string;
  hasMore: boolean;
};
export type MessagePage = { messages: Message[]; hasMore: boolean };
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
    if (authentication === "session" && path !== "/auth/session" && sessionToken)
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
        !path.startsWith("/auth/login") &&
        !path.startsWith("/auth/register")
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
  session = () => this.#request<Session>("/auth/session");
  registration = () => this.#request<RegistrationPolicy>("/auth/registration");
  registrationInvites = () =>
    this.#request<{ invites: RegistrationInvite[] }>("/auth/admin/invites");
  createRegistrationInvite = () =>
    this.#request<CreatedRegistrationInvite>("/auth/admin/invites", {
      method: "POST",
      body: JSON.stringify({}),
    });
  revokeRegistrationInvite = (id: string) =>
    this.#request<void>(`/auth/admin/invites/${part(id)}`, {
      method: "DELETE",
    });
  platformUsers = async (
    options: { cursor?: string; limit?: number; q?: string } = {},
  ): Promise<PlatformUsersPage> => {
    const params = new URLSearchParams();
    if (options.cursor) params.set("cursor", options.cursor);
    if (options.limit !== undefined) params.set("limit", String(options.limit));
    if (options.q) params.set("q", options.q);
    const result = await this.#request<PlatformUsersPage>(
      `/auth/admin/users${params.size ? `?${params}` : ""}`,
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
      `/auth/admin/users/${part(userId)}/password-reset-links`,
      { method: "POST", body: JSON.stringify({}) },
    );
  inspectPasswordReset = (token: string) =>
    this.#request<PasswordResetInspection>(
      "/auth/password-reset/inspect",
      { method: "POST", body: JSON.stringify({ token }) },
      "json",
      "capability",
    );
  resetPassword = (token: string, newPassword: string) =>
    this.#request<void>(
      "/auth/password-reset",
      { method: "POST", body: JSON.stringify({ token, newPassword }) },
      "json",
      "capability",
    );
  agents = async () => ({
    agents: (await this.#request<{ agents: Agent[] }>("/agents")).agents.map(
      (agent) => publicAgent(agent),
    ),
  });
  agent = async (id: string) => ({
    agent: publicAgent(
      (await this.#request<{ agent: Agent }>(`/agents/${part(id)}`)).agent,
    ),
  });
  createAgent = async (profile: AgentProfileInput) => ({
    agent: publicAgent(
      (
        await this.#request<{ agent: Agent }>("/agents", {
          method: "POST",
          body: JSON.stringify(publicAgentProfile(profile)),
        })
      ).agent,
    ),
  });
  updateAgent = async (id: string, profile: Partial<AgentProfileInput>) => ({
    agent: publicAgent(
      (
        await this.#request<{ agent: Agent }>(`/agents/${part(id)}`, {
          method: "PATCH",
          body: JSON.stringify(publicAgentProfile(profile)),
        })
      ).agent,
    ),
  });
  deleteAgent = (id: string) =>
    this.#request<void>(`/agents/${part(id)}`, { method: "DELETE" });
  workspaceAgents = async (id: string) => ({
    agents: (
      await this.#request<{ agents: Agent[] }>(`/workspaces/${part(id)}/agents`)
    ).agents.map((agent) => publicAgent(agent, id)),
  });
  modelGateway = async () =>
    publicModelGateway(
      await this.#request<ModelGateway>("/admin/model-gateway"),
    );
  saveModelGateway = async (config: ModelGatewayInput) =>
    publicModelGateway(
      await this.#request<ModelGateway>("/admin/model-gateway", {
        method: "PUT",
        body: JSON.stringify(config),
      }),
    );
  testModelGateway = () =>
    this.#request<{ ok: boolean; model: string; latencyMs: number }>(
      "/admin/model-gateway/test",
      { method: "POST", body: JSON.stringify({}) },
    );
  login = (email: string, password: string) =>
    this.#request<Session>("/auth/login", {
      method: "POST",
      body: JSON.stringify({ email, password }),
    });
  register = (data: {
    email: string;
    password: string;
    displayName: string;
    inviteCode: string;
  }) =>
    this.#request<Session>("/auth/register", {
      method: "POST",
      body: JSON.stringify(data),
    });
  logout = () => this.#request<void>("/auth/logout", { method: "POST" });
  changePassword = (currentPassword: string, newPassword: string) =>
    this.#request<void>("/auth/password", {
      method: "POST",
      body: JSON.stringify({ currentPassword, newPassword }),
    });
  profile = (displayName: string) =>
    this.#request<{ user: User }>("/auth/profile", {
      method: "PATCH",
      body: JSON.stringify({ displayName }),
    });
  bootstrap = () => this.#request<Bootstrap>("/bootstrap");
  createWorkspace = (name: string) =>
    this.#request<{ workspace: Workspace; conversation: Conversation }>(
      "/workspaces",
      { method: "POST", body: JSON.stringify({ name }) },
    );
  members = (id: string) =>
    this.#request<{ members: Member[] }>(`/workspaces/${part(id)}/members`);
  createInvite = (id: string) =>
    this.#request<{ code: string; expiresAt: string }>(
      `/workspaces/${part(id)}/invites`,
      { method: "POST" },
    );
  invite = (code: string) =>
    this.#request<{
      workspace: Pick<Workspace, "id" | "name">;
      expiresAt: string;
    }>(`/invites/${part(code)}`);
  acceptInvite = (code: string) =>
    this.#request<{ workspace: Workspace; conversations: Conversation[] }>(
      `/invites/${part(code)}/accept`,
      { method: "POST" },
    );
  removeMember = (workspaceId: string, userId: string) =>
    this.#request<void>(
      `/workspaces/${part(workspaceId)}/members/${part(userId)}`,
      { method: "DELETE" },
    );
  conversations = (id: string) =>
    this.#request<{ conversations: Conversation[] }>(
      `/workspaces/${part(id)}/conversations`,
    );
  addConversationMember = (id: string, userId: string) =>
    this.#request<{ conversation: Conversation }>(
      `/conversations/${part(id)}/members`,
      { method: "POST", body: JSON.stringify({ userId }) },
    );
  removeConversationMember = (id: string, userId: string) =>
    this.#request<{ conversation: Conversation }>(
      `/conversations/${part(id)}/members/${part(userId)}`,
      { method: "DELETE" },
    );
  createConversation = (
    id: string,
    data: { type: Conversation["type"]; name?: string; memberIds?: string[] },
  ) =>
    this.#request<{ conversation: Conversation }>(
      `/workspaces/${part(id)}/conversations`,
      { method: "POST", body: JSON.stringify(data) },
    );
  messages = (id: string, beforeSeq?: number) =>
    this.#request<MessagePage>(
      `/conversations/${part(id)}/messages?limit=50${beforeSeq ? `&beforeSeq=${beforeSeq}` : ""}`,
    );
  send = (
    id: string,
    data: {
      clientMessageId: string;
      body: string;
      replyToId?: string;
      attachmentIds?: string[];
    },
  ) =>
    this.#request<{ message: Message }>(`/conversations/${part(id)}/messages`, {
      method: "POST",
      body: JSON.stringify(data),
    });
  uploadAttachment = (
    conversationId: string,
    file: File,
    signal?: AbortSignal,
  ) => {
    const form = new FormData();
    form.append("file", file, file.name);
    return this.#request<{ attachment: Attachment }>(
      `/conversations/${part(conversationId)}/attachments`,
      { method: "POST", body: form, signal },
    );
  };
  deletePendingAttachment = (conversationId: string, attachmentId: string) =>
    this.#request<void>(
      `/conversations/${part(conversationId)}/attachments/${part(attachmentId)}`,
      { method: "DELETE" },
    );
  attachmentContent = (conversationId: string, attachmentId: string) =>
    this.#request<Blob>(
      `/conversations/${part(conversationId)}/attachments/${part(attachmentId)}/content`,
      {},
      "blob",
    );
  edit = (conversationId: string, id: string, body: string) =>
    this.#request<{ message: Message }>(
      `/conversations/${part(conversationId)}/messages/${part(id)}`,
      { method: "PATCH", body: JSON.stringify({ body }) },
    );
  retract = (conversationId: string, id: string) =>
    this.#request<{ message: Message }>(
      `/conversations/${part(conversationId)}/messages/${part(id)}`,
      { method: "DELETE" },
    );
  read = (id: string, seq: number) =>
    this.#request<{ readSeq: number }>(`/conversations/${part(id)}/read`, {
      method: "POST",
      body: JSON.stringify({ seq }),
    });
  sync = (cursor: string) =>
    this.#request<SyncPage>(`/sync?cursor=${part(cursor)}&limit=100`);
}
export const api = new TeamieClient();
