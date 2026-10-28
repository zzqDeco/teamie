import { useCallback, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowLeft,
  ArrowRight,
  Bot,
  CirclePause,
  LoaderCircle,
  Pencil,
  Play,
  Plus,
  Trash2,
} from "lucide-react";
import { api, ApiError } from "@teamie/api-client";
import type { ModelCatalog } from "@teamie/api-client";
import { useAuth } from "./auth";
import { useChat } from "./chat";
import {
  AgentAvatar,
  AgentBadge,
  AgentEnabledStatus,
} from "./agent-ui";
import {
  agentFailureText,
  canManageAgent,
} from "./agent-management";
import { BadgePill, Button, Modal, Notice } from "./ui";
import { toast } from "./toast";
import { ICON } from "./icons";
import {
  AgentProfileDialog,
  WorkspaceChips,
} from "./agents/agent-profile-dialog";

const queryRetry = (count: number, error: Error) =>
  !(error instanceof ApiError && [401, 403, 404].includes(error.status)) &&
  count < 1;

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
  const catalogQuery = useQuery<ModelCatalog>({
    queryKey: ["modelCatalog", user!.id],
    queryFn: () =>
      api.modelCatalog().catch((): ModelCatalog => ({ models: [] })),
    enabled: agents.some((item) => Boolean(item.modelId?.trim())),
    retry: false,
  });
  const catalogModels = catalogQuery.data?.models || [];
  const resolveModelLabel = (id?: string) => {
    const trimmed = id?.trim();
    if (!trimmed) return "";
    return (
      catalogModels.find((item) => item.id === trimmed)?.displayName || trimmed
    );
  };
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
            <Plus size={ICON.sm} />
            创建 Agent
          </Button>
        )}
      </div>
      {query.isLoading ? (
        <div className="agents-empty">
          <LoaderCircle size={ICON.xl} className="spin" />
          <p>加载 Agent…</p>
        </div>
      ) : query.error ? (
        <div className="agents-empty">
          <Notice>Agent 列表暂时无法加载，请稍后重试。</Notice>
          <Button variant="secondary" onClick={() => void query.refetch()}>
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
              <div className="agent-card-badges">
                <AgentBadge compact />
                {resolveModelLabel(agent.modelId) && (
                  <BadgePill className="agent-model-chip">
                    {resolveModelLabel(agent.modelId)}
                  </BadgePill>
                )}
              </div>
              {agent.description && <p>{agent.description}</p>}
              <WorkspaceChips
                ids={agent.workspaceIds}
                workspaces={data?.workspaces || []}
              />
              <div className="agent-card-bottom">
                <span>
                  管理 Agent <ArrowRight size={ICON.xs} />
                </span>
              </div>
            </Link>
          ))}
        </div>
      ) : (
        <div className="agents-empty">
          <span className="agents-empty-icon">
            <Bot size={ICON.xl} />
          </span>
          <h2>还没有 Agent</h2>
          <p>创建 Agent，让它加入你的团队。</p>
          {canCreate ? (
            <Button onClick={() => setCreating(true)}>
              <Plus size={ICON.sm} />
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
  const catalogQuery = useQuery<ModelCatalog>({
    queryKey: ["modelCatalog", user!.id],
    queryFn: () =>
      api.modelCatalog().catch((): ModelCatalog => ({ models: [] })),
    enabled: Boolean(agent?.modelId?.trim()),
    retry: false,
  });
  const catalogModels = catalogQuery.data?.models || [];
  const resolvedModelLabel = agent?.modelId?.trim()
    ? catalogModels.find((item) => item.id === agent.modelId?.trim())
        ?.displayName || agent.modelId.trim()
    : "";
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
      toast.success(
        response.agent.enabled
          ? `已启用「${response.agent.name}」`
          : `已停用「${response.agent.name}」`,
      );
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
      toast.success(`已删除「${agent.name}」`);
    } catch (e) {
      if (token === api.csrfToken) setError(agentFailureText(e));
    } finally {
      setBusy(false);
    }
  }
  if (query.isLoading)
    return (
      <div className="agents-empty">
        <LoaderCircle size={ICON.xl} className="spin" />
        <p>正在加载 Agent 资料…</p>
      </div>
    );
  if (
    !agent &&
    query.error &&
    !(query.error instanceof ApiError && [403, 404].includes(query.error.status))
  )
    return (
      <div className="unavailable-view">
        <span className="large-symbol">
          <Bot size={ICON.xl} />
        </span>
        <h2>Agent 资料暂时无法加载</h2>
        <p>{agentFailureText(query.error)}</p>
        <Button onClick={() => void query.refetch()}>重新加载</Button>
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
          <Bot size={ICON.xl} />
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
        <ArrowLeft size={ICON.xs} />
        个人 Agent
      </Link>
      <div className="agent-detail-heading">
        <AgentAvatar size="large" disabled={!agent.enabled} />
        <div className="agent-detail-summary">
          <div className="agent-detail-labels">
            <AgentBadge compact />
            <AgentEnabledStatus enabled={agent.enabled} />
            {resolvedModelLabel && (
              <BadgePill className="agent-model-chip">
                {resolvedModelLabel}
              </BadgePill>
            )}
          </div>
          <h1>{agent.name}</h1>
          {agent.description && <p>{agent.description}</p>}
        </div>
        <div className="agent-detail-actions">
          {editable && (
            <>
              <Button variant="secondary" onClick={() => setEditing(true)}>
                <Pencil size={ICON.xs} />
                编辑资料
              </Button>
              <Button
                variant={agent.enabled ? "secondary" : "primary"}
                busy={busy}
                disabled={!agent.enabled && !canEnable}
                aria-describedby={
                  !agent.enabled && !canEnable
                    ? "agent-detail-enable-disabled-reason"
                    : undefined
                }
                onClick={() => void enable()}
              >
                {agent.enabled ? <CirclePause size={ICON.xs} /> : <Play size={ICON.xs} />}{" "}
                {agent.enabled ? "停用" : "启用"}
              </Button>
              {!agent.enabled && !canEnable && (
                <span
                  id="agent-detail-enable-disabled-reason"
                  className="agent-disabled-reason"
                >
                  平台暂未开放启用 Agent
                </span>
              )}
            </>
          )}
        </div>
      </div>
      <Notice>
        {!deleting
          ? error || (query.error ? agentFailureText(query.error) : "")
          : ""}
      </Notice>
      <section className="agent-profile-section">
        <header>
          <h2>团队可见范围</h2>
          {editable && (
            <button onClick={() => setEditing(true)}>
              调整可见工作空间 <Pencil size={ICON.xs} />
            </button>
          )}
        </header>
        <WorkspaceChips
          ids={agent.workspaceIds}
          workspaces={data?.workspaces || []}
          interactive
        />
      </section>
      {deletable && (
        <div className="agent-delete-section">
          <div>
            <h3>删除 Agent</h3>
            <p>删除后停止工作，保留已有消息。</p>
          </div>
          <Button
            variant="secondary"
            className="danger-text"
            onClick={() => setDeleting(true)}
          >
            <Trash2 size={ICON.xs} />
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
          dismissible={!busy}
        >
          <Notice>{error}</Notice>
          <div className="modal-actions">
            <Button
              variant="secondary"
              onClick={closeDelete}
              disabled={busy}
              data-autofocus
            >
              保留 Agent
            </Button>
            <Button
              variant="danger"
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

