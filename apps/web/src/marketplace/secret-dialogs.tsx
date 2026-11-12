import { useState } from "react";
import { CheckCheck, Copy } from "lucide-react";
import { copyToClipboard } from "../clipboard";
import { Button, ConfirmDialog, Modal, Notice } from "../ui";
import { ICON } from "../icons";

/**
 * Confirms a signing-secret rotation. The previous secret stops verifying
 * immediately, so the external service must be updated right after.
 */
export function RotateSecretConfirm({
  appName,
  published,
  busy,
  error,
  onCancel,
  onConfirm,
}: {
  appName: string;
  published: boolean;
  busy: boolean;
  error?: string;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  return (
    <ConfirmDialog
      tone="danger"
      title={`轮换「${appName}」的签名密钥？`}
      impact={[
        "旧密钥将立即失效，外部服务用旧密钥校验 Context Token 会立刻失败。",
        "请先准备好更新外部服务的 TEAMIE_APP_SIGNING_SECRET 配置，新密钥只会显示一次。",
        ...(published
          ? ["该应用已上架，在外部服务完成更新前，所有已安装它的工作空间都会中断。"]
          : []),
      ]}
      confirmLabel="轮换密钥"
      busy={busy}
      error={error}
      onCancel={onCancel}
      onConfirm={onConfirm}
    />
  );
}

/**
 * Shows a newly issued signing secret once. Escape, the backdrop and the X
 * are blocked; only the explicit 「我已保存」 acknowledgement closes it.
 */
export function IssuedSecretDialog({
  appName,
  secret,
  onAcknowledge,
}: {
  appName: string;
  secret: string;
  onAcknowledge: () => void;
}) {
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState("");
  async function copy() {
    try {
      await copyToClipboard(secret);
      setCopied(true);
      setCopyError("");
    } catch {
      setCopyError("复制未成功，请手动选中密钥复制。");
    }
  }
  return (
    <Modal
      title={`「${appName}」的签名密钥`}
      subtitle="此 HMAC-SHA256 密钥仅显示一次。关闭后无法再次查看，请先保存并配置为外部服务的 TEAMIE_APP_SIGNING_SECRET。"
      dismissible={false}
      onClose={onAcknowledge}
    >
      <div className="copy-input">
        <input
          value={secret}
          readOnly
          aria-label="应用签名密钥"
          onFocus={(e) => e.target.select()}
        />
        <button
          type="button"
          onClick={() => void copy()}
          aria-label="复制签名密钥"
        >
          {copied ? <CheckCheck size={ICON.md} /> : <Copy size={ICON.md} />}
        </button>
      </div>
      <Notice>{copyError}</Notice>
      <div className="modal-actions">
        <Button type="button" data-autofocus onClick={onAcknowledge}>
          我已保存
        </Button>
      </div>
    </Modal>
  );
}
