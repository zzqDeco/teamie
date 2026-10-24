import { useEffect, useRef } from "react";
import type { RefObject } from "react";
import { Link } from "react-router-dom";
import {
  ArrowLeft,
  Bell,
  Bot,
  ChevronDown,
  Hash,
  KeyRound,
  Menu,
  Plus,
  Settings,
  ShieldCheck,
  Users,
  X,
} from "lucide-react";
import type {
  Agent,
  AttentionItem,
  Conversation,
  Member,
  User,
  Workspace,
} from "@teamie/api-client";
import { useAuth } from "./auth";
import type { Connection } from "./chat";
import { AccountMenu } from "./account-menu";
import { ConversationActions } from "./conversation-actions";
import { AgentAvatar, AgentBadge } from "./agent-ui";
import { ProfileSettings } from "./dialogs";
import type { DialogKind } from "./dialogs";
import { PlatformInvitations } from "./admin";
import { PlatformUsers } from "./platform-users";
import { ModelGatewaySettings } from "./model-gateway";
import { AgentDetail, MyAgents } from "./agents";
import { WorkspaceMembers } from "./members";
import type { Participant } from "./participants";
import { conversationName } from "./messages";
import { currentAttention } from "./attention";
import type { ChatSnapshot } from "./store";
import { Avatar, IconButton, Logo, useOverlayFocus } from "./ui";
import { ICON } from "./icons";

