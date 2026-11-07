import type {
  AdminModelProfile,
  AdminModelProvider,
} from "@teamie/api-client";
import { ConfirmDialog } from "../ui";

export type GatewayConfirmation =
  | { kind: "deleteProvider"; provider: AdminModelProvider }
  | { kind: "deleteModel"; model: AdminModelProfile };

/**
 * Destructive gateway catalog changes. Deleting never rewrites the configured
 * default routing or any Agent `modelId`; affected calls fall back instead,
 * so the copy explains the fallback rather than promising a migration.
 */
export function GatewayConfirmDialog({
  confirmation,
  busy,
  error,
  onCancel,
  onConfirm,
}: {
  confirmation: GatewayConfirmation;
  busy: boolean;
  error?: string;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  if (confirmation.kind === "deleteProvider") {
    const { provider } = confirmation;
    return (
      <ConfirmDialog
        tone="danger"
        title={`删除服务商「${provider.displayName}」？`}
        impact={[
          "服务商连接地址与已保存的 API Key 将被永久删除。",
          "删除后无法恢复，需要时请重新添加并填写密钥。",
        ]}
        confirmLabel="确认删除"
        busy={busy}
        error={error}
        onCancel={onCancel}
        onConfirm={onConfirm}
      />
    );
  }
  const { model } = confirmation;
  return (
    <ConfirmDialog
      tone="danger"
      title={`移除模型「${model.displayName}」？`}
      impact={[
        "该模型将从 Agent 可选目录中移除，选择了它的 Agent 会自动回退到平台默认模型。",
        ...(model.isDefault
          ? [
              "这是当前的平台默认模型。移除后默认设置不会自动改写，所有回退调用将改用网关自动选择的其他可用模型；建议先将其他模型设为默认。",
            ]
          : []),
        "移除后无法恢复，需要时请重新添加。",
      ]}
      confirmLabel="确认移除"
      busy={busy}
      error={error}
      onCancel={onCancel}
      onConfirm={onConfirm}
    />
  );
}
