import { useState } from "react";
import type { FormEvent } from "react";
import {
  Bot,
  Check,
  CirclePause,
  Clock3,
  Eye,
  KeyRound,
  LoaderCircle,
  LockKeyhole,
  Play,
  ShieldCheck,
  Sparkles,
} from "lucide-react";
import type { Workspace } from "@teamie/api-client";
import { Button, Notice } from "./ui";

export function AgentBadge({
  ownerName,
  compact = false,
}: {
  ownerName?: string;
  compact?: boolean;
}) {
  return (
    <span
      className={`agent-badge ${compact ? "compact" : ""}`}
      title={ownerName ? `Agent · 由 ${ownerName} 创建` : "Agent"}
    >
      <Bot size={compact ? 10 : 12} />
      <span>Agent</span>
      {ownerName && !compact && <small>由 {ownerName} 创建</small>}
    </span>
  );
}
export function AgentAvatar({
  size = "",
  disabled = false,
}: {
  size?: "small" | "large" | "";
  disabled?: boolean;
}) {
  return (
    <span
      className={`avatar agent-avatar ${size} ${disabled ? "disabled" : ""}`}
      aria-hidden="true"
    >
      <Bot size={size === "large" ? 31 : size === "small" ? 15 : 20} />
      <i />
    </span>
  );
}
export function WorkspaceVisibilityFields({
  workspaces,
  selectedIds,
  onChange,
  disabled = false,
}: {
  workspaces: Workspace[];
  selectedIds: string[];
  onChange: (ids: string[]) => void;
  disabled?: boolean;
}) {
  return (
    <fieldset className="agent-visibility" disabled={disabled}>
      <legend>
        <Eye size={15} />
        可见工作空间
      </legend>
      <p>
        选择你已加入的工作空间。空间内的伙伴可发现这个
        Agent，并邀请它参与私聊或小组对话。
      </p>
      {workspaces.length ? (
        <div className="visibility-options">
          {workspaces.map((workspace) => (
            <label
              key={workspace.id}
              className={`visibility-option ${selectedIds.includes(workspace.id) ? "selected" : ""}`}
            >
              <input
                type="checkbox"
                checked={selectedIds.includes(workspace.id)}
                onChange={(event) =>
                  onChange(
                    event.target.checked
                      ? [
                          ...selectedIds.filter((id) => id !== workspace.id),
                          workspace.id,
                        ]
                      : selectedIds.filter((id) => id !== workspace.id),
                  )
                }
              />
              <span className="visibility-space-icon">
                {Array.from(workspace.name).slice(0, 1).join("")}
              </span>
              <span>{workspace.name}</span>
              <i>{selectedIds.includes(workspace.id) && <Check size={12} />}</i>
            </label>
          ))}
        </div>
      ) : (
        <div className="visibility-empty">
          你还没有加入工作空间。先创建或加入一个空间，再设置 Agent 的可见范围。
        </div>
      )}
      <small>
        <LockKeyhole size={12} />
        选中团队后，Agent
        按普通成员权限访问空间频道；私聊和小组仍需加入具体对话。
      </small>
    </fieldset>
  );
}
export type AgentDisplayStatus =
  | "waiting_configuration"
  | "idle"
  | "running"
  | "disabled"
  | "paused"
  | "failed"
  | "pending";
