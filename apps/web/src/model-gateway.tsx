import { useState } from "react";
import type { FormEvent } from "react";
import { Link } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  CheckCheck,
  Clock3,
  KeyRound,
  LoaderCircle,
  Pencil,
  Plus,
  ShieldCheck,
  Star,
  Trash2,
} from "lucide-react";
import { api, ApiError } from "@teamie/api-client";
import type { AdminModelProfile } from "@teamie/api-client";
import { useAuth } from "./auth";
import {
  canConfigureModelGateway,
  gatewayFailureText,
  modelGatewayInput,
  modelProfileCreateInput,
  modelProfileUpdateInput,
} from "./agent-management";
import type { ModelProfileFormValue } from "./agent-management";
import { AgentEnabledStatus, GatewayConfigurationFields } from "./agent-ui";
import type { GatewayFormValue } from "./agent-ui";
import { Button, Modal, Notice } from "./ui";
import { ICON } from "./icons";

function ModelProfileDialog({
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
    [isDefault, setIsDefault] = useState(initial?.isDefault ?? false),
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
        isDefault,
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
        {!initial && (
          <label>
            模型标识 <span className="optional">选填 · 留空自动生成</span>
            <input
              name="profileId"
              value={id}
              onChange={(e) => setId(e.target.value)}
              placeholder="例如：deepseek-r1"
              autoComplete="off"
            />
          </label>
        )}
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
        <label className="agent-enabled-switch">
          <input
            type="checkbox"
            name="profileEnabled"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
          />
          <span>
            <b>启用该模型</b>
            <small>启用且配置密钥后，用户创建 Agent 时可选择此模型。</small>
          </span>
          <i />
        </label>
        <label className="agent-enabled-switch">
          <input
            type="checkbox"
            name="profileIsDefault"
            checked={isDefault}
            onChange={(e) => setIsDefault(e.target.checked)}
          />
          <span>
            <b>设为平台默认模型</b>
            <small>未指定模型的 Agent 或所选模型停用时将自动使用默认模型。</small>
          </span>
          <i />
        </label>
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

export function ModelGatewaySettings() {
  const { user } = useAuth();
  const client = useQueryClient();
  const admin = canConfigureModelGateway(user);
  const [denied, setDenied] = useState(false),
    [busy, setBusy] = useState<"save" | "test" | null>(null),
    [modelBusyId, setModelBusyId] = useState<string | null>(null),
    [creatingModel, setCreatingModel] = useState(false),
    [editingModel, setEditingModel] = useState<AdminModelProfile | null>(null),
    [error, setError] = useState(""),
    [saved, setSaved] = useState(false);
  const [test, setTest] = useState<{
    ok: boolean;
    model: string;
    latencyMs: number;
  } | null>(null);
  const key = ["modelGateway", user!.id];
  const query = useQuery({
    queryKey: key,
    queryFn: api.modelGateway,
    enabled: admin && !denied,
    retry: (count, e) =>
      !(e instanceof ApiError && [401, 403].includes(e.status)) && count < 1,
  });
  const forbidden =
    denied || (query.error instanceof ApiError && query.error.status === 403);
  async function syncGatewayResponse(response: Awaited<ReturnType<typeof api.modelGateway>>) {
    client.setQueryData(key, response);
    await Promise.all([
      client.invalidateQueries({ queryKey: ["modelCatalog"] }),
      client.invalidateQueries({ queryKey: ["agents"] }),
    ]);
  }
  async function save(value: GatewayFormValue): Promise<boolean> {
    const sessionToken = api.csrfToken;
    setBusy("save");
    setError("");
    setSaved(false);
    setTest(null);
    try {
      const response = await api.saveModelGateway(modelGatewayInput(value));
      if (sessionToken !== api.csrfToken) return false;
      await syncGatewayResponse(response);
      setSaved(true);
      return true;
    } catch (e) {
      if (sessionToken === api.csrfToken) {
        if (e instanceof ApiError && e.status === 403) setDenied(true);
        setError(gatewayFailureText(e));
      }
      return false;
    } finally {
      setBusy(null);
    }
  }
  async function testConnection() {
    const sessionToken = api.csrfToken;
    setBusy("test");
    setError("");
    setTest(null);
    try {
      const result = await api.testModelGateway();
      if (sessionToken === api.csrfToken)
        setTest({
          ok: result.ok,
          model: result.model,
          latencyMs: result.latencyMs,
        });
    } catch (e) {
      if (sessionToken === api.csrfToken) {
        if (e instanceof ApiError && e.status === 403) setDenied(true);
        setError(gatewayFailureText(e));
      }
    } finally {
      setBusy(null);
    }
  }
  async function handleCreateModel(value: ModelProfileFormValue): Promise<boolean> {
    const sessionToken = api.csrfToken;
    const input = modelProfileCreateInput(value);
    const response = await api.createModelProfile(input);
    if (sessionToken !== api.csrfToken) return false;
    await syncGatewayResponse(response);
    setSaved(true);
    return true;
  }
  async function handleUpdateModel(
    profileId: string,
    value: ModelProfileFormValue,
  ): Promise<boolean> {
    const sessionToken = api.csrfToken;
    const input = modelProfileUpdateInput(value);
    const response = await api.updateModelProfile(profileId, input);
    if (sessionToken !== api.csrfToken) return false;
    await syncGatewayResponse(response);
    setSaved(true);
    return true;
  }
  async function handleSetDefaultModel(profileId: string) {
    const sessionToken = api.csrfToken;
    setModelBusyId(profileId);
    setError("");
    try {
      const response = await api.updateModelProfile(profileId, {
        isDefault: true,
      });
      if (sessionToken === api.csrfToken) {
        await syncGatewayResponse(response);
        setSaved(true);
      }
    } catch (e) {
      if (sessionToken === api.csrfToken) setError(gatewayFailureText(e));
    } finally {
      setModelBusyId(null);
    }
  }
  async function handleDeleteModel(profileId: string) {
    const sessionToken = api.csrfToken;
    setModelBusyId(profileId);
    setError("");
    try {
      const response = await api.deleteModelProfile(profileId);
      if (sessionToken === api.csrfToken) {
        await syncGatewayResponse(response);
        setSaved(true);
      }
    } catch (e) {
      if (sessionToken === api.csrfToken) setError(gatewayFailureText(e));
    } finally {
      setModelBusyId(null);
    }
  }
  async function handleTestModel(profileId: string) {
    const sessionToken = api.csrfToken;
    setModelBusyId(profileId);
    setError("");
    setSaved(false);
    setTest(null);
    try {
      const result = await api.testModelProfile(profileId);
      if (sessionToken === api.csrfToken)
        setTest({
          ok: result.ok,
          model: result.model,
          latencyMs: result.latencyMs,
        });
    } catch (e) {
      if (sessionToken === api.csrfToken) setError(gatewayFailureText(e));
    } finally {
      setModelBusyId(null);
    }
  }
  if (!admin || forbidden)
    return (
      <div className="unavailable-view">
        <span className="large-symbol">
          <ShieldCheck size={ICON.xl} />
        </span>
        <h2>需要平台管理员权限</h2>
        <p>模型连接由平台管理员统一配置。你仍可以管理自己的 Agent。</p>
        <Link className="button" to="/app/agents">
          管理个人 Agent
        </Link>
      </div>
    );
  const models = query.data?.models || [];
  return (
    <div className="admin-view gateway-view">
      <div className="admin-heading">
        <div>
          <h1>
            模型网关{" "}
            <span className="platform-role-badge">
              <ShieldCheck size={ICON.xs} />
              平台管理员
            </span>
          </h1>
          <p>所有 Agent 共用的模型连接与多模型目录。</p>
        </div>
        <span className="admin-heading-icon">
          <KeyRound size={ICON.xl} />
        </span>
      </div>
      {query.isLoading ? (
        <div className="admin-empty">
          <LoaderCircle className="spin" size={ICON.xl} />
          <p>正在加载模型配置…</p>
        </div>
      ) : !query.data ? (
        <div className="gateway-load-error">
          <Notice>{gatewayFailureText(query.error)}</Notice>
          <Button className="secondary" onClick={() => void query.refetch()}>
            重新加载
          </Button>
        </div>
      ) : (
        <>
          <div className={`gateway-saved-state ${query.data.status}`}>
            <span>
              {query.data.status === "ready" ? (
                <CheckCheck size={ICON.xl} />
              ) : (
                <Clock3 size={ICON.xl} />
              )}
            </span>
            <div>
              <b>
                {query.data.status === "unconfigured"
                  ? "尚未配置"
                  : query.data.status === "disabled"
                    ? "模型网关已停用"
                    : "配置已保存"}
              </b>
              <p>
                {query.data.status === "ready"
                  ? test?.ok
                    ? "连接测试已通过，Agent 可以调用模型。"
                    : "连接尚需测试验证。"
                  : "保存并启用后，Agent 才能调用模型。"}
              </p>
            </div>
          </div>
          <Notice kind="success">{saved ? "配置已保存。" : ""}</Notice>
          <Notice kind={test?.ok ? "success" : "error"}>
            {test
              ? test.ok
                ? `连接测试通过 · ${test.model} · ${Math.round(test.latencyMs)} ms`
                : "连接测试未通过，请检查服务地址、模型和密钥。"
              : ""}
          </Notice>
          <section className="gateway-models-section">
            <div className="gateway-models-header">
              <div>
                <h2>可选模型目录</h2>
                <p>
                  配置多个可用模型供创建 Agent 时选择；若所选模型停用或移除，Agent
                  会自动平滑回退到平台默认模型。
                </p>
              </div>
              <Button
                type="button"
                variant="secondary"
                onClick={() => setCreatingModel(true)}
              >
                <Plus size={ICON.xs} />
                添加模型
              </Button>
            </div>
            {models.length > 0 && (
              <div className="gateway-models-list">
                {models.map((item) => (
                  <div key={item.id} className="gateway-model-card">
                    <div className="gateway-model-info">
                      <div className="gateway-model-title">
                        <b>{item.displayName}</b>
                        <code>{item.id}</code>
                        {item.isDefault && (
                          <span className="platform-role-badge">
                            <Star size={ICON.xs} />
                            默认模型
                          </span>
                        )}
                        <AgentEnabledStatus enabled={item.enabled} />
                      </div>
                      {item.description && <p>{item.description}</p>}
                      <small>
                        模型：{item.model} · 地址：{item.baseUrl} · 上下文：
                        {item.contextWindowTokens} Token ·{" "}
                        {item.hasApiKey ? "已配置密钥" : "待配置密钥"}
                      </small>
                    </div>
                    <div className="gateway-model-actions">
                      {!item.isDefault && (
                        <Button
                          type="button"
                          variant="secondary"
                          disabled={modelBusyId === item.id}
                          onClick={() => void handleSetDefaultModel(item.id)}
                        >
                          设为默认
                        </Button>
                      )}
                      <Button
                        type="button"
                        variant="secondary"
                        disabled={
                          modelBusyId === item.id ||
                          !item.configured ||
                          !item.enabled
                        }
                        onClick={() => void handleTestModel(item.id)}
                      >
                        测试此模型
                      </Button>
                      <Button
                        type="button"
                        variant="secondary"
                        disabled={modelBusyId === item.id}
                        onClick={() => setEditingModel(item)}
                      >
                        <Pencil size={ICON.xs} />
                        编辑
                      </Button>
                      <Button
                        type="button"
                        variant="secondary"
                        className="danger-text"
                        disabled={modelBusyId === item.id}
                        onClick={() => void handleDeleteModel(item.id)}
                      >
                        <Trash2 size={ICON.xs} />
                        移除
                      </Button>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </section>
          <GatewayConfigurationFields
            key={`${user!.id}:${query.data.config.updatedAt}`}
            initial={query.data.config}
            onSave={save}
            error={error}
            busy={busy === "save"}
            testing={busy === "test"}
          />
          <div className="gateway-test-panel">
            <div>
              <h3>测试已保存的配置</h3>
              <p>使用已保存的配置发送 1 次模型请求，可能产生费用。</p>
            </div>
            <Button
              className="secondary"
              busy={busy === "test"}
              disabled={
                busy === "save" ||
                !query.data.config.configured ||
                !query.data.config.enabled
              }
              onClick={() => void testConnection()}
            >
              测试连接
            </Button>
          </div>
          {creatingModel && (
            <ModelProfileDialog
              onSave={handleCreateModel}
              onClose={() => setCreatingModel(false)}
            />
          )}
          {editingModel && (
            <ModelProfileDialog
              initial={editingModel}
              onSave={(value) => handleUpdateModel(editingModel.id, value)}
              onClose={() => setEditingModel(null)}
            />
          )}
        </>
      )}
    </div>
  );
}
