import { useState } from "react";
import type { FormEvent } from "react";
import type {
  AdminModelProfile,
  AdminModelProvider,
} from "@teamie/api-client";
import { gatewayFailureText } from "../agent-management";
import type { ModelProfileFormValue } from "../agent-management";
import { Button, FormRow, Modal, Notice, SwitchField } from "../ui";

export function ModelProfileDialog({
  providers,
  defaultProviderId,
  initial,
  onSave,
  onClose,
}: {
  providers: AdminModelProvider[];
  defaultProviderId?: string;
  initial?: AdminModelProfile;
  onSave: (value: ModelProfileFormValue) => Promise<boolean>;
  onClose: () => void;
}) {
  const [id, setId] = useState(initial?.id || ""),
    [providerId, setProviderId] = useState(
      initial?.providerId || defaultProviderId || providers[0]?.id || "",
    ),
    [displayName, setDisplayName] = useState(initial?.displayName || ""),
    [description, setDescription] = useState(initial?.description || ""),
    [model, setModel] = useState(initial?.model || ""),
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
        providerId,
        displayName,
        description,
        model,
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
        <label>
          所属模型服务商
          <select
            name="profileProviderId"
            value={providerId}
            onChange={(e) => setProviderId(e.target.value)}
            required
            disabled={busy}
          >
            {providers.map((item) => (
              <option key={item.id} value={item.id}>
                {item.displayName}（{item.id}）
              </option>
            ))}
          </select>
        </label>
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
        <FormRow>
          <label>
            上游模型 ID
            <input
              name="profileModel"
              value={model}
              onChange={(e) => setModel(e.target.value)}
              placeholder="服务商支持的模型名称，如 gpt-4o"
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
        <SwitchField
          name="profileEnabled"
          checked={enabled}
          title="启用该模型"
          description="启用且所属服务商可用时，用户创建 Agent 时可选择此模型。"
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
