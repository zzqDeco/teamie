import { useCallback, useEffect, useState } from "react";
import {
  Link,
  Navigate,
  useLocation,
  useMatch,
  useNavigate,
} from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowRight,
  Bot,
  Check,
  ChevronDown,
  ChevronRight,
  CircleHelp,
  Hash,
  Home,
  KeyRound,
  LoaderCircle,
  LogOut,
  MessageCircle,
  MessageSquarePlus,
  Plus,
  Settings,
  ShieldCheck,
  Sparkles,
  UserPlus,
  Users,
  WifiOff,
  X,
} from "lucide-react";
import { api } from "@teamie/api-client";
import type { Conversation, Member, Workspace } from "@teamie/api-client";
import { useAuth } from "./auth";
import { errorText, useChat, useChatState } from "./chat";
import { ProfileSettings, WorkspaceDialog } from "./dialogs";
import { PlatformInvitations } from "./admin";
import { ModelGatewaySettings } from "./model-gateway";
import { AgentDetail, MyAgents, WorkspaceAgents } from "./agents";
import { AgentAvatar, AgentBadge } from "./agent-ui";
import { GroupMembersDialog } from "./group-members";
import { enrichParticipants } from "./participants";
import { chatStore } from "./store";
import type { DialogKind } from "./dialogs";
import { ConversationView, conversationName } from "./messages";
import { Avatar, Button, IconButton, Loading, Logo, Modal, Notice } from "./ui";

