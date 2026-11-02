import { useMemo, useState } from "react";
import { Link, useInRouterContext } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Code2 } from "lucide-react";
import { api } from "@teamie/api-client";
import type { DeveloperMarketplaceApp, MarketplaceApp, Workspace } from "@teamie/api-client";
import { errorText } from "../errors";
import { BadgePill, Button, ConfirmDialog, Modal, ModalCloseButton } from "../ui";
import { toast } from "../toast";
import { ICON } from "../icons";

export function WorkspaceMarketplaceModal({
  workspace,
  isOwner,
  onClose,
}: {
  workspace: Workspace;
  isOwner: boolean;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const inRouter = useInRouterContext();
  const [busyAppId, setBusyAppId] = useState("");
  const [confirmingUninstall, setConfirmingUninstall] = useState<{
    id: string;
    name: string;
  } | null>(null);

  const catalogQuery = useQuery({
    queryKey: ["marketplaceCatalog"],
    queryFn: () => api.marketplaceCatalog(),
  });

  const installedQuery = useQuery({
    queryKey: ["workspaceApps", workspace.id],
    queryFn: () => api.workspaceApps(workspace.id),
  });

  const developerAppsQuery = useQuery({
    queryKey: ["developerMarketplaceApps", "modal"],
    queryFn: () =>
      Promise.resolve()
        .then(() => api.developerMarketplaceApps())
        .catch(() => ({ apps: [] as DeveloperMarketplaceApp[] })),
    enabled: isOwner,
  });

  const installedIds = useMemo(
    () => new Set((installedQuery.data?.apps || []).map((a) => a.id)),
    [installedQuery.data],
  );

  const catalogApps = useMemo(() => catalogQuery.data?.apps || [], [catalogQuery.data]);
  const catalogIdSet = useMemo(
    () => new Set(catalogApps.map((a) => a.id)),
    [catalogApps],
  );

  const testableDraftApps = useMemo(() => {
    if (!isOwner) return [];
    return (developerAppsQuery.data?.apps || []).filter((app) => {
      if (catalogIdSet.has(app.id)) return false;
      if (app.adminSuspended || app.reviewStatus === "suspended" || !app.enabled) return false;
      return Boolean(app.draftWebUrl && app.draftMcpUrl);
    });
  }, [catalogIdSet, developerAppsQuery.data, isOwner]);

  async function toggleInstall(appId: string, installed: boolean, name: string) {
    if (!isOwner || busyAppId) return;
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

  function renderAppRow(
    app: MarketplaceApp | DeveloperMarketplaceApp,
    options?: { isDraftOnly?: boolean },
  ) {
    const isInstalled = installedIds.has(app.id);
    const displayName =
      options?.isDraftOnly && "draftName" in app && app.draftName
        ? app.draftName
        : app.name;
    const displayDesc =
      options?.isDraftOnly && "draftDescription" in app
        ? app.draftDescription || app.description
        : app.description;
    const publisher =
      "publisherName" in app
        ? app.publisherName
        : "ownerDisplayName" in app
          ? app.ownerDisplayName
          : undefined;

    return (
      <li key={app.id} className="member-directory-row">
        <div className="member-directory-identity">
          <div>
            <b>
              {displayName} <BadgePill>{app.slug}</BadgePill>
              {app.isOfficial ? (
                <BadgePill className="mp-badge-approved">官方</BadgePill>
              ) : publisher ? (
                <BadgePill>开发者：{publisher}</BadgePill>
              ) : null}
              {options?.isDraftOnly && (
                <BadgePill className="mp-badge-draft">自测草稿</BadgePill>
              )}
              {isInstalled && <BadgePill>已安装</BadgePill>}
            </b>
            {displayDesc && <p className="field-help">{displayDesc}</p>}
          </div>
        </div>
        {isOwner && (
          <div className="member-directory-actions">
            <Button
              type="button"
              className={isInstalled ? "secondary danger-text" : ""}
              busy={busyAppId === app.id}
              disabled={!!busyAppId}
              onClick={() => {
                if (isInstalled) {
                  setConfirmingUninstall({ id: app.id, name: displayName });
                } else void toggleInstall(app.id, false, displayName);
              }}
            >
              {isInstalled ? "卸载应用" : "安装到工作空间"}
            </Button>
          </div>
        )}
      </li>
    );
  }

  return (
    <Modal
      title={`「${workspace.name}」工作空间应用`}
      subtitle="安装后的应用将在当前工作空间的所有小组中对人类成员与 Agent 开放。"
      onClose={onClose}
      busy={!!busyAppId}
    >
      <div>
        {catalogQuery.isLoading || installedQuery.isLoading ? (
          <p className="muted">正在加载应用目录…</p>
        ) : catalogQuery.isError || installedQuery.isError ? (
          <div className="admin-empty" role="alert">
            <p>加载失败：{errorText(catalogQuery.error || installedQuery.error)}</p>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => {
                if (catalogQuery.isError) void catalogQuery.refetch();
                if (installedQuery.isError) void installedQuery.refetch();
              }}
            >
              重新加载
            </Button>
          </div>
        ) : catalogApps.length === 0 ? (
          <p className="muted">平台目录中暂无可用应用。</p>
        ) : (
          <ul className="member-directory-list" aria-label="工作空间可选应用">
            {catalogApps.map((app) => renderAppRow(app))}
          </ul>
        )}

        {isOwner && testableDraftApps.length > 0 && (
          <div className="mp-modal-section">
            <div className="mp-section-header">
              <b>我的待测草稿应用</b>
              <small className="muted">仅在你创建的工作空间内可安装自测</small>
            </div>
            <ul className="member-directory-list" aria-label="我的待测草稿应用">
              {testableDraftApps.map((app) => renderAppRow(app, { isDraftOnly: true }))}
            </ul>
          </div>
        )}

        <div className="modal-actions">
          {isOwner &&
            (inRouter ? (
              <Link
                to="/app/developers"
                className="button secondary small-button"
                onClick={onClose}
              >
                <Code2 size={ICON.sm} /> 开发者平台
              </Link>
            ) : (
              <a href="/app/developers" className="button secondary small-button">
                <Code2 size={ICON.sm} /> 开发者平台
              </a>
            ))}
          <ModalCloseButton>关闭</ModalCloseButton>
        </div>
      </div>
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
    </Modal>
  );
}
