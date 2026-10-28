import { useState } from "react";
import type { FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Eye, Hash } from "lucide-react";
import { api } from "@teamie/api-client";
import type { Agent, ModelCatalog, Workspace } from "@teamie/api-client";
import { useAuth } from "../auth";
import { useChat } from "../chat";
import { WorkspaceVisibilityFields } from "../agent-ui";
import {
  agentFailureText,
  agentProfileChanges,
  agentProfileInput,
  snapshotAgentProfile,
} from "../agent-management";
import { Button, Modal, Notice, SwitchField } from "../ui";
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
          <Eye size={ICON.xs} />
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
    queryFn: () =>
      api.modelCatalog().catch((): ModelCatalog => ({ models: [] })),
    retry: false,
  });
  const catalogModels = catalogQuery.data?.models || [];
  const defaultModel = catalogModels.find(
    (item) => item.isDefault || item.id === catalogQuery.data?.defaultModelId,
  );
  const [workspaceIds, setWorkspaceIds] = useState(
    initialAgent
      ? initialAgent.workspaceIds.filter((id) =>
          workspaces.some((w) => w.id === id),
        )
      : initialWorkspaceId
        ? [initialWorkspaceId]
        : [],
  );
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [dirty, setDirty] = useState(false);
  async function submit(event: FormEvent) {
    event.preventDefault();
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
      const response = initialAgent
        ? await api.updateAgent(initialAgent.id, changes)
        : await api.createAgent(profile);
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
      <form onSubmit={submit} onInput={() => setDirty(true)}>
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
            disabled={busy}
          >
            <option value="">
              {defaultModel
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
                {modelId}（已下线，将自动回退到平台默认模型）
              </option>
            )}
          </select>
          <small className="field-help">
            若所选模型停用或移除，将自动平滑回退到平台默认模型继续运行。
          </small>
        </label>
        <WorkspaceVisibilityFields
          workspaces={workspaces}
          selectedIds={workspaceIds}
          onChange={setWorkspaceIds}
          disabled={busy}
        />
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
          <Button type="button" variant="secondary" onClick={onClose}>
            取消
          </Button>
          <Button type="submit" busy={busy} disabled={!name.trim()}>
            {initialAgent ? "保存资料" : "创建 Agent"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
