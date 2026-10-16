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
};
export type Member = {
  userId: string;
  displayName: string;
  role: "owner" | "member";
  kind: "human" | "agent";
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
