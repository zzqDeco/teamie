import { useRef, useState } from "react";
import type { FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { EyeOff, Hash, LoaderCircle, RefreshCw } from "lucide-react";
import { api } from "@teamie/api-client";
import type { Agent, ModelCatalog, Workspace } from "@teamie/api-client";
import { useAuth } from "../auth";
import { useChat } from "../chat";
import { WorkspaceVisibilityFields } from "../agent-ui";
import {
  agentFailureText,
  agentProfileChanges,
  agentProfileInput,
  canConfigureModelGateway,
  snapshotAgentProfile,
} from "../agent-management";
import { Button, ConfirmDialog, Modal, ModalCloseButton, Notice, SwitchField } from "../ui";
import { toast } from "../toast";
import { ICON } from "../icons";

export function WorkspaceChips({
  ids,
  workspaces,
  interactive = false,
}: {
  ids: string[];
  workspaces: Workspace[];
  interactive?: boolean;
}) {
  return (
    <div className="agent-workspace-chips">
      {ids.length ? (
        ids.map((id) => {
          const ws = workspaces.find((w) => w.id === id);
          if (interactive && ws) {
            return (
              <Link
                key={id}
                to={`/app/w/${id}/members`}
                aria-label={`查看 ${ws.name} 成员目录`}
              >
                <Hash size={ICON.xs} />
                {ws.name}
              </Link>
            );
          }
          return (
            <span key={id}>
              <Hash size={ICON.xs} />
              {ws?.name || "工作空间"}
            </span>
          );
        })
      ) : (
        <span className="private-agent-chip">
          <EyeOff size={ICON.xs} />
          未选择工作空间
        </span>
      )}
    </div>
  );
}
export function AgentProfileDialog({
  agent,
  workspaces,
  initialWorkspaceId,
  onClose,
}: {
  agent?: Agent;
  workspaces: Workspace[];
  initialWorkspaceId?: string;
  onClose: () => void;
}) {
  const client = useQueryClient();
  const { data } = useChat();
  const { user } = useAuth();
  const navigate = useNavigate();
  const [initialAgent] = useState(() =>
    agent ? snapshotAgentProfile(agent) : undefined,
  );
  const [name, setName] = useState(initialAgent?.name || ""),
    [description, setDescription] = useState(initialAgent?.description || ""),
    [modelId, setModelId] = useState(initialAgent?.modelId || ""),
    [enabled, setEnabled] = useState(initialAgent?.enabled ?? true);
  const catalogQuery = useQuery<ModelCatalog>({
    queryKey: ["modelCatalog", user!.id],
    queryFn: () => api.modelCatalog(),
    retry: false,
  });
  const catalogModels = catalogQuery.data?.models || [];
  const defaultModel = catalogModels.find(
    (item) => item.isDefault || item.id === catalogQuery.data?.defaultModelId,
  );
  // A new Agent starts visible in the workspace the owner came from (only if
  // it is a current membership), or the only workspace they belong to.
  const [workspaceIds, setWorkspaceIds] = useState(() =>
    initialAgent
      ? initialAgent.workspaceIds.filter((id) =>
          workspaces.some((w) => w.id === id),
        )
      : initialWorkspaceId && workspaces.some((w) => w.id === initialWorkspaceId)
        ? [initialWorkspaceId]
        : workspaces.length === 1
          ? [workspaces[0].id]
          : [],
  );
  const createIntent = useRef<{ fingerprint: string; key: string } | null>(null);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [dirty, setDirty] = useState(false),
    [confirmingHidden, setConfirmingHidden] = useState(false);
  const hidden = workspaceIds.length === 0;
  // Saving with no workspace makes the Agent undiscoverable; ask once unless
  // an existing Agent was already hidden and its visibility is unchanged.
  const needsHiddenConfirm =
    hidden && (!initialAgent || initialAgent.workspaceIds.length > 0);
  function submit(event: FormEvent) {
    event.preventDefault();
    if (busy) return;
    if (needsHiddenConfirm) setConfirmingHidden(true);
    else void save();
  }
  async function save() {
    if (busy) return;
    setConfirmingHidden(false);
    setBusy(true);
    setError("");
    const sessionToken = api.csrfToken;
    try {
      const effectiveModelId =
        initialAgent !== undefined
          ? modelId
          : modelId.trim()
            ? modelId.trim()
            : undefined;
      const profile = agentProfileInput(
        name,
        description,
        workspaceIds,
        workspaces,
        enabled,
        effectiveModelId,
      );
      const changes = initialAgent
        ? agentProfileChanges(initialAgent, profile)
        : profile;
      if (initialAgent && Object.keys(changes).length === 0) {
        onClose();
        return;
      }
      if (!initialAgent) {
        const fingerprint = JSON.stringify({
          ownerId: user!.id,
          ...profile,
          workspaceIds: [...profile.workspaceIds].sort(),
        });
        if (createIntent.current?.fingerprint !== fingerprint) {
          createIntent.current = { fingerprint, key: crypto.randomUUID() };
        }
      }
      const response = initialAgent
        ? await api.updateAgent(initialAgent.id, changes)
        : await api.createAgent(profile, createIntent.current!.key);
      if (sessionToken !== api.csrfToken) return;
      client.setQueryData(["agent", user!.id, response.agent.id], response);
      await Promise.all([
        client.invalidateQueries({ queryKey: ["agents"] }),
        client.invalidateQueries({ queryKey: ["workspaceAgents"] }),
        client.invalidateQueries({ queryKey: ["members"] }),
      ]);
      navigate(`/app/agents/${response.agent.id}`);
      onClose();
      toast.success(
        initialAgent
          ? `已保存「${response.agent.name}」的资料`
          : `已创建 Agent「${response.agent.name}」`,
      );
    } catch (e) {
      if (sessionToken === api.csrfToken) setError(agentFailureText(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      title={initialAgent ? "编辑 Agent 资料" : "创建 Agent"}
      onClose={onClose}
      busy={busy}
      dirty={dirty}
    >
      <form noValidate onSubmit={submit} onInput={() => setDirty(true)}>
        <Notice>{error}</Notice>
        <label>
          Agent 名称
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={80}
            placeholder="例如：产品伙伴"
            required
            autoComplete="off"
          />
        </label>
        <label>
          简介 <span className="optional">选填</span>
          <textarea
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            maxLength={1000}
            rows={3}
            placeholder="简单介绍它的职责"
          />
        </label>
        <label>
          运行模型 <span className="optional">选填</span>
          <select
            name="agentModelId"
            value={modelId}
            onChange={(e) => setModelId(e.target.value)}
            disabled={busy || catalogQuery.isPending}
          >
            <option value="">
              {catalogQuery.isPending
                ? "正在加载可用模型…"
                : defaultModel
                  ? `平台默认模型（${defaultModel.displayName}）`
                  : "平台默认模型"}
            </option>
            {catalogModels.map((item) => (
              <option key={item.id} value={item.id}>
                {item.displayName}
                {item.isDefault ? " · 默认" : ""}
                {item.description ? ` — ${item.description}` : ""}
              </option>
            ))}
            {modelId && !catalogModels.some((item) => item.id === modelId) && (
              <option value={modelId}>
                {catalogQuery.isSuccess
                  ? `${modelId}（已下线，将自动回退到平台默认模型）`
                  : modelId}
              </option>
            )}
          </select>
          <small className="field-help">
            若所选模型停用或移除，将自动平滑回退到平台默认模型继续运行。
          </small>
        </label>
        {catalogQuery.isPending && (
          <div className="agent-catalog-notice" role="status">
            <LoaderCircle size={ICON.xs} className="spin" aria-hidden="true" />
            <span>正在加载平台模型目录…</span>
          </div>
        )}
        {catalogQuery.isError && (
          <div className="agent-catalog-notice error" role="alert">
            <span>模型目录暂时无法加载，已保留当前模型选择。</span>
            <Button
              type="button"
              variant="secondary"
              size="sm"
              onClick={() => void catalogQuery.refetch()}
            >
              <RefreshCw size={ICON.xs} /> 重试
            </Button>
          </div>
        )}
        {catalogQuery.isSuccess && catalogModels.length === 0 && (
          <div className="agent-catalog-notice" role="status">
            <span>
              平台暂未配置可用模型，配置完成前 Agent 暂时无法回复。
              {canConfigureModelGateway(user) && (
                <>
                  {" "}
                  <Link to="/app/admin/gateway" onClick={onClose}>
                    前往模型网关配置
                  </Link>
                </>
              )}
            </span>
          </div>
        )}
        <WorkspaceVisibilityFields
          workspaces={workspaces}
          selectedIds={workspaceIds}
          onChange={(ids) => {
            setDirty(true);
            setWorkspaceIds(ids);
          }}
          disabled={busy}
        />
        {hidden && (
          <p className="field-warning" role="status">
            未选择工作空间：其他成员将无法发现、@ 或邀请这个 Agent。
          </p>
        )}
        <SwitchField
          checked={enabled}
          disabled={
            busy || (!initialAgent?.enabled && !data?.capabilities["agents.enabled"])
          }
          title="启用 Agent"
          description="处理已加入对话中的新消息"
          reason={
            !initialAgent?.enabled && !data?.capabilities["agents.enabled"]
              ? "平台暂未开放启用 Agent"
              : undefined
          }
          reasonId="agent-enable-disabled-reason"
          onChange={setEnabled}
        />
        <div className="modal-actions">
          <ModalCloseButton>取消</ModalCloseButton>
          <Button type="submit" busy={busy} disabled={!name.trim()}>
            {initialAgent ? "保存资料" : "创建 Agent"}
          </Button>
        </div>
      </form>
      {confirmingHidden && (
        <ConfirmDialog
          title={initialAgent ? "不在任何工作空间显示？" : "不选择工作空间就创建？"}
          impact={[
            "其他成员将无法在成员列表中发现它，也无法 @ 它或邀请它加入对话。",
            "之后可以随时在「编辑资料」中选择可见的工作空间。",
          ]}
          cancelLabel="返回选择"
          confirmLabel={initialAgent ? "仍然保存" : "仍然创建"}
          onCancel={() => setConfirmingHidden(false)}
          onConfirm={() => void save()}
        />
      )}
    </Modal>
  );
}