export const agentStatusLabels: Record<AgentDisplayStatus, string> = {
  waiting_configuration: "等待管理员配置",
  idle: "等待新消息",
  running: "正在处理",
  disabled: "已停用",
  paused: "已暂停",
  failed: "处理失败",
  pending: "等待处理",
};
export function AgentStatus({ status }: { status: AgentDisplayStatus }) {
  const Icon =
    status === "running"
      ? LoaderCircle
      : status === "waiting_configuration"
        ? KeyRound
        : status === "disabled" || status === "paused"
          ? CirclePause
          : status === "idle"
            ? Clock3
            : Play;
  return (
    <span className={`agent-status ${status}`}>
      <Icon size={12} className={status === "running" ? "spin" : ""} />
      {agentStatusLabels[status]}
    </span>
  );
}
export function WaitingForConfiguration({
  admin = false,
  onConfigure,
}: {
  admin?: boolean;
  onConfigure?: () => void;
}) {
  return (
    <div className="agent-configuration-notice" role="status">
      <span>
        <KeyRound size={20} />
      </span>
      <div>
        <b>Agent 正在等待管理员配置模型</b>
        <p>
          消息对话可以正常使用。模型连接配置完成后，Agent
          才能处理新的消息；当前不会生成模型回复。
        </p>
        {admin && onConfigure && (
          <button onClick={onConfigure}>
            前往模型网关配置 <span>↗</span>
          </button>
        )}
      </div>
    </div>
  );
}
export function AgentPrinciples() {
  return (
    <div className="agent-principles">
      <span>
        <Sparkles size={15} />
        一个 Agent，一段持续会话
      </span>
      <p>
        同一个 Agent
        会延续工作上下文。后台处理状态在这里展示，只有发出的聊天消息才会出现在对话中。
      </p>
    </div>
  );
}
export type GatewayFormValue = {
  baseUrl: string;
  model: string;
  apiKey: string;
  enabled: boolean;
  clearApiKey?: boolean;
};
export function GatewayConfigurationFields({
  initial,
  onSave,
  onTest,
  error = "",
  busy = false,
}: {
  initial: Omit<GatewayFormValue, "apiKey" | "clearApiKey"> & {
    hasApiKey: boolean;
  };
  onSave: (value: GatewayFormValue) => Promise<void>;
  onTest?: () => Promise<void>;
  error?: string;
  busy?: boolean;
}) {
  const [baseUrl, setBaseUrl] = useState(initial.baseUrl),
    [model, setModel] = useState(initial.model),
    [apiKey, setApiKey] = useState(""),
    [enabled, setEnabled] = useState(initial.enabled),
    [clearApiKey, setClearApiKey] = useState(false);
  async function submit(event: FormEvent) {
    event.preventDefault();
    try {
      await onSave({
        baseUrl: baseUrl.trim(),
        model: model.trim(),
        apiKey,
        enabled,
        ...(clearApiKey ? { clearApiKey: true } : {}),
      });
    } finally {
      setApiKey("");
    }
  }
  return (
    <form
      className="gateway-configuration-form"
      onSubmit={submit}
      autoComplete="off"
    >
      <Notice>{error}</Notice>
      <div className="gateway-admin-only">
        <ShieldCheck size={17} />
        <span>此配置由平台管理员管理，所有 Agent 使用同一个模型网关。</span>
      </div>
      <label>
        OpenAI 兼容服务地址
        <input
          type="url"
          name="gatewayBaseUrl"
          value={baseUrl}
          onChange={(event) => setBaseUrl(event.target.value)}
          placeholder="https://api.example.com/v1"
          autoComplete="off"
          required
        />
        <small className="field-help">
          填写 API
          基础地址；密钥单独保存在下方，不要放入地址、查询参数或用户名密码中。
        </small>
      </label>
      <label>
        模型名称
        <input
          name="gatewayModel"
          value={model}
          onChange={(event) => setModel(event.target.value)}
          placeholder="服务支持的模型 ID"
          autoComplete="off"
          required
        />
      </label>
      <label>
        API Key{" "}
        <span className="optional">
          {initial.hasApiKey ? "已有密钥 · 留空保留" : "尚未配置"}
        </span>
        <input
          type="password"
          name="gatewayApiKey"
          value={apiKey}
          onChange={(event) => setApiKey(event.target.value)}
          placeholder={
            initial.hasApiKey
              ? "留空保留已有密钥，填写以替换"
              : "输入服务提供的 API Key"
          }
          autoComplete="new-password"
          spellCheck={false}
          required={!initial.hasApiKey && enabled && !clearApiKey}
          disabled={clearApiKey || busy}
        />
        <small className="field-help">
          密钥仅用于本次保存，不会回显，也不会写入浏览器本地存储。
        </small>
      </label>
      {initial.hasApiKey && (
        <label className="gateway-clear-key">
          <input
            type="checkbox"
            checked={clearApiKey}
            onChange={(event) => {
              setClearApiKey(event.target.checked);
              setApiKey("");
            }}
          />
          <span>
            清除已保存的 API Key
            <small>保存后，Agent 会等待新的模型凭据。</small>
          </span>
        </label>
      )}
      <label className="agent-enabled-switch">
        <input
          type="checkbox"
          checked={enabled}
          onChange={(event) => setEnabled(event.target.checked)}
        />
        <span>
          <b>启用模型网关</b>
          <small>关闭时，Agent 保留对话记录并等待管理员重新启用。</small>
        </span>
        <i />
      </label>
      <div className="gateway-form-actions">
        {onTest && (
          <Button
            type="button"
            className="secondary"
            busy={busy}
            onClick={() => void onTest()}
          >
            测试已保存的连接
          </Button>
        )}
        <Button type="submit" busy={busy}>
          保存配置
        </Button>
      </div>
    </form>
  );
}
