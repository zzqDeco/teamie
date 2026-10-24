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
  Hash,
  KeyRound,
  Menu,
  Settings,
  ShieldCheck,
  Users,
} from "lucide-react";
import { api } from "@teamie/api-client";
import type { Member, Workspace } from "@teamie/api-client";
import { useAuth } from "./auth";
import { errorText, useChat, useChatState } from "./chat";
import { ProfileSettings, WorkspaceDialog } from "./dialogs";
import { PlatformInvitations } from "./admin";
import { PlatformUsers } from "./platform-users";
import { ModelGatewaySettings } from "./model-gateway";
import { AgentDetail, MyAgents } from "./agents";
import { ConnectionBanner } from "./connection-banner";
import { WorkspaceMembers } from "./members";
import { GroupMembersDialog } from "./group-members";
import { enrichParticipants } from "./participants";
import type { Participant } from "./participants";
import { KeyboardShortcutsModal, QuickSwitcher } from "./quick-switcher";
import { chatStore, compareTimestampsDesc } from "./store";
import { setActiveNotificationContext } from "./notifications";
import type { DialogKind } from "./dialogs";
import { ConversationView, conversationName } from "./messages";
import {
  AttentionInbox,
  currentAttention,
  useAttentionCount,
} from "./attention";
import {
  Button,
  IconButton,
  Loading,
  Logo,
  Modal,
  Notice,
  useOverlayFocus,
} from "./ui";
import { ICON } from "./icons";
import {
  pickAdjacentConversation,
  route,
  ShellSidebar,
} from "./shell-sidebar";
import { ShellMembersPane } from "./shell-members-pane";
import { parseInviteCode, Welcome } from "./shell-welcome";
export { parseInviteCode };

