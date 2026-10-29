import { useEffect, useRef } from "react";
import type { RefObject } from "react";
import { Link } from "react-router-dom";
import {
  ArrowLeft,
  Blocks,
  Bot,
  KeyRound,
  Menu,
  Settings,
  ShieldCheck,
  Users,
} from "lucide-react";
import type {
  Agent,
  AttentionUnreadDTO,
  Conversation,
  Member,
  User,
  Workspace,
} from "@teamie/api-client";
import { ProfileSettings } from "../dialogs";
import { PlatformInvitations } from "../admin";
import { PlatformUsers } from "../platform-users";
import { ModelGatewaySettings } from "../model-gateway";
import { PlatformMarketplace } from "../marketplace";
import { AgentDetail, MyAgents } from "../agents";
import { WorkspaceMembers } from "../members";
import type { Participant } from "../participants";
import { conversationName } from "../messages";
import {
  effectiveNotificationLevel,
  summarizeConversationUnread,
} from "../store";
import type { ChatSnapshot } from "../store";
import { IconButton, useOverlayFocus } from "../ui";
import { ICON } from "../icons";

export function ShellManagementView({
  user,
  isGatewayPage,
  isUsersPage,
  isMarketplacePage = false,
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
  isMarketplacePage?: boolean;
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
        ) : isMarketplacePage ? (
          <Blocks size={ICON.md} />
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
            : isMarketplacePage
              ? "应用市场"
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
      {(isAdminPage || isGatewayPage || isUsersPage || isMarketplacePage) &&
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
            <Link
              to="/app/admin/marketplace"
              className={isMarketplacePage ? "active" : ""}
            >
              <Blocks size={ICON.xs} />
              应用市场
            </Link>
          </nav>
        )}
      {isUsersPage ? (
        <PlatformUsers />
      ) : isMarketplacePage ? (
        <PlatformMarketplace />
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
    items?: unknown[];
    attentionUnreadByConversation?: AttentionUnreadDTO[];
  };
}) {
  const activeWorkspaceIds = new Set(workspaces.map((w) => w.id));
  const allAuthorizedConversations = Object.values(
    snapshot.conversations,
  ).filter((c) => activeWorkspaceIds.has(c.workspaceId));
  const summaries = allAuthorizedConversations.map((c) => summarizeConversationUnread(c, snapshot, userId, attentionData?.attentionUnreadByConversation));
  const urgentCount = summaries.reduce((sum, summary) => sum + summary.contributesToUrgentCount, 0);
  const hasChannelUnread = summaries.some((summary) => summary.contributesToChannelUnreadDot);
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
  isMarketplacePage = false,
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
  isMarketplacePage?: boolean;
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
      : isMarketplacePage
        ? "应用市场"
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
    { fallbackFocus: () =>
      navigationRef.current?.parentElement?.querySelector<HTMLElement>(".app-main textarea:not(:disabled)") ||
      navigationRef.current?.parentElement?.querySelector<HTMLElement>(".app-main .mobile-menu") },
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
        (effectiveNotificationLevel(candidate) !== "none" &&
          candidate.lastSeq > candidate.readSeq &&
          candidate.id !== currentId))
    ) {
      return candidate;
    }
  }
  return undefined;
}
