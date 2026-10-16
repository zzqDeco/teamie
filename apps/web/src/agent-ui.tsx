import { useState } from "react";
import type { FormEvent } from "react";
import { Bot, Check, CirclePause, Eye, LockKeyhole } from "lucide-react";
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
        可见团队
      </legend>
      <p>选择的团队成员可以发现此 Agent。</p>
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
        <div className="visibility-empty">加入团队后，可在这里选择。</div>
      )}
      <small>
        <LockKeyhole size={12} />
        可访问所选团队的频道；私信和小组需要加入对话。
      </small>
    </fieldset>
  );
}
export function AgentEnabledStatus({ enabled }: { enabled: boolean }) {
  const Icon = enabled ? Check : CirclePause;
  return (
    <span className={`agent-status ${enabled ? "enabled" : "disabled"}`}>
      <Icon size={12} aria-hidden="true" />
      {enabled ? "已启用" : "已停用"}
    </span>
  );
}
export type GatewayFormValue = {
  baseUrl: string;
  model: string;
  apiKey: string;
  enabled: boolean;
  clearApiKey?: boolean;
  contextWindowTokens?: number;
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
    [clearApiKey, setClearApiKey] = useState(false),
    [contextWindowTokens, setContextWindowTokens] = useState(
      initial.contextWindowTokens ?? 16384,
    );
  async function submit(event: FormEvent) {
    event.preventDefault();
    try {
      await onSave({
        baseUrl: baseUrl.trim(),
        model: model.trim(),
        apiKey,
        enabled,
        contextWindowTokens,
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
        <small className="field-help">API 基础地址，通常以 /v1 结尾。</small>
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
        <small className="field-help">密钥保存后不再显示。</small>
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
          <small>关闭后，Agent 暂停调用模型。</small>
        </span>
        <i />
      </label>
      <details className="gateway-advanced">
        <summary>高级设置</summary>
        <label>
          上下文窗口（Token）
          <input
            type="number"
            name="gatewayContextWindow"
            min={4096}
            max={2000000}
            step={1}
            value={contextWindowTokens}
            onChange={(event) =>
              setContextWindowTokens(Number(event.target.value))
            }
            required
            disabled={busy}
          />
          <small className="field-help">按服务支持的上下文长度填写。</small>
        </label>
      </details>
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
