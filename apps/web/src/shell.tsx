import { useCallback, useEffect, useRef, useState } from "react";
import {
  Link,
  Navigate,
  useLocation,
  useMatch,
  useNavigate,
} from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowLeft,
  Bot,
  ChevronDown,
  Hash,
  Home,
  Menu,
  MessageCircle,
  KeyRound,
  LoaderCircle,
  Plus,
  Settings,
  ShieldCheck,
  UserPlus,
  Users,
  X,
} from "lucide-react";
import { api } from "@teamie/api-client";
import type { Conversation, Member, Workspace } from "@teamie/api-client";
import { useAuth } from "./auth";
import { errorText, useChat, useChatState } from "./chat";
import { ProfileSettings, WorkspaceDialog } from "./dialogs";
import { PlatformInvitations } from "./admin";
import { PlatformUsers } from "./platform-users";
import { ModelGatewaySettings } from "./model-gateway";
import { AgentDetail, MyAgents } from "./agents";
import { AccountMenu } from "./account-menu";
import { ConversationActions } from "./conversation-actions";
import { WorkspaceMembers } from "./members";
import { AgentAvatar, AgentBadge } from "./agent-ui";
import { GroupMembersDialog } from "./group-members";
import { enrichParticipants } from "./participants";
import type { Participant } from "./participants";
import { chatStore } from "./store";
import type { DialogKind } from "./dialogs";
import { ConversationView, conversationName } from "./messages";
import {
  Avatar,
  Button,
  IconButton,
  Loading,
  Logo,
  Modal,
  Notice,
  useOverlayFocus,
} from "./ui";

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
function useMediaQuery(query: string) {
  const [matches, setMatches] = useState(
    () => window.matchMedia(query).matches,
  );
  useEffect(() => {
    const media = window.matchMedia(query);
    const change = () => setMatches(media.matches);
    change();
    media.addEventListener("change", change);
    return () => media.removeEventListener("change", change);
  }, [query]);
  return matches;
}
export function Shell() {
  const { user, logout } = useAuth();
  const mobile = useMediaQuery("(max-width: 780px)");
  const overlayMembers = useMediaQuery("(max-width: 1250px)");
  const navigationRef = useRef<HTMLDivElement>(null);
  const membersRef = useRef<HTMLElement>(null);
  const chat = useChat(),
    snapshot = useChatState();
  const client = useQueryClient();
  const match = useMatch("/app/w/:workspaceId/c/:conversationId"),
    directoryMatch = useMatch("/app/w/:workspaceId/members"),
    legacyDirectoryMatch = useMatch("/app/w/:workspaceId/agents"),
    agentMatch = useMatch("/app/agents/:agentId"),
    location = useLocation(),
    navigate = useNavigate();
  const sidebarFocus = useRef({ open: false, pathname: location.pathname });
  const lastConversationByWorkspace = useRef<Record<string, string>>({});
  const lastChatRoute = useRef<string>("");
  const [lastChatPath, setLastChatPath] = useState("");
  const [dialog, setDialog] = useState<DialogKind | null>(null),
    [sidebar, setSidebar] = useState(false),
    [details, setDetails] = useState(false),
    [managingGroup, setManagingGroup] = useState(false),
    [removingFromGroup, setRemovingFromGroup] = useState(false);
  const [removing, setRemoving] = useState<Member | null>(null),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const workspace =
    chat.data?.workspaces.find(
      (w) =>
        w.id ===
        (match?.params.workspaceId ||
          directoryMatch?.params.workspaceId ||
          legacyDirectoryMatch?.params.workspaceId),
    ) ||
    (match || directoryMatch || legacyDirectoryMatch
      ? undefined
      : chat.data?.workspaces[0]);
  const owner = workspace?.createdBy === user!.id;
  const conversation = match?.params.conversationId
    ? snapshot.conversations[match.params.conversationId]
    : undefined;
  useEffect(() => {
    lastConversationByWorkspace.current = {};
    lastChatRoute.current = "";
    setLastChatPath("");
  }, [user?.id]);
  useEffect(() => {
    if (conversation && workspace && conversation.workspaceId === workspace.id) {
      const nextRoute = route(conversation);
      lastConversationByWorkspace.current[workspace.id] = conversation.id;
      lastChatRoute.current = nextRoute;
      setLastChatPath(nextRoute);
    }
  }, [conversation, workspace]);
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
    !!chat.data?.capabilities["agents.discover"];
  const workspaceAgentsQuery = useQuery({
    queryKey: ["workspaceAgents", user!.id, workspace?.id],
    queryFn: () => api.workspaceAgents(workspace!.id),
    enabled: !!workspace && discoveryAllowed,
    refetchInterval: 30_000,
  });
  const workspaceAgents = discoveryAllowed
    ? workspaceAgentsQuery.data?.agents || []
    : [];
  const members: Participant[] = enrichParticipants(
    membersQuery.data?.members || [],
    workspaceAgents,
  );
  const visibleMembers: Participant[] =
    conversation && conversation.type !== "channel"
      ? members.filter((m) => conversation.memberIds.includes(m.userId))
      : members;
  const closeDialog = useCallback(() => setDialog(null), []),
    closeRemoving = useCallback(() => setRemoving(null), []),
    closeGroupManager = useCallback(() => setManagingGroup(false), []);
  useEffect(() => {
    setSidebar(false);
    setManagingGroup(false);
  }, [location.pathname]);
  const closeSidebar = useCallback(() => setSidebar(false), []);
  const closeDetails = useCallback(() => setDetails(false), []);
  useOverlayFocus(navigationRef, mobile && sidebar && !dialog, closeSidebar);
  useEffect(() => {
    const previous = sidebarFocus.current;
    sidebarFocus.current = { open: sidebar, pathname: location.pathname };
    if (
      sidebar ||
      !mobile ||
      dialog ||
      (!previous.open && previous.pathname === location.pathname)
    ) return;
    const focused = document.activeElement;
    if (
      focused === document.body ||
      navigationRef.current?.contains(focused)
    ) {
      navigationRef.current?.parentElement
        ?.querySelector<HTMLElement>(".app-main .mobile-menu")
        ?.focus({ preventScroll: true });
    }
  }, [sidebar, mobile, dialog, location.pathname]);
  useOverlayFocus(
    membersRef,
    overlayMembers &&
      details &&
      !sidebar &&
      !dialog &&
      !managingGroup &&
      !removing &&
      !!conversation,
    closeDetails,
  );
  useEffect(() => {
    const viewport = window.visualViewport;
    const resize = () =>
      document.documentElement.style.setProperty(
        "--app-height",
        `${viewport?.height || window.innerHeight}px`,
      );
    resize();
    viewport?.addEventListener("resize", resize);
    window.addEventListener("resize", resize);
    return () => {
      viewport?.removeEventListener("resize", resize);
      window.removeEventListener("resize", resize);
      document.documentElement.style.removeProperty("--app-height");
    };
  }, []);
  const caps = chat.data?.capabilities || {};
  const isSettings = ["/app/settings", "/app/profile"].includes(
    location.pathname,
  );
  const isAdminPage = location.pathname === "/app/admin";
  const isGatewayPage = location.pathname === "/app/admin/gateway";
  const isUsersPage = location.pathname === "/app/admin/users";
  const isAgentsPage = location.pathname === "/app/agents" || !!agentMatch;
  const isDirectoryPage = !!directoryMatch;
  const isManagement =
    isSettings ||
    isAdminPage ||
    isGatewayPage ||
    isUsersPage ||
    isAgentsPage ||
    isDirectoryPage;
  const backChatRoute = Object.values(snapshot.conversations).some(
    (c) => route(c) === lastChatPath,
  )
    ? lastChatPath
    : "/app";
  const connectionLabel =
    chat.connection === "connected"
      ? "消息已连接"
      : chat.connection === "connecting"
        ? "正在连接…"
        : chat.connection === "offline"
          ? "网络已断开"
          : "正在重新连接…";
  function openDialog(kind: DialogKind) {
    setSidebar(false);
    setDialog(kind);
  }
  async function startDirect(member: Participant) {
    if (!workspace || member.selectable === false || !caps["im.dm.create"])
      return;
    setBusy(true);
    setError("");
    const generation = chatStore.generation;
    try {
      const result = await api.createConversation(workspace.id, {
        type: "direct",
        memberIds: [member.userId],
      });
      if (generation !== chatStore.generation) return;
      chatStore.addConversation(result.conversation);
      await chat.refresh();
      if (generation !== chatStore.generation) return;
      if (overlayMembers) setDetails(false);
      navigate(route(result.conversation));
    } catch (e) {
      if (generation === chatStore.generation) setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
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
    const sessionToken = api.csrfToken;
    setBusy(true);
    setError("");
    try {
      await logout();
      if (!api.csrfToken) navigate("/login");
    } catch (e) {
      if (sessionToken === api.csrfToken) setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  function selectWorkspace(space: Workspace) {
    const rememberedId = lastConversationByWorkspace.current[space.id];
    const remembered = rememberedId
      ? snapshot.conversations[rememberedId]
      : undefined;
    const first =
      (remembered && remembered.workspaceId === space.id
        ? remembered
        : undefined) ||
      Object.values(snapshot.conversations).find(
        (c) => c.workspaceId === space.id && c.type === "channel",
      ) ||
      Object.values(snapshot.conversations).find(
        (c) => c.workspaceId === space.id,
      );
    if (first) navigate(route(first));
    else if (isManagement || workspace?.id !== space.id)
      navigate(`/app/w/${space.id}/members`);
    setSidebar(false);
  }
  if (chat.loading && !chat.data) return <Loading />;
  if (legacyDirectoryMatch)
    return (
      <Navigate
        to={`/app/w/${legacyDirectoryMatch.params.workspaceId}/members`}
        replace
      />
    );
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
  return (
    <div
      className={`app-shell ${sidebar ? "sidebar-open" : ""} ${details ? "details-open" : ""}`}
    >
      <div
        className="app-navigation"
        ref={navigationRef}
        role={mobile && sidebar ? "dialog" : undefined}
        aria-modal={mobile && sidebar ? true : undefined}
        aria-label={mobile ? "对话与团队导航" : undefined}
        tabIndex={-1}
        inert={mobile && !sidebar}
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
            onClick={() => openDialog("workspace")}
          >
            <Plus size={23} />
          </IconButton>
          <span className="rail-spacer" />
          <Link
            to="/app/agents"
            className={`rail-settings rail-agents ${isAgentsPage ? "selected" : ""}`}
            title="个人 Agent"
            aria-label="个人 Agent"
            aria-current={isAgentsPage ? "page" : undefined}
            onClick={closeSidebar}
          >
            <Bot size={22} aria-hidden="true" />
          </Link>
          <AccountMenu
            user={user!}
            busy={busy}
            active={!mobile || sidebar}
            connection={chat.connection}
            connectionLabel={connectionLabel}
            onSignOut={() => void signOut()}
            onNavigate={closeSidebar}
          />
        </aside>
        <aside className="navigation-sidebar">
          <div className="workspace-header">
            <div className="workspace-title" title={workspace?.name}>
              <b>{workspace?.name || "我的 Teamie"}</b>
            </div>
            {workspace && (caps["im.dm.create"] || caps["im.group.create"]) && (
              <ConversationActions
                key={workspace.id}
                active={(!mobile || sidebar) && !dialog}
                canDirect={!!caps["im.dm.create"]}
                canGroup={!!caps["im.group.create"]}
                onSelect={openDialog}
              />
            )}
            <IconButton
              className="mobile-close"
              label="关闭频道列表"
              onClick={() => setSidebar(false)}
            >
              <X size={18} />
            </IconButton>
          </div>
          <div className="sidebar-scroll">
            <div className="nav-section">
              <div className="nav-section-title">
                <span>
                  <ChevronDown size={12} /> 团队频道
                </span>
                {workspace && owner && caps["im.channel.create"] && (
                  <IconButton
                    label="创建频道"
                    onClick={() => openDialog("channel")}
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
                  {workspace ? "暂无频道" : "暂无团队"}
                </div>
              )}
            </div>
            <div className="nav-section">
              <div className="nav-section-title">
                <span>
                  <ChevronDown size={12} /> 私信与小组
                </span>
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
                  {workspace ? "暂无对话" : "暂无对话"}
                </div>
              )}
            </div>
          </div>
        </aside>
      </div>
      {sidebar && mobile && (
        <button
          className="sidebar-shade"
          onClick={() => setSidebar(false)}
          aria-label="关闭侧栏"
        />
      )}
      <main
        className="app-main"
        inert={
          (mobile && sidebar) ||
          (overlayMembers && details && !isManagement && !!conversation)
        }
      >
        {error && !removing && <Notice>{error}</Notice>}
        {isManagement ? (
          <>
            <header className="settings-header">
              <IconButton
                label="打开频道列表"
                className="mobile-menu"
                onClick={() => setSidebar(true)}
              >
                <Menu size={21} />
              </IconButton>
              {isGatewayPage ? (
                <KeyRound size={18} />
              ) : isDirectoryPage || isUsersPage ? (
                <Users size={18} />
              ) : isAgentsPage ? (
                <Bot size={18} />
              ) : isAdminPage ? (
                <ShieldCheck size={18} />
              ) : (
                <Settings size={18} />
              )}
              <span>
                {isGatewayPage
                  ? "模型网关"
                  : isUsersPage
                    ? "用户管理"
                  : isAdminPage
                    ? "注册邀请"
                    : isAgentsPage
                      ? "个人 Agent"
                      : isDirectoryPage
                        ? "成员"
                        : "账号设置"}
              </span>
              <Link to={backChatRoute}>
                <ArrowLeft size={15} />
                <span>对话</span>
              </Link>
            </header>
            {(isAdminPage || isGatewayPage || isUsersPage) &&
              user!.platformRole === "admin" && (
                <nav className="admin-section-nav">
                  <Link to="/app/admin" className={isAdminPage ? "active" : ""}>
                    <ShieldCheck size={13} />
                    注册邀请
                  </Link>
                  <Link to="/app/admin/users" className={isUsersPage ? "active" : ""}>
                    <Users size={13} />
                    用户管理
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
            {isUsersPage ? (
              <PlatformUsers />
            ) : isGatewayPage ? (
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
                <WorkspaceMembers
                  key={`${user!.id}:${workspace.id}`}
                  workspace={workspace}
                  members={members}
                  agents={workspaceAgents}
                  loading={
                    membersQuery.isLoading ||
                    (discoveryAllowed && workspaceAgentsQuery.isLoading)
                  }
                  loadError={
                    !!membersQuery.error ||
                    (discoveryAllowed && !!workspaceAgentsQuery.error)
                  }
                  onRetry={() => {
                    void membersQuery.refetch();
                    if (discoveryAllowed) void workspaceAgentsQuery.refetch();
                  }}
                />
              ) : (
                <div className="unavailable-view">
                  <h2>无法访问这个工作空间</h2>
                  <p>查看成员需要当前空间成员权限。</p>
                  <Link className="button" to="/app">
                    返回对话
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
            onMembers={() => {
              setSidebar(false);
              setDetails(!details);
            }}
            membersOpen={details}
            onMenu={() => setSidebar(true)}
          />
        ) : chat.data?.workspaces.length === 0 ? (
          <Welcome
            onCreate={() => openDialog("workspace")}
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
              <Menu size={21} />
            </IconButton>
            <span className="large-symbol">
              <Hash size={40} />
            </span>
            <h2>{match ? "这个对话暂时无法访问" : "选择一个对话"}</h2>
            <p>
              {match
                ? "你可能已离开工作空间，或尚未加入这个对话。"
                : "打开对话列表，选择频道或私信。"}
            </p>
            <Button
              onClick={() => {
                void chat.refresh();
                navigate("/app");
              }}
            >
              返回对话
            </Button>
          </div>
        )}
      </main>
      {details && !isManagement && workspace && conversation && (
        <>
          {overlayMembers && !sidebar && (
            <button
              className="members-shade"
              aria-label="关闭成员列表"
              onClick={closeDetails}
            />
          )}
          <aside
            className="members-pane"
            ref={membersRef}
            role={overlayMembers ? "dialog" : undefined}
            aria-modal={overlayMembers ? true : undefined}
            aria-label="对话成员"
            tabIndex={-1}
            inert={mobile && sidebar}
          >
            <header>
              <span>
                对话成员 <b>{visibleMembers.length}</b>
              </span>
              <IconButton
                label="关闭成员列表"
                onClick={() => setDetails(false)}
              >
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
                          : "Agent"
                        : member.role === "owner"
                          ? "空间所有者"
                          : "团队成员"}
                    </span>
                  </div>
                  {member.role === "owner" && (
                    <ShieldCheck size={14} className="owner-shield" />
                  )}
                  {member.userId !== user!.id &&
                    caps["im.dm.create"] &&
                    member.selectable !== false && (
                      <IconButton
                        className="member-dm"
                        label={`与 ${member.displayName} 发起私聊`}
                        disabled={busy}
                        onClick={() => void startDirect(member)}
                      >
                        <MessageCircle size={14} />
                      </IconButton>
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
            <Link
              className="members-directory-link"
              to={`/app/w/${workspace.id}/members`}
              onClick={closeDetails}
            >
              <Users size={16} />
              查看全部成员
            </Link>
            {conversation?.type === "group" &&
              conversation.canManageMembers && (
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
                邀请成员
                <Plus size={14} />
              </button>
            )}
          </aside>
        </>
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
          <Menu size={21} />
        </IconButton>
        <span>
          <Home size={16} />
          你的起点
        </span>
      </header>
      <div className="welcome-content">
        <div className="welcome-illustration">
          <span className="welcome-bubble">
            <MessagesSquareIcon />
          </span>
        </div>
        <h1>你好，{userName}</h1>
        <p>
          创建一个团队，开始协作。
          <br />
          已有团队邀请？打开邀请链接即可加入。
        </p>
        <Button onClick={onCreate}>
          <Plus size={18} />
          创建工作空间
        </Button>
      </div>
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
