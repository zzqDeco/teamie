import { useEffect, useMemo, useState } from "react";
import { Link, useInRouterContext } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Blocks, Code2 } from "lucide-react";
import { api } from "@teamie/api-client";
import type { DeveloperMarketplaceApp, MarketplaceApp, Workspace } from "@teamie/api-client";
import { errorText } from "../errors";
import { BadgePill, Button, ConfirmDialog, DataList, EmptyState, Notice } from "../ui";
import { toast } from "../toast";
import { ICON } from "../icons";

type AppRow = MarketplaceApp | DeveloperMarketplaceApp;

function appDisplay(app: AppRow, isDraftOnly: boolean) {
  const name = isDraftOnly && "draftName" in app && app.draftName ? app.draftName : app.name;
  const description =
    isDraftOnly && "draftDescription" in app
      ? app.draftDescription || app.description
      : app.description;
  const publisher =
    "publisherName" in app
      ? app.publisherName
      : "ownerDisplayName" in app
        ? app.ownerDisplayName
        : undefined;
  return { name, description, publisher };
}

function DeveloperPlatformLink() {
  const inRouter = useInRouterContext();
  const content = (
    <>
      <Code2 size={ICON.sm} aria-hidden="true" /> 开发者平台
    </>
  );
  return inRouter ? (
    <Link to="/app/developers" className="button secondary small-button">
      {content}
    </Link>
  ) : (
    <a href="/app/developers" className="button secondary small-button">
      {content}
    </a>
  );
}

/**
 * Workspace apps: every current member sees the installed list read-only; the
 * workspace owner additionally sees the catalog, testable drafts and
 * install/uninstall actions while `im.marketplace.enabled` is on. Turning the
 * capability off hides the write actions but keeps installed apps visible.
 */
