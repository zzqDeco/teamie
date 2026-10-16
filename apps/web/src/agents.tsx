import { useCallback, useState } from "react";
import type { FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowLeft,
  ArrowRight,
  Bot,
  CalendarDays,
  CirclePause,
  Eye,
  Hash,
  LoaderCircle,
  MessageCircle,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Trash2,
  Users,
} from "lucide-react";
import { api, ApiError } from "@teamie/api-client";
import type { Agent, Workspace } from "@teamie/api-client";
import { useAuth } from "./auth";
import { errorText, useChat } from "./chat";
import { chatStore } from "./store";
import {
  AgentAvatar,
  AgentBadge,
  AgentPrinciples,
  AgentStatus,
  WaitingForConfiguration,
  WorkspaceVisibilityFields,
} from "./agent-ui";
import {
  agentProfileInput,
  canManageAgent,
  displayAgentState,
} from "./agent-management";
import { Avatar, Button, Modal, Notice } from "./ui";
import { AddAgentToGroupDialog } from "./group-members";
import { WorkspaceDialog } from "./dialogs";
const queryRetry = (count: number, error: Error) =>
  !(error instanceof ApiError && [401, 403, 404].includes(error.status)) &&
  count < 1;
const formatDate = (value: string) =>
  new Date(value).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
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
          仅自己管理 · 尚未对团队可见
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
  const { user } = useAuth();
  const navigate = useNavigate();
  const [name, setName] = useState(agent?.name || ""),
    [description, setDescription] = useState(agent?.description || ""),
    [enabled, setEnabled] = useState(agent?.enabled ?? true);
  const [workspaceIds, setWorkspaceIds] = useState(
    agent
      ? agent.workspaceIds.filter((id) => workspaces.some((w) => w.id === id))
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
      const response = agent
        ? await api.updateAgent(agent.id, profile)
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
      if (sessionToken === api.csrfToken) setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      title={agent ? "编辑 Agent 资料" : "认识你的新 Agent"}
      subtitle="给它一个名字，选择哪些团队可以发现它。你始终是它的所有者。"
      onClose={onClose}
    >
      <form onSubmit={submit}>
        <div className="agent-form-intro">
          <AgentAvatar size="large" />
          <div>
            <b>{name.trim() || "你的下一位伙伴"}</b>
            <AgentBadge ownerName={user!.displayName} />
          </div>
        </div>
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
          简介 <span className="optional">选填 · 公开资料</span>
          <textarea
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            maxLength={1000}
            rows={3}
            placeholder="用一句话介绍这个 Agent，方便伙伴认识它。"
          />
          <small className="field-help">
            将作为公开资料展示给可见团队的伙伴。
          </small>
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
            onChange={(e) => setEnabled(e.target.checked)}
          />
          <span>
            <b>启用 Agent</b>
            <small>启用后，在模型可用时处理它参与的对话中的新消息。</small>
          </span>
          <i />
        </label>
        <div className="modal-actions">
          <Button type="button" className="secondary" onClick={onClose}>
            取消
          </Button>
          <Button type="submit" busy={busy} disabled={!name.trim()}>
            {agent ? "保存资料" : "创建 Agent"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
export function MyAgents() {
  const { user } = useAuth();
  const { data } = useChat();
  const navigate = useNavigate();
  const [creating, setCreating] = useState(false);
  const closeCreate = useCallback(() => setCreating(false), []);
  const query = useQuery({
    queryKey: ["agents", user!.id],
    queryFn: api.agents,
    refetchInterval: 15_000,
    retry: queryRetry,
  });
  const agents = (query.data?.agents || []).filter(
    (agent) => canManageAgent(user, agent) && !agent.deletedAt,
  );
  const canCreate =
    !!data?.capabilities["agents.enabled"] &&
    !!data.capabilities["agents.create"];
  return (
    <div className="agents-view">
      <div className="agents-heading">
        <div>
          <div className="eyebrow">YOUR AGENTS. YOUR TEAMMATES.</div>
          <h1>
            我的 Agent <span>{agents.length}</span>
          </h1>
          <p>由你创建，由你管理。让合适的 Agent 出现在合适的团队。</p>
        </div>
        {canCreate && (
          <Button onClick={() => setCreating(true)}>
            <Plus size={16} />
            创建 Agent
          </Button>
        )}
      </div>
      {agents.some(
        (a) => a.enabled && a.runtimeState === "waiting_configuration",
      ) && (
        <WaitingForConfiguration
          admin={user!.platformRole === "admin"}
          onConfigure={() => navigate("/app/admin/gateway")}
        />
      )}
      <AgentPrinciples />
      {query.isLoading ? (
        <div className="agents-empty">
          <LoaderCircle size={24} className="spin" />
          <p>正在寻找你的 Agent…</p>
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
                <AgentStatus
                  status={displayAgentState(agent.runtimeState, agent.enabled)}
                />
              </div>
              <h2>{agent.name}</h2>
              <AgentBadge ownerName={agent.ownerName} />
              <p>
                {agent.description ||
                  "还没有填写简介，给伙伴一个认识它的机会。"}
              </p>
              <WorkspaceChips
                ids={agent.workspaceIds}
                workspaces={data?.workspaces || []}
              />
              <div className="agent-card-bottom">
                <span>
                  {agent.lastActiveAt
                    ? `最近活跃 ${formatDate(agent.lastActiveAt)}`
                    : "尚未处理消息"}
                </span>
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
          <h2>下一位伙伴，由你来创造。</h2>
          <p>
            起一个名字，选择可见的团队。
            <br />
            Agent 会以清楚标记的身份，参与你允许它加入的对话。
          </p>
          {canCreate ? (
            <Button onClick={() => setCreating(true)}>
              <Plus size={16} />
              创建我的第一个 Agent
            </Button>
          ) : (
            <Notice kind="info">
              当前暂未开放创建 Agent。已有 Agent 与聊天记录仍然保留。
            </Notice>
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
    refetchInterval: 5000,
    retry: queryRetry,
  });
  const agent = query.data?.agent;
  const allowed = !!agent && canManageAgent(user, agent);
  const editable =
    !!data?.capabilities["agents.enabled"] &&
    !!data.capabilities["agents.update"];
  const deletable =
    !!data?.capabilities["agents.enabled"] &&
    !!data.capabilities["agents.delete"];
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
      if (token === api.csrfToken) setError(errorText(e));
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
      if (token === api.csrfToken) setError(errorText(e));
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
    agent.deletedAt ||
    (query.error instanceof ApiError && [403, 404].includes(query.error.status))
  )
    return (
      <div className="unavailable-view">
        <span className="large-symbol">
          <Bot size={38} />
        </span>
        <h2>这个 Agent 暂时无法管理</h2>
        <p>只有创建者可以管理 Agent。平台管理员同样遵循所有者权限。</p>
        <Link className="button" to="/app/agents">
          回到我的 Agent
        </Link>
      </div>
    );
  return (
    <div className="agents-view agent-detail-view">
      <Link className="agent-back" to="/app/agents">
        <ArrowLeft size={14} />
        我的 Agent
      </Link>
      <div className="agent-detail-heading">
        <AgentAvatar size="large" disabled={!agent.enabled} />
        <div>
          <AgentBadge ownerName={agent.ownerName} />
          <h1>{agent.name}</h1>
          <p>{agent.description || "还没有填写简介"}</p>
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
      {agent.enabled && agent.runtimeState === "waiting_configuration" && (
        <WaitingForConfiguration
          admin={user!.platformRole === "admin"}
          onConfigure={() => navigate("/app/admin/gateway")}
        />
      )}
      <section className="agent-session-panel">
        <header>
          <div>
            <h2>持续会话</h2>
            <p>同一个 Agent，接得上每一次对话。</p>
          </div>
          <AgentStatus
            status={displayAgentState(agent.runtimeState, agent.enabled)}
          />
        </header>
        <div className="agent-session-grid">
          <div>
            <span>
              <Bot size={13} />
              会话
            </span>
            <code title={agent.sessionId}>{agent.sessionId || "正在准备"}</code>
          </div>
          <div>
            <span>
              <CalendarDays size={13} />
              最近活跃
            </span>
            <b>
              {agent.lastActiveAt
                ? formatDate(agent.lastActiveAt)
                : "尚未开始处理消息"}
            </b>
          </div>
          <div>
            <span>
              <RefreshCw size={13} />
              资料版本
            </span>
            <b>v{agent.revision}</b>
          </div>
        </div>
        {agent.runtimeState === "error" && (
          <Notice kind="info">
            {agent.lastError ||
              "最近一次处理未完成。请检查模型连接状态，或稍后再发送新消息。"}
          </Notice>
        )}
        {!agent.enabled && (
          <p className="agent-session-note">
            Agent 已停止接收新的处理任务。资料和已有聊天记录仍然保留。
          </p>
        )}
      </section>
      <section className="agent-profile-section">
        <header>
          <h2>团队可见范围</h2>
          {editable && (
            <button onClick={() => setEditing(true)}>
              调整可见团队 <Pencil size={12} />
            </button>
          )}
        </header>
        <p>
          在选中的团队中，Agent
          按普通成员权限访问空间频道；私聊与小组仍按各自成员名单授权。
        </p>
        <WorkspaceChips
          ids={agent.workspaceIds}
          workspaces={data?.workspaces || []}
        />
      </section>
      <div className="agent-owner-card">
        <Avatar name={agent.ownerName} seed={agent.ownerId} />
        <div>
          <b>{agent.ownerName}</b>
          <span>Agent 所有者 · 只有你可以编辑、启停或删除</span>
        </div>
      </div>
      <AgentPrinciples />
      {deletable && (
        <div className="agent-delete-section">
          <div>
            <h3>删除 Agent</h3>
            <p>删除后不再接收新消息，已有聊天消息保留其身份标记。</p>
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
export function WorkspaceAgents({ workspace }: { workspace: Workspace }) {
  const { user } = useAuth();
  const { data, refresh } = useChat();
  const navigate = useNavigate();
  const [busyId, setBusyId] = useState(""),
    [error, setError] = useState(""),
    [creating, setCreating] = useState(false),
    [groupAgent, setGroupAgent] = useState<Agent | null>(null),
    [newGroupAgent, setNewGroupAgent] = useState<Agent | null>(null);
  const canDiscover =
    !!data?.capabilities["agents.enabled"] &&
    !!data.capabilities["agents.discover"];
  const query = useQuery({
    queryKey: ["workspaceAgents", user!.id, workspace.id],
    queryFn: () => api.workspaceAgents(workspace.id),
    enabled: canDiscover,
    refetchInterval: 30_000,
    retry: queryRetry,
  });
  const closeCreate = useCallback(() => setCreating(false), []);
  const closeGroup = useCallback(() => setGroupAgent(null), []),
    closeNewGroup = useCallback(() => setNewGroupAgent(null), []);
  async function direct(agent: Agent) {
    setBusyId(agent.id);
    setError("");
    const generation = chatStore.generation;
    try {
      const result = await api.createConversation(workspace.id, {
        type: "direct",
        memberIds: [agent.principalId],
      });
      if (generation !== chatStore.generation) return;
      chatStore.addConversation(result.conversation);
      await refresh();
      navigate(`/app/w/${workspace.id}/c/${result.conversation.id}`);
    } catch (e) {
      if (generation === chatStore.generation) setError(errorText(e));
    } finally {
      setBusyId("");
    }
  }
  return (
    <div className="agents-view agent-directory">
      <div className="agents-heading">
        <div>
          <div className="eyebrow">FIND THE RIGHT TEAMMATE</div>
          <h1>发现 Agent</h1>
          <p>
            <span className="directory-workspace">
              <Hash size={13} />
              {workspace.name}
            </span>{" "}
            可见的 Agent，清楚知道由谁创建。
          </p>
        </div>
        <Link to="/app/agents" className="button secondary">
          我的 Agent <ArrowRight size={14} />
        </Link>
      </div>
      <Notice>{error}</Notice>
      {!canDiscover ? (
        <Notice kind="info">
          当前未开放新的 Agent 发现。已保存的对话与 Agent 消息仍然可见。
        </Notice>
      ) : query.isLoading ? (
        <div className="agents-empty">
          <LoaderCircle className="spin" size={24} />
          <p>正在加载团队里的 Agent…</p>
        </div>
      ) : query.error ? (
        <div className="agents-empty">
          <Notice>无法加载这个工作空间的 Agent，请确认你仍是空间成员。</Notice>
          <Button className="secondary" onClick={() => void query.refetch()}>
            重新加载
          </Button>
        </div>
      ) : query.data?.agents.length ? (
        <div className="agent-card-grid">
          {query.data.agents
            .filter((a) => !a.deletedAt)
            .map((agent) => (
              <article
                key={agent.id}
                className="agent-card directory-agent-card"
              >
                <div className="agent-card-top">
                  <AgentAvatar disabled={!agent.enabled} />
                  <AgentStatus
                    status={displayAgentState(
                      agent.runtimeState,
                      agent.enabled,
                    )}
                  />
                </div>
                <h2>{agent.name}</h2>
                <AgentBadge ownerName={agent.ownerName} />
                <p>
                  {agent.description || "一位随时准备参与团队协作的 Agent。"}
                </p>
                <div className="directory-agent-actions">
                  {data?.capabilities["im.dm.create"] && (
                    <Button
                      className="secondary"
                      disabled={!agent.enabled || !!busyId}
                      busy={busyId === agent.id}
                      onClick={() => void direct(agent)}
                    >
                      <MessageCircle size={14} />
                      发起私聊
                    </Button>
                  )}
                  {data?.capabilities["im.group.create"] && (
                    <Button
                      className="secondary"
                      disabled={!agent.enabled || !!busyId}
                      onClick={() => setGroupAgent(agent)}
                    >
                      <Users size={14} />
                      添加到小组
                    </Button>
                  )}
                  {canManageAgent(user, agent) && (
                    <Link
                      to={`/app/agents/${agent.id}`}
                      className="directory-manage-link"
                    >
                      管理 <ArrowRight size={12} />
                    </Link>
                  )}
                </div>
              </article>
            ))}
        </div>
      ) : (
        <div className="agents-empty">
          <span className="agents-empty-icon">
            <Bot size={34} />
          </span>
          <h2>团队的下一位 Agent，还在路上。</h2>
          <p>
            你可以创建自己的 Agent，并让它在这个空间内被发现。
            <br />
            也可以请已有 Agent 的所有者调整可见范围。
          </p>
          {data?.capabilities["agents.enabled"] &&
            data.capabilities["agents.create"] && (
              <Button onClick={() => setCreating(true)}>
                <Plus size={15} />
                为这个团队创建 Agent
              </Button>
            )}
        </div>
      )}
      <div className="directory-group-tip">
        <Users size={17} />
        <span>
          创建小组时，可在同一份成员列表中选择人和 Agent。Agent
          始终保留身份与创建者标记。
        </span>
      </div>
      {creating && (
        <AgentProfileDialog
          workspaces={data?.workspaces || []}
          initialWorkspaceId={workspace.id}
          onClose={closeCreate}
        />
      )}{" "}
      {groupAgent && (
        <AddAgentToGroupDialog
          agent={groupAgent}
          workspace={workspace}
          onClose={closeGroup}
          onCreateGroup={() => {
            setNewGroupAgent(groupAgent);
            setGroupAgent(null);
          }}
        />
      )}{" "}
      {newGroupAgent && (
        <WorkspaceDialog
          kind="group"
          workspace={workspace}
          initialMemberIds={[newGroupAgent.principalId]}
          onClose={closeNewGroup}
        />
      )}
    </div>
  );
}
