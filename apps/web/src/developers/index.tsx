import { Link } from "react-router-dom";
import { BookOpen, Code2, ShieldCheck } from "lucide-react";
import type { Workspace } from "@teamie/api-client";
import { canConfigureModelGateway } from "../agent-management";
import { useAuth } from "../auth";
import { ICON } from "../icons";
import { DeveloperAppsList } from "./developer-apps";
import { DeveloperAppDetail } from "./developer-app-detail";
import { DeveloperHandbook } from "./developer-handbook";

export function DeveloperPortal({
  appId,
  isDocsPage = false,
  workspaces,
}: {
  appId?: string;
  isDocsPage?: boolean;
  workspaces?: Workspace[];
}) {
  const { user } = useAuth();
  const isAppsSection = !isDocsPage;
  return (
    <>
      <nav className="admin-section-nav" aria-label="开发者平台导航">
        <Link to="/app/developers" className={isAppsSection ? "active" : ""}>
          <Code2 size={ICON.xs} />
          我的应用
        </Link>
        <Link to="/app/developers/docs" className={isDocsPage ? "active" : ""}>
          <BookOpen size={ICON.xs} />
          开发者手册
        </Link>
        {canConfigureModelGateway(user) && (
          <Link to="/app/admin/marketplace">
            <ShieldCheck size={ICON.xs} />
            平台审核队列
          </Link>
        )}
      </nav>
      {isDocsPage ? (
        <DeveloperHandbook />
      ) : appId ? (
        <DeveloperAppDetail appId={appId} workspaces={workspaces} />
      ) : (
        <DeveloperAppsList />
      )}
    </>
  );
}
