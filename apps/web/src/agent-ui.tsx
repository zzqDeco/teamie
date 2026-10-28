import { useState } from "react";
import type { FormEvent } from "react";
import { Bot, Check, CirclePause, Eye, LockKeyhole } from "lucide-react";
import type { WebSearchGatewayConfig, Workspace } from "@teamie/api-client";
import { Button, Modal, Notice, SwitchField } from "./ui";
import { ICON } from "./icons";

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
      <Bot size={ICON.xs} />
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
      <Bot
        size={size === "large" ? ICON.lg : size === "small" ? ICON.xs : ICON.sm}
      />
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
        <Eye size={ICON.sm} />
        可见工作空间
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
              <i>
                {selectedIds.includes(workspace.id) && <Check size={ICON.xs} />}
              </i>
            </label>
          ))}
        </div>
      ) : (
        <div className="visibility-empty">加入团队后，可在这里选择。</div>
      )}
      <small>
        <LockKeyhole size={ICON.xs} />
        可访问所选团队的频道；私信和小组需要加入对话。
      </small>
    </fieldset>
  );
}
export function AgentEnabledStatus({ enabled }: { enabled: boolean }) {
  const Icon = enabled ? Check : CirclePause;
  return (
    <span className={`agent-status ${enabled ? "enabled" : "disabled"}`}>
      <Icon size={ICON.xs} aria-hidden="true" />
      {enabled ? "已启用" : "已停用"}
    </span>
  );
}
export type WebSearchFormValue = {
  enabled: boolean;
  apiKey: string;
  clearApiKey?: boolean;
};

export function WebSearchConfigurationFields({
  initial,
  onSave,
  error = "",
  busy = false,
}: {
  initial: WebSearchGatewayConfig;
  onSave: (value: WebSearchFormValue) => Promise<boolean>;
  error?: string;
  busy?: boolean;
}) {
  const [enabled, setEnabled] = useState(initial.enabled);
  const [apiKey, setApiKey] = useState("");
  const [clearApiKey, setClearApiKey] = useState(false);
  const [confirm, setConfirm] = useState(false);
  async function save() {
    setConfirm(false);
    if (
      await onSave({
        enabled,
        apiKey,
        ...(clearApiKey ? { clearApiKey: true } : {}),
      })
    ) {
      setApiKey("");
      setClearApiKey(false);
    }
  }
  function submit(event: FormEvent) {
    event.preventDefault();
    if (clearApiKey) setConfirm(true);
    else void save();
  }
  return (
    <section className="gateway-web-search-section">
      <h3>联网搜索</h3>
      <form
        className="gateway-configuration-form"
        onSubmit={submit}
        autoComplete="off"
      >
        <Notice>{error}</Notice>
        <SwitchField
          name="gatewayWebSearchEnabled"
          checked={enabled}
          disabled={busy}
          title="启用联网搜索与网页提取"
          onChange={setEnabled}
        />
        <label>
          Tavily API Key{" "}
          <span className="optional">
            {initial.hasApiKey ? "已有密钥 · 留空保留" : "尚未配置"}
          </span>
          <input
            name="gatewayWebSearchApiKey"
            type="password"
            value={apiKey}
            onChange={(e) => setApiKey(e.target.value)}
            autoComplete="new-password"
            spellCheck={false}
            required={!initial.hasApiKey && enabled && !clearApiKey}
            disabled={busy || clearApiKey}
          />
        </label>
        {initial.hasApiKey && (
          <label className="gateway-clear-key">
            <input
              name="gatewayClearWebSearchApiKey"
              type="checkbox"
              checked={clearApiKey}
              onChange={(e) => {
                setClearApiKey(e.target.checked);
                setApiKey("");
              }}
              disabled={busy}
            />
            <span>清除已保存的搜索密钥</span>
          </label>
        )}
        <div className="gateway-form-actions">
          <Button type="submit" busy={busy}>
            保存搜索设置
          </Button>
        </div>
      </form>
      {confirm && (
        <Modal title="清除搜索密钥" onClose={() => setConfirm(false)}>
          <p>联网搜索与网页提取将暂停，直到重新配置。</p>
          <div className="modal-actions">
            <Button
              type="button"
              variant="secondary"
              onClick={() => setConfirm(false)}
            >
              取消
            </Button>
            <Button type="button" onClick={() => void save()}>
              确认保存
            </Button>
          </div>
        </Modal>
      )}
    </section>
  );
}
