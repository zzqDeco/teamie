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
export type AgentRuntimeState =
  | "waiting_configuration"
  | "idle"
  | "running"
  | "disabled"
  | "error"
  | "provisioning";
export type AgentRun = {
  id: string;
  status: string;
  inputCount: number;
  createdAt: string;
  updatedAt: string;
  startedAt?: string;
  finishedAt?: string;
  errorCode?: string;
};
export type AgentTask = {
  id: string;
  runId?: string;
  kind: string;
  description: string;
  status: string;
  createdAt: string;
  updatedAt: string;
  finishedAt?: string;
  exitCode?: number;
};
export type AgentRuntime = {
  state: string;
  sessionId: string;
  activeRunId?: string;
  pendingInputs: number;
  runningTasks: number;
  lastActiveAt?: string;
  lastError?: string;
};
export type AgentRuntimeEvent = {
  id: string;
  type: string;
  runId?: string;
  taskId?: string;
  toolName?: string;
  status?: string;
  createdAt: string;
  errorCode?: string;
};
export type AgentEventPage = {
  events: AgentRuntimeEvent[];
  cursor: string;
  hasMore: boolean;
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
  revision: number;
  createdAt: string;
  updatedAt: string;
  deletedAt?: string;
  runtimeState: AgentRuntimeState;
  lastActiveAt?: string;
  lastError?: string;
  sessionId: string;
};
export type AgentProfileInput = {
  name: string;
  description?: string;
  workspaceIds: string[];
  enabled?: boolean;
};
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
};
export type Bootstrap = {
  user: User;
  workspaces: Workspace[];
  conversations: Conversation[];
  capabilities: Record<string, boolean>;
  flagsRevision: string;
  syncCursor: string;
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
  async request<T>(path: string, options: RequestInit = {}): Promise<T> {
    const method = options.method ?? "GET";
    const sessionToken = this.csrfToken;
    const headers = new Headers(options.headers);
    if (options.body) headers.set("Content-Type", "application/json");
    if (method !== "GET" && this.csrfToken)
      headers.set("X-CSRF-Token", this.csrfToken);
    const response = await fetch(`/api/v1${path}`, {
      ...options,
      headers,
      credentials: "same-origin",
      cache: "no-store",
    });
    if (!response.ok) {
      const data = await response.json().catch(() => ({}));
      if (
        sessionToken === this.csrfToken &&
        response.status === 401 &&
        !path.startsWith("/auth/login") &&
        !path.startsWith("/auth/register")
      )
        this.onUnauthorized?.();
      if (
        sessionToken === this.csrfToken &&
        response.status === 403 &&
        /\/(conversations|workspaces|sync)/.test(path)
      )
        this.onForbidden?.();
      throw new ApiError(
        response.status,
        data.error?.code ?? "request_failed",
        data.error?.message ?? `请求失败 (${response.status})`,
      );
    }
    if (response.status === 204) return undefined as T;
    return response.json() as Promise<T>;
  }
  session = () => this.request<Session>("/auth/session");
  registration = () => this.request<RegistrationPolicy>("/auth/registration");
  registrationInvites = () =>
    this.request<{ invites: RegistrationInvite[] }>("/auth/admin/invites");
  createRegistrationInvite = () =>
    this.request<CreatedRegistrationInvite>("/auth/admin/invites", {
      method: "POST",
      body: JSON.stringify({}),
    });
  revokeRegistrationInvite = (id: string) =>
    this.request<void>(`/auth/admin/invites/${part(id)}`, { method: "DELETE" });
  agents = () => this.request<{ agents: Agent[] }>("/agents");
  agent = (id: string) => this.request<{ agent: Agent }>(`/agents/${part(id)}`);
  createAgent = (profile: AgentProfileInput) =>
    this.request<{ agent: Agent }>("/agents", {
      method: "POST",
      body: JSON.stringify(profile),
    });
  updateAgent = (id: string, profile: Partial<AgentProfileInput>) =>
    this.request<{ agent: Agent }>(`/agents/${part(id)}`, {
      method: "PATCH",
      body: JSON.stringify(profile),
    });
  deleteAgent = (id: string) =>
    this.request<void>(`/agents/${part(id)}`, { method: "DELETE" });
  workspaceAgents = (id: string) =>
    this.request<{ agents: Agent[] }>(`/workspaces/${part(id)}/agents`);
  agentRuntime = (id: string) =>
    this.request<AgentRuntime>(`/agents/${part(id)}/runtime`);
  agentRuns = (id: string, cursor?: string) =>
    this.request<{ runs: AgentRun[]; cursor?: string }>(
      `/agents/${part(id)}/runs?limit=30${cursor ? `&cursor=${part(cursor)}` : ""}`,
    );
  agentRun = (id: string, runId: string) =>
    this.request<{ run: AgentRun }>(`/agents/${part(id)}/runs/${part(runId)}`);
  agentTasks = (id: string, cursor?: string) =>
    this.request<{ tasks: AgentTask[]; cursor?: string }>(
      `/agents/${part(id)}/tasks?limit=30${cursor ? `&cursor=${part(cursor)}` : ""}`,
    );
  agentTask = (id: string, taskId: string) =>
    this.request<{ task: AgentTask }>(`/agents/${part(id)}/tasks/${part(taskId)}`);
  agentEvents = (id: string, cursor = "0") =>
    this.request<AgentEventPage>(`/agents/${part(id)}/events?cursor=${part(cursor)}&limit=100`);
  agentEventsURL = (id: string, cursor = "0") =>
    `/api/v1/agents/${part(id)}/events/stream?cursor=${part(cursor)}`;
  cancelAgentRun = (id: string, runId: string) =>
    this.request<{ ok: boolean }>(`/agents/${part(id)}/runs/${part(runId)}/cancel`, {
      method: "POST", body: JSON.stringify({}),
    });
  cancelAgentTask = (id: string, taskId: string) =>
    this.request<{ ok: boolean }>(`/agents/${part(id)}/tasks/${part(taskId)}/cancel`, {
      method: "POST", body: JSON.stringify({}),
    });
  retryAgentRun = (id: string, runId: string) =>
    this.request<{ ok: boolean }>(`/agents/${part(id)}/runs/${part(runId)}/retry`, {
      method: "POST", body: JSON.stringify({}),
    });
  modelGateway = async () =>
    publicModelGateway(
      await this.request<ModelGateway>("/admin/model-gateway"),
    );
  saveModelGateway = async (config: ModelGatewayInput) =>
    publicModelGateway(
      await this.request<ModelGateway>("/admin/model-gateway", {
        method: "PUT",
        body: JSON.stringify(config),
      }),
    );
  testModelGateway = () =>
    this.request<{ ok: boolean; model: string; latencyMs: number }>(
      "/admin/model-gateway/test",
      { method: "POST", body: JSON.stringify({}) },
    );
  login = (email: string, password: string) =>
    this.request<Session>("/auth/login", {
      method: "POST",
      body: JSON.stringify({ email, password }),
    });
  register = (data: {
    email: string;
    password: string;
    displayName: string;
    inviteCode: string;
  }) =>
    this.request<Session>("/auth/register", {
      method: "POST",
      body: JSON.stringify(data),
    });
  logout = () => this.request<void>("/auth/logout", { method: "POST" });
  profile = (displayName: string) =>
    this.request<{ user: User }>("/auth/profile", {
      method: "PATCH",
      body: JSON.stringify({ displayName }),
    });
  bootstrap = () => this.request<Bootstrap>("/bootstrap");
  createWorkspace = (name: string) =>
    this.request<{ workspace: Workspace; conversation: Conversation }>(
      "/workspaces",
      { method: "POST", body: JSON.stringify({ name }) },
    );
  members = (id: string) =>
    this.request<{ members: Member[] }>(`/workspaces/${part(id)}/members`);
  createInvite = (id: string) =>
    this.request<{ code: string; expiresAt: string }>(
      `/workspaces/${part(id)}/invites`,
      { method: "POST" },
    );
  invite = (code: string) =>
    this.request<{
      workspace: Pick<Workspace, "id" | "name">;
      expiresAt: string;
    }>(`/invites/${part(code)}`);
  acceptInvite = (code: string) =>
    this.request<{ workspace: Workspace; conversations: Conversation[] }>(
      `/invites/${part(code)}/accept`,
      { method: "POST" },
    );
  removeMember = (workspaceId: string, userId: string) =>
    this.request<void>(
      `/workspaces/${part(workspaceId)}/members/${part(userId)}`,
      { method: "DELETE" },
    );
  conversations = (id: string) =>
    this.request<{ conversations: Conversation[] }>(
      `/workspaces/${part(id)}/conversations`,
    );
  addConversationMember = (id: string, userId: string) =>
    this.request<{ conversation: Conversation }>(
      `/conversations/${part(id)}/members`,
      { method: "POST", body: JSON.stringify({ userId }) },
    );
  removeConversationMember = (id: string, userId: string) =>
    this.request<{ conversation: Conversation }>(
      `/conversations/${part(id)}/members/${part(userId)}`,
      { method: "DELETE" },
    );
  createConversation = (
    id: string,
    data: { type: Conversation["type"]; name?: string; memberIds?: string[] },
  ) =>
    this.request<{ conversation: Conversation }>(
      `/workspaces/${part(id)}/conversations`,
      { method: "POST", body: JSON.stringify(data) },
    );
  messages = (id: string, beforeSeq?: number) =>
    this.request<MessagePage>(
      `/conversations/${part(id)}/messages?limit=50${beforeSeq ? `&beforeSeq=${beforeSeq}` : ""}`,
    );
  send = (
    id: string,
    data: { clientMessageId: string; body: string; replyToId?: string },
  ) =>
    this.request<{ message: Message }>(`/conversations/${part(id)}/messages`, {
      method: "POST",
      body: JSON.stringify(data),
    });
  edit = (conversationId: string, id: string, body: string) =>
    this.request<{ message: Message }>(
      `/conversations/${part(conversationId)}/messages/${part(id)}`,
      { method: "PATCH", body: JSON.stringify({ body }) },
    );
  retract = (conversationId: string, id: string) =>
    this.request<{ message: Message }>(
      `/conversations/${part(conversationId)}/messages/${part(id)}`,
      { method: "DELETE" },
    );
  read = (id: string, seq: number) =>
    this.request<{ readSeq: number }>(`/conversations/${part(id)}/read`, {
      method: "POST",
      body: JSON.stringify({ seq }),
    });
  sync = (cursor: string) =>
    this.request<SyncPage>(`/sync?cursor=${part(cursor)}&limit=100`);
}
export const api = new TeamieClient();