function useDocumentTitle(
  pageTitle: string,
  urgentCount: number,
  hasChannelUnread: boolean,
) {
  useEffect(() => {
    const base = pageTitle ? `${pageTitle} · Teamie` : "Teamie";
    const prefix =
      urgentCount > 0
        ? `(${urgentCount > 99 ? "99+" : urgentCount}) `
        : hasChannelUnread
          ? "• "
          : "";
    document.title = `${prefix}${base}`;
  }, [pageTitle, urgentCount, hasChannelUnread]);
  useEffect(
    () => () => {
      document.title = "Teamie";
    },
    [],
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
  const attentionCount = useAttentionCount();
  const match = useMatch("/app/w/:workspaceId/c/:conversationId"),
    directoryMatch = useMatch("/app/w/:workspaceId/members"),
    legacyDirectoryMatch = useMatch("/app/w/:workspaceId/agents"),
    agentMatch = useMatch("/app/agents/:agentId"),
    workspaceRootMatch = useMatch("/app/w/:workspaceId"),
    location = useLocation(),
    navigate = useNavigate();
  const sidebarFocus = useRef({ open: false, pathname: location.pathname });
  const lastConversationByWorkspace = useRef<Record<string, string>>({});
  const lastChatRoute = useRef<string>("");
  const [lastChatPath, setLastChatPath] = useState("");
  const [recentConversationIds, setRecentConversationIds] = useState<string[]>(
    [],
  );
  const [quickSwitcherOpen, setQuickSwitcherOpen] = useState(false);
  const [shortcutsOpen, setShortcutsOpen] = useState(false);
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
    setRecentConversationIds([]);
    setQuickSwitcherOpen(false);
    setShortcutsOpen(false);
  }, [user?.id]);
  useEffect(() => {
    if (conversation && workspace && conversation.workspaceId === workspace.id) {
      const nextRoute = route(conversation);
      lastConversationByWorkspace.current[workspace.id] = conversation.id;
      lastChatRoute.current = nextRoute;
      setLastChatPath(nextRoute);
      setRecentConversationIds((prev) =>
        prev[0] === conversation.id
          ? prev
          : [
              conversation.id,
              ...prev.filter((id) => id !== conversation.id),
            ].slice(0, 20),
      );
    }
  }, [conversation, workspace]);
  useEffect(() => {
    const activeId =
      conversation && workspace && conversation.workspaceId === workspace.id
        ? conversation.id
        : null;
    setActiveNotificationContext(activeId, (to) => navigate(to));
    return () => {
      setActiveNotificationContext(undefined, null);
    };
  }, [conversation, workspace, navigate]);
  const conversations = Object.values(snapshot.conversations).filter(
    (c) => c.workspaceId === workspace?.id,
  );
  const channels = conversations
    .filter((c) => c.type === "channel")
    .sort((a, b) => a.name.localeCompare(b.name));
  const direct = conversations
    .filter((c) => c.type !== "channel")
    .sort((a, b) => compareTimestampsDesc(a.updatedAt, b.updatedAt));
  const membersQuery = useQuery({
    queryKey: ["members", user!.id, workspace?.id],
    queryFn: () => api.members(workspace!.id),
    enabled: !!workspace,
    refetchInterval: 60_000,
  });
  const discoveryAllowed = !!chat.data?.capabilities["agents.discover"];
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
    closeGroupManager = useCallback(() => setManagingGroup(false), []),
    closeQuickSwitcher = useCallback(() => setQuickSwitcherOpen(false), []),
    closeShortcuts = useCallback(() => setShortcutsOpen(false), []);
  useEffect(() => {
    setSidebar(false);
    setManagingGroup(false);
    if (
      location.pathname === "/app/attention" ||
      new URLSearchParams(location.search).has("message")
    )
      setDetails(false);
  }, [location.pathname, location.search]);
  const closeSidebar = useCallback(() => setSidebar(false), []);
  const closeDetails = useCallback(() => setDetails(false), []);
  const hasOverlayModal =
    !!dialog || quickSwitcherOpen || shortcutsOpen || managingGroup || !!removing;
  useOverlayFocus(
    navigationRef,
    mobile && sidebar && !hasOverlayModal,
    closeSidebar,
  );
  useEffect(() => {
    const previous = sidebarFocus.current;
    sidebarFocus.current = { open: sidebar, pathname: location.pathname };
    if (
      sidebar ||
      !mobile ||
      hasOverlayModal ||
      (!previous.open && previous.pathname === location.pathname)
    )
      return;
    const focused = document.activeElement;
    if (focused === document.body || navigationRef.current?.contains(focused)) {
      navigationRef.current?.parentElement
        ?.querySelector<HTMLElement>(".app-main .mobile-menu")
        ?.focus({ preventScroll: true });
    }
  }, [sidebar, mobile, hasOverlayModal, location.pathname]);
  useOverlayFocus(
    membersRef,
    overlayMembers &&
      details &&
      !sidebar &&
      !hasOverlayModal &&
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
  const pathname = location.pathname.replace(/\/+$/, "") || "/app";
  const caps = chat.data?.capabilities || {};
  const isSettings = ["/app/settings", "/app/profile"].includes(pathname);
  const isAdminPage = pathname === "/app/admin";
  const isGatewayPage = pathname === "/app/admin/gateway";
  const isUsersPage = pathname === "/app/admin/users";
  const isAgentsPage = pathname === "/app/agents" || !!agentMatch;
  const isDirectoryPage = !!directoryMatch;
  const isAttentionPage = pathname === "/app/attention";
  const activeWorkspaceIds = new Set(
    (chat.data?.workspaces || []).map((w) => w.id),
  );
  const allAuthorizedConversations = Object.values(
    snapshot.conversations,
  ).filter((c) => activeWorkspaceIds.has(c.workspaceId));
  const directUnreadCount = allAuthorizedConversations.reduce(
    (sum, c) =>
      c.type !== "channel"
        ? sum + Math.max(0, c.lastSeq - c.readSeq)
        : sum,
    0,
  );
  const rawChannelAttention = attentionCount.data?.channelUnreadCount || 0;
  let locallyResolvedChannel = 0;
  for (const item of attentionCount.data?.items || []) {
    if (item.isRead || item.conversation.type !== "channel") continue;
    const current = currentAttention(item, snapshot, user!.id);
    if (
      !current ||
      current.isRead ||
      current.conversation.type !== "channel"
    ) {
      locallyResolvedChannel += 1;
    }
  }
  const channelAttentionCount = Math.max(
    0,
    rawChannelAttention - locallyResolvedChannel,
  );
  const urgentCount = directUnreadCount + channelAttentionCount;
  const hasChannelUnread = allAuthorizedConversations.some(
    (c) => c.type === "channel" && c.lastSeq > c.readSeq,
  );
  const pageTitle = isAttentionPage
    ? "提及与回复"
    : isGatewayPage
      ? "模型网关"
      : isUsersPage
        ? "用户管理"
        : isAdminPage
          ? "注册邀请"
          : isAgentsPage
            ? "个人 Agent"
            : isDirectoryPage
              ? workspace
                ? `${workspace.name} · 成员`
                : "成员"
              : isSettings
                ? "账号设置"
                : conversation &&
                    workspace &&
                    conversation.workspaceId === workspace.id
                  ? conversation.type === "channel"
                    ? `# ${conversationName(conversation, user!.id, members)}`
                    : conversationName(conversation, user!.id, members)
                  : workspace?.name || "";
  useDocumentTitle(pageTitle, urgentCount, hasChannelUnread);
  useEffect(() => {
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.isComposing || event.keyCode === 229) return;
      const anyModalOpen = !!document.querySelector(".modal-backdrop");
      if (
        (event.metaKey || event.ctrlKey) &&
        !event.altKey &&
        !event.shiftKey &&
        event.key.toLowerCase() === "k"
      ) {
        if (anyModalOpen && !quickSwitcherOpen && !shortcutsOpen) return;
        event.preventDefault();
        setShortcutsOpen(false);
        setQuickSwitcherOpen((open) => !open);
        return;
      }
      const target = event.target as HTMLElement | null;
      const inEditable =
        !!target &&
        (target.tagName === "INPUT" ||
          target.tagName === "TEXTAREA" ||
          target.tagName === "SELECT" ||
          target.isContentEditable);
      if (inEditable || hasOverlayModal || anyModalOpen) return;
      if (
        event.altKey &&
        !event.metaKey &&
        !event.ctrlKey &&
        (event.key === "ArrowUp" || event.key === "ArrowDown")
      ) {
        const direction = event.key === "ArrowDown" ? 1 : -1;
        const ordered = [...channels, ...direct];
        const orderedWorkspaces = [
          ...(workspace ? [workspace] : []),
          ...(chat.data?.workspaces || []).filter(
            (w) => w.id !== workspace?.id,
          ),
        ];
        const allOrdered = orderedWorkspaces.flatMap((ws) => {
          if (ws.id === workspace?.id) return ordered;
          const inWs = Object.values(snapshot.conversations).filter(
            (c) => c.workspaceId === ws.id,
          );
          return [
            ...inWs
              .filter((c) => c.type === "channel")
              .sort((a, b) => a.name.localeCompare(b.name)),
            ...inWs
              .filter((c) => c.type !== "channel")
              .sort((a, b) => compareTimestampsDesc(a.updatedAt, b.updatedAt)),
          ];
        });
        const pool =
          event.shiftKey || ordered.length === 0 ? allOrdered : ordered;
        const next = pickAdjacentConversation(
          pool,
          conversation?.id,
          direction,
          event.shiftKey,
        );
        if (next) {
          event.preventDefault();
          navigate(route(next));
        }
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [
    channels,
    direct,
    workspace,
    snapshot.conversations,
    chat.data?.workspaces,
    conversation?.id,
    hasOverlayModal,
    quickSwitcherOpen,
    shortcutsOpen,
    navigate,
  ]);
  const messageTarget =
    new URLSearchParams(location.search).get("message") || "";
  const initialMessageId = messageTarget.length <= 128 ? messageTarget : "";
  // Clearing ?message= must not remount the view (that would drop staged
  // uploads); only a new conversation or a new non-empty target remounts it.
  const viewConversationId = match?.params.conversationId || "";
  const [viewKey, setViewKey] = useState({ conversation: "", target: "" });
  if (
    viewKey.conversation !== viewConversationId ||
    (initialMessageId && initialMessageId !== viewKey.target)
  )
    setViewKey({ conversation: viewConversationId, target: initialMessageId });
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
    pathname === "/app" &&
    chat.data?.workspaces.length &&
    (backChatRoute !== "/app" || conversations.length)
  )
    return (
      <Navigate
        to={
          backChatRoute !== "/app"
            ? backChatRoute
            : route(channels[0] || conversations[0])
        }
        replace
      />
    );
  if (workspaceRootMatch && chat.data) {
    const id = workspaceRootMatch.params.workspaceId!;
    const stored = Object.values(snapshot.conversations).filter(
      (c) => c.workspaceId === id,
    );
    const inWorkspace = stored.length
      ? stored
      : chat.data.conversations.filter((c) => c.workspaceId === id);
    const first =
      inWorkspace.find((c) => c.type === "channel") || inWorkspace[0];
    return (
      <Navigate
        to={first ? route(first) : `/app/w/${id}/members`}
        replace
      />
    );
  }
  if (
    chat.data &&
    !match &&
    !directoryMatch &&
    !legacyDirectoryMatch &&
    !agentMatch &&
    ![
      "/app",
      "/app/agents",
      "/app/settings",
      "/app/profile",
      "/app/admin",
      "/app/admin/gateway",
      "/app/admin/users",
      "/app/attention",
    ].includes(pathname)
  )
    return <Navigate to="/app" replace />;
  return (
    <div
      className={`app-shell ${sidebar ? "sidebar-open" : ""} ${details ? "details-open" : ""}`}
    >
      <ShellSidebar
        navigationRef={navigationRef}
        mobile={mobile}
        sidebar={sidebar}
        hasOverlayModal={hasOverlayModal}
        user={user!}
        busy={busy}
        connection={chat.connection}
        connectionLabel={connectionLabel}
        isAttentionPage={isAttentionPage}
        isAgentsPage={isAgentsPage}
        attentionUnreadCount={attentionCount.data?.unreadCount}
        workspaces={chat.data?.workspaces || []}
        workspace={workspace}
        owner={owner}
        caps={caps}
        allAuthorizedConversations={allAuthorizedConversations}
        channels={channels}
        direct={direct}
        conversationId={conversation?.id}
        members={members}
        onCloseSidebar={closeSidebar}
        onSelectWorkspace={selectWorkspace}
        onOpenDialog={openDialog}
        onSignOut={() => void signOut()}
        onShortcuts={() => setShortcutsOpen(true)}
      />
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
        <ConnectionBanner
          connection={chat.connection}
          syncError={chat.syncError}
          showSyncError={!conversation || isManagement || isAttentionPage}
          onRetry={chat.reconnect ?? chat.sync}
        />
        {error && !removing && <Notice>{error}</Notice>}
        {isAttentionPage ? (
          <AttentionInbox key={user!.id} onMenu={() => setSidebar(true)} />
        ) : isManagement ? (
          <>
            <header className="settings-header">
              <IconButton
                label="打开频道列表"
                className="mobile-menu"
                onClick={() => setSidebar(true)}
              >
                <Menu size={ICON.lg} />
              </IconButton>
              {isGatewayPage ? (
                <KeyRound size={ICON.md} />
              ) : isDirectoryPage || isUsersPage ? (
                <Users size={ICON.md} />
              ) : isAgentsPage ? (
                <Bot size={ICON.md} />
              ) : isAdminPage ? (
                <ShieldCheck size={ICON.md} />
              ) : (
                <Settings size={ICON.md} />
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
                <ArrowLeft size={ICON.sm} />
                <span>对话</span>
              </Link>
            </header>
            {(isAdminPage || isGatewayPage || isUsersPage) &&
              user!.platformRole === "admin" && (
                <nav className="admin-section-nav">
                  <Link to="/app/admin" className={isAdminPage ? "active" : ""}>
                    <ShieldCheck size={ICON.xs} />
                    注册邀请
                  </Link>
                  <Link
                    to="/app/admin/users"
                    className={isUsersPage ? "active" : ""}
                  >
                    <Users size={ICON.xs} />
                    用户管理
                  </Link>
                  <Link
                    to="/app/admin/gateway"
                    className={isGatewayPage ? "active" : ""}
                  >
                    <KeyRound size={ICON.xs} />
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
            key={`${user!.id}:${conversation.id}:${viewKey.target}`}
            conversation={conversation}
            initialMessageId={initialMessageId}
            onClearMessageTarget={() =>
              navigate(location.pathname, { replace: true })
            }
            members={members}
            onMembers={() => {
              setSidebar(false);
              setDetails(!details);
            }}
            membersOpen={details}
            onMenu={() => setSidebar(true)}
            isWorkspaceOwner={owner}
            onInvite={() => openDialog("invite")}
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
              <Menu size={ICON.lg} />
            </IconButton>
            <span className="large-symbol">
              <Hash size={ICON.xl} />
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
        <ShellMembersPane
          membersRef={membersRef}
          overlayMembers={overlayMembers}
          mobile={mobile}
          sidebar={sidebar}
          workspace={workspace}
          conversation={conversation}
          userId={user!.id}
          owner={owner}
          caps={caps}
          busy={busy}
          members={members}
          visibleMembers={visibleMembers}
          isLoading={membersQuery.isLoading}
          hasError={!!membersQuery.error}
          onClose={closeDetails}
          onStartDirect={(member) => void startDirect(member)}
          onRequestRemove={(member, fromGroup) => {
            setRemoving(member);
            setRemovingFromGroup(fromGroup);
            setError("");
          }}
          onManageGroup={() => setManagingGroup(true)}
          onInvite={() => setDialog("invite")}
        />
      )}
      {dialog && (
        <WorkspaceDialog
          kind={dialog}
          workspace={workspace}
          onClose={closeDialog}
        />
      )}{" "}
      {quickSwitcherOpen && (
        <QuickSwitcher
          workspaces={chat.data?.workspaces || []}
          conversations={allAuthorizedConversations}
          currentWorkspaceId={workspace?.id}
          discoveryAllowed={discoveryAllowed}
          members={members}
          userId={user!.id}
          recentConversationIds={recentConversationIds}
          onSelect={(path) => {
            setQuickSwitcherOpen(false);
            setSidebar(false);
            const wsPrefix = "/app/w/";
            if (path.startsWith(wsPrefix) && !path.includes("/c/")) {
              const targetWs = chat.data?.workspaces.find(
                (w) => w.id === path.slice(wsPrefix.length),
              );
              if (targetWs) {
                selectWorkspace(targetWs);
                return;
              }
            }
            navigate(path);
          }}
          onClose={closeQuickSwitcher}
        />
      )}
      {shortcutsOpen && <KeyboardShortcutsModal onClose={closeShortcuts} />}
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
