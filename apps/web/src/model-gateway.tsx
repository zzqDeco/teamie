import { useRef, useState } from "react";
import { Link } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  CheckCheck,
  Clock3,
  KeyRound,
  LoaderCircle,
  Pencil,
  Plus,
  Server,
  ShieldCheck,
  Star,
  Trash2,
} from "lucide-react";
import { api, ApiError } from "@teamie/api-client";
import type {
  AdminModelProfile,
  AdminModelProvider,
} from "@teamie/api-client";
import { useAuth } from "./auth";
import {
  canConfigureModelGateway,
  gatewayFailureText,
  modelProfileCreateInput,
  modelProfileUpdateInput,
  providerProfileCreateInput,
  providerProfileUpdateInput,
  webSearchInput,
} from "./agent-management";
import type {
  ModelProfileFormValue,
  ProviderProfileFormValue,
} from "./agent-management";
import { AgentEnabledStatus, WebSearchConfigurationFields } from "./agent-ui";
import type { WebSearchFormValue } from "./agent-ui";
import { Button, Notice } from "./ui";
import { ICON } from "./icons";
import { ModelProfileDialog } from "./admin/model-profile-dialog";
import { ProviderProfileDialog } from "./admin/provider-profile-dialog";

type ModelTestOutcome = {
  ok: boolean;
  model: string;
  latencyMs: number;
  error?: string;
};

