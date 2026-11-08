import { Pencil, Star, Trash2 } from "lucide-react";
import type {
  AdminModelProfile,
  AdminModelProvider,
  ModelGateway,
} from "@teamie/api-client";
import { AgentEnabledStatus } from "../agent-ui";
import { ICON } from "../icons";
import { Button } from "../ui";

export type ModelTestOutcome = {
  ok: boolean;
  model: string;
  latencyMs: number;
  error?: string;
  errorCode?: string;
  testedAt?: string;
  baselineTestedAt?: string;
  baselineModelUpdatedAt?: string;
  baselineProviderUpdatedAt?: string;
};

export type ResolvedModelTest = {
  ok: boolean;
  model: string;
  latencyMs?: number;
  testedAt?: string;
  errorCode?: string;
  error?: string;
};

const MODEL_TEST_ERROR_MESSAGES: Record<string, string> = {
  provider_auth_failed: "认证失败（请检查 API Key 或权限配置）",
  provider_rate_limited: "请求触发限流（请稍后重试或检查配额）",
  upstream_rate_limited: "请求触发限流（请稍后重试或检查配额）",
  provider_bad_request: "请求参数被拒绝（请检查模型名称或协议兼容性）",
  provider_timeout: "连接超时（上游响应过慢）",
  upstream_timeout: "连接超时（上游响应过慢）",
  provider_unreachable: "无法连接上游服务（请检查 Base URL 或网络）",
  upstream_unavailable: "无法连接上游服务（请检查 Base URL 或网络）",
  provider_upstream_error: "上游服务异常（5xx 错误）",
  upstream_http_error: "上游服务异常（5xx 错误）",
  upstream_error: "上游服务异常（5xx 错误）",
  provider_invalid_response: "上游返回格式无效（非预期响应结构）",
  gateway_misconfigured: "网关配置不完整（请检查提供商与密钥）",
};

export function formatModelTestErrorCode(errorCode?: string): string {
  const code = errorCode?.trim();
  if (!code) {
    return "连接测试未通过，请检查服务商地址、密钥和上游模型 ID。";
  }
  return MODEL_TEST_ERROR_MESSAGES[code] ?? `测试未通过（${code}）`;
}

export function formatModelTestedAt(testedAt?: string): string {
  const raw = testedAt?.trim();
  if (!raw) return "";
  const isoMatch = /^(\d{4}-\d{2}-\d{2})[T ](\d{2}:\d{2})/.exec(raw);
  if (isoMatch) {
    return `${isoMatch[1]} ${isoMatch[2]}`;
  }
  const parsed = new Date(raw);
  if (Number.isNaN(parsed.getTime())) return raw;
  const yyyy = parsed.getUTCFullYear();
  const mm = String(parsed.getUTCMonth() + 1).padStart(2, "0");
  const dd = String(parsed.getUTCDate()).padStart(2, "0");
  const hh = String(parsed.getUTCHours()).padStart(2, "0");
  const min = String(parsed.getUTCMinutes()).padStart(2, "0");
  return `${yyyy}-${mm}-${dd} ${hh}:${min}`;
}

export function isInMemoryTestStale(
  item: AdminModelProfile,
  inMemory?: ModelTestOutcome,
  provider?: AdminModelProvider,
): boolean {
  if (!inMemory) return false;
  const modelAdvanced = Boolean(
    inMemory.baselineModelUpdatedAt &&
      item.updatedAt &&
      item.updatedAt > inMemory.baselineModelUpdatedAt,
  );
  const providerAdvanced = Boolean(
    inMemory.baselineProviderUpdatedAt &&
      provider?.updatedAt &&
      provider.updatedAt > inMemory.baselineProviderUpdatedAt,
  );
  const serverCleared =
    inMemory.baselineTestedAt !== undefined && item.lastTest === undefined;
  return modelAdvanced || providerAdvanced || serverCleared;
}

