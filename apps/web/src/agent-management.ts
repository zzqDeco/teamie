import type {
  Agent,
  AgentProfileInput,
  ModelGatewayInput,
  User,
  Workspace,
} from "@teamie/api-client";
import type { GatewayFormValue } from "./agent-ui";
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
): AgentProfileInput {
  const permitted = new Set(memberships.map((workspace) => workspace.id));
  const selected = [...new Set(workspaceIds)];
  if (selected.length > 100) throw new Error("最多选择 100 个可见工作空间。");
  if (workspaceIds.some((id) => !permitted.has(id)))
    throw new Error("只能选择你当前已加入的工作空间，请刷新后重新选择。");
  const trimmed = name.trim();
  if (!trimmed || Array.from(trimmed).length > 80)
    throw new Error("Agent 名称需要 1 至 80 个字符。");
  return {
    name: trimmed,
    description: description.trim(),
    workspaceIds: selected,
    enabled,
  };
}
export function snapshotAgentProfile(agent: Agent) {
  return Object.freeze({
    id: agent.id,
    name: agent.name,
    description: agent.description,
    enabled: agent.enabled,
    workspaceIds: Object.freeze([...agent.workspaceIds]),
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
  const status =
    error && typeof error === "object" && "status" in error ? error.status : 0;
  if (status === 401) return "登录已失效，请重新登录。";
  if (status === 403) return "你当前没有管理这个 Agent 的权限。";
  if (status === 404) return "这个 Agent 暂时无法管理，请返回列表重试。";
  if (status === 400) return "资料未通过校验，请检查名称和可见团队。";
  if (status === 409) return "资料已发生变化，请刷新后重试。";
  return "操作未完成，请检查资料或稍后重试。";
}
export function modelGatewayInput(value: GatewayFormValue): ModelGatewayInput {
  const key = value.apiKey.trim();
  const baseUrl = value.baseUrl.trim();
  const url = new URL(baseUrl);
  if (
    !["http:", "https:"].includes(url.protocol) ||
    url.username ||
    url.password ||
    url.search ||
    url.hash
  )
    throw new Error("服务地址不得包含密钥、认证信息或查询参数。");
  if (value.clearApiKey && key)
    throw new Error("清除密钥和填写新密钥不能同时进行。");
  if (
    value.contextWindowTokens !== undefined &&
    (!Number.isInteger(value.contextWindowTokens) ||
      value.contextWindowTokens < 4096 ||
      value.contextWindowTokens > 2000000)
  )
    throw new Error("上下文窗口需为 4096 至 2000000 的整数。");
  return {
    baseUrl,
    model: value.model.trim(),
    enabled: value.enabled,
    ...(value.contextWindowTokens === undefined
      ? {}
      : { contextWindowTokens: value.contextWindowTokens }),
    ...(key ? { apiKey: key } : {}),
    ...(value.clearApiKey ? { clearApiKey: true } : {}),
  };
}
export function gatewayFailureText(error: unknown): string {
  const status =
    error && typeof error === "object" && "status" in error ? error.status : 0;
  if (status === 403) return "需要平台管理员权限才能配置模型网关。";
  if (status === 401) return "登录已失效，请重新登录。";
  if (status === 400) return "配置未通过校验，请检查服务地址、模型名称与密钥。";
  return "模型网关请求未完成，请检查配置或稍后重试。";
}