export function WorkspaceAppsPanel({
  workspace,
  isOwner,
  marketplaceEnabled = true,
  onBusyChange,
}: {
  workspace: Workspace;
  isOwner: boolean;
  marketplaceEnabled?: boolean;
  onBusyChange?: (busy: boolean) => void;
}) {
  const queryClient = useQueryClient();
  const canManage = isOwner && marketplaceEnabled;
  const [busyAppId, setBusyAppId] = useState("");
  const [confirmingUninstall, setConfirmingUninstall] = useState<{
    id: string;
    name: string;
  } | null>(null);

  useEffect(() => {
    onBusyChange?.(!!busyAppId);
  }, [busyAppId, onBusyChange]);

  const installedQuery = useQuery({
    queryKey: ["workspaceApps", workspace.id],
    queryFn: () => api.workspaceApps(workspace.id),
  });
  const catalogQuery = useQuery({
    queryKey: ["marketplaceCatalog"],
    queryFn: () => api.marketplaceCatalog(),
    enabled: canManage,
  });
  const developerAppsQuery = useQuery({
    queryKey: ["developerMarketplaceApps", "modal"],
    queryFn: () =>
      Promise.resolve()
        .then(() => api.developerMarketplaceApps())
        .catch(() => ({ apps: [] as DeveloperMarketplaceApp[] })),
    enabled: canManage,
  });

  const installedApps = useMemo(() => installedQuery.data?.apps || [], [installedQuery.data]);
  const installedIds = useMemo(() => new Set(installedApps.map((a) => a.id)), [installedApps]);
  const catalogApps = useMemo(() => catalogQuery.data?.apps || [], [catalogQuery.data]);
  const catalogIdSet = useMemo(() => new Set(catalogApps.map((a) => a.id)), [catalogApps]);
  const installableApps = catalogApps.filter((app) => !installedIds.has(app.id));
  const testableDraftApps = useMemo(() => {
    if (!canManage) return [];
    return (developerAppsQuery.data?.apps || []).filter((app) => {
      if (catalogIdSet.has(app.id) || installedIds.has(app.id)) return false;
      if (app.adminSuspended || app.reviewStatus === "suspended" || !app.enabled) return false;
      return Boolean(app.draftWebUrl && app.draftMcpUrl);
    });
  }, [canManage, catalogIdSet, developerAppsQuery.data, installedIds]);

  async function toggleInstall(appId: string, installed: boolean, name: string) {
    if (!canManage || busyAppId) return;
    setBusyAppId(appId);
    try {
      if (installed) {
        await api.uninstallWorkspaceApp(workspace.id, appId);
        setConfirmingUninstall(null);
        toast.success(`已从「${workspace.name}」卸载「${name}」`);
      } else {
        await api.installWorkspaceApp(workspace.id, appId);
        toast.success(`已将「${name}」安装到「${workspace.name}」`);
      }
      await queryClient.invalidateQueries({ queryKey: ["workspaceApps", workspace.id] });
      await queryClient.invalidateQueries({ queryKey: ["conversationApps"] });
    } catch (e) {
      const message = errorText(e);
      toast.error(installed ? `卸载失败：${message}` : `安装失败：${message}`);
    } finally {
      setBusyAppId("");
    }
  }

  function renderAppRow(app: AppRow, options: { installed: boolean; isDraftOnly?: boolean }) {
    const { name, description, publisher } = appDisplay(app, !!options.isDraftOnly);
    return (
      <li key={app.id} className="member-directory-row">
        <div className="member-directory-identity">
          <div>
            <b>
              {name} <BadgePill>{app.slug}</BadgePill>
              {app.isOfficial ? (
                <BadgePill className="mp-badge-approved">官方</BadgePill>
              ) : publisher ? (
                <BadgePill>开发者：{publisher}</BadgePill>
              ) : null}
              {options.isDraftOnly && <BadgePill className="mp-badge-draft">自测草稿</BadgePill>}
              {options.installed && <BadgePill>已安装</BadgePill>}
            </b>
            {description && <p className="field-help">{description}</p>}
          </div>
        </div>
        {canManage && (
          <div className="member-directory-actions">
            <Button
              type="button"
              variant={options.installed ? "danger" : "primary"}
              busy={busyAppId === app.id}
              disabled={!!busyAppId}
              onClick={() => {
                if (options.installed) setConfirmingUninstall({ id: app.id, name });
                else void toggleInstall(app.id, false, name);
              }}
            >
              {options.installed ? "卸载应用" : "安装到工作空间"}
            </Button>
          </div>
        )}
      </li>
    );
  }

  function retryButton(onRetry: () => void) {
    return (
      <Button variant="secondary" size="sm" onClick={onRetry}>
        重新加载
      </Button>
    );
  }

  return (
    <div className="workspace-apps">
      {!marketplaceEnabled && (
        <Notice kind="info">
          应用市场已由平台关闭（平台当前已暂停应用写入与新会话），已安装的应用仅供只读查看。
        </Notice>
      )}
      {marketplaceEnabled && !isOwner && (
        <p className="field-help workspace-apps-hint">
          安装后的应用在当前工作空间的所有小组中对人类成员与 Agent 开放。仅工作空间创建者可以安装或卸载应用。
        </p>
      )}
      <section className="workspace-apps-section" aria-labelledby={`installed-${workspace.id}`}>
        <h2 id={`installed-${workspace.id}`}>已安装</h2>
        {installedQuery.isLoading ? (
          <p className="muted" role="status">正在加载已安装应用…</p>
        ) : installedQuery.isError ? (
          <div className="admin-empty" role="alert">
            <p>加载失败：{errorText(installedQuery.error)}</p>
            {retryButton(() => void installedQuery.refetch())}
          </div>
        ) : installedApps.length === 0 ? (
          <EmptyState
            icon={<Blocks size={ICON.lg} />}
            title="尚未安装应用"
            description={`「${workspace.name}」尚未安装应用。`}
          />
        ) : (
          <DataList label="已安装的应用" className="member-directory-list">
            {installedApps.map((app) => renderAppRow(app, { installed: true }))}
          </DataList>
        )}
      </section>
      {canManage && (
        <section className="workspace-apps-section" aria-labelledby={`catalog-${workspace.id}`}>
          <h2 id={`catalog-${workspace.id}`}>应用目录</h2>
          {catalogQuery.isLoading ? (
            <p className="muted" role="status">正在加载应用目录…</p>
          ) : catalogQuery.isError ? (
            <div className="admin-empty" role="alert">
              <p>加载失败：{errorText(catalogQuery.error)}</p>
              {retryButton(() => void catalogQuery.refetch())}
            </div>
          ) : installableApps.length === 0 ? (
            <EmptyState
              icon={<Blocks size={ICON.lg} />}
              title={catalogApps.length ? "目录应用均已安装" : "暂无可选应用"}
              description={catalogApps.length ? "目录中的应用都已安装。" : "平台目录中暂无可用应用。"}
            />
          ) : (
            <DataList label="工作空间可选应用" className="member-directory-list">
              {installableApps.map((app) => renderAppRow(app, { installed: false }))}
            </DataList>
          )}
        </section>
      )}
      {canManage && testableDraftApps.length > 0 && (
        <section className="workspace-apps-section mp-modal-section">
          <div className="mp-section-header">
            <b>我的待测草稿应用</b>
            <small className="muted">仅在你创建的工作空间内可安装自测</small>
          </div>
          <DataList label="我的待测草稿应用" className="member-directory-list">
            {testableDraftApps.map((app) => renderAppRow(app, { installed: false, isDraftOnly: true }))}
          </DataList>
        </section>
      )}
      {canManage && (
        <div className="workspace-apps-footer">
          <DeveloperPlatformLink />
        </div>
      )}
      {confirmingUninstall && (
        <ConfirmDialog
          tone="danger"
          title={`从「${workspace.name}」卸载「${confirmingUninstall.name}」？`}
          impact={[
            "该工作空间所有小组的应用面板都会移除这个应用，正在使用的成员会立即断开。",
            "Agent 将无法再调用这个应用提供的工具。",
            "之后可以随时从目录重新安装。",
          ]}
          confirmLabel="卸载应用"
          busy={busyAppId === confirmingUninstall.id}
          onCancel={() => setConfirmingUninstall(null)}
          onConfirm={() =>
            void toggleInstall(confirmingUninstall.id, true, confirmingUninstall.name)
          }
        />
      )}
    </div>
  );
}

/** Routable workspace apps page (`/app/w/:workspaceId/apps`). */
export function WorkspaceAppsPage({
  workspace,
  isOwner,
  marketplaceEnabled,
}: {
  workspace: Workspace;
  isOwner: boolean;
  marketplaceEnabled: boolean;
}) {
  return (
    <div className="members-view workspace-apps-view">
      <div className="members-heading">
        <p className="members-summary">
          <b>{workspace.name}</b> · 安装后的应用在所有小组中对人类成员与 Agent 开放
        </p>
      </div>
      <WorkspaceAppsPanel
        workspace={workspace}
        isOwner={isOwner}
        marketplaceEnabled={marketplaceEnabled}
      />
    </div>
  );
}
