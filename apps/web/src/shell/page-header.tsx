import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import type { LucideIcon } from "lucide-react";
import {
  ArrowLeft,
  Blocks,
  Bot,
  Code2,
  Compass,
  KeyRound,
  Megaphone,
  Menu,
  Settings,
  ShieldCheck,
  Users,
} from "lucide-react";
import type { Workspace } from "@teamie/api-client";
import { IconButton } from "../ui";
import { ICON } from "../icons";
import type { ShellRoutes } from "./shell-routes";

/** The one mobile control that opens the shell navigation drawer. */
export function MobileNavButton({ onClick }: { onClick: () => void }) {
  return (
    <IconButton label="打开导航" className="mobile-menu" onClick={onClick}>
      <Menu size={ICON.lg} aria-hidden="true" />
    </IconButton>
  );
}

export type PageTab = {
  to: string;
  label: string;
  icon?: LucideIcon;
  current: boolean;
};

/**
 * Shared header for management pages: navigation drawer button (mobile),
 * icon, the page's single `<h1>`, optional actions, a back link and
 * sub-navigation tabs marked with `aria-current`.
 */
export function PageHeader({
  icon: Icon,
  title,
  back,
  actions,
  tabs,
  tabsLabel,
  onOpenNavigation,
}: {
  icon: LucideIcon;
  title: string;
  back?: { to: string; label: string };
  actions?: ReactNode;
  tabs?: PageTab[];
  tabsLabel?: string;
  onOpenNavigation: () => void;
}) {
  return (
    <>
      <header className="settings-header page-header">
        <MobileNavButton onClick={onOpenNavigation} />
        <Icon size={ICON.md} aria-hidden="true" />
        <h1 className="page-header-title" tabIndex={-1}>{title}</h1>
        {actions && <div className="page-header-actions">{actions}</div>}
        {back && (
          <Link to={back.to} className="page-header-back">
            <ArrowLeft size={ICON.sm} aria-hidden="true" />
            <span>{back.label}</span>
          </Link>
        )}
      </header>
      {!!tabs?.length && (
        <nav className="admin-section-nav page-tabs" aria-label={tabsLabel || title}>
          {tabs.map(({ to, label, icon: TabIcon, current }) => (
            <Link
              key={to}
              to={to}
              className={current ? "active" : ""}
              aria-current={current ? "page" : undefined}
            >
              {TabIcon && <TabIcon size={ICON.xs} aria-hidden="true" />}
              {label}
            </Link>
          ))}
        </nav>
      )}
    </>
  );
}

/** Title, icon and sub-navigation of the management page at the current route. */
export function managementPageMeta(
  routes: ShellRoutes,
  { workspace, isPlatformAdmin }: { workspace?: Workspace; isPlatformAdmin: boolean },
): { icon: LucideIcon; title: string; tabs?: PageTab[]; tabsLabel?: string } {
  if (routes.isAdminArea) {
    const entries: Array<[string, string, LucideIcon, boolean]> = [
      ["/app/admin", "注册邀请", ShieldCheck, routes.isAdminPage],
      ["/app/admin/users", "用户管理", Users, routes.isUsersPage],
      ["/app/admin/gateway", "模型网关", KeyRound, routes.isGatewayPage],
      ["/app/admin/marketplace", "应用市场", Blocks, routes.isMarketplacePage],
      ["/app/admin/announcements", "平台公告", Megaphone, routes.isAnnouncementsAdminPage],
    ];
    const active = entries.find((entry) => entry[3]) || entries[0];
    return {
      icon: active[2],
      title: active[1],
      tabsLabel: "平台管理",
      tabs: isPlatformAdmin
        ? entries.map(([to, label, icon, current]) => ({ to, label, icon, current }))
        : undefined,
    };
  }
  // The developer portal renders its own sub-navigation below this header.
  if (routes.isDevelopersPage) return { icon: Code2, title: "开发者平台" };
  if (routes.isAgentsPage) return { icon: Bot, title: "个人 Agent" };
  if (routes.isDirectoryPage || routes.isAppsPage) {
    const base = workspace ? `/app/w/${workspace.id}` : "";
    return {
      icon: routes.isAppsPage ? Blocks : Users,
      title: routes.isAppsPage ? "应用" : "成员",
      tabsLabel: workspace ? `${workspace.name} 工作空间` : undefined,
      tabs: workspace
        ? [
            { to: `${base}/members`, label: "成员", icon: Users, current: routes.isDirectoryPage },
            { to: `${base}/apps`, label: "应用", icon: Blocks, current: routes.isAppsPage },
          ]
        : undefined,
    };
  }
  return { icon: Settings, title: "账号设置" };
}

/** In-shell page shown for unknown `/app/*` routes and inaccessible workspaces. */
export function NotFoundView({
  onOpenNavigation,
  title = "页面不存在",
  description = "链接可能已失效，或你没有访问这个页面的权限。",
}: {
  onOpenNavigation: () => void;
  title?: string;
  description?: string;
}) {
  return (
    <div className="unavailable-view not-found-view">
      <MobileNavButton onClick={onOpenNavigation} />
      <span className="large-symbol" aria-hidden="true">
        <Compass size={ICON.xl} aria-hidden="true" />
      </span>
      <h1 tabIndex={-1}>{title}</h1>
      <p>{description}</p>
      <Link className="button" to="/app">
        返回对话
      </Link>
    </div>
  );
}
