import { Pencil, Plus, Server, Trash2 } from "lucide-react";
import type {
  AdminModelProfile,
  AdminModelProvider,
} from "@teamie/api-client";
import { AgentEnabledStatus } from "../agent-ui";
import { Button } from "../ui";
import { ICON } from "../icons";
import { GatewayModelCard } from "./model-last-test";
import type { ModelTestOutcome } from "./model-last-test";

export function GatewayProviderCard({
  provider,
  models,
  providerBusy,
  modelBusyId,
  modelTests,
  testingModels,
  onAddModel,
  onEditProvider,
  onDeleteProvider,
  onRequestSetDefaultModel,
  onTestModel,
  onEditModel,
  onDeleteModel,
}: {
  provider: AdminModelProvider;
  models: AdminModelProfile[];
  providerBusy: boolean;
  modelBusyId: string | null;
  modelTests: Record<string, ModelTestOutcome>;
  testingModels: Record<string, boolean>;
  onAddModel: () => void;
  onEditProvider: () => void;
  onDeleteProvider: () => void;
  onRequestSetDefaultModel: (model: AdminModelProfile) => void;
  onTestModel: (modelId: string) => void;
  onEditModel: (model: AdminModelProfile) => void;
  onDeleteModel: (model: AdminModelProfile) => void;
}) {
  return (
    <div className="gateway-provider-card">
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
            地址：{provider.baseUrl || "未配置"} · 关联模型：{models.length} 个
          </small>
        </div>
        <div className="gateway-provider-actions">
          <Button type="button" variant="secondary" onClick={onAddModel}>
            <Plus size={ICON.xs} />
            添加模型
          </Button>
          <Button
            type="button"
            variant="secondary"
            disabled={providerBusy}
            onClick={onEditProvider}
          >
            <Pencil size={ICON.xs} />
            编辑服务商
          </Button>
          <Button
            type="button"
            variant="secondary"
            className="danger-text"
            disabled={providerBusy || models.length > 0}
            title={
              models.length > 0
                ? "请先迁移或移除该服务商下的模型后再删除"
                : "删除服务商"
            }
            onClick={onDeleteProvider}
          >
            <Trash2 size={ICON.xs} />
            删除服务商
          </Button>
        </div>
      </div>
      {models.length === 0 ? (
        <p className="gateway-provider-models-empty">
          该服务商暂未添加模型，点击「添加模型」即可直接复用连接与密钥。
        </p>
      ) : (
        <div className="gateway-models-list">
          {models.map((item) => (
            <GatewayModelCard
              key={item.id}
              item={item}
              provider={provider}
              inMemoryTest={modelTests[item.id]}
              busy={modelBusyId === item.id}
              testing={Boolean(testingModels[item.id])}
              onSetDefault={() => onRequestSetDefaultModel(item)}
              onTest={() => onTestModel(item.id)}
              onEdit={() => onEditModel(item)}
              onDelete={() => onDeleteModel(item)}
            />
          ))}
        </div>
      )}
    </div>
  );
}
