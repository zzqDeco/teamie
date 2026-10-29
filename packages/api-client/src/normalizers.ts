import type {
  AdminModelProfile,
  AdminModelProvider,
  Agent,
  AgentProfileInput,
  Conversation,
  Message,
  ModelCatalog,
  ModelGateway,
  PlatformUsersPage,
  User,
  Workspace,
} from "./types";

export function normalizeUser(user: User): User {
  return {
    id: user.id,
    email: user.email,
    displayName: user.displayName,
    kind: user.kind,
    platformRole: user.platformRole,
    createdAt: user.createdAt,
  };
}

export function normalizeWorkspace(workspace: Workspace): Workspace {
  return { ...workspace };
}

export function normalizeConversation(
  conversation: Conversation,
): Conversation {
  return { ...conversation };
}

export function normalizeMessage(message: Message): Message {
  return { ...message };
}

export function normalizePlatformUsersPage(
  result: PlatformUsersPage,
): PlatformUsersPage {
  return {
    users: result.users.map(normalizeUser),
    ...(result.cursor ? { cursor: result.cursor } : {}),
  };
}

export function publicAgent(value: Agent, workspaceId?: string): Agent {
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
    ...(value.modelId ? { modelId: value.modelId } : {}),
  };
}
export const normalizeAgent = publicAgent;

export function publicAgentProfile(
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
    ...(value.modelId === undefined ? {} : { modelId: value.modelId }),
  };
}

export function publicAdminModelProvider(
  provider: AdminModelProvider,
): AdminModelProvider {
  return {
    id: provider.id,
    displayName: provider.displayName,
    baseUrl: provider.baseUrl,
    hasApiKey: provider.hasApiKey,
    enabled: provider.enabled,
    configured: provider.configured,
    modelCount: provider.modelCount,
    updatedAt: provider.updatedAt,
  };
}

export function publicAdminModelProfile(
  model: AdminModelProfile,
): AdminModelProfile {
  return {
    id: model.id,
    providerId: model.providerId,
    providerDisplayName: model.providerDisplayName,
    providerEnabled: model.providerEnabled,
    displayName: model.displayName,
    description: model.description,
    baseUrl: model.baseUrl,
    model: model.model,
    contextWindowTokens: model.contextWindowTokens,
    hasApiKey: model.hasApiKey,
    enabled: model.enabled,
    effectiveEnabled: model.effectiveEnabled,
    configured: model.configured,
    isDefault: model.isDefault,
    updatedAt: model.updatedAt,
  };
}

export function publicModelCatalog(value: ModelCatalog): ModelCatalog {
  return {
    models: (value.models || []).map((item) => ({
      id: item.id,
      displayName: item.displayName,
      description: item.description,
      contextWindowTokens: item.contextWindowTokens,
      isDefault: item.isDefault,
    })),
    ...(value.defaultModelId ? { defaultModelId: value.defaultModelId } : {}),
  };
}

export function publicModelGateway(value: ModelGateway): ModelGateway {
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
    routing: {
      defaultModelId: value.routing.defaultModelId,
      effectiveModelId: value.routing.effectiveModelId,
    },
    webSearch: {
      provider: value.webSearch.provider,
      hasApiKey: value.webSearch.hasApiKey,
      enabled: value.webSearch.enabled,
      configured: value.webSearch.configured,
      ...(value.webSearch.updatedAt
        ? { updatedAt: value.webSearch.updatedAt }
        : {}),
    },
    status: value.status,
    ...(value.providers === undefined
      ? {}
      : { providers: value.providers.map(publicAdminModelProvider) }),
    ...(value.models === undefined
      ? {}
      : { models: value.models.map(publicAdminModelProfile) }),
    ...(value.defaultModelId === undefined
      ? {}
      : { defaultModelId: value.defaultModelId }),
  };
}
export const normalizeModelGateway = publicModelGateway;