export function ModelGatewaySettings() {
  const { user } = useAuth();
  const client = useQueryClient();
  const admin = canConfigureModelGateway(user);
  const [denied, setDenied] = useState(false),
    [searchBusy, setSearchBusy] = useState(false),
    [testing, setTesting] = useState(false),
    [providerBusyId, setProviderBusyId] = useState<string | null>(null),
    [modelBusyId, setModelBusyId] = useState<string | null>(null),
    [creatingProvider, setCreatingProvider] = useState(false),
    [editingProvider, setEditingProvider] =
      useState<AdminModelProvider | null>(null),
    [creatingForProviderId, setCreatingForProviderId] = useState<
      string | null
    >(null),
    [editingModel, setEditingModel] = useState<AdminModelProfile | null>(null),
    [error, setError] = useState(""),
    [saved, setSaved] = useState(false);
  const testVersions = useRef(new Map<string, number>());
  const defaultTestVersion = useRef(0);
  const [test, setTest] = useState<ModelTestOutcome | null>(null);
  const [modelTests, setModelTests] = useState<
    Record<string, ModelTestOutcome>
  >({});
  const [testingModels, setTestingModels] = useState<Record<string, boolean>>(
    {},
  );
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
  function invalidateTests(ids: string[]) {
    defaultTestVersion.current++;
    setTest(null);
    setTesting(false);
    for (const id of ids)
      testVersions.current.set(id, (testVersions.current.get(id) || 0) + 1);
    const unaffected = <T,>(prev: Record<string, T>) => Object.fromEntries(
      Object.entries(prev).filter(([id]) => !ids.includes(id)),
    );
    setModelTests(unaffected);
    setTestingModels(unaffected);
  }
  async function syncGatewayResponse(
    response: Awaited<ReturnType<typeof api.modelGateway>>,
  ) {
    client.setQueryData(key, response);
    await Promise.all([
      client.invalidateQueries({ queryKey: ["modelCatalog"] }),
      client.invalidateQueries({ queryKey: ["agents"] }),
    ]);
  }
  async function save(value: WebSearchFormValue): Promise<boolean> {
    const sessionToken = api.csrfToken;
    setSearchBusy(true);
    setError("");
    setSaved(false);
    try {
      const response = await api.saveWebSearch(webSearchInput(value));
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
      setSearchBusy(false);
    }
  }
  async function testConnection() {
    const sessionToken = api.csrfToken;
    const version = ++defaultTestVersion.current;
    const current = () => sessionToken === api.csrfToken &&
      defaultTestVersion.current === version;
    setTesting(true);
    setError("");
    setTest(null);
    try {
      const result = await api.testModelGateway();
      if (current())
        setTest({
          ok: result.ok,
          model: result.model,
          latencyMs: result.latencyMs,
        });
    } catch (e) {
      if (current()) {
        if (e instanceof ApiError && e.status === 403) setDenied(true);
        setError(gatewayFailureText(e));
      }
    } finally {
      if (current()) setTesting(false);
    }
  }
  async function handleCreateProvider(
    value: ProviderProfileFormValue,
  ): Promise<boolean> {
    const sessionToken = api.csrfToken;
    const input = providerProfileCreateInput(value);
    const response = await api.createModelProvider(input);
    if (sessionToken !== api.csrfToken) return false;
    invalidateTests([]);
    await syncGatewayResponse(response);
    setSaved(true);
    return true;
  }
  async function handleUpdateProvider(
    providerId: string,
    value: ProviderProfileFormValue,
  ): Promise<boolean> {
    const sessionToken = api.csrfToken;
    const input = providerProfileUpdateInput(value);
    const response = await api.updateModelProvider(providerId, input);
    if (sessionToken !== api.csrfToken) return false;
    invalidateTests((query.data?.models || [])
      .filter((m) => (m.providerId || m.id) === providerId).map((m) => m.id));
    await syncGatewayResponse(response);
    setSaved(true);
    return true;
  }
  async function handleDeleteProvider(providerId: string) {
    const sessionToken = api.csrfToken;
    setProviderBusyId(providerId);
    setError("");
    try {
      const response = await api.deleteModelProvider(providerId);
      if (sessionToken === api.csrfToken) {
        invalidateTests([]);
        await syncGatewayResponse(response);
        setSaved(true);
      }
    } catch (e) {
      if (sessionToken === api.csrfToken) setError(gatewayFailureText(e));
    } finally {
      setProviderBusyId(null);
    }
  }
  async function handleCreateModel(
    value: ModelProfileFormValue,
  ): Promise<boolean> {
    const sessionToken = api.csrfToken;
    const input = modelProfileCreateInput(value);
    const response = await api.createModelProfile(input);
    if (sessionToken !== api.csrfToken) return false;
    invalidateTests([]);
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
    invalidateTests([profileId]);
    await syncGatewayResponse(response);
    setSaved(true);
    return true;
  }
  async function handleSetDefaultModel(profileId: string) {
    const sessionToken = api.csrfToken;
    setModelBusyId(profileId);
    setError("");
    try {
      const response = await api.saveModelRouting({
        defaultModelId: profileId,
      });
      if (sessionToken === api.csrfToken) {
        invalidateTests([]);
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
        invalidateTests([profileId]);
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
    const version = (testVersions.current.get(profileId) || 0) + 1;
    testVersions.current.set(profileId, version);
    const current = () => sessionToken === api.csrfToken &&
      testVersions.current.get(profileId) === version;
    setTestingModels((prev) => ({ ...prev, [profileId]: true }));
    setError("");
    setSaved(false);
    try {
      const result = await api.testModelProfile(profileId);
      if (current())
        setModelTests((prev) => ({
          ...prev,
          [profileId]: {
            ok: result.ok,
            model: result.model,
            latencyMs: result.latencyMs,
          },
        }));
    } catch (e) {
      if (current()) {
        const message = gatewayFailureText(e);
        setModelTests((prev) => ({
          ...prev,
          [profileId]: { ok: false, model: "", latencyMs: 0, error: message },
        }));
      }
    } finally {
      if (current()) {
        setTestingModels((prev) => {
          const next = { ...prev };
          delete next[profileId];
          return next;
        });
      }
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
  const providers: AdminModelProvider[] =
    query.data?.providers && query.data.providers.length > 0
      ? query.data.providers
      : [
          ...new Map(
            models.map((item) => {
              const pid = item.providerId || item.id;
              return [
                pid,
                {
                  id: pid,
                  displayName: item.providerDisplayName || item.displayName,
                  baseUrl: item.baseUrl,
                  hasApiKey: item.hasApiKey,
                  enabled: item.providerEnabled ?? true,
                  configured: Boolean(item.baseUrl && item.hasApiKey),
                  modelCount: models.filter(
                    (m) => (m.providerId || m.id) === pid,
                  ).length,
                  updatedAt: item.updatedAt,
                },
              ];
            }),
          ).values(),
        ];
  const anyVerified = Boolean(
    test?.ok || Object.values(modelTests).some((entry) => entry.ok),
  );
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
          <p>所有 Agent 共用的服务商连接与多模型目录。</p>
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
                  ? anyVerified
                    ? "连接测试已通过，Agent 可以调用模型。"
                    : "连接尚需测试验证。"
                  : "保存并启用服务商与模型后，Agent 才能调用模型。"}
              </p>
            </div>
          </div>
          <Notice>{error}</Notice>
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
                <h2>服务商与可选模型目录</h2>
                <p>
                  一次配置服务商地址与 API Key，即可在其下添加多个可用模型；若所选模型或服务商停用，Agent
                  会自动平滑回退到平台默认模型。
                </p>
              </div>
              <div className="gateway-header-actions">
                <Button
                  type="button"
                  variant="secondary"
                  onClick={() => setCreatingProvider(true)}
                >
                  <Plus size={ICON.xs} />
                  添加服务商
                </Button>
                <Button
                  type="button"
                  disabled={providers.length === 0}
                  onClick={() =>
                    setCreatingForProviderId(providers[0]?.id ?? "")
                  }
                >
                  <Plus size={ICON.xs} />
                  添加模型
                </Button>
              </div>
            </div>
            {providers.length === 0 ? (
              <div className="gateway-provider-empty">
                <Server size={ICON.xl} />
                <p>暂未配置模型服务商。请先添加服务商连接，再在其下添加多个可用模型。</p>
              </div>
            ) : (
              <div className="gateway-providers-list">
                {providers.map((provider) => {
                  const providerModels = models.filter(
                    (m) => (m.providerId || m.id) === provider.id,
                  );
                  return (
                    <div key={provider.id} className="gateway-provider-card">
                      <div className="gateway-provider-header">
                        <div className="gateway-provider-info">
                          <div className="gateway-provider-title">
                            <Server size={ICON.xs} />
                            <b>{provider.displayName}</b>
                            <code>{provider.id}</code>
                            <AgentEnabledStatus enabled={provider.enabled} />
                            <span
                              className={`gateway-key-badge ${provider.hasApiKey ? "configured" : "missing"}`}
                            >
                              {provider.hasApiKey ? "已配置密钥" : "缺少密钥"}
                            </span>
                          </div>
                          <small>
                            地址：{provider.baseUrl || "未配置"} · 关联模型：
                            {providerModels.length} 个
                          </small>
                        </div>
                        <div className="gateway-provider-actions">
                          <Button
                            type="button"
                            variant="secondary"
                            onClick={() =>
                              setCreatingForProviderId(provider.id)
                            }
                          >
                            <Plus size={ICON.xs} />
                            添加模型
                          </Button>
                          <Button
                            type="button"
                            variant="secondary"
                            disabled={providerBusyId === provider.id}
                            onClick={() => setEditingProvider(provider)}
                          >
                            <Pencil size={ICON.xs} />
                            编辑服务商
                          </Button>
                          <Button
                            type="button"
                            variant="secondary"
                            className="danger-text"
                            disabled={
                              providerBusyId === provider.id ||
                              providerModels.length > 0
                            }
                            title={
                              providerModels.length > 0
                                ? "请先迁移或移除该服务商下的模型后再删除"
                                : "删除服务商"
                            }
                            onClick={() =>
                              void handleDeleteProvider(provider.id)
                            }
                          >
                            <Trash2 size={ICON.xs} />
                            删除服务商
                          </Button>
                        </div>
                      </div>
                      {providerModels.length === 0 ? (
                        <p className="gateway-provider-models-empty">
                          该服务商暂未添加模型，点击「添加模型」即可直接复用连接与密钥。
                        </p>
                      ) : (
                        <div className="gateway-models-list">
                          {providerModels.map((item) => {
                            const modelTest = modelTests[item.id];
                            const effectiveEnabled =
                              item.effectiveEnabled ??
                              (item.enabled && provider.enabled);
                            return (
                              <div
                                key={item.id}
                                className="gateway-model-card"
                              >
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
                                    {item.enabled && !provider.enabled && (
                                      <span className="gateway-model-paused-hint">
                                        随服务商停用
                                      </span>
                                    )}
                                  </div>
                                  {item.description && (
                                    <p>{item.description}</p>
                                  )}
                                  <small>
                                    上游模型：{item.model} · 上下文：
                                    {item.contextWindowTokens} Token
                                  </small>
                                  {modelTest && (
                                    <div
                                      className={`gateway-model-test-result ${modelTest.ok ? "ok" : "failed"}`}
                                      role={modelTest.ok ? "status" : "alert"}
                                    >
                                      {modelTest.ok
                                        ? `连接测试通过 · ${modelTest.model} · ${Math.round(modelTest.latencyMs)} ms`
                                        : modelTest.error ||
                                          "连接测试未通过，请检查服务商地址、密钥和上游模型 ID。"}
                                    </div>
                                  )}
                                </div>
                                <div className="gateway-model-actions">
                                  {!item.isDefault && (
                                    <Button
                                      type="button"
                                      variant="secondary"
                                      disabled={modelBusyId === item.id}
                                      onClick={() =>
                                        void handleSetDefaultModel(item.id)
                                      }
                                    >
                                      设为默认
                                    </Button>
                                  )}
                                  <Button
                                    type="button"
                                    variant="secondary"
                                    busy={Boolean(testingModels[item.id])}
                                    disabled={
                                      modelBusyId === item.id ||
                                      !item.configured ||
                                      !effectiveEnabled
                                    }
                                    onClick={() =>
                                      void handleTestModel(item.id)
                                    }
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
                                    onClick={() =>
                                      void handleDeleteModel(item.id)
                                    }
                                  >
                                    <Trash2 size={ICON.xs} />
                                    移除
                                  </Button>
                                </div>
                              </div>
                            );
                          })}
                        </div>
                      )}
                    </div>
                  );
                })}
              </div>
            )}
          </section>
          <WebSearchConfigurationFields
            key={`${user!.id}:${query.data.webSearch.updatedAt}`}
            initial={query.data.webSearch}
            onSave={save}
            error={error}
            busy={searchBusy}
          />
          <div className="gateway-test-panel">
            <div>
              <h3>测试已保存的配置</h3>
              <p>使用当前生效的默认模型发送 1 次请求，可能产生费用。</p>
            </div>
            <Button
              className="secondary"
              busy={testing}
              disabled={
                !query.data.config.configured || !query.data.config.enabled
              }
              onClick={() => void testConnection()}
            >
              测试连接
            </Button>
          </div>
          {creatingProvider && (
            <ProviderProfileDialog
              onSave={handleCreateProvider}
              onClose={() => setCreatingProvider(false)}
            />
          )}
          {editingProvider && (
            <ProviderProfileDialog
              key={editingProvider.id}
              initial={editingProvider}
              onSave={(value) =>
                handleUpdateProvider(editingProvider.id, value)
              }
              onClose={() => setEditingProvider(null)}
            />
          )}
          {creatingForProviderId !== null && providers.length > 0 && (
            <ModelProfileDialog
              providers={providers}
              defaultProviderId={creatingForProviderId}
              onSave={handleCreateModel}
              onClose={() => setCreatingForProviderId(null)}
            />
          )}
          {editingModel && (
            <ModelProfileDialog
              key={editingModel.id}
              providers={providers}
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
