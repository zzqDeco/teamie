import { useState } from "react";
import type { FormEvent } from "react";
import type { AdminModelProvider } from "@teamie/api-client";
import { gatewayFailureText } from "../agent-management";
import type { ProviderProfileFormValue } from "../agent-management";
import {
  Button,
  ConfirmDialog,
  FormRow,
  Modal,
  ModalCloseButton,
  Notice,
  SwitchField,
} from "../ui";

export function ProviderProfileDialog({
  initial,
  onSave,
  onClose,
}: {
  initial?: AdminModelProvider;
  onSave: (value: ProviderProfileFormValue) => Promise<boolean>;
  onClose: () => void;
}) {
  const [id, setId] = useState(initial?.id || ""),
    [displayName, setDisplayName] = useState(initial?.displayName || ""),
    [baseUrl, setBaseUrl] = useState(initial?.baseUrl || ""),
    [apiKey, setApiKey] = useState(""),
    [clearApiKey, setClearApiKey] = useState(false),
    [enabled, setEnabled] = useState(initial?.enabled ?? true),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [dirty, setDirty] = useState(false),
    [confirming, setConfirming] = useState(false);
  // Clearing the key or disabling a live connection pauses every model under
  // it, so (like the web-search key) it needs an explicit confirmation.
  const disabling = !!initial?.enabled && !enabled;
  const needsConfirm = !!initial && (clearApiKey || disabling);

  function submit(event: FormEvent) {
    event.preventDefault();
    if (needsConfirm) setConfirming(true);
    else void save();
  }

  async function save() {
    setConfirming(false);
    setBusy(true);
    setError("");
    try {
      const ok = await onSave({
        ...(initial ? {} : { id }),
        displayName,
        baseUrl,
        apiKey,
        ...(clearApiKey ? { clearApiKey: true } : {}),
        enabled,
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
      title={
        initial ? `编辑服务商「${initial.displayName}」` : "添加模型服务商"
      }
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
            服务商名称
            <input
              name="providerDisplayName"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder="例如：OpenAI 主连接 / DeepSeek"
              autoComplete="off"
              required
            />
          </label>
          {!initial && (
            <label>
              服务商标识 <span className="optional">留空自动生成</span>
              <input
                name="providerId"
                value={id}
                onChange={(e) => setId(e.target.value)}
                placeholder="例如：openai-main"
                autoComplete="off"
              />
            </label>
          )}
        </FormRow>
        <label>
          OpenAI 兼容服务地址
          <input
            type="url"
            name="providerBaseUrl"
            value={baseUrl}
            onChange={(e) => setBaseUrl(e.target.value)}
            placeholder="https://api.example.com/v1"
            autoComplete="off"
            required
          />
        </label>
        <label>
          API Key{" "}
          <span className="optional">
            {initial?.hasApiKey ? "已有密钥 · 留空保留" : "尚未配置"}
          </span>
          <input
            type="password"
            name="providerApiKey"
            value={apiKey}
            onChange={(e) => setApiKey(e.target.value)}
            placeholder={
              initial?.hasApiKey
                ? "留空保留已有密钥，填写以轮换密钥"
                : "输入服务商提供的 API Key"
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
              name="providerClearApiKey"
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
          name="providerEnabled"
          checked={enabled}
          title="启用该服务商连接"
          description="停用后，该服务商下的所有模型将暂停调用并平滑回退。"
          onChange={setEnabled}
        />
        <div className="modal-actions">
          <ModalCloseButton>取消</ModalCloseButton>
          <Button type="submit" busy={busy}>
            {initial ? "保存服务商" : "添加服务商"}
          </Button>
        </div>
      </form>
      {confirming && initial && (
        <ConfirmDialog
          tone="danger"
          title={
            clearApiKey && disabling
              ? `清除密钥并停用「${initial.displayName}」？`
              : clearApiKey
                ? `清除「${initial.displayName}」的 API Key？`
                : `停用服务商「${initial.displayName}」？`
          }
          impact={[
            `该服务商下的 ${initial.modelCount} 个模型将从 Agent 可选目录中隐藏，进行中的调用会被取消。`,
            "选择了这些模型的 Agent 会平滑回退到平台默认模型；若默认模型也在该服务商下，将回退到其他可用模型。",
            ...(clearApiKey
              ? ["已保存的 API Key 将被永久清除，恢复使用需要重新填写。"]
              : []),
          ]}
          confirmLabel="确认保存"
          onCancel={() => setConfirming(false)}
          onConfirm={() => void save()}
        />
      )}
    </Modal>
  );
}
