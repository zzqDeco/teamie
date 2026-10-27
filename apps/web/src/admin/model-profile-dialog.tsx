import { useState } from "react";
import type { FormEvent } from "react";
import type { AdminModelProfile } from "@teamie/api-client";
import { gatewayFailureText } from "../agent-management";
import type { ModelProfileFormValue } from "../agent-management";
import { Button, FormRow, Modal, Notice, SwitchField } from "../ui";

export function ModelProfileDialog({
  initial,
  onSave,
  onClose,
}: {
  initial?: AdminModelProfile;
  onSave: (value: ModelProfileFormValue) => Promise<boolean>;
  onClose: () => void;
}) {
  const [id, setId] = useState(initial?.id || ""),
    [displayName, setDisplayName] = useState(initial?.displayName || ""),
    [description, setDescription] = useState(initial?.description || ""),
    [baseUrl, setBaseUrl] = useState(initial?.baseUrl || ""),
    [model, setModel] = useState(initial?.model || ""),
    [apiKey, setApiKey] = useState(""),
    [clearApiKey, setClearApiKey] = useState(false),
    [contextWindowTokens, setContextWindowTokens] = useState(
      initial?.contextWindowTokens ?? 16384,
    ),
    [enabled, setEnabled] = useState(initial?.enabled ?? true),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [dirty, setDirty] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const ok = await onSave({
        ...(initial ? {} : { id }),
        displayName,
        description,
        baseUrl,
        model,
        apiKey,
        ...(clearApiKey ? { clearApiKey: true } : {}),
        enabled,
        contextWindowTokens,
      });
      if (ok) onClose();
    } catch (e) {
      setError(gatewayFailureText(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title={initial ? `编辑模型「${initial.displayName}」` : "添加可用模型"}
      onClose={onClose}
      busy={busy}
      dirty={dirty}
    >
      <form
        className="gateway-model-profile-form"
        onSubmit={submit}
        onInput={() => setDirty(true)}
        autoComplete="off"
      >
        <Notice>{error}</Notice>
        <FormRow>
          <label>
            显示名称
            <input
              name="profileDisplayName"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder="例如：DeepSeek R1（推理）"
              autoComplete="off"
              required
            />
          </label>
          {!initial && (
            <label>
              模型标识 <span className="optional">留空自动生成</span>
              <input
                name="profileId"
                value={id}
                onChange={(e) => setId(e.target.value)}
                placeholder="例如：deepseek-r1"
                autoComplete="off"
              />
            </label>
          )}
        </FormRow>
        <label>
          简介 <span className="optional">选填</span>
          <input
            name="profileDescription"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="适用场景说明，供创建 Agent 时参考"
            autoComplete="off"
          />
        </label>
        <label>
          OpenAI 兼容服务地址
          <input
            type="url"
            name="profileBaseUrl"
            value={baseUrl}
            onChange={(e) => setBaseUrl(e.target.value)}
            placeholder="https://api.example.com/v1"
            autoComplete="off"
            required
          />
        </label>
        <FormRow>
          <label>
            上游模型 ID
            <input
              name="profileModel"
              value={model}
              onChange={(e) => setModel(e.target.value)}
              placeholder="服务支持的模型名称"
              autoComplete="off"
              required
            />
          </label>
          <label>
            上下文窗口（Token）
            <input
              type="number"
              name="profileContextWindow"
              min={4096}
              max={2000000}
              step={1}
              value={contextWindowTokens}
              onChange={(e) => setContextWindowTokens(Number(e.target.value))}
              required
              disabled={busy}
            />
          </label>
        </FormRow>
        <label>
          API Key{" "}
          <span className="optional">
            {initial?.hasApiKey ? "已有密钥 · 留空保留" : "尚未配置"}
          </span>
          <input
            type="password"
            name="profileApiKey"
            value={apiKey}
            onChange={(e) => setApiKey(e.target.value)}
            placeholder={
              initial?.hasApiKey
                ? "留空保留已有密钥，填写以替换"
                : "输入服务提供的 API Key"
            }
            autoComplete="new-password"
            spellCheck={false}
            required={!initial?.hasApiKey && enabled && !clearApiKey}
            disabled={clearApiKey || busy}
          />
        </label>
        {initial?.hasApiKey && (
          <label className="gateway-clear-key">
            <input
              type="checkbox"
              name="profileClearApiKey"
              checked={clearApiKey}
              onChange={(e) => {
                setClearApiKey(e.target.checked);
                setApiKey("");
              }}
            />
            <span>清除已保存的 API Key</span>
          </label>
        )}
        <SwitchField
          name="profileEnabled"
          checked={enabled}
          title="启用该模型"
          description="启用且配置密钥后，用户创建 Agent 时可选择此模型。"
          onChange={setEnabled}
        />
        <div className="modal-actions">
          <Button type="button" variant="secondary" onClick={onClose}>
            取消
          </Button>
          <Button type="submit" busy={busy}>
            {initial ? "保存模型" : "添加模型"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

