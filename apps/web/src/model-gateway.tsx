import { useRef, useState } from "react";
import { Link } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  CheckCheck,
  Clock3,
  KeyRound,
  LoaderCircle,
  Plus,
  Server,
  ShieldCheck,
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
import { WebSearchConfigurationFields } from "./agent-ui";
import type { WebSearchFormValue } from "./agent-ui";
import { Button, EmptyState, Notice } from "./ui";
import { ICON } from "./icons";
import {
  clearModelsLastTest,
  resolveModelLastTest,
} from "./admin/model-last-test";
import type { ModelTestOutcome } from "./admin/model-last-test";
import { ModelProfileDialog } from "./admin/model-profile-dialog";
import { ProviderProfileDialog } from "./admin/provider-profile-dialog";
import { GatewayConfirmDialog } from "./admin/gateway-confirm";
import type { GatewayConfirmation } from "./admin/gateway-confirm";
import { GatewayProviderCard } from "./admin/gateway-provider-card";
import { GatewayRoutingSummary } from "./admin/gateway-routing-summary";

export function ModelGatewaySettings() {
  const { user } = useAuth();
  const client = useQueryClient();
  const admin = canConfigureModelGateway(user);
  const [denied, setDenied] = useState(false),
    [searchBusy, setSearchBusy] = useState(false),
    [searchError, setSearchError] = useState(""),
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
    [saved, setSaved] = useState(false),
    [confirmation, setConfirmation] = useState<GatewayConfirmation | null>(null);
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
    setSearchError("");
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
        setSearchError(gatewayFailureText(e));
      }
      return false;
    } finally {
      setSearchBusy(false);
    }
  }
  async function testConnection() {
    const sessionToken = api.csrfToken;
    const version = ++defaultTestVersion.current;
    const current = () =>
      sessionToken === api.csrfToken &&
      defaultTestVersion.current === version;
    setTesting(true);
    setError("");
    setTest(null);
    try {
      const result = await api.testModelGateway();
      if (current()) {
        setTest({
          ok: result.ok,
          model: result.model,
          latencyMs: result.latencyMs,
        });
        await client.invalidateQueries({ queryKey: key });
      }
    } catch (e) {
      if (current()) {
        if (e instanceof ApiError && e.status === 403) setDenied(true);
        setError(gatewayFailureText(e));
        if (!(e instanceof ApiError && e.status === 403)) {
          await client.invalidateQueries({ queryKey: key });
        }
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
    const affectedIds = (query.data?.models || [])
      .filter((m) => (m.providerId || m.id) === providerId)
      .map((m) => m.id);
    invalidateTests(affectedIds);
    await syncGatewayResponse(clearModelsLastTest(response, affectedIds));
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
        setConfirmation(null);
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
    await syncGatewayResponse(clearModelsLastTest(response, [profileId]));
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
        setConfirmation(null);
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
  function requestSetDefaultModel(item: AdminModelProfile, provider: AdminModelProvider) {
    const effectiveEnabled =
      item.effectiveEnabled ?? (item.enabled && provider.enabled);
    if (!item.configured || !effectiveEnabled) {
      setError("");
      setConfirmation({ kind: "setUnavailableDefault", model: item });
      return;
    }
    void handleSetDefaultModel(item.id);
  }
  async function handleDeleteModel(profileId: string) {
    const sessionToken = api.csrfToken;
    setModelBusyId(profileId);
    setError("");
    try {
      const response = await api.deleteModelProfile(profileId);
      if (sessionToken === api.csrfToken) {
        setConfirmation(null);
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
    const targetModel = query.data?.models?.find((m) => m.id === profileId);
    const targetProvider = query.data?.providers?.find(
      (p) => p.id === (targetModel?.providerId || profileId),
    );
    const baselineTestedAt = targetModel?.lastTest?.testedAt;
    const baselineModelUpdatedAt = targetModel?.updatedAt;
    const baselineProviderUpdatedAt = targetProvider?.updatedAt;
    const current = () =>
      sessionToken === api.csrfToken &&
      testVersions.current.get(profileId) === version;
    setTestingModels((prev) => ({ ...prev, [profileId]: true }));
    setError("");
    setSaved(false);
    try {
      const result = await api.testModelProfile(profileId);
      if (current()) {
        setModelTests((prev) => ({
          ...prev,
          [profileId]: {
            ok: result.ok,
            model: result.model,
            latencyMs: result.latencyMs,
            baselineTestedAt,
            baselineModelUpdatedAt,
            baselineProviderUpdatedAt,
          },
        }));
        await client.invalidateQueries({ queryKey: key });
      }
    } catch (e) {
      if (current()) {
        const message = gatewayFailureText(e);
        setModelTests((prev) => ({
          ...prev,
          [profileId]: {
            ok: false,
            model: "",
            latencyMs: 0,
            error: message,
            errorCode: e instanceof ApiError ? e.code : undefined,
            baselineTestedAt,
            baselineModelUpdatedAt,
            baselineProviderUpdatedAt,
          },
        }));
        await client.invalidateQueries({ queryKey: key });
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
    test?.ok ||
      (test === null &&
        models.some((m) => {
          const p = providers.find((pr) => pr.id === (m.providerId || m.id));
          return resolveModelLastTest(m, modelTests[m.id], p)?.ok === true;
        })),
  );
  return (
    <div className="admin-view gateway-view">
      <div className="admin-heading">
        <div>
          <span className="platform-role-badge">
            <ShieldCheck size={ICON.xs} aria-hidden="true" />
            平台管理员
          </span>
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
          <Button variant="secondary" onClick={() => void query.refetch()}>
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
          <GatewayRoutingSummary
            routing={query.data.routing}
            defaultModelId={query.data.defaultModelId}
            models={models}
          />
          <Notice>{confirmation ? "" : error}</Notice>
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
              <EmptyState
                icon={<Server size={ICON.xl} />}
                title="暂未配置模型服务商"
                description="请先添加服务商连接，再在其下添加多个可用模型。"
              />
            ) : (
              <div className="gateway-providers-list">
                {providers.map((provider) => {
                  const providerModels = models.filter(
                    (m) => (m.providerId || m.id) === provider.id,
                  );
                  return (
                    <GatewayProviderCard
                      key={provider.id}
                      provider={provider}
                      models={providerModels}
                      providerBusy={providerBusyId === provider.id}
                      modelBusyId={modelBusyId}
                      modelTests={modelTests}
                      testingModels={testingModels}
                      onAddModel={() => setCreatingForProviderId(provider.id)}
                      onEditProvider={() => setEditingProvider(provider)}
                      onDeleteProvider={() => {
                        setError("");
                        setConfirmation({ kind: "deleteProvider", provider });
                      }}
                      onRequestSetDefaultModel={(item) =>
                        requestSetDefaultModel(item, provider)
                      }
                      onTestModel={(id) => void handleTestModel(id)}
                      onEditModel={(item) => setEditingModel(item)}
                      onDeleteModel={(item) => {
                        setError("");
                        setConfirmation({ kind: "deleteModel", model: item });
                      }}
                    />
                  );
                })}
              </div>
            )}
          </section>
          <WebSearchConfigurationFields
            key={`${user!.id}:${query.data.webSearch.updatedAt}`}
            initial={query.data.webSearch}
            onSave={save}
            error={searchError}
            busy={searchBusy}
          />
          <div className="gateway-test-panel">
            <div>
              <h3>测试已保存的配置</h3>
              <p>使用当前生效的默认模型发送 1 次请求，可能产生费用。</p>
            </div>
            <Button
              variant="secondary"
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
          {confirmation && (
            <GatewayConfirmDialog
              confirmation={confirmation}
              busy={
                confirmation.kind === "deleteProvider"
                  ? providerBusyId === confirmation.provider.id
                  : modelBusyId === confirmation.model.id
              }
              error={error}
              onCancel={() => setConfirmation(null)}
              onConfirm={() =>
                void (confirmation.kind === "deleteProvider"
                  ? handleDeleteProvider(confirmation.provider.id)
                  : confirmation.kind === "setUnavailableDefault"
                    ? handleSetDefaultModel(confirmation.model.id)
                    : handleDeleteModel(confirmation.model.id))
              }
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
