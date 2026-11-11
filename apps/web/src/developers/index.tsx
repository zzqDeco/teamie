import { Suspense } from "react";
import { Link } from "react-router-dom";
import { BookOpen, Code2, ShieldCheck } from "lucide-react";
import type { Workspace } from "@teamie/api-client";
import { canConfigureModelGateway } from "../agent-management";
import { useAuth } from "../auth";
import { ICON } from "../icons";
import { lazyView } from "../lazy-view";
import { Loading } from "../ui";

const DeveloperAppsList = lazyView(() =>
  import("./developer-apps").then((m) => ({ default: m.DeveloperAppsList })),
);
const DeveloperAppDetail = lazyView(() =>
  import("./developer-app-detail").then((m) => ({
    default: m.DeveloperAppDetail,
  })),
);
const DeveloperHandbook = lazyView(() =>
  import("./developer-handbook").then((m) => ({
    default: m.DeveloperHandbook,
  })),
);

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
        <Link
          to="/app/developers"
          className={isAppsSection ? "active" : ""}
          aria-current={isAppsSection ? "page" : undefined}
        >
          <Code2 size={ICON.xs} aria-hidden="true" />
          我的应用
        </Link>
        <Link
          to="/app/developers/docs"
          className={isDocsPage ? "active" : ""}
          aria-current={isDocsPage ? "page" : undefined}
        >
          <BookOpen size={ICON.xs} aria-hidden="true" />
          开发者手册
        </Link>
        {canConfigureModelGateway(user) && (
          <Link to="/app/admin/marketplace">
            <ShieldCheck size={ICON.xs} aria-hidden="true" />
            平台审核队列
          </Link>
        )}
      </nav>
      <Suspense fallback={<Loading />}>
        {isDocsPage ? (
          <DeveloperHandbook />
        ) : appId ? (
          <DeveloperAppDetail appId={appId} workspaces={workspaces} />
        ) : (
          <DeveloperAppsList />
        )}
      </Suspense>
    </>
  );
}
