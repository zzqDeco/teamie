import { useEffect, useRef } from "react";
import type { RefObject } from "react";
import type {
  Agent,
  AttentionUnreadDTO,
  Conversation,
  Member,
  User,
  Workspace,
} from "@teamie/api-client";
import { canConfigureModelGateway } from "../agent-management";
import { ProfileSettings } from "../dialogs";
import { lazyView } from "../lazy-view";
import { RouteBoundary } from "../route-boundary";
// Static: the in-context apps modal already bundles this module with the shell.
import { WorkspaceAppsPage } from "../marketplace/workspace-apps";
import type { Participant } from "../participants";
import { conversationName } from "../messages";
import { summarizeConversationUnread } from "../store";
import type { ChatSnapshot } from "../store";
import { useOverlayFocus } from "../ui";
import { conversationPathWorkspaceId } from "./current-workspace";
import { managementPageMeta, NotFoundView, PageHeader } from "./page-header";
import type { ShellRoutes } from "./shell-routes";

const PlatformInvitations = lazyView(() =>
  import("../admin").then((m) => ({ default: m.PlatformInvitations })),
);
const PlatformUsers = lazyView(() =>
  import("../platform-users").then((m) => ({ default: m.PlatformUsers })),
);
const ModelGatewaySettings = lazyView(() =>
  import("../model-gateway").then((m) => ({ default: m.ModelGatewaySettings })),
);
const PlatformMarketplace = lazyView(() =>
  import("../marketplace/admin-marketplace").then((m) => ({
    default: m.PlatformMarketplace,
  })),
);
const PlatformAnnouncementsAdmin = lazyView(() =>
  import("../announcements-admin").then((m) => ({
    default: m.PlatformAnnouncementsAdmin,
  })),
);
const DeveloperPortal = lazyView(() =>
  import("../developers").then((m) => ({ default: m.DeveloperPortal })),
);
const AgentDetail = lazyView(() =>
  import("../agents").then((m) => ({ default: m.AgentDetail })),
);
const MyAgents = lazyView(() =>
  import("../agents").then((m) => ({ default: m.MyAgents })),
);
const WorkspaceMembers = lazyView(() =>
  import("../members").then((m) => ({ default: m.WorkspaceMembers })),
);


export function ShellManagementView({
  user,
  routes,
  announcementsPublishEnabled = true,
  marketplaceEnabled = true,
  workspaces,
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
  routes: ShellRoutes;
  announcementsPublishEnabled?: boolean;
  marketplaceEnabled?: boolean;
  workspaces?: Workspace[];
  backChatRoute: string;
  workspace?: Workspace;
  members: Participant[];
  workspaceAgents: Agent[];
  membersLoading: boolean;
  membersLoadError: boolean;
  onOpenSidebar: () => void;
  onRetryMembers: () => void;
}) {
  const workspaceScoped = routes.isDirectoryPage || routes.isAppsPage;
  if (workspaceScoped && !workspace)
    return <NotFoundView onOpenNavigation={onOpenSidebar} />;
  const meta = managementPageMeta(routes, {
    workspace,
    isPlatformAdmin: canConfigureModelGateway(user),
  });
  return (
    <>
      <PageHeader
        icon={meta.icon}
        title={meta.title}
        tabs={meta.tabs}
        tabsLabel={meta.tabsLabel}
        back={{ to: backChatRoute, label: "对话" }}
        onOpenNavigation={onOpenSidebar}
      />
      <RouteBoundary>
      {routes.isUsersPage ? (
        <PlatformUsers />
      ) : routes.isMarketplacePage ? (
        <PlatformMarketplace />
      ) : routes.isAnnouncementsAdminPage ? (
        <PlatformAnnouncementsAdmin
          publishEnabled={announcementsPublishEnabled}
        />
      ) : routes.isDevelopersPage ? (
        <DeveloperPortal
          key={`${user.id}:${routes.developerAppId || ""}:${routes.isDeveloperDocsPage ? "docs" : "apps"}`}
          appId={routes.developerAppId}
          isDocsPage={routes.isDeveloperDocsPage}
          workspaces={workspaces}
        />
      ) : routes.isGatewayPage ? (
        <ModelGatewaySettings key={user.id} />
      ) : routes.isAdminPage ? (
        <PlatformInvitations key={user.id} />
      ) : routes.isAgentsPage ? (
        routes.agentId ? (
          <AgentDetail key={`${user.id}:${routes.agentId}`} agentId={routes.agentId} />
        ) : (
          <MyAgents
            key={user.id}
            lastWorkspaceId={routeWorkspaceId(backChatRoute)}
          />
        )
      ) : workspace && routes.isAppsPage ? (
        <WorkspaceAppsPage
          key={`${user.id}:${workspace.id}`}
          workspace={workspace}
          isOwner={workspace.createdBy === user.id}
          marketplaceEnabled={marketplaceEnabled}
        />
      ) : workspace && routes.isDirectoryPage ? (
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
        <ProfileSettings />
      )}
      </RouteBoundary>
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
  routes,
  titleDetail = "",
  workspace,
  conversation,
  userId,
  members,
}: {
  routes: ShellRoutes;
  /** Entity name of a detail page (Agent, developer app), if known. */
  titleDetail?: string;
  workspace?: Workspace;
  conversation?: Conversation;
  userId: string;
  members: Member[];
}) {
  if (routes.isNotFound) return "页面不存在";
  if (routes.isAttentionPage) return "提及与回复";
  if (routes.isManagement) {
    const { title } = managementPageMeta(routes, { workspace, isPlatformAdmin: false });
    if (routes.isDirectoryPage || routes.isAppsPage)
      return workspace ? `${workspace.name} · ${title}` : "页面不存在";
    if (routes.isDeveloperDocsPage) return `开发者手册 · ${title}`;
    return titleDetail ? `${titleDetail} · ${title}` : title;
  }
  if (conversation && workspace && conversation.workspaceId === workspace.id)
    return conversation.type === "channel"
      ? `# ${conversationName(conversation, userId, members)}`
      : conversationName(conversation, userId, members);
  return workspace?.name || "";
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

/** Workspace ID of a `route()` path, or "" for non-conversation paths. */
export function routeWorkspaceId(path: string) {
  return conversationPathWorkspaceId(path);
}

const hasSummaryUnread = (conversation: Conversation) =>
  summarizeConversationUnread(conversation, {}, "").hasUnread;

/**
 * Next/previous conversation in `list`. With `unreadOnly` it uses the same
 * unread judgement as the sidebar (`summary.hasUnread`, passed as `isUnread`).
 */
export function pickAdjacentConversation(
  list: Conversation[],
  currentId: string | undefined,
  direction: -1 | 1,
  unreadOnly: boolean,
  isUnread: (conversation: Conversation) => boolean = hasSummaryUnread,
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
      (!unreadOnly || (candidate.id !== currentId && isUnread(candidate)))
    ) {
      return candidate;
    }
  }
  return undefined;
}