export function resolveModelLastTest(
  item: AdminModelProfile,
  inMemory?: ModelTestOutcome,
  provider?: AdminModelProvider,
): ResolvedModelTest | null {
  const persisted = item.lastTest;
  if (inMemory && !isInMemoryTestStale(item, inMemory, provider)) {
    const serverUpdated =
      persisted !== undefined &&
      persisted.testedAt !== inMemory.baselineTestedAt;
    if (serverUpdated) {
      return {
        ok: persisted.ok,
        model: inMemory.model || item.model,
        latencyMs: persisted.latencyMs ?? inMemory.latencyMs,
        testedAt: persisted.testedAt,
        errorCode: persisted.errorCode ?? inMemory.errorCode,
        error: inMemory.error,
      };
    }
    return {
      ok: inMemory.ok,
      model: inMemory.model || item.model,
      latencyMs: inMemory.latencyMs,
      testedAt: inMemory.testedAt,
      errorCode: inMemory.errorCode,
      error: inMemory.error,
    };
  }
  if (!persisted) return null;
  return {
    ok: persisted.ok,
    model: item.model,
    latencyMs: persisted.latencyMs,
    testedAt: persisted.testedAt,
    errorCode: persisted.errorCode,
  };
}

export function clearModelsLastTest(
  gateway: ModelGateway,
  modelIds: string[],
): ModelGateway {
  if (!gateway.models || modelIds.length === 0) return gateway;
  const affected = new Set(modelIds);
  return {
    ...gateway,
    models: gateway.models.map((item) => {
      if (!affected.has(item.id) || !item.lastTest) return item;
      const next = { ...item };
      delete next.lastTest;
      return next;
    }),
  };
}

export function GatewayModelCard({
  item,
  provider,
  inMemoryTest,
  busy,
  testing,
  onSetDefault,
  onTest,
  onEdit,
  onDelete,
}: {
  item: AdminModelProfile;
  provider: AdminModelProvider;
  inMemoryTest?: ModelTestOutcome;
  busy: boolean;
  testing: boolean;
  onSetDefault: () => void;
  onTest: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const resolvedTest = resolveModelLastTest(item, inMemoryTest, provider);
  const effectiveEnabled =
    item.effectiveEnabled ?? (item.enabled && provider.enabled);
  const formattedTime = formatModelTestedAt(resolvedTest?.testedAt);
  const hasLatency =
    resolvedTest?.latencyMs !== undefined &&
    (resolvedTest.ok || resolvedTest.latencyMs > 0);
  const roundedLatency = hasLatency
    ? Math.round(resolvedTest.latencyMs!)
    : null;

  return (
    <div className="gateway-model-card">
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
            <span className="gateway-model-paused-hint">随服务商停用</span>
          )}
          {resolvedTest && (
            <span
              className={`gateway-model-test-badge ${resolvedTest.ok ? "ok" : "failed"}`}
            >
              {resolvedTest.ok ? "测试通过" : "测试失败"}
              {roundedLatency !== null ? ` · ${roundedLatency}ms` : ""}
            </span>
          )}
        </div>
        {item.description && <p>{item.description}</p>}
        <small>
          上游模型：{item.model} · 上下文：{item.contextWindowTokens} Token
        </small>
        {resolvedTest && (
          <div
            className={`gateway-model-test-result ${resolvedTest.ok ? "ok" : "failed"}`}
            role={resolvedTest.ok ? "status" : "alert"}
          >
            {resolvedTest.ok
              ? `连接测试通过 · ${resolvedTest.model} · ${roundedLatency ?? 0} ms${formattedTime ? ` · 测试时间：${formattedTime}` : ""}`
              : `${resolvedTest.errorCode ? formatModelTestErrorCode(resolvedTest.errorCode) : resolvedTest.error || formatModelTestErrorCode()}${roundedLatency !== null ? ` · ${roundedLatency} ms` : ""}${formattedTime ? ` · 测试时间：${formattedTime}` : ""}`}
          </div>
        )}
      </div>
      <div className="gateway-model-actions">
        {!item.isDefault && (
          <Button
            type="button"
            variant="secondary"
            disabled={busy}
            onClick={onSetDefault}
          >
            设为默认
          </Button>
        )}
        <Button
          type="button"
          variant="secondary"
          busy={testing}
          disabled={busy || !item.configured || !effectiveEnabled}
          onClick={onTest}
        >
          测试此模型
        </Button>
        <Button
          type="button"
          variant="secondary"
          disabled={busy}
          onClick={onEdit}
        >
          <Pencil size={ICON.xs} />
          编辑
        </Button>
        <Button
          type="button"
          variant="secondary"
          className="danger-text"
          disabled={busy}
          onClick={onDelete}
        >
          <Trash2 size={ICON.xs} />
          移除
        </Button>
      </div>
    </div>
  );
}
