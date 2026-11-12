import { useCallback, useEffect, useRef, useState } from "react";
import { Navigate, useNavigate } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@teamie/api-client";
import type { Member, Workspace } from "@teamie/api-client";
import { useAuth } from "./auth";
import { useChat, useChatState } from "./chat";
import { errorText } from "./errors";
import { WorkspaceDialog } from "./dialogs";
import { ConnectionBanner } from "./connection-banner";
import { GroupExitModal, GroupMembersDialog } from "./group-members";
import { enrichParticipants } from "./participants";
import type { Participant } from "./participants";
import { KeyboardShortcutsModal, QuickSwitcher, SHORTCUTS_ENTRY } from "./quick-switcher";
import { chatStore, compareTimestampsDesc } from "./store";
import { presenceStore } from "./engagement-store";
import { setActiveNotificationContext } from "./notifications";
import type { DialogKind } from "./dialogs";
import { ConversationView } from "./messages";
import { AttentionInbox, useAttentionCount } from "./attention";
import { Button, Loading, Logo, Notice } from "./ui";
import { toast } from "./toast";
import { conversationUnreadSummary } from "./unread-badge";
import { canConfigureModelGateway } from "./agent-management";
import { useCurrentWorkspace } from "./shell/current-workspace";
import { NotFoundView } from "./shell/page-header";
import { PageTitleProvider } from "./shell/page-title";
import { useShellRoutes } from "./shell/shell-routes";
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
import {
  clearPersistedAppPaneWidth,
  ConversationAppsPane,
} from "./marketplace/conversation-apps";
import { WorkspaceMarketplaceModal } from "./marketplace/workspace-modal";
import { AnnouncementModals, usePlatformAnnouncements } from "./announcements";
import { parseInviteCode, Welcome } from "./shell-welcome";
import {
  clearPersistedShellNav,
  UnavailableConversationView,
  useDocumentTitle,
  useMediaQuery,
  useShellKeyboardShortcuts,
  useShellPersistedNav,
  useSyncShellPersistedNav,
} from "./shell/shell-hooks";
import { MAX_WIDTH_QUERY } from "./breakpoints";
export { parseInviteCode, clearPersistedShellNav };

