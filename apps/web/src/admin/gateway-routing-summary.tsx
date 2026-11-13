import { AlertTriangle, CheckCheck, Info } from "lucide-react";
import type {
  AdminModelProfile,
  ModelRoutingConfigDTO,
} from "@teamie/api-client";
import { ICON } from "../icons";

export function GatewayRoutingSummary({
  routing,
  defaultModelId,
  models,
}: {
  routing?: ModelRoutingConfigDTO;
  defaultModelId?: string;
  models: AdminModelProfile[];
}) {
  if (models.length === 0) return null;

  const configuredId = (routing?.defaultModelId ?? defaultModelId ?? "").trim();
  const effectiveId = (routing?.effectiveModelId ?? "").trim();
  const configuredModel = configuredId
    ? models.find((m) => m.id === configuredId)
    : undefined;
  const effectiveModel = effectiveId
    ? models.find((m) => m.id === effectiveId)
    : undefined;

  const formatModelLabel = (id: string, profile?: AdminModelProfile) =>
    profile ? `${profile.displayName}（${id}）` : id;

  const configuredUnavailable = Boolean(
    configuredId &&
      (!configuredModel ||
        !configuredModel.configured ||
        configuredModel.effectiveEnabled === false ||
        !configuredModel.enabled),
  );
  const isFallbackActive =
    Boolean(configuredId && configuredId !== effectiveId) ||
    configuredUnavailable;

  if (isFallbackActive) {
    return (
      <div className="gateway-routing-banner warning" role="status">
        <AlertTriangle size={ICON.sm} aria-hidden="true" />
        <div>
          <b>默认路由已触发自动回退</b>
          <p>
            管理员设定的默认模型「
            {formatModelLabel(configuredId, configuredModel)}
            」当前已停用、缺少密钥或已被移除。
            {effectiveId
              ? `当前实际生效模型：${formatModelLabel(effectiveId, effectiveModel)}。`
              : "当前无任何可用回退模型，Agent 请求将暂时无法响应，请启用可用模型或配置密钥。"}
          </p>
        </div>
      </div>
    );
  }

  if (!configuredId) {
    return (
      <div className="gateway-routing-banner" role="status">
        <Info size={ICON.sm} aria-hidden="true" />
        <div>
          <b>自动默认路由</b>
          <p>
            尚未固定平台默认模型。
            {effectiveId
              ? `当前实际生效模型：${formatModelLabel(effectiveId, effectiveModel)}。`
              : "当前尚无已启用且配置好密钥的可用模型。"}
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="gateway-routing-banner ok" role="status">
      <CheckCheck size={ICON.sm} aria-hidden="true" />
      <div>
        <b>默认模型路由正常</b>
        <p>
          当前实际生效默认模型：
          {formatModelLabel(
            effectiveId || configuredId,
            effectiveModel || configuredModel,
          )}
          。
        </p>
      </div>
    </div>
  );
}
