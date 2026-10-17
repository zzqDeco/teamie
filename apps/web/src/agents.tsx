import { useCallback, useState } from "react";
import type { FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowLeft,
  ArrowRight,
  Bot,
  CirclePause,
  Eye,
  Hash,
  LoaderCircle,
  Pencil,
  Play,
  Plus,
  Trash2,
} from "lucide-react";
import { api, ApiError } from "@teamie/api-client";
import type { Agent, Workspace } from "@teamie/api-client";
import { useAuth } from "./auth";
import { useChat } from "./chat";
import {
  AgentAvatar,
  AgentBadge,
  AgentEnabledStatus,
  WorkspaceVisibilityFields,
} from "./agent-ui";
import {
  agentProfileChanges,
  agentFailureText,
  agentProfileInput,
  canManageAgent,
  snapshotAgentProfile,
} from "./agent-management";
import { Button, Modal, Notice } from "./ui";
const queryRetry = (count: number, error: Error) =>
  !(error instanceof ApiError && [401, 403, 404].includes(error.status)) &&
  count < 1;
function WorkspaceChips({
  ids,
  workspaces,
}: {
  ids: string[];
  workspaces: Workspace[];
}) {
  return (
    <div className="agent-workspace-chips">
      {ids.length ? (
        ids.map((id) => (
          <span key={id}>
            <Hash size={10} />
            {workspaces.find((w) => w.id === id)?.name || "工作空间"}
          </span>
        ))
      ) : (
        <span className="private-agent-chip">
          <Eye size={11} />
          未选择团队
        </span>
      )}
    </div>
  );
}
function AgentProfileDialog({
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
    [enabled, setEnabled] = useState(initialAgent?.enabled ?? true);
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
    [error, setError] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    const sessionToken = api.csrfToken;
    try {
      const profile = agentProfileInput(
        name,
        description,
        workspaceIds,
        workspaces,
        enabled,
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
    >
      <form onSubmit={submit}>
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
        <WorkspaceVisibilityFields
          workspaces={workspaces}
          selectedIds={workspaceIds}
          onChange={setWorkspaceIds}
          disabled={busy}
        />
        <label className="agent-enabled-switch">
          <input
            type="checkbox"
            checked={enabled}
            disabled={busy || (!initialAgent?.enabled && !data?.capabilities["agents.enabled"])}
            onChange={(e) => setEnabled(e.target.checked)}
          />
          <span>
            <b>启用 Agent</b>
            <small>处理已加入对话中的新消息</small>
          </span>
          <i />
        </label>
        <div className="modal-actions">
          <Button type="button" className="secondary" onClick={onClose}>
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
export function MyAgents() {
  const { user } = useAuth();
  const { data } = useChat();
  const [creating, setCreating] = useState(false);
  const closeCreate = useCallback(() => setCreating(false), []);
  const query = useQuery({
    queryKey: ["agents", user!.id],
    queryFn: api.agents,
    retry: queryRetry,
  });
  const agents = (query.data?.agents || []).filter((agent) =>
    canManageAgent(user, agent),
  );
  const canCreate =
    !!data?.capabilities["agents.enabled"] &&
    !!data.capabilities["agents.create"];
  return (
    <div className="agents-view">
      <div className="agents-heading">
        <div>
          <h1>
            个人 Agent <span>{agents.length}</span>
          </h1>
        </div>
        {canCreate && (
          <Button onClick={() => setCreating(true)}>
            <Plus size={16} />
            创建 Agent
          </Button>
        )}
      </div>
      {query.isLoading ? (
        <div className="agents-empty">
          <LoaderCircle size={24} className="spin" />
          <p>加载 Agent…</p>
        </div>
      ) : query.error ? (
        <div className="agents-empty">
          <Notice>Agent 列表暂时无法加载，请稍后重试。</Notice>
          <Button className="secondary" onClick={() => void query.refetch()}>
            重新加载
          </Button>
        </div>
      ) : agents.length ? (
        <div className="agent-card-grid">
          {agents.map((agent) => (
            <Link
              key={agent.id}
              to={`/app/agents/${agent.id}`}
              className="agent-card"
            >
              <div className="agent-card-top">
                <AgentAvatar disabled={!agent.enabled} />
                <AgentEnabledStatus enabled={agent.enabled} />
              </div>
              <h2>{agent.name}</h2>
              <AgentBadge compact />
              {agent.description && <p>{agent.description}</p>}
              <WorkspaceChips
                ids={agent.workspaceIds}
                workspaces={data?.workspaces || []}
              />
              <div className="agent-card-bottom">
                <span>
                  管理 Agent <ArrowRight size={13} />
                </span>
              </div>
            </Link>
          ))}
        </div>
      ) : (
        <div className="agents-empty">
          <span className="agents-empty-icon">
            <Bot size={34} />
          </span>
          <h2>还没有 Agent</h2>
          <p>创建 Agent，让它加入你的团队。</p>
          {canCreate ? (
            <Button onClick={() => setCreating(true)}>
              <Plus size={16} />
              创建 Agent
            </Button>
          ) : (
            <Notice kind="info">当前无法创建 Agent。</Notice>
          )}
        </div>
      )}
      {creating && (
        <AgentProfileDialog
          workspaces={data?.workspaces || []}
          onClose={closeCreate}
        />
      )}
    </div>
  );
}
export function AgentDetail({ agentId }: { agentId: string }) {
  const { user } = useAuth();
  const { data } = useChat();
  const client = useQueryClient();
  const navigate = useNavigate();
  const [editing, setEditing] = useState(false),
    [deleting, setDeleting] = useState(false),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const closeEdit = useCallback(() => setEditing(false), []),
    closeDelete = useCallback(() => setDeleting(false), []);
  const query = useQuery({
    queryKey: ["agent", user!.id, agentId],
    queryFn: () => api.agent(agentId),
    retry: queryRetry,
  });
  const agent = query.data?.agent;
  const allowed = !!agent && canManageAgent(user, agent);
  const editable = !!data?.capabilities["agents.update"];
  const deletable = !!data?.capabilities["agents.delete"];
  const canEnable = !!data?.capabilities["agents.enabled"];
  async function enable() {
    if (!agent || !allowed) return;
    setBusy(true);
    setError("");
    const token = api.csrfToken;
    try {
      const response = await api.updateAgent(agentId, {
        enabled: !agent.enabled,
      });
      if (token !== api.csrfToken) return;
      client.setQueryData(["agent", user!.id, agentId], response);
      await Promise.all([
        client.invalidateQueries({ queryKey: ["agents"] }),
        client.invalidateQueries({ queryKey: ["workspaceAgents"] }),
        client.invalidateQueries({ queryKey: ["members"] }),
      ]);
    } catch (e) {
      if (token === api.csrfToken) setError(agentFailureText(e));
    } finally {
      setBusy(false);
    }
  }
  async function remove() {
    if (!agent || !allowed) return;
    setBusy(true);
    setError("");
    const token = api.csrfToken;
    try {
      await api.deleteAgent(agentId);
      if (token !== api.csrfToken) return;
      client.removeQueries({ queryKey: ["agent", user!.id, agentId] });
      await Promise.all([
        client.invalidateQueries({ queryKey: ["agents"] }),
        client.invalidateQueries({ queryKey: ["workspaceAgents"] }),
        client.invalidateQueries({ queryKey: ["members"] }),
        client.invalidateQueries({ queryKey: ["bootstrap"] }),
      ]);
      navigate("/app/agents", { replace: true });
    } catch (e) {
      if (token === api.csrfToken) setError(agentFailureText(e));
    } finally {
      setBusy(false);
    }
  }
  if (query.isLoading)
    return (
      <div className="agents-empty">
        <LoaderCircle size={24} className="spin" />
        <p>正在加载 Agent 资料…</p>
      </div>
    );
  if (
    !agent ||
    !allowed ||
    (query.error instanceof ApiError && [403, 404].includes(query.error.status))
  )
    return (
      <div className="unavailable-view">
        <span className="large-symbol">
          <Bot size={38} />
        </span>
        <h2>这个 Agent 暂时无法管理</h2>
        <p>请使用所有者账户访问。</p>
        <Link className="button" to="/app/agents">
          回到个人 Agent
        </Link>
      </div>
    );
  return (
    <div className="agents-view agent-detail-view">
      <Link className="agent-back" to="/app/agents">
        <ArrowLeft size={14} />
        个人 Agent
      </Link>
      <div className="agent-detail-heading">
        <AgentAvatar size="large" disabled={!agent.enabled} />
        <div>
          <div className="agent-detail-labels">
            <AgentBadge compact />
            <AgentEnabledStatus enabled={agent.enabled} />
          </div>
          <h1>{agent.name}</h1>
          {agent.description && <p>{agent.description}</p>}
        </div>
        <div className="agent-detail-actions">
          {editable && (
            <>
              <Button className="secondary" onClick={() => setEditing(true)}>
                <Pencil size={14} />
                编辑资料
              </Button>
              <Button
                className={agent.enabled ? "secondary" : ""}
                busy={busy}
                disabled={!agent.enabled && !canEnable}
                onClick={() => void enable()}
              >
                {agent.enabled ? <CirclePause size={14} /> : <Play size={14} />}{" "}
                {agent.enabled ? "停用" : "启用"}
              </Button>
            </>
          )}
        </div>
      </div>
      <Notice>{!deleting ? error : ""}</Notice>
      <section className="agent-profile-section">
        <header>
          <h2>团队可见范围</h2>
          {editable && (
            <button onClick={() => setEditing(true)}>
              调整可见团队 <Pencil size={12} />
            </button>
          )}
        </header>
        <WorkspaceChips
          ids={agent.workspaceIds}
          workspaces={data?.workspaces || []}
        />
      </section>
      {deletable && (
        <div className="agent-delete-section">
          <div>
            <h3>删除 Agent</h3>
            <p>删除后停止工作，保留已有消息。</p>
          </div>
          <Button
            className="secondary danger-text"
            onClick={() => setDeleting(true)}
          >
            <Trash2 size={13} />
            删除 Agent
          </Button>
        </div>
      )}
      {editing && (
        <AgentProfileDialog
          agent={agent}
          workspaces={data?.workspaces || []}
          onClose={closeEdit}
        />
      )}{" "}
      {deleting && (
        <Modal
          title={`删除「${agent.name}」？`}
          subtitle="删除后，这个 Agent 将停止工作并从发现列表移除，已有聊天消息仍会保留。"
          onClose={closeDelete}
        >
          <Notice>{error}</Notice>
          <div className="modal-actions">
            <Button className="secondary" onClick={closeDelete}>
              保留 Agent
            </Button>
            <Button
              className="danger"
              busy={busy}
              onClick={() => void remove()}
            >
              确认删除
            </Button>
          </div>
        </Modal>
      )}
    </div>
  );
}