export function Shell() {
  const { user, logout } = useAuth();
  const mobile = useMediaQuery(MAX_WIDTH_QUERY.md);
  const overlayMembers = useMediaQuery(MAX_WIDTH_QUERY.lg);
  const navigationRef = useRef<HTMLDivElement>(null);
  const membersRef = useRef<HTMLElement>(null);
  const chat = useChat(),
    snapshot = useChatState();
  const client = useQueryClient();
  const attentionCount = useAttentionCount();
  const routes = useShellRoutes();
  const { match, legacyDirectoryMatch, workspaceRootMatch, location, pathname } = routes;
  const navigate = useNavigate();
  const nav = useShellPersistedNav(user?.id, clearPersistedAppPaneWidth);
  const { lastChatPath, rememberedByWorkspace, recentConversationIds } = nav;
  const [quickSwitcherOpen, setQuickSwitcherOpen] = useState(false);
  const [shortcutsOpen, setShortcutsOpen] = useState(false);
  const [announcementsHistoryOpen, setAnnouncementsHistoryOpen] = useState(false);
  const announcements = usePlatformAnnouncements(user?.id);
  const [dialog, setDialog] = useState<DialogKind | null>(null),
    [sidebar, setSidebar] = useState(false),
    [details, setDetails] = useState(false),
    [appsOpen, setAppsOpen] = useState(false),
    [managingWorkspaceApps, setManagingWorkspaceApps] = useState(false),
    [managingGroup, setManagingGroup] = useState(false),
    [groupExit, setGroupExit] = useState<"leave" | "dissolve" | null>(null),
    [removingFromGroup, setRemovingFromGroup] = useState(false);
  const [removing, setRemoving] = useState<Member | null>(null),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  // Direct-message failures render inside the members pane: when it is an
  // overlay, <main> is inert and a shell-level notice would be invisible.
  const [directError, setDirectError] = useState(""),
    [directBusyId, setDirectBusyId] = useState<string | null>(null);
  const [titleDetail, setTitleDetailState] = useState({ path: "", detail: "" });
  const setTitleDetail = useCallback((path: string, detail: string) => {
    setTitleDetailState((prev) =>
      detail ? { path, detail } : prev.path === path ? { path: "", detail: "" } : prev,
    );
  }, []);
  const workspace = useCurrentWorkspace({
    workspaces: chat.data?.workspaces || [],
    lastChatPath,
    lastConversationByWorkspace: rememberedByWorkspace,
    conversations: snapshot.conversations,
  });
  const owner = workspace?.createdBy === user!.id;
  const conversation = match?.params.conversationId
    ? snapshot.conversations[match.params.conversationId]
    : undefined;
  useEffect(() => {
    setQuickSwitcherOpen(false);
    setShortcutsOpen(false);
    setAnnouncementsHistoryOpen(false);
  }, [user?.id]);
  useSyncShellPersistedNav(nav, {
    userId: user?.id,
    workspace,
    conversation,
    conversations: snapshot.conversations,
  });
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
    setGroupExit(null);
    setAppsOpen(false);
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
    setAppsOpen(false);
    setDirectError("");
  }, []);
  const baseOverlayModal =
    !!dialog || quickSwitcherOpen || shortcutsOpen || managingGroup || !!groupExit || managingWorkspaceApps || !!removing;
  const caps = chat.data?.capabilities || {};
  const marketplaceEnabled = caps["im.marketplace.enabled"] !== false;
  const announcementsPublishEnabled = caps["im.announcements.publish"] !== false;
  const { isAnnouncementsAdminPage, isAttentionPage, isAgentsPage, isManagement } = routes;
  const announcementPopupVisible =
    !announcementsHistoryOpen &&
    !baseOverlayModal &&
    !isAnnouncementsAdminPage &&
    announcements.hasUnread;
  const hasOverlayModal =
    baseOverlayModal || announcementsHistoryOpen || announcementPopupVisible;
  const { allAuthorizedConversations, urgentCount, hasChannelUnread } =
    computeShellAttention({
      workspaces: chat.data?.workspaces || [],
      snapshot,
      userId: user!.id,
      attentionData: attentionCount.data,
    });
  const pageTitle = computePageTitle({
    routes, workspace, conversation, userId: user!.id, members,
    titleDetail: titleDetail.path === location.pathname ? titleDetail.detail : "",
  });
  useDocumentTitle(pageTitle, urgentCount, hasChannelUnread);
  useShellKeyboardShortcuts({
    channels, direct, workspace, conversationsById: snapshot.conversations,
    workspaces: chat.data?.workspaces, conversationId: conversation?.id,
    hasOverlayModal, quickSwitcherOpen, shortcutsOpen,
    setShortcutsOpen, setQuickSwitcherOpen, navigate,
    isUnread: (c) =>
      conversationUnreadSummary(c, snapshot, user!.id, attentionCount.data?.attentionUnreadByConversation).hasUnread,
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
  const appsAvailable = !isManagement && !routes.isNotFound && !!workspace && conversation?.type === "group";
  const appsVisible = appsOpen && !details && appsAvailable;
  const membersVisible = details && !isManagement && !routes.isNotFound && !!workspace && !!conversation;
  const rightPaneVisible = appsVisible || membersVisible;
  useEffect(() => {
    if (user) {
      client.setQueryData(["capability", "im.marketplace.enabled", user.id], marketplaceEnabled);
    }
    if (!appsAvailable) {
      setAppsOpen(false);
      setManagingWorkspaceApps(false);
    } else if (!marketplaceEnabled) {
      setManagingWorkspaceApps(false);
    }
  }, [appsAvailable, client, marketplaceEnabled, user]);
  useShellViewportAndFocus({
    navigationRef, membersRef, mobile, overlayMembers, sidebar, details: rightPaneVisible,
    hasOverlayModal, hasConversation: !!conversation, pathname: location.pathname,
    closeSidebar, closeDetails,
  });
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
  async function completeGroupExit(kind: "leave" | "dissolve", removedId: string) {
    const fallbackConv = channels.find((c) => c.id !== removedId) || conversations.find((c) => c.id !== removedId);
    const fallbackTarget = workspace ? (fallbackConv ? route(fallbackConv) : `/app/w/${workspace.id}/members`) : "/app";
    nav.pruneConversation(removedId, fallbackConv ? route(fallbackConv) : "");
    setGroupExit(null);
    setManagingGroup(false);
    setDetails(false);
    setAppsOpen(false);
    chatStore.removeConversation(removedId);
    navigate(fallbackTarget, { replace: true });
    toast.success(kind === "dissolve" ? "已解散小组" : "已退出小组");
    await Promise.all([
      chat.refresh(),
      client.invalidateQueries({ queryKey: ["attention"] }),
      client.invalidateQueries({ queryKey: ["members"] }),
    ]);
  }
  async function signOut() {
    const sessionToken = api.csrfToken;
    const currentUserId = user?.id;
    setBusy(true);
    setError("");
    try {
      await logout();
      if (!api.csrfToken) {
        nav.clearForUser(currentUserId);
        clearPersistedAppPaneWidth(currentUserId);
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
      rememberedId: nav.lastConversationByWorkspace.current[space.id],
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
        <Button variant="secondary" onClick={signOut} busy={busy}>
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
  return (
    <PageTitleProvider value={setTitleDetail}>
    <div className={`app-shell ${sidebar ? "sidebar-open" : ""} ${rightPaneVisible ? "details-open" : ""}`}>
      <a
        href="#main-content"
        className="skip-link"
        onClick={(event) => {
          event.preventDefault();
          document.getElementById("main-content")?.focus({ preventScroll: true });
        }}
      >
        跳到主内容
      </a>
      <ShellSidebar
        navigationRef={navigationRef} mobile={mobile} sidebar={sidebar}
        hasOverlayModal={hasOverlayModal} user={user!} busy={busy}
        connection={chat.connection} connectionLabel={connectionLabel}
        isAttentionPage={isAttentionPage} isAgentsPage={isAgentsPage}
        isAccountPage={routes.isAccountPage} isAppsPage={routes.isAppsPage}
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
        onOpenQuickSwitcher={() => setQuickSwitcherOpen(true)}
        onOpenAnnouncements={() => setAnnouncementsHistoryOpen(true)}
        hasUnreadAnnouncement={announcements.hasUnread}
      />
      {mobile && (
        <button
          type="button"
          className="sidebar-shade"
          onClick={() => setSidebar(false)}
          aria-label="关闭侧栏"
          inert={!sidebar}
          tabIndex={-1}
        />
      )}
      <main
        id="main-content"
        className="app-main"
        tabIndex={-1}
        inert={(mobile && sidebar) || (overlayMembers && rightPaneVisible)}
      >
        <ConnectionBanner
          connection={chat.connection} syncError={chat.syncError}
          showSyncError={!conversation || isManagement || isAttentionPage || routes.isNotFound}
          onRetry={chat.reconnect ?? chat.sync}
        />
        {error && !removing && <Notice>{error}</Notice>}
        {routes.isNotFound ? (
          <NotFoundView onOpenNavigation={() => setSidebar(true)} />
        ) : isAttentionPage ? (
          <AttentionInbox key={user!.id} onMenu={() => setSidebar(true)} />
        ) : isManagement ? (
          <ShellManagementView
            user={user!} routes={routes}
            announcementsPublishEnabled={announcementsPublishEnabled}
            marketplaceEnabled={marketplaceEnabled}
            workspaces={chat.data?.workspaces || []}
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
              setAppsOpen(false);
              setDetails(!details);
            }}
            membersOpen={details}
                appsOpen={appsVisible}
            onApps={
              conversation.type === "group"
                ? () => {
                    setSidebar(false);
                    setDetails(false);
                    setAppsOpen(!appsOpen);
                  }
                : undefined
            }
            onMenu={() => setSidebar(true)}
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
      {membersVisible && workspace && conversation && (
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
          onLeaveGroup={() => setGroupExit("leave")}
          onDissolveGroup={() => setGroupExit("dissolve")}
          onInvite={() => setDialog("invite")}
        />
      )}
      {appsVisible && workspace && conversation && (
        <ConversationAppsPane
          paneRef={membersRef}
          overlayMembers={overlayMembers}
          mobile={mobile}
          sidebar={sidebar}
          workspace={workspace}
          conversation={conversation}
          user={user!}
          owner={owner}
          onClose={() => setAppsOpen(false)}
          onOpenWorkspaceApps={() => setManagingWorkspaceApps(true)}
        />
      )}
      {managingWorkspaceApps && marketplaceEnabled && appsAvailable && workspace && (
        <WorkspaceMarketplaceModal
          workspace={workspace}
          isOwner={owner}
          onClose={() => setManagingWorkspaceApps(false)}
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
          isPlatformAdmin={canConfigureModelGateway(user)}
          attentionProjection={attentionCount.data?.attentionUnreadByConversation}
          onSelect={(path) => {
            setQuickSwitcherOpen(false);
            setSidebar(false);
            if (path === SHORTCUTS_ENTRY) {
              setShortcutsOpen(true);
              return;
            }
            const wsPrefix = "/app/w/";
            if (path.startsWith(wsPrefix) && !path.slice(wsPrefix.length).includes("/")) {
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
          conversation={conversation}
          members={members}
          onClose={closeGroupManager}
          onDissolved={(id) => completeGroupExit("dissolve", id)}
        />
      )}
      {groupExit && conversation?.type === "group" && (
        <GroupExitModal
          kind={groupExit}
          conversation={conversation}
          userId={user!.id}
          isWorkspaceOwner={owner}
          onClose={() => setGroupExit(null)}
          onCompleted={completeGroupExit}
        />
      )}
      {removing && (
        <RemoveMemberModal
          removing={removing} removingFromGroup={removingFromGroup}
          error={error} busy={busy} onClose={closeRemoving}
          onConfirm={() => void remove()}
        />
      )}
      <AnnouncementModals
        user={user!}
        latest={announcements.latest}
        latestSeq={announcements.latestSeq}
        readSeq={announcements.readSeq}
        hasUnread={announcements.hasUnread}
        unreadCount={announcements.unreadCount}
        suppressPopup={baseOverlayModal || isAnnouncementsAdminPage}
        historyOpen={announcementsHistoryOpen}
        onOpenHistory={() => setAnnouncementsHistoryOpen(true)}
        onCloseHistory={() => setAnnouncementsHistoryOpen(false)}
        onMarkRead={announcements.markRead}
        onDismissOptimistic={announcements.applyLocalReadSeq}
      />
    </div>
    </PageTitleProvider>
  );
}