function route(conversation: Conversation) {
  return `/app/w/${conversation.workspaceId}/c/${conversation.id}`;
}
function ConversationItem({
  conversation,
  selected,
  members,
  onClick,
}: {
  conversation: Conversation;
  selected: boolean;
  members: Member[];
  onClick: () => void;
}) {
  const { user } = useAuth();
  const unread = Math.max(0, conversation.lastSeq - conversation.readSeq);
  const otherMember = members.find(
    (member) =>
      member.userId !== user!.id &&
      conversation.memberIds.includes(member.userId),
  );
  return (
    <Link
      to={route(conversation)}
      onClick={onClick}
      className={`conversation-link ${selected ? "active" : ""} ${unread ? "has-unread" : ""}`}
      aria-current={selected ? "page" : undefined}
    >
      {conversation.type === "channel" ? (
        <Hash size={18} />
      ) : conversation.type === "group" ? (
        <span className="group-avatar">
          <Users size={15} />
        </span>
      ) : otherMember?.kind === "agent" ? (
        <AgentAvatar size="small" />
      ) : (
        <Avatar
          size="small"
          name={conversationName(conversation, user!.id, members)}
          seed={conversation.memberIds.find((id) => id !== user!.id)}
        />
      )}
      <span>{conversationName(conversation, user!.id, members)}</span>
      {conversation.type === "direct" && otherMember?.kind === "agent" && (
        <AgentBadge compact ownerName={otherMember.ownerName} />
      )}
      {unread > 0 && (
        <span className="unread-badge" aria-label={`${unread} 条未读`}>
          {unread > 99 ? "99+" : unread}
        </span>
      )}
    </Link>
  );
}
export function Shell() {
  const { user, logout } = useAuth();
  const chat = useChat(),
    snapshot = useChatState();
  const client = useQueryClient();
  const match = useMatch("/app/w/:workspaceId/c/:conversationId"),
    directoryMatch = useMatch("/app/w/:workspaceId/agents"),
    agentMatch = useMatch("/app/agents/:agentId"),
    location = useLocation(),
    navigate = useNavigate();
  const [dialog, setDialog] = useState<DialogKind | null>(null),
    [spaceMenu, setSpaceMenu] = useState(false),
    [sidebar, setSidebar] = useState(false),
    [details, setDetails] = useState(window.innerWidth >= 1360),
    [managingGroup, setManagingGroup] = useState(false),
    [removingFromGroup, setRemovingFromGroup] = useState(false);
  const [removing, setRemoving] = useState<Member | null>(null),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const workspace =
    chat.data?.workspaces.find(
      (w) =>
        w.id ===
        (match?.params.workspaceId || directoryMatch?.params.workspaceId),
    ) || (match || directoryMatch ? undefined : chat.data?.workspaces[0]);
  const owner = workspace?.createdBy === user!.id;
  const conversation = match?.params.conversationId
    ? snapshot.conversations[match.params.conversationId]
    : undefined;
  const conversations = Object.values(snapshot.conversations).filter(
    (c) => c.workspaceId === workspace?.id,
  );
  const channels = conversations
    .filter((c) => c.type === "channel")
    .sort((a, b) => a.name.localeCompare(b.name));
  const direct = conversations
    .filter((c) => c.type !== "channel")
    .sort((a, b) => b.updatedAt.localeCompare(a.updatedAt));
  const membersQuery = useQuery({
    queryKey: ["members", user!.id, workspace?.id],
    queryFn: () => api.members(workspace!.id),
    enabled: !!workspace,
    refetchInterval: 60_000,
  });
  const discoveryAllowed =
    !!chat.data?.capabilities["agents.enabled"] &&
    !!chat.data.capabilities["agents.discover"];
  const workspaceAgentsQuery = useQuery({
    queryKey: ["workspaceAgents", user!.id, workspace?.id],
    queryFn: () => api.workspaceAgents(workspace!.id),
    enabled: !!workspace && discoveryAllowed,
    refetchInterval: 30_000,
  });
  const members = enrichParticipants(
    membersQuery.data?.members || [],
    workspaceAgentsQuery.data?.agents || [],
  );
  const visibleMembers =
    conversation && conversation.type !== "channel"
      ? members.filter((m) => conversation.memberIds.includes(m.userId))
      : members;
  const closeDialog = useCallback(() => setDialog(null), []),
    closeRemoving = useCallback(() => setRemoving(null), []),
    closeGroupManager = useCallback(() => setManagingGroup(false), []);
  useEffect(() => {
    setSidebar(false);
    setSpaceMenu(false);
    setManagingGroup(false);
  }, [location.pathname]);
  const caps = chat.data?.capabilities || {};
  async function remove() {
    if (!workspace || !removing) return;
    setBusy(true);
    setError("");
    try {
      if (removingFromGroup && conversation) {
        const result = await api.removeConversationMember(
          conversation.id,
          removing.userId,
        );
        chatStore.addConversation(result.conversation);
        await chat.refresh();
      } else await api.removeMember(workspace.id, removing.userId);
      setRemoving(null);
      await client.invalidateQueries({ queryKey: ["members"] });
      chat.sync();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  async function signOut() {
    setBusy(true);
    setError("");
    try {
      await logout();
      navigate("/login");
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  function selectWorkspace(space: Workspace) {
    const first =
      Object.values(snapshot.conversations).find(
        (c) => c.workspaceId === space.id && c.type === "channel",
      ) ||
      Object.values(snapshot.conversations).find(
        (c) => c.workspaceId === space.id,
      );
    if (first) navigate(route(first));
  }
  if (chat.loading && !chat.data) return <Loading />;
  if (chat.error && !chat.data)
    return (
      <div className="loading-screen">
        <Logo />
        <Notice>{chat.error}</Notice>
        <Button onClick={() => void chat.refresh()}>重新连接</Button>
        <Button className="secondary" onClick={signOut} busy={busy}>
          退出登录
        </Button>
      </div>
    );
  if (
    !match &&
    location.pathname === "/app" &&
    chat.data?.workspaces.length &&
    conversations.length
  )
    return <Navigate to={route(channels[0] || conversations[0])} replace />;
  const isSettings = ["/app/settings", "/app/profile"].includes(
    location.pathname,
  );
  const isAdminPage = location.pathname === "/app/admin";
  const isGatewayPage = location.pathname === "/app/admin/gateway";
  const isAgentsPage = location.pathname === "/app/agents" || !!agentMatch;
  const isDirectoryPage = !!directoryMatch;
  const isManagement =
    isSettings ||
    isAdminPage ||
    isGatewayPage ||
    isAgentsPage ||
    isDirectoryPage;
  return (
    <div
      className={`app-shell ${sidebar ? "sidebar-open" : ""} ${details ? "details-open" : ""}`}
    >
      <aside className="workspace-rail" aria-label="工作空间切换">
        <Link className="rail-home" to="/" title="Teamie 首页">
          <Logo compact />
        </Link>
        <div className="rail-divider" />
        <div className="workspace-buttons">
          {chat.data?.workspaces.map((space) => (
            <button
              key={space.id}
              className={`workspace-button ${space.id === workspace?.id ? "selected" : ""}`}
              title={space.name}
              aria-label={`打开工作空间 ${space.name}`}
              onClick={() => selectWorkspace(space)}
            >
              <span>{Array.from(space.name).slice(0, 2).join("")}</span>
              {space.id === workspace?.id && <i />}
            </button>
          ))}
        </div>
        <IconButton
          label="创建工作空间"
          className="rail-add"
          onClick={() => setDialog("workspace")}
        >
          <Plus size={23} />
        </IconButton>
        <span className="rail-spacer" />
        <Link
          to="/app/agents"
          className={`rail-settings rail-agents ${isAgentsPage ? "selected" : ""}`}
          aria-label="我的 Agent"
          title="我的 Agent"
        >
          <Bot size={21} />
        </Link>
        {user!.platformRole === "admin" && (
          <>
            <Link
              to="/app/admin"
              className={`rail-settings rail-admin ${isAdminPage ? "selected" : ""}`}
              aria-label="平台管理：注册邀请"
              title="平台管理：注册邀请"
            >
              <ShieldCheck size={21} />
            </Link>
            <Link
              to="/app/admin/gateway"
              className={`rail-settings rail-admin ${isGatewayPage ? "selected" : ""}`}
              aria-label="平台管理：模型网关"
              title="平台管理：模型网关"
            >
              <KeyRound size={20} />
            </Link>
          </>
        )}
        <Link
          to="/app/settings"
          className={`rail-settings ${isSettings ? "selected" : ""}`}
          aria-label="个人设置"
          title="个人设置"
        >
          <Settings size={21} />
        </Link>
      </aside>
      <aside className="navigation-sidebar">
        <div className="workspace-header">
          <button
            className="workspace-title"
            onClick={() => setSpaceMenu(!spaceMenu)}
            aria-expanded={spaceMenu}
          >
            <span className="workspace-eyebrow">YOUR WORKSPACE</span>
            <b>
              {workspace?.name || "我的 Teamie"}
              <ChevronDown size={17} />
            </b>
          </button>
          <IconButton
            className="mobile-close"
            label="关闭频道列表"
            onClick={() => setSidebar(false)}
          >
            <X size={18} />
          </IconButton>
          {spaceMenu && (
            <>
              <button
                className="popover-dismiss"
                aria-label="关闭菜单"
                onClick={() => setSpaceMenu(false)}
              />
              <div className="space-popover">
                <span>{workspace?.name || "我的工作空间"}</span>
                {workspace && owner && (
                  <button
                    onClick={() => {
                      setDialog("invite");
                      setSpaceMenu(false);
                    }}
                  >
                    <UserPlus size={16} />
                    邀请伙伴
                  </button>
                )}
                <button
                  onClick={() => {
                    setDialog("workspace");
                    setSpaceMenu(false);
                  }}
                >
                  <Plus size={16} />
                  创建工作空间
                </button>
                <Link to="/app/settings">
                  <Settings size={16} />
                  个人设置
                </Link>
                <Link to="/app/agents">
                  <Bot size={16} />
                  我的 Agent
                </Link>
                {user!.platformRole === "admin" && (
                  <Link to="/app/admin">
                    <ShieldCheck size={16} />
                    平台管理 · 注册邀请
                  </Link>
                )}
                {user!.platformRole === "admin" && (
                  <Link to="/app/admin/gateway">
                    <KeyRound size={16} />
                    平台管理 · 模型网关
                  </Link>
                )}
                <button
                  className="danger-text"
                  onClick={signOut}
                  disabled={busy}
                >
                  <LogOut size={16} />
                  退出登录
                </button>
              </div>
            </>
          )}
        </div>
        <div className="sidebar-scroll">
          <div className="agent-sidebar-links">
            <Link to="/app/agents" className={isAgentsPage ? "active" : ""}>
              <Bot size={16} />
              <span>我的 Agent</span>
            </Link>
            {workspace && discoveryAllowed && (
              <Link
                to={`/app/w/${workspace.id}/agents`}
                className={isDirectoryPage ? "active" : ""}
              >
                <Sparkles size={15} />
                <span>发现团队 Agent</span>
                <ChevronRight size={12} />
              </Link>
            )}
          </div>
          <div className="sidebar-overview">
            <span className="overview-icon">
              <MessageCircle size={17} />
            </span>
            <span>
              让好对话发生<small>YOUR TEAM, IN SYNC.</small>
            </span>
            <Sparkles size={15} />
          </div>
          <div className="nav-section">
            <div className="nav-section-title">
              <span>
                <ChevronDown size={12} /> 团队频道
              </span>
              {workspace && owner && caps["im.channel.create"] && (
                <IconButton
                  label="创建频道"
                  onClick={() => setDialog("channel")}
                >
                  <Plus size={15} />
                </IconButton>
              )}
            </div>
            {channels.map((c) => (
              <ConversationItem
                key={c.id}
                conversation={c}
                selected={c.id === conversation?.id}
                members={members}
                onClick={() => setSidebar(false)}
              />
            ))}
            {channels.length === 0 && (
              <div className="sidebar-empty">
                {workspace ? "频道正在准备中" : "创建空间，开启第一段对话"}
              </div>
            )}
          </div>
          <div className="nav-section">
            <div className="nav-section-title">
              <span>
                <ChevronDown size={12} /> 私信与小组
              </span>
              {workspace &&
                (caps["im.dm.create"] || caps["im.group.create"]) && (
                  <IconButton
                    label="开始新对话"
                    onClick={() =>
                      setDialog(caps["im.dm.create"] ? "direct" : "group")
                    }
                  >
                    <Plus size={15} />
                  </IconButton>
                )}
            </div>
            {direct.map((c) => (
              <ConversationItem
                key={c.id}
                conversation={c}
                selected={c.id === conversation?.id}
                members={members}
                onClick={() => setSidebar(false)}
              />
            ))}
            {direct.length === 0 && (
              <div className="sidebar-empty">
                {workspace
                  ? "一句你好，让默契开始。"
                  : "你的伙伴，会在这里相遇。"}
              </div>
            )}
            {workspace && caps["im.dm.create"] && (
              <button
                className="sidebar-new-chat"
                onClick={() => setDialog("direct")}
              >
                <MessageSquarePlus size={15} />
                发起私聊
              </button>
            )}
            {workspace && caps["im.group.create"] && (
              <button
                className="sidebar-new-chat"
                onClick={() => setDialog("group")}
              >
                <Users size={15} />
                创建小组
              </button>
            )}
          </div>
        </div>
        {workspace && owner && (
          <div className="sidebar-invite">
            <span className="invite-doodle">✳</span>
            <strong>好伙伴，都在这里。</strong>
            <p>给团队多留一个位置。</p>
            <button onClick={() => setDialog("invite")}>
              <UserPlus size={14} />
              邀请伙伴
              <ArrowRight size={14} />
            </button>
          </div>
        )}
        <div className={`connection-status ${chat.connection}`}>
          <span className="status-dot" />
          {chat.connection === "connected"
            ? "消息已连接"
            : chat.connection === "connecting"
              ? "正在连接…"
              : chat.connection === "offline"
                ? "网络已断开"
                : "正在重新连接…"}
          {chat.connection === "offline" && <WifiOff size={12} />}
        </div>
        <div className="user-panel">
          <Link to="/app/profile" className="user-profile">
            <Avatar name={user!.displayName} seed={user!.id} />
            <span>
              <b>{user!.displayName}</b>
              <small>
                {user!.platformRole === "admin" ? "平台管理员" : "我的个人账户"}
              </small>
            </span>
          </Link>
          <Link
            className="icon-button"
            to="/app/settings"
            title="个人设置"
            aria-label="个人设置"
          >
            <Settings size={17} />
          </Link>
        </div>
      </aside>
      {sidebar && (
        <button
          className="sidebar-shade"
          onClick={() => setSidebar(false)}
          aria-label="关闭侧栏"
        />
      )}
      <main className="app-main">
        {error && !removing && <Notice>{error}</Notice>}
        {isManagement ? (
          <>
            <header className="settings-header">
              <IconButton
                label="打开频道列表"
                className="mobile-menu"
                onClick={() => setSidebar(true)}
              >
                <span>☰</span>
              </IconButton>
              {isGatewayPage ? (
                <KeyRound size={18} />
              ) : isAgentsPage || isDirectoryPage ? (
                <Bot size={18} />
              ) : isAdminPage ? (
                <ShieldCheck size={18} />
              ) : (
                <Settings size={18} />
              )}
              <span>
                {isGatewayPage
                  ? "平台管理 · 模型网关"
                  : isAdminPage
                    ? "平台管理 · 注册邀请"
                    : isAgentsPage
                      ? "我的 Agent"
                      : isDirectoryPage
                        ? "发现团队 Agent"
                        : "个人设置"}
              </span>
              <Link to="/app">
                回到对话
                <ChevronRight size={15} />
              </Link>
            </header>
            {(isAdminPage || isGatewayPage) &&
              user!.platformRole === "admin" && (
                <nav className="admin-section-nav">
                  <Link to="/app/admin" className={isAdminPage ? "active" : ""}>
                    <ShieldCheck size={13} />
                    注册邀请
                  </Link>
                  <Link
                    to="/app/admin/gateway"
                    className={isGatewayPage ? "active" : ""}
                  >
                    <KeyRound size={13} />
                    模型网关
                  </Link>
                </nav>
              )}
            {isGatewayPage ? (
              <ModelGatewaySettings key={user!.id} />
            ) : isAdminPage ? (
              <PlatformInvitations key={user!.id} />
            ) : isAgentsPage ? (
              agentMatch?.params.agentId ? (
                <AgentDetail
                  key={`${user!.id}:${agentMatch.params.agentId}`}
                  agentId={agentMatch.params.agentId}
                />
              ) : (
                <MyAgents key={user!.id} />
              )
            ) : isDirectoryPage ? (
              workspace ? (
                <WorkspaceAgents
                  key={`${user!.id}:${workspace.id}`}
                  workspace={workspace}
                />
              ) : (
                <div className="unavailable-view">
                  <h2>无法访问这个工作空间</h2>
                  <p>发现 Agent 需要当前空间成员权限。</p>
                  <Link className="button" to="/app">
                    回到我的工作空间
                  </Link>
                </div>
              )
            ) : (
              <ProfileSettings />
            )}
          </>
        ) : conversation &&
          workspace &&
          conversation.workspaceId === workspace.id ? (
          <ConversationView
            key={`${user!.id}:${conversation.id}`}
            conversation={conversation}
            members={members}
            agents={workspaceAgentsQuery.data?.agents || []}
            onMembers={() => setDetails(!details)}
            onMenu={() => setSidebar(true)}
          />
        ) : chat.data?.workspaces.length === 0 ? (
          <Welcome
            onCreate={() => setDialog("workspace")}
            userName={user!.displayName}
            onMenu={() => setSidebar(true)}
          />
        ) : (
          <div className="unavailable-view">
            <IconButton
              label="打开频道列表"
              className="mobile-menu"
              onClick={() => setSidebar(true)}
            >
              <span>☰</span>
            </IconButton>
            <span className="large-symbol">
              <Hash size={40} />
            </span>
            <h2>{match ? "这个对话暂时无法访问" : "找个话题，接着聊。"}</h2>
            <p>
              {match
                ? "你可能已离开工作空间，或尚未加入这个对话。"
                : "从左侧选择一个频道，或和伙伴开始一段私聊。"}
            </p>
            <Button
              onClick={() => {
                void chat.refresh();
                navigate("/app");
              }}
            >
              回到我的工作空间
            </Button>
          </div>
        )}
      </main>
      {details && !isManagement && workspace && (
        <aside className="members-pane">
          <header>
            <span>
              对话成员 <b>{visibleMembers.length}</b>
            </span>
            <IconButton label="关闭成员列表" onClick={() => setDetails(false)}>
              <X size={17} />
            </IconButton>
          </header>
          <div className="members-description">
            <span className="member-cover">
              <Users size={27} />
            </span>
            <h3>
              {conversation
                ? conversationName(conversation, user!.id, members)
                : workspace.name}
            </h3>
            <p>
              {conversation?.type === "channel"
                ? "团队的好点子，在这里碰头。"
                : "每一位伙伴，都让对话更好。"}
            </p>
          </div>
          <div className="members-list">
            <span className="members-list-label">
              {conversation?.type === "channel"
                ? "工作空间成员"
                : "当前对话成员"}
            </span>
            {membersQuery.isLoading && (
              <div className="member-loading">
                <LoaderCircle size={16} className="spin" />
                加载成员…
              </div>
            )}
            {membersQuery.error && <Notice>成员暂时无法加载。</Notice>}
            {visibleMembers.map((member) => (
              <div className="member-row" key={member.userId}>
                {member.kind === "agent" ? (
                  <AgentAvatar />
                ) : (
                  <Avatar name={member.displayName} seed={member.userId} />
                )}
                <div>
                  <b>
                    {member.displayName}
                    {member.userId === user!.id && <small>你</small>}
                    {member.kind === "agent" && (
                      <AgentBadge compact ownerName={member.ownerName} />
                    )}
                  </b>
                  <span>
                    {member.kind === "agent"
                      ? member.ownerName
                        ? `由 ${member.ownerName} 创建`
                        : "Agent · 所有者信息暂不可用"
                      : member.role === "owner"
                        ? "空间所有者"
                        : "团队成员"}
                  </span>
                </div>
                {member.role === "owner" && (
                  <ShieldCheck size={14} className="owner-shield" />
                )}
                {((conversation?.type === "group" &&
                  conversation.canManageMembers) ||
                  (conversation?.type === "channel" &&
                    owner &&
                    member.role !== "owner")) &&
                  member.userId !== user!.id && (
                    <IconButton
                      className="remove-member"
                      label={`${conversation?.type === "group" ? "从小组" : "从空间"}移除 ${member.displayName}`}
                      onClick={() => {
                        setRemoving(member);
                        setRemovingFromGroup(conversation?.type === "group");
                        setError("");
                      }}
                    >
                      <X size={14} />
                    </IconButton>
                  )}
              </div>
            ))}
          </div>
          {conversation?.type === "group" && conversation.canManageMembers && (
            <button
              className="members-invite"
              onClick={() => setManagingGroup(true)}
            >
              <Users size={16} />
              管理小组成员
              <Plus size={14} />
            </button>
          )}
          {owner && conversation?.type === "channel" && (
            <button
              className="members-invite"
              onClick={() => setDialog("invite")}
            >
              <UserPlus size={16} />
              邀请更多伙伴
              <Plus size={14} />
            </button>
          )}
          <div className="member-pane-note">
            <span>✧</span>
            <p>
              好团队的超能力，
              <br />
              是每个人都在场。
            </p>
          </div>
        </aside>
      )}
      {dialog && (
        <WorkspaceDialog
          kind={dialog}
          workspace={workspace}
          onClose={closeDialog}
        />
      )}{" "}
      {managingGroup &&
        conversation?.type === "group" &&
        conversation.canManageMembers && (
          <GroupMembersDialog
            conversation={conversation}
            members={members}
            onClose={closeGroupManager}
          />
        )}
      {removing && (
        <Modal
          title={`移除 ${removing.displayName}？`}
          subtitle={
            removingFromGroup
              ? "该成员将无法继续访问这个小组对话。工作空间成员资格不受影响。"
              : "该成员将无法继续访问这个工作空间及其中的对话。可以通过新邀请重新加入。"
          }
          onClose={closeRemoving}
        >
          <Notice>{error}</Notice>
          <div className="modal-actions">
            <Button className="secondary" onClick={closeRemoving}>
              取消
            </Button>
            <Button className="danger" busy={busy} onClick={remove}>
              确认移除
            </Button>
          </div>
        </Modal>
      )}
    </div>
  );
}
function Welcome({
  userName,
  onCreate,
  onMenu,
}: {
  userName: string;
  onCreate: () => void;
  onMenu: () => void;
}) {
  return (
    <div className="welcome-view">
      <header>
        <IconButton label="打开侧栏" className="mobile-menu" onClick={onMenu}>
          <span>☰</span>
        </IconButton>
        <span>
          <Home size={16} />
          你的起点
        </span>
        <span>WELCOME TO TEAMIE</span>
      </header>
      <div className="welcome-content">
        <div className="welcome-illustration">
          <span className="welcome-orbit" />
          <span className="welcome-bubble">
            <MessagesSquareIcon />
          </span>
          <span className="welcome-small-bubble">
            <Check size={20} />
          </span>
          <span className="welcome-star">✳</span>
        </div>
        <span className="eyebrow">A NEW CHAPTER, TOGETHER.</span>
        <h1>
          你好，{userName}
          <span>
            。<br />
            好团队，从这里开始。
          </span>
        </h1>
        <p>
          一个工作空间，把伙伴、话题和好点子放在一起。
          <br />
          给团队起个名字，开启你们的第一段对话。
        </p>
        <Button onClick={onCreate}>
          创建我的工作空间
          <Plus size={18} />
        </Button>
        <div className="welcome-steps">
          <span>
            <i>01</i>创建一个空间
          </span>
          <span className="step-line" />
          <span>
            <i>02</i>邀请你的伙伴
          </span>
          <span className="step-line" />
          <span>
            <i>03</i>一起聊，接着做
          </span>
        </div>
        <div className="welcome-tip">
          <CircleHelp size={16} />
          <span>已经收到工作空间邀请？直接打开邀请链接，即可加入伙伴们。</span>
        </div>
      </div>
      <footer>你们的下一件好事，值得一个好开始。</footer>
    </div>
  );
}
function MessagesSquareIcon() {
  return (
    <svg
      width="70"
      height="64"
      viewBox="0 0 70 64"
      fill="none"
      aria-hidden="true"
    >
      <path
        d="M12 12h39a9 9 0 0 1 9 9v20a9 9 0 0 1-9 9H33L21 60V50h-9a9 9 0 0 1-9-9V21a9 9 0 0 1 9-9z"
        fill="#fff"
      />
      <path
        d="M26 3h33a8 8 0 0 1 8 8v19a8 8 0 0 1-8 8h-2v8l-10-8H26a8 8 0 0 1-8-8V11a8 8 0 0 1 8-8z"
        fill="#d8efae"
        stroke="#7164d9"
        strokeWidth="3"
      />
      <circle cx="31" cy="21" r="2.7" fill="#7164d9" />
      <circle cx="42" cy="21" r="2.7" fill="#7164d9" />
      <circle cx="53" cy="21" r="2.7" fill="#7164d9" />
    </svg>
  );
}
