import { useLocation, useMatch } from "react-router-dom";

// Exact `/app*` paths the shell renders. Anything else under `/app` (that is
// not one of the parameterized routes below) renders the in-shell 404 page.
const STATIC_APP_PATHS = new Set([
  "/app",
  "/app/agents",
  "/app/developers",
  "/app/developers/docs",
  "/app/settings",
  "/app/profile",
  "/app/admin",
  "/app/admin/gateway",
  "/app/admin/users",
  "/app/admin/marketplace",
  "/app/admin/announcements",
  "/app/attention",
]);

/** Route flags for the authenticated shell, derived from the current URL. */
export function useShellRoutes() {
  const location = useLocation();
  const match = useMatch("/app/w/:workspaceId/c/:conversationId");
  const directoryMatch = useMatch("/app/w/:workspaceId/members");
  const appsMatch = useMatch("/app/w/:workspaceId/apps");
  const legacyDirectoryMatch = useMatch("/app/w/:workspaceId/agents");
  const agentMatch = useMatch("/app/agents/:agentId");
  const developerAppMatch = useMatch("/app/developers/apps/:appId");
  const workspaceRootMatch = useMatch("/app/w/:workspaceId");
  const pathname = location.pathname.replace(/\/+$/, "") || "/app";
  const routeWorkspaceId =
    match?.params.workspaceId ||
    directoryMatch?.params.workspaceId ||
    appsMatch?.params.workspaceId ||
    legacyDirectoryMatch?.params.workspaceId;
  const isSettings = pathname === "/app/settings" || pathname === "/app/profile";
  const isAdminPage = pathname === "/app/admin";
  const isGatewayPage = pathname === "/app/admin/gateway";
  const isUsersPage = pathname === "/app/admin/users";
  const isMarketplacePage = pathname === "/app/admin/marketplace";
  const isAnnouncementsAdminPage = pathname === "/app/admin/announcements";
  const isAdminArea =
    isAdminPage || isGatewayPage || isUsersPage || isMarketplacePage || isAnnouncementsAdminPage;
  const isDeveloperDocsPage = pathname === "/app/developers/docs";
  const isDevelopersPage =
    pathname === "/app/developers" || isDeveloperDocsPage || !!developerAppMatch;
  const isAgentsPage = pathname === "/app/agents" || !!agentMatch;
  const isDirectoryPage = !!directoryMatch;
  const isAppsPage = !!appsMatch;
  const isAttentionPage = pathname === "/app/attention";
  const isKnownRoute =
    !!(match || directoryMatch || appsMatch || legacyDirectoryMatch || agentMatch ||
      developerAppMatch || workspaceRootMatch) || STATIC_APP_PATHS.has(pathname);
  return {
    location,
    pathname,
    match,
    legacyDirectoryMatch,
    workspaceRootMatch,
    routeWorkspaceId,
    agentId: agentMatch?.params.agentId,
    developerAppId: developerAppMatch?.params.appId,
    isSettings,
    isAdminPage,
    isGatewayPage,
    isUsersPage,
    isMarketplacePage,
    isAnnouncementsAdminPage,
    isAdminArea,
    isDeveloperDocsPage,
    isDevelopersPage,
    isAgentsPage,
    isDirectoryPage,
    isAppsPage,
    isAttentionPage,
    /** Pages reached from the account (avatar) menu. */
    isAccountPage: isSettings || isDevelopersPage || isAdminArea,
    isManagement:
      isSettings || isAdminArea || isDevelopersPage || isAgentsPage || isDirectoryPage || isAppsPage,
    isNotFound: !isKnownRoute,
  };
}

export type ShellRoutes = ReturnType<typeof useShellRoutes>;
