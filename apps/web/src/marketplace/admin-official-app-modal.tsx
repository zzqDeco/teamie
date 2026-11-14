import { useState } from "react";
import type { FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { api } from "@teamie/api-client";
import { errorText } from "../errors";
import { Button, Modal, ModalCloseButton, Notice, SwitchField } from "../ui";
import { toast } from "../toast";
import { ICON } from "../icons";
import {
  MARKETPLACE_DESCRIPTION_MAX_LENGTH,
  MARKETPLACE_SLUG_HELP,
  sanitizeMcpToolSegment,
  validateMarketplaceSlug,
} from "./marketplace-labels";

export function AdminOfficialAppModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (issued: { appName: string; secret: string }) => void;
}) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [description, setDescription] = useState("");
  const [webUrl, setWebUrl] = useState("");
  const [mcpUrl, setMcpUrl] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const dirty =
    Boolean(name.trim()) ||
    Boolean(slug.trim()) ||
    Boolean(description.trim()) ||
    Boolean(webUrl.trim()) ||
    Boolean(mcpUrl.trim()) ||
    !enabled;

  const toolNamespace = slug.trim()
    ? `mcp__${sanitizeMcpToolSegment(slug)}__<tool>`
    : "mcp__<slug>__<tool>";

  async function handleCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy) return;
    const trimmedName = name.trim();
    const normalizedSlug = slug.trim().toLowerCase();
    const trimmedWeb = webUrl.trim();
    const trimmedMcp = mcpUrl.trim();
    if (!trimmedName) {
      setError("请填写应用名称。");
      return;
    }
    const slugError = validateMarketplaceSlug(normalizedSlug);
    if (slugError) {
      setError(slugError);
      return;
    }
    if (!trimmedWeb || !trimmedMcp) {
      setError("请填写完整的 Web UI 地址与 MCP Server 地址。");
      return;
    }
    setBusy(true);
    setError("");
    try {
      const created = await api.createAdminMarketplaceApp({
        name: trimmedName,
        slug: normalizedSlug,
        description: description.trim(),
        webUrl: trimmedWeb,
        mcpUrl: trimmedMcp,
        enabled,
      });
      await queryClient.invalidateQueries({ queryKey: ["adminMarketplaceApps"] });
      await queryClient.invalidateQueries({ queryKey: ["marketplaceCatalog"] });
      await queryClient.invalidateQueries({ queryKey: ["workspaceApps"] });
      await queryClient.invalidateQueries({ queryKey: ["conversationApps"] });
      toast.success(`已上架官方应用「${created.app.name}」`);
      onCreated({
        appName: created.app.name,
        secret: created.signingSecret,
      });
    } catch (e) {
      const msg = errorText(e);
      setError(msg);
      toast.error(msg);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title="上架官方应用"
      subtitle="注册官方直发小组应用并签发一次性 HMAC-SHA256 签名密钥。"
      className="mp-modal-wide"
      busy={busy}
      dirty={dirty}
      onClose={onClose}
    >
      <form className="settings-form" noValidate onSubmit={handleCreate}>
        <Notice>{error}</Notice>
        <label>
          应用名称
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="例如：协作桌游面板"
            maxLength={80}
            required
          />
        </label>
        <label>
          唯一标识（Slug）
          <input
            value={slug}
            onChange={(e) => setSlug(e.target.value)}
            placeholder="例如：board-room"
            maxLength={64}
            required
          />
          <small className="field-help">
            {MARKETPLACE_SLUG_HELP} 当前工具前缀：{toolNamespace}
          </small>
        </label>
        <label>
          应用简介
          <input
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="简要说明该小组应用的用途与交互方式"
            maxLength={MARKETPLACE_DESCRIPTION_MAX_LENGTH}
          />
        </label>
        <label>
          Web UI 地址（HTTPS / HTTP）
          <input
            type="url"
            value={webUrl}
            onChange={(e) => setWebUrl(e.target.value)}
            placeholder="https://app.example.com/ui"
            required
          />
        </label>
        <label>
          MCP Server 地址（Streamable HTTP）
          <input
            type="url"
            value={mcpUrl}
            onChange={(e) => setMcpUrl(e.target.value)}
            placeholder="https://app.example.com/mcp"
            required
          />
        </label>
        <SwitchField
          checked={enabled}
          title="立即启用"
          description="启用后，工作空间所有者即可在目录中浏览并安装该应用。"
          onChange={setEnabled}
        />
        <div className="modal-actions">
          <ModalCloseButton>取消</ModalCloseButton>
          <Button
            type="submit"
            busy={busy}
            disabled={!name.trim() || !slug.trim() || !webUrl.trim() || !mcpUrl.trim()}
          >
            <Plus size={ICON.sm} /> 注册应用
          </Button>
        </div>
      </form>
    </Modal>
  );
}
