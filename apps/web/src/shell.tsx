import { useCallback, useEffect, useRef, useState } from "react";
import { Navigate, useLocation, useMatch, useNavigate } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@teamie/api-client";
import type { Member, Workspace } from "@teamie/api-client";
import { useAuth } from "./auth";
import { useChat, useChatState } from "./chat";
import { errorText } from "./errors";
import { WorkspaceDialog } from "./dialogs";
import { ConnectionBanner } from "./connection-banner";
import { GroupMembersDialog } from "./group-members";
import { enrichParticipants } from "./participants";
import type { Participant } from "./participants";
import { KeyboardShortcutsModal, QuickSwitcher } from "./quick-switcher";
import { chatStore, compareTimestampsDesc } from "./store";
import { presenceStore } from "./engagement-store";
import { setActiveNotificationContext } from "./notifications";
import type { DialogKind } from "./dialogs";
import { ConversationView } from "./messages";
import { AttentionInbox, useAttentionCount } from "./attention";
import { Button, Loading, Logo, Notice } from "./ui";
import {
  computePageTitle,
  computeShellAttention,
  resolveWorkspaceTarget,
  route,
  ShellManagementView,
  ShellSidebar,
  useShellViewportAndFocus,
} from "./shell-sidebar";
import { RemoveMemberModal, ShellMembersPane } from "./shell-members-pane";
import { parseInviteCode, Welcome } from "./shell-welcome";
import {
  clearPersistedShellNav,
  readPersistedShellNav,
  UnavailableConversationView,
  useDocumentTitle,
  useMediaQuery,
  useShellKeyboardShortcuts,
  writePersistedShellNav,
} from "./shell/shell-hooks";
export { parseInviteCode, clearPersistedShellNav };

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
  const [initialNav] = useState(() => readPersistedShellNav(user?.id));
  const navUserIdRef = useRef(user?.id);
  const lastConversationByWorkspace = useRef<Record<string, string>>(
    initialNav.lastConversationByWorkspace,
  );
  const lastChatRoute = useRef<string>(initialNav.lastChatPath);
  const [lastChatPath, setLastChatPath] = useState(
    initialNav.lastChatPath,
  );
  const [recentConversationIds, setRecentConversationIds] = useState<string[]>(
    initialNav.recentConversationIds,
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
  // Direct-message failures render inside the members pane: when it is an
  // overlay, <main> is inert and a shell-level notice would be invisible.
  const [directError, setDirectError] = useState(""),
    [directBusyId, setDirectBusyId] = useState<string | null>(null);
  const workspace =
    chat.data?.workspaces.find(
      (w) =>
        w.id ===
        (match?.params.workspaceId ||
          directoryMatch?.params.workspaceId ||
          legacyDirectoryMatch?.params.workspaceId),
    ) ||
    (match || directoryMatch || legacyDirectoryMatch ? undefined : chat.data?.workspaces[0]);
  const owner = workspace?.createdBy === user!.id;
  const conversation = match?.params.conversationId
    ? snapshot.conversations[match.params.conversationId]
    : undefined;
  useEffect(() => {
    if (navUserIdRef.current && navUserIdRef.current !== user?.id) {
      clearPersistedShellNav(navUserIdRef.current);
    }
    navUserIdRef.current = user?.id;
    const restored = readPersistedShellNav(user?.id);
    lastConversationByWorkspace.current = restored.lastConversationByWorkspace;
    lastChatRoute.current = restored.lastChatPath;
    setLastChatPath(restored.lastChatPath);
    setRecentConversationIds(restored.recentConversationIds);
    setQuickSwitcherOpen(false);
    setShortcutsOpen(false);
  }, [user?.id]);
  useEffect(() => {
    if (
      conversation &&
      workspace &&
      conversation.workspaceId === workspace.id &&
      user?.id
    ) {
      const nextRoute = route(conversation);
      lastConversationByWorkspace.current[workspace.id] = conversation.id;
      lastChatRoute.current = nextRoute;
      setLastChatPath(nextRoute);
      setRecentConversationIds((prev) => {
        const nextRecent =
          prev[0] === conversation.id
            ? prev
            : [
                conversation.id,
                ...prev.filter((id) => id !== conversation.id),
              ].slice(0, 20);
        writePersistedShellNav(user.id, {
          lastChatPath: nextRoute,
          lastConversationByWorkspace: {
            ...lastConversationByWorkspace.current,
          },
          recentConversationIds: nextRecent,
        });
        return nextRecent;
      });
    }
  }, [conversation, workspace, user?.id]);
  useEffect(() => {
    const activeId =
      conversation && workspace && conversation.workspaceId === workspace.id
        ? conversation.id
        : null;
    setActiveNotificationContext(activeId, (to) => navigate(to));
    return () => { setActiveNotificationContext(undefined, null); };
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
  const workspaceAgents = discoveryAllowed ? workspaceAgentsQuery.data?.agents || [] : [];
  const presenceAllowed = !!chat.data?.capabilities["im.presence.publish"];
  const workspacePresenceQuery = useQuery({
    queryKey: ["workspacePresence", user!.id, workspace?.id],
    queryFn: () => api.workspacePresence(workspace!.id),
    enabled: !!workspace && presenceAllowed,
    refetchInterval: 60_000,
  });
  useEffect(() => {
    if (workspace && presenceAllowed && workspacePresenceQuery.data) {
      const humanIds = (membersQuery.data?.members || [])
        .filter((m) => m.kind === "human")
        .map((m) => m.userId);
      presenceStore.applyWorkspaceSnapshot(
        humanIds,
        workspacePresenceQuery.data.items || [],
      );
    }
  }, [
    workspace,
    presenceAllowed,
    workspacePresenceQuery.data,
    membersQuery.data,
  ]);
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
    setError("");
    setDirectError("");
    if (
      location.pathname === "/app/attention" ||
      new URLSearchParams(location.search).has("message")
    )
      setDetails(false);
  }, [location.pathname, location.search]);
  const closeSidebar = useCallback(() => setSidebar(false), []);
  const closeDetails = useCallback(() => {
    setDetails(false);
    setDirectError("");
  }, []);
  const hasOverlayModal =
    !!dialog || quickSwitcherOpen || shortcutsOpen || managingGroup || !!removing;
  useShellViewportAndFocus({
    navigationRef, membersRef, mobile, overlayMembers, sidebar, details,
    hasOverlayModal, hasConversation: !!conversation, pathname: location.pathname,
    closeSidebar, closeDetails,
  });
  const pathname = location.pathname.replace(/\/+$/, "") || "/app";
  const caps = chat.data?.capabilities || {};
  const isSettings = ["/app/settings", "/app/profile"].includes(pathname);
  const isAdminPage = pathname === "/app/admin";
  const isGatewayPage = pathname === "/app/admin/gateway";
  const isUsersPage = pathname === "/app/admin/users";
  const isAgentsPage = pathname === "/app/agents" || !!agentMatch;
  const isDirectoryPage = !!directoryMatch;
  const isAttentionPage = pathname === "/app/attention";
  const { allAuthorizedConversations, urgentCount, hasChannelUnread } =
    computeShellAttention({
      workspaces: chat.data?.workspaces || [],
      snapshot,
      userId: user!.id,
      attentionData: attentionCount.data,
    });
  const pageTitle = computePageTitle({
    isAttentionPage, isGatewayPage, isUsersPage, isAdminPage, isAgentsPage,
    isDirectoryPage, isSettings, workspace, conversation, userId: user!.id, members,
  });
  useDocumentTitle(pageTitle, urgentCount, hasChannelUnread);
  useShellKeyboardShortcuts({
    channels, direct, workspace, conversationsById: snapshot.conversations,
    workspaces: chat.data?.workspaces, conversationId: conversation?.id,
    hasOverlayModal, quickSwitcherOpen, shortcutsOpen,
    setShortcutsOpen, setQuickSwitcherOpen, navigate,
  });
  const messageTarget = new URLSearchParams(location.search).get("message") || "";
  const initialMessageId = messageTarget.length <= 128 ? messageTarget : "";
  const viewConversationId = match?.params.conversationId || "";
  const [viewKey, setViewKey] = useState({ conversation: "", target: "" });
  if (
    viewKey.conversation !== viewConversationId ||
    (initialMessageId && initialMessageId !== viewKey.target)
  )
    setViewKey({ conversation: viewConversationId, target: initialMessageId });
  const isManagement =
    isSettings || isAdminPage || isGatewayPage || isUsersPage || isAgentsPage || isDirectoryPage;
  const backChatRoute = Object.values(snapshot.conversations).some((c) => route(c) === lastChatPath)
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
    if (!workspace || member.selectable === false || !caps["im.dm.create"]) return;
    setBusy(true);
    setDirectBusyId(member.userId);
    setDirectError("");
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
      if (generation === chatStore.generation) setDirectError(errorText(e));
    } finally {
      setBusy(false);
      setDirectBusyId(null);
    }
  }
  async function remove() {
    if (!workspace || !removing) return;
    setBusy(true);
    setError("");
    try {
      if (removingFromGroup && conversation) {
        const result = await api.removeConversationMember(conversation.id, removing.userId);
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
    const currentUserId = user?.id;
    setBusy(true);
    setError("");
    try {
      await logout();
      if (!api.csrfToken) {
        clearPersistedShellNav(currentUserId);
        lastConversationByWorkspace.current = {};
        lastChatRoute.current = "";
        setLastChatPath("");
        setRecentConversationIds([]);
        navigate("/login");
      }
    } catch (e) {
      if (sessionToken === api.csrfToken) setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  function selectWorkspace(space: Workspace) {
    const target = resolveWorkspaceTarget({
      space,
      rememberedId: lastConversationByWorkspace.current[space.id],
      conversations: snapshot.conversations,
      isManagement,
      currentWorkspaceId: workspace?.id,
    });
    if (target) navigate(target);
    setSidebar(false);
  }
  if (chat.loading && !chat.data) return <Loading />;
  if (legacyDirectoryMatch)
    return <Navigate to={`/app/w/${legacyDirectoryMatch.params.workspaceId}/members`} replace />;
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
        to={backChatRoute !== "/app" ? backChatRoute : route(channels[0] || conversations[0])}
        replace
      />
    );
  if (workspaceRootMatch && chat.data) {
    const id = workspaceRootMatch.params.workspaceId!;
    const stored = Object.values(snapshot.conversations).filter((c) => c.workspaceId === id);
    const inWorkspace = stored.length
      ? stored
      : chat.data.conversations.filter((c) => c.workspaceId === id);
    const first = inWorkspace.find((c) => c.type === "channel") || inWorkspace[0];
    return <Navigate to={first ? route(first) : `/app/w/${id}/members`} replace />;
  }
  if (
    chat.data && !match && !directoryMatch && !legacyDirectoryMatch && !agentMatch &&
    ![
      "/app", "/app/agents", "/app/settings", "/app/profile",
      "/app/admin", "/app/admin/gateway", "/app/admin/users", "/app/attention",
    ].includes(pathname)
  )
    return <Navigate to="/app" replace />;
  return (
    <div className={`app-shell ${sidebar ? "sidebar-open" : ""} ${details ? "details-open" : ""}`}>
      <ShellSidebar
        navigationRef={navigationRef} mobile={mobile} sidebar={sidebar}
        hasOverlayModal={hasOverlayModal} user={user!} busy={busy}
        connection={chat.connection} connectionLabel={connectionLabel}
        isAttentionPage={isAttentionPage} isAgentsPage={isAgentsPage}
        attentionUnreadCount={attentionCount.data?.unreadCount}
        attentionProjection={attentionCount.data?.attentionUnreadByConversation}
        snapshot={snapshot}
        workspaces={chat.data?.workspaces || []} workspace={workspace}
        owner={owner} caps={caps}
        allAuthorizedConversations={allAuthorizedConversations}
        channels={channels} direct={direct} conversationId={conversation?.id}
        members={members} onCloseSidebar={closeSidebar}
        onSelectWorkspace={selectWorkspace} onOpenDialog={openDialog}
        onSignOut={() => void signOut()} onShortcuts={() => setShortcutsOpen(true)}
      />
      {sidebar && mobile && (
        <button className="sidebar-shade" onClick={() => setSidebar(false)} aria-label="关闭侧栏" />
      )}
      <main
        className="app-main"
        inert={(mobile && sidebar) || (overlayMembers && details && !isManagement && !!conversation)}
      >
        <ConnectionBanner
          connection={chat.connection} syncError={chat.syncError}
          showSyncError={!conversation || isManagement || isAttentionPage}
          onRetry={chat.reconnect ?? chat.sync}
        />
        {error && !removing && <Notice>{error}</Notice>}
        {isAttentionPage ? (
          <AttentionInbox key={user!.id} onMenu={() => setSidebar(true)} />
        ) : isManagement ? (
          <ShellManagementView
            user={user!} isGatewayPage={isGatewayPage} isUsersPage={isUsersPage}
            isAdminPage={isAdminPage} isAgentsPage={isAgentsPage}
            isDirectoryPage={isDirectoryPage} agentId={agentMatch?.params.agentId}
            backChatRoute={backChatRoute} workspace={workspace} members={members}
            workspaceAgents={workspaceAgents}
            membersLoading={
              membersQuery.isLoading || (discoveryAllowed && workspaceAgentsQuery.isLoading)
            }
            membersLoadError={
              !!membersQuery.error || (discoveryAllowed && !!workspaceAgentsQuery.error)
            }
            onOpenSidebar={() => setSidebar(true)}
            onRetryMembers={() => {
              void membersQuery.refetch();
              if (discoveryAllowed) void workspaceAgentsQuery.refetch();
            }}
          />
        ) : conversation && workspace && conversation.workspaceId === workspace.id ? (
          <ConversationView
            key={`${user!.id}:${conversation.id}:${viewKey.target}`}
            conversation={conversation} initialMessageId={initialMessageId}
            onClearMessageTarget={() => navigate(location.pathname, { replace: true })}
            members={members}
            onMembers={() => {
              setSidebar(false);
              setDirectError("");
              setDetails(!details);
            }}
            membersOpen={details} onMenu={() => setSidebar(true)}
            isWorkspaceOwner={owner} onInvite={() => openDialog("invite")}
          />
        ) : chat.data?.workspaces.length === 0 ? (
          <Welcome
            onCreate={() => openDialog("workspace")}
            userName={user!.displayName}
            onMenu={() => setSidebar(true)}
          />
        ) : (
          <UnavailableConversationView
            hasMatch={!!match}
            onOpenSidebar={() => setSidebar(true)}
            onBack={() => {
              void chat.refresh();
              navigate("/app");
            }}
          />
        )}
      </main>
      {details && !isManagement && workspace && conversation && (
        <ShellMembersPane
          membersRef={membersRef} overlayMembers={overlayMembers} mobile={mobile}
          sidebar={sidebar} workspace={workspace} conversation={conversation}
          userId={user!.id} owner={owner} caps={caps} busy={busy}
          directBusyId={directBusyId} directError={directError}
          members={members} visibleMembers={visibleMembers}
          isLoading={membersQuery.isLoading} hasError={!!membersQuery.error}
          onClose={closeDetails} onStartDirect={(member) => void startDirect(member)}
          onRequestRemove={(member, fromGroup) => {
            setRemoving(member);
            setRemovingFromGroup(fromGroup);
            setError("");
          }}
          onManageGroup={() => setManagingGroup(true)}
          onInvite={() => setDialog("invite")}
        />
      )}
      {dialog && <WorkspaceDialog kind={dialog} workspace={workspace} onClose={closeDialog} />}
      {quickSwitcherOpen && (
        <QuickSwitcher
          workspaces={chat.data?.workspaces || []}
          conversations={allAuthorizedConversations}
          currentWorkspaceId={workspace?.id}
          discoveryAllowed={discoveryAllowed}
          members={members} userId={user!.id}
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
      {managingGroup && conversation?.type === "group" && conversation.canManageMembers && (
        <GroupMembersDialog
          conversation={conversation} members={members} onClose={closeGroupManager}
        />
      )}
      {removing && (
        <RemoveMemberModal
          removing={removing} removingFromGroup={removingFromGroup}
          error={error} busy={busy} onClose={closeRemoving}
          onConfirm={() => void remove()}
        />
      )}
    </div>
  );
}