export function ShellManagementView({
  user,
  isGatewayPage,
  isUsersPage,
  isAdminPage,
  isAgentsPage,
  isDirectoryPage,
  agentId,
  backChatRoute,
  workspace,
  members,
  workspaceAgents,
  membersLoading,
  membersLoadError,
  onOpenSidebar,
  onRetryMembers,
}: {
  user: User;
  isGatewayPage: boolean;
  isUsersPage: boolean;
  isAdminPage: boolean;
  isAgentsPage: boolean;
  isDirectoryPage: boolean;
  agentId?: string;
  backChatRoute: string;
  workspace?: Workspace;
  members: Participant[];
  workspaceAgents: Agent[];
  membersLoading: boolean;
  membersLoadError: boolean;
  onOpenSidebar: () => void;
  onRetryMembers: () => void;
}) {
  return (
    <>
      <header className="settings-header">
        <IconButton
          label="打开频道列表"
          className="mobile-menu"
          onClick={onOpenSidebar}
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
        user.platformRole === "admin" && (
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
        <ModelGatewaySettings key={user.id} />
      ) : isAdminPage ? (
        <PlatformInvitations key={user.id} />
      ) : isAgentsPage ? (
        agentId ? (
          <AgentDetail key={`${user.id}:${agentId}`} agentId={agentId} />
        ) : (
          <MyAgents key={user.id} />
        )
      ) : isDirectoryPage ? (
        workspace ? (
          <WorkspaceMembers
            key={`${user.id}:${workspace.id}`}
            workspace={workspace}
            members={members}
            agents={workspaceAgents}
            loading={membersLoading}
            loadError={membersLoadError}
            onRetry={onRetryMembers}
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
  );
}

export function computeShellAttention({
  workspaces,
  snapshot,
  userId,
  attentionData,
}: {
  workspaces: Workspace[];
  snapshot: ChatSnapshot;
  userId: string;
  attentionData?: {
    unreadCount?: number;
    channelUnreadCount?: number;
    items?: AttentionItem[];
  };
}) {
  const activeWorkspaceIds = new Set(workspaces.map((w) => w.id));
  const allAuthorizedConversations = Object.values(
    snapshot.conversations,
  ).filter((c) => activeWorkspaceIds.has(c.workspaceId));
  const directUnreadCount = allAuthorizedConversations.reduce(
    (sum, c) =>
      c.type !== "channel" ? sum + Math.max(0, c.lastSeq - c.readSeq) : sum,
    0,
  );
  const rawChannelAttention = attentionData?.channelUnreadCount || 0;
  let locallyResolvedChannel = 0;
  for (const item of attentionData?.items || []) {
    if (item.isRead || item.conversation.type !== "channel") continue;
    const current = currentAttention(item, snapshot, userId);
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
  return {
    allAuthorizedConversations,
    urgentCount,
    hasChannelUnread,
  };
}

export function resolveWorkspaceTarget({
  space,
  rememberedId,
  conversations,
  isManagement,
  currentWorkspaceId,
}: {
  space: Workspace;
  rememberedId?: string;
  conversations: Record<string, Conversation>;
  isManagement: boolean;
  currentWorkspaceId?: string;
}) {
  const remembered = rememberedId ? conversations[rememberedId] : undefined;
  const first =
    (remembered && remembered.workspaceId === space.id
      ? remembered
      : undefined) ||
    Object.values(conversations).find(
      (c) => c.workspaceId === space.id && c.type === "channel",
    ) ||
    Object.values(conversations).find((c) => c.workspaceId === space.id);
  if (first) return route(first);
  if (isManagement || currentWorkspaceId !== space.id)
    return `/app/w/${space.id}/members`;
  return null;
}

export function computePageTitle({
  isAttentionPage,
  isGatewayPage,
  isUsersPage,
  isAdminPage,
  isAgentsPage,
  isDirectoryPage,
  isSettings,
  workspace,
  conversation,
  userId,
  members,
}: {
  isAttentionPage: boolean;
  isGatewayPage: boolean;
  isUsersPage: boolean;
  isAdminPage: boolean;
  isAgentsPage: boolean;
  isDirectoryPage: boolean;
  isSettings: boolean;
  workspace?: Workspace;
  conversation?: Conversation;
  userId: string;
  members: Member[];
}) {
  return isAttentionPage
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
                    ? `# ${conversationName(conversation, userId, members)}`
                    : conversationName(conversation, userId, members)
                  : workspace?.name || "";
}

export function useShellViewportAndFocus({
  navigationRef,
  membersRef,
  mobile,
  overlayMembers,
  sidebar,
  details,
  hasOverlayModal,
  hasConversation,
  pathname,
  closeSidebar,
  closeDetails,
}: {
  navigationRef: RefObject<HTMLDivElement | null>;
  membersRef: RefObject<HTMLElement | null>;
  mobile: boolean;
  overlayMembers: boolean;
  sidebar: boolean;
  details: boolean;
  hasOverlayModal: boolean;
  hasConversation: boolean;
  pathname: string;
  closeSidebar: () => void;
  closeDetails: () => void;
}) {
  const sidebarFocus = useRef({ open: false, pathname });
  useOverlayFocus(
    navigationRef,
    mobile && sidebar && !hasOverlayModal,
    closeSidebar,
  );
  useEffect(() => {
    const previous = sidebarFocus.current;
    sidebarFocus.current = { open: sidebar, pathname };
    if (
      sidebar ||
      !mobile ||
      hasOverlayModal ||
      (!previous.open && previous.pathname === pathname)
    )
      return;
    const focused = document.activeElement;
    if (focused === document.body || navigationRef.current?.contains(focused)) {
      navigationRef.current?.parentElement
        ?.querySelector<HTMLElement>(".app-main .mobile-menu")
        ?.focus({ preventScroll: true });
    }
  }, [sidebar, mobile, hasOverlayModal, pathname, navigationRef]);
  useOverlayFocus(
    membersRef,
    overlayMembers &&
      details &&
      !sidebar &&
      !hasOverlayModal &&
      hasConversation,
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
}

export function route(conversation: Conversation) {
  return `/app/w/${conversation.workspaceId}/c/${conversation.id}`;
}

export function pickAdjacentConversation(
  list: Conversation[],
  currentId: string | undefined,
  direction: -1 | 1,
  unreadOnly: boolean,
) {
  if (!list.length) return undefined;
  const currentIndex = currentId
    ? list.findIndex((c) => c.id === currentId)
    : -1;
  for (let step = 1; step <= list.length; step++) {
    const index =
      currentIndex === -1
        ? direction === 1
          ? step - 1
          : list.length - step
        : (currentIndex + direction * step + list.length) % list.length;
    const candidate = list[index];
    if (
      candidate &&
      (!unreadOnly ||
        (candidate.lastSeq > candidate.readSeq && candidate.id !== currentId))
    ) {
      return candidate;
    }
  }
  return undefined;
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
        <Hash size={ICON.md} />
      ) : conversation.type === "group" ? (
        <span className="group-avatar">
          <Users size={ICON.sm} />
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

export function ShellSidebar({
  navigationRef,
  mobile,
  sidebar,
  hasOverlayModal,
  user,
  busy,
  connection,
  connectionLabel,
  isAttentionPage,
  isAgentsPage,
  attentionUnreadCount,
  workspaces,
  workspace,
  owner,
  caps,
  allAuthorizedConversations,
  channels,
  direct,
  conversationId,
  members,
  onCloseSidebar,
  onSelectWorkspace,
  onOpenDialog,
  onSignOut,
  onShortcuts,
}: {
  navigationRef: RefObject<HTMLDivElement | null>;
  mobile: boolean;
  sidebar: boolean;
  hasOverlayModal: boolean;
  user: User;
  busy: boolean;
  connection: Connection;
  connectionLabel: string;
  isAttentionPage: boolean;
  isAgentsPage: boolean;
  attentionUnreadCount?: number;
  workspaces: Workspace[];
  workspace?: Workspace;
  owner: boolean;
  caps: Record<string, boolean>;
  allAuthorizedConversations: Conversation[];
  channels: Conversation[];
  direct: Conversation[];
  conversationId?: string;
  members: Member[];
  onCloseSidebar: () => void;
  onSelectWorkspace: (space: Workspace) => void;
  onOpenDialog: (kind: DialogKind) => void;
  onSignOut: () => void;
  onShortcuts: () => void;
}) {
  return (
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
        <Link
          className="rail-home"
          to="/app"
          title="回到聊天"
          aria-label="Teamie 工作空间"
          onClick={onCloseSidebar}
        >
          <Logo compact />
        </Link>
        <div className="rail-divider" />
        <Link
          to="/app/attention"
          className={`rail-attention ${isAttentionPage ? "selected" : ""}`}
          title="提及与回复"
          aria-label="提及与回复"
          aria-current={isAttentionPage ? "page" : undefined}
          onClick={onCloseSidebar}
        >
          <Bell size={ICON.lg} />
          {!!attentionUnreadCount && (
            <span
              className="rail-attention-count"
              aria-label={`${attentionUnreadCount} 条未读提醒`}
            >
              {attentionUnreadCount > 99 ? "99+" : attentionUnreadCount}
            </span>
          )}
        </Link>
        <div className="workspace-buttons">
          {workspaces.map((space) => {
            const hasUnread = allAuthorizedConversations.some(
              (c) => c.workspaceId === space.id && c.lastSeq > c.readSeq,
            );
            return (
              <button
                key={space.id}
                className={`workspace-button ${space.id === workspace?.id ? "selected" : ""}`}
                title={hasUnread ? `${space.name}（有未读消息）` : space.name}
                aria-label={
                  hasUnread
                    ? `打开工作空间 ${space.name}（有未读消息）`
                    : `打开工作空间 ${space.name}`
                }
                onClick={() => onSelectWorkspace(space)}
              >
                <span>{Array.from(space.name).slice(0, 2).join("")}</span>
                {space.id === workspace?.id && <i />}
                {hasUnread && (
                  <em className="workspace-unread-dot" aria-hidden="true" />
                )}
              </button>
            );
          })}
        </div>
        <IconButton
          label="创建工作空间"
          className="rail-add"
          onClick={() => onOpenDialog("workspace")}
        >
          <Plus size={ICON.xl} />
        </IconButton>
        <span className="rail-spacer" />
        <Link
          to="/app/agents"
          className={`rail-settings rail-agents ${isAgentsPage ? "selected" : ""}`}
          title="个人 Agent"
          aria-label="个人 Agent"
          aria-current={isAgentsPage ? "page" : undefined}
          onClick={onCloseSidebar}
        >
          <Bot size={ICON.lg} aria-hidden="true" />
        </Link>
        <AccountMenu
          user={user}
          busy={busy}
          active={(!mobile || sidebar) && !hasOverlayModal}
          connection={connection}
          connectionLabel={connectionLabel}
          onSignOut={onSignOut}
          onNavigate={onCloseSidebar}
          onShortcuts={onShortcuts}
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
              active={(!mobile || sidebar) && !hasOverlayModal}
              canDirect={!!caps["im.dm.create"]}
              canGroup={!!caps["im.group.create"]}
              onSelect={onOpenDialog}
            />
          )}
          <IconButton
            className="mobile-close"
            label="关闭频道列表"
            onClick={onCloseSidebar}
          >
            <X size={ICON.md} />
          </IconButton>
        </div>
        <div className="sidebar-scroll">
          <div className="nav-section">
            <div className="nav-section-title">
              <span>
                <ChevronDown size={ICON.xs} /> 团队频道
              </span>
              {workspace && owner && caps["im.channel.create"] && (
                <IconButton
                  label="创建频道"
                  onClick={() => onOpenDialog("channel")}
                >
                  <Plus size={ICON.sm} />
                </IconButton>
              )}
            </div>
            {channels.map((c) => (
              <ConversationItem
                key={c.id}
                conversation={c}
                selected={c.id === conversationId}
                members={members}
                onClick={onCloseSidebar}
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
                <ChevronDown size={ICON.xs} /> 私信与小组
              </span>
            </div>
            {direct.map((c) => (
              <ConversationItem
                key={c.id}
                conversation={c}
                selected={c.id === conversationId}
                members={members}
                onClick={onCloseSidebar}
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
  );
}
