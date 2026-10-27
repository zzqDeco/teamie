import type {
  Agent,
  AgentProfileInput,
  CreateModelProfileInput,
  ModelGatewayInput,
  UpdateModelProfileInput,
  User,
  Workspace,
} from "@teamie/api-client";
import type { GatewayFormValue } from "./agent-ui";
const MODEL_ID_PATTERN = /^[a-z0-9][a-z0-9._-]{0,63}$/;
/**
 * Client-side validation failure whose message is safe, user-facing Chinese
 * text. Failure text helpers show it verbatim; any other error (API, network,
 * runtime) keeps its generic copy so private details are never echoed.
 */
export class AgentInputError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "AgentInputError";
  }
}
export function canManageAgent(
  user: Pick<User, "id" | "kind"> | null,
  agent: Pick<Agent, "ownerId">,
): boolean {
  return !!user && user.kind === "human" && user.id === agent.ownerId;
}
export function canConfigureModelGateway(
  user: Pick<User, "platformRole" | "kind"> | null,
): boolean {
  return user?.kind === "human" && user.platformRole === "admin";
}
export function agentProfileInput(
  name: string,
  description: string,
  workspaceIds: string[],
  memberships: Pick<Workspace, "id">[],
  enabled: boolean,
  modelId?: string,
): AgentProfileInput {
  const permitted = new Set(memberships.map((workspace) => workspace.id));
  const selected = [...new Set(workspaceIds)];
  if (selected.length > 100)
    throw new AgentInputError("最多选择 100 个可见工作空间。");
  if (workspaceIds.some((id) => !permitted.has(id)))
    throw new AgentInputError(
      "只能选择你当前已加入的工作空间，请刷新后重新选择。",
    );
  const trimmed = name.trim();
  if (!trimmed || Array.from(trimmed).length > 80)
    throw new AgentInputError("Agent 名称需要 1 至 80 个字符。");
  const trimmedModelId = modelId?.trim();
  if (trimmedModelId && !MODEL_ID_PATTERN.test(trimmedModelId))
    throw new AgentInputError("所选模型标识格式不正确，请重新选择。");
  return {
    name: trimmed,
    description: description.trim(),
    workspaceIds: selected,
    enabled,
    ...(trimmedModelId === undefined ? {} : { modelId: trimmedModelId }),
  };
}
export function snapshotAgentProfile(agent: Agent) {
  return Object.freeze({
    id: agent.id,
    name: agent.name,
    description: agent.description,
    enabled: agent.enabled,
    workspaceIds: Object.freeze([...agent.workspaceIds]),
    ...(agent.modelId ? { modelId: agent.modelId } : {}),
  });
}
export function agentProfileChanges(
  initial: ReturnType<typeof snapshotAgentProfile>,
  profile: AgentProfileInput,
): Partial<AgentProfileInput> {
  const changes: Partial<AgentProfileInput> = {};
  if (profile.name !== initial.name) changes.name = profile.name;
  if (
    profile.description !== undefined &&
    profile.description !== initial.description
  )
    changes.description = profile.description;
  if (profile.enabled !== undefined && profile.enabled !== initial.enabled)
    changes.enabled = profile.enabled;
  if (
    profile.modelId !== undefined &&
    profile.modelId !== (initial.modelId ?? "")
  )
    changes.modelId = profile.modelId;
  const previous = new Set(initial.workspaceIds);
  const selected = new Set(profile.workspaceIds);
  if (
    previous.size !== selected.size ||
    [...selected].some((id) => !previous.has(id))
  )
    changes.workspaceIds = [...selected];
  return changes;
}
export function agentFailureText(error: unknown): string {
  if (error instanceof AgentInputError) return error.message;
  const status =
    error && typeof error === "object" && "status" in error ? error.status : 0;
  if (status === 401) return "登录已失效，请重新登录。";
  if (status === 403) return "你当前没有管理这个 Agent 的权限。";
  if (status === 404) return "这个 Agent 暂时无法管理，请返回列表重试。";
  if (status === 400) return "资料未通过校验，请检查名称和可见团队。";
  if (status === 409) return "资料已发生变化，请刷新后重试。";
  return "操作未完成，请检查资料或稍后重试。";
}
function validateProviderBaseUrl(rawUrl: string): string {
  const baseUrl = rawUrl.trim();
  let url: URL;
  try {
    url = new URL(baseUrl);
  } catch {
    throw new AgentInputError(
      "服务地址格式不正确，请填写完整的 http(s) 地址。",
    );
  }
  if (
    !["http:", "https:"].includes(url.protocol) ||
    url.username ||
    url.password ||
    url.search ||
    url.hash
  )
    throw new AgentInputError("服务地址不得包含密钥、认证信息或查询参数。");
  return baseUrl;
}
function validateContextWindowTokens(tokens?: number) {
  if (
    tokens !== undefined &&
    (!Number.isInteger(tokens) || tokens < 4096 || tokens > 2000000)
  )
    throw new AgentInputError("上下文窗口需为 4096 至 2000000 的整数。");
}
export function modelGatewayInput(value: GatewayFormValue): ModelGatewayInput {
  const key = value.apiKey.trim();
  const webKey = (value.webSearchApiKey ?? "").trim();
  const baseUrl = validateProviderBaseUrl(value.baseUrl);
  if (value.clearApiKey && key)
    throw new AgentInputError("清除密钥和填写新密钥不能同时进行。");
  if (value.clearWebSearchApiKey && webKey)
    throw new AgentInputError("清除联网搜索密钥和填写新密钥不能同时进行。");
  validateContextWindowTokens(value.contextWindowTokens);
  return {
    baseUrl,
    model: value.model.trim(),
    enabled: value.enabled,
    ...(value.contextWindowTokens === undefined
      ? {}
      : { contextWindowTokens: value.contextWindowTokens }),
    ...(key ? { apiKey: key } : {}),
    ...(value.clearApiKey ? { clearApiKey: true } : {}),
    ...(value.webSearchEnabled === undefined
      ? {}
      : { webSearchEnabled: value.webSearchEnabled }),
    ...(webKey ? { webSearchApiKey: webKey } : {}),
    ...(value.clearWebSearchApiKey ? { clearWebSearchApiKey: true } : {}),
  };
}
export type ModelProfileFormValue = {
  id?: string;
  displayName: string;
  description: string;
  baseUrl: string;
  model: string;
  apiKey: string;
  clearApiKey?: boolean;
  enabled: boolean;
  contextWindowTokens?: number;
  isDefault?: boolean;
};
export function modelProfileCreateInput(
  value: ModelProfileFormValue,
): CreateModelProfileInput {
  const id = (value.id ?? "").trim();
  if (id && !MODEL_ID_PATTERN.test(id))
    throw new AgentInputError(
      "模型标识需由小写字母、数字、点、下划线或连字符组成（1-64 位）。",
    );
  const baseUrl = validateProviderBaseUrl(value.baseUrl);
  const model = value.model.trim();
  if (!model) throw new AgentInputError("请填写模型名称。");
  const key = value.apiKey.trim();
  validateContextWindowTokens(value.contextWindowTokens);
  const displayName = value.displayName.trim() || model;
  const description = value.description.trim();
  return {
    ...(id ? { id } : {}),
    displayName,
    ...(description ? { description } : {}),
    baseUrl,
    model,
    ...(key ? { apiKey: key } : {}),
    enabled: value.enabled,
    ...(value.contextWindowTokens === undefined
      ? {}
      : { contextWindowTokens: value.contextWindowTokens }),
    ...(value.isDefault === undefined ? {} : { isDefault: value.isDefault }),
  };
}
export function modelProfileUpdateInput(
  value: ModelProfileFormValue,
): UpdateModelProfileInput {
  const baseUrl = validateProviderBaseUrl(value.baseUrl);
  const model = value.model.trim();
  if (!model) throw new AgentInputError("请填写模型名称。");
  const key = value.apiKey.trim();
  if (value.clearApiKey && key)
    throw new AgentInputError("清除密钥和填写新密钥不能同时进行。");
  validateContextWindowTokens(value.contextWindowTokens);
  return {
    displayName: value.displayName.trim() || model,
    description: value.description.trim(),
    baseUrl,
    model,
    ...(key ? { apiKey: key } : {}),
    ...(value.clearApiKey ? { clearApiKey: true } : {}),
    enabled: value.enabled,
    ...(value.contextWindowTokens === undefined
      ? {}
      : { contextWindowTokens: value.contextWindowTokens }),
    ...(value.isDefault === undefined ? {} : { isDefault: value.isDefault }),
  };
}
export function gatewayFailureText(error: unknown): string {
  if (error instanceof AgentInputError) return error.message;
  const status =
    error && typeof error === "object" && "status" in error ? error.status : 0;
  if (status === 403) return "需要平台管理员权限才能配置模型网关。";
  if (status === 401) return "登录已失效，请重新登录。";
  if (status === 400) return "配置未通过校验，请检查服务地址、模型名称与密钥。";
  if (status === 409) return "模型标识已存在，请更换其他标识后重试。";
  if (status === 404) return "目标模型不存在或已被删除，请刷新后重试。";
  return "模型网关请求未完成，请检查配置或稍后重试。";
}
