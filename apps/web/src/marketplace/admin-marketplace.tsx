import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Blocks,
  Eye,
  KeyRound,
  Plus,
  Search,
  ShieldCheck,
  Trash2,
} from "lucide-react";
import { api, ApiError } from "@teamie/api-client";
import type { DeveloperMarketplaceApp, User } from "@teamie/api-client";
import { useAuth } from "../auth";
import { errorText } from "../errors";
import {
  BadgePill,
  Button,
  ConfirmDialog,
  DataList,
  EmptyState,
  Notice,
} from "../ui";
import { toast } from "../toast";
import { ICON } from "../icons";
import { AdminOfficialAppModal } from "./admin-official-app-modal";
import { AdminReviewModal } from "./admin-review-modal";
import { reviewStatusMeta } from "./marketplace-labels";
import { IssuedSecretDialog, RotateSecretConfirm } from "./secret-dialogs";

const ADMIN_MARKETPLACE_PAGE_SIZE = 20;

type AdminFilter =
  | "all"
  | "pending_review"
  | "published"
  | "rejected"
  | "draft"
  | "official"
  | "suspended";

function AdminRequired() {
  return (
    <div className="unavailable-view">
      <span className="large-symbol">
        <ShieldCheck size={ICON.xl} />
      </span>
      <h2>需要平台管理员权限</h2>
      <p>全局应用市场目录与开发者提审由平台管理员维护。</p>
      <Link to="/app" className="button">
        回到我的工作空间
      </Link>
    </div>
  );
}

function matchesAdminFilter(app: DeveloperMarketplaceApp, filter: AdminFilter): boolean {
  const isSuspended = Boolean(app.adminSuspended || app.reviewStatus === "suspended");
  switch (filter) {
    case "pending_review":
      return !isSuspended && app.reviewStatus === "pending_review";
    case "published":
      return !isSuspended && Boolean(app.isPublished || app.reviewStatus === "approved");
    case "rejected":
      return !isSuspended && app.reviewStatus === "rejected";
    case "draft":
      return !isSuspended && !app.isPublished && (!app.reviewStatus || app.reviewStatus === "draft");
    case "official":
      return Boolean(app.isOfficial);
    case "suspended":
      return isSuspended;
    default:
      return true;
  }
}

function getPublisherName(app: DeveloperMarketplaceApp): string | undefined {
  return (
    app.ownerDisplayName ||
    (app as DeveloperMarketplaceApp & { publisherName?: string }).publisherName
  );
}

function matchesAdminSearch(app: DeveloperMarketplaceApp, rawQuery: string): boolean {
  const q = rawQuery.trim().toLowerCase();
  if (!q) return true;
  const haystacks = [
    app.name,
    app.draftName,
    app.slug,
    getPublisherName(app),
    app.isOfficial ? "官方 teamie" : undefined,
    app.id,
  ];
  return haystacks.some((field) => field?.toLowerCase().includes(q));
}

export function PlatformMarketplace({
  marketplaceEnabled = true,
}: {
  marketplaceEnabled?: boolean;
}) {
  const { user } = useAuth();
  if (user?.platformRole !== "admin") return <AdminRequired />;
  return (
    <AdminMarketplaceCatalog
      key={`${user.id}:${user.platformRole}`}
      user={user}
      marketplaceEnabled={marketplaceEnabled}
    />
  );
}

function AdminMarketplaceCatalog({
  user,
  marketplaceEnabled,
}: {
  user: User;
  marketplaceEnabled: boolean;
}) {
  const queryClient = useQueryClient();
  const [filter, setFilter] = useState<AdminFilter>("all");
  const [searchQuery, setSearchQuery] = useState("");
  const [visibleLimit, setVisibleLimit] = useState(ADMIN_MARKETPLACE_PAGE_SIZE);
  const [creatingOfficial, setCreatingOfficial] = useState(false);
  const [reviewAppId, setReviewAppId] = useState("");
  const [busyAppId, setBusyAppId] = useState("");
  const [error, setError] = useState("");
  const [issuedSecret, setIssuedSecret] = useState<{
    appName: string;
    secret: string;
  } | null>(null);
  const [pending, setPending] = useState<{
    kind: "rotate" | "delete";
    app: DeveloperMarketplaceApp;
  } | null>(null);

  const appsQuery = useQuery({
    queryKey: ["adminMarketplaceApps", user.id],
    queryFn: () => api.adminMarketplaceApps(),
  });

  async function runAppMutation(appId: string, fn: () => Promise<string | void>) {
    if (busyAppId) return;
    setBusyAppId(appId);
    setError("");
    try {
      const successMsg = await fn();
      setPending(null);
      await queryClient.invalidateQueries({ queryKey: ["adminMarketplaceApps"] });
      await queryClient.invalidateQueries({ queryKey: ["marketplaceCatalog"] });
      if (successMsg) toast.success(successMsg);
    } catch (e) {
      const msg = errorText(e);
      setError(msg);
      toast.error(msg);
    } finally {
      setBusyAppId("");
    }
  }

  function confirmPending() {
    if (!pending) return;
    const { kind, app } = pending;
    const label = app.name || app.draftName;
    void runAppMutation(app.id, async () => {
      if (kind === "delete") {
        await api.deleteAdminMarketplaceApp(app.id);
        return `已删除「${label}」`;
      }
      const rotated = await api.rotateAdminMarketplaceAppSecret(app.id);
      setIssuedSecret({ appName: rotated.app.name, secret: rotated.signingSecret });
      return `已轮换「${label}」的签名密钥`;
    });
  }

  const apps = useMemo(() => appsQuery.data?.apps || [], [appsQuery.data]);
  const counts = useMemo(
    () => ({
      all: apps.length,
      pending_review: apps.filter((a) => matchesAdminFilter(a, "pending_review")).length,
      published: apps.filter((a) => matchesAdminFilter(a, "published")).length,
      rejected: apps.filter((a) => matchesAdminFilter(a, "rejected")).length,
      draft: apps.filter((a) => matchesAdminFilter(a, "draft")).length,
      official: apps.filter((a) => matchesAdminFilter(a, "official")).length,
      suspended: apps.filter((a) => matchesAdminFilter(a, "suspended")).length,
    }),
    [apps],
  );

  const filteredApps = useMemo(
    () =>
      apps.filter(
        (app) =>
          matchesAdminFilter(app, filter) && matchesAdminSearch(app, searchQuery),
      ),
    [apps, filter, searchQuery],
  );

  const visibleApps = useMemo(
    () => filteredApps.slice(0, visibleLimit),
    [filteredApps, visibleLimit],
  );
  const remainingCount = Math.max(0, filteredApps.length - visibleApps.length);

  if (
    appsQuery.error instanceof ApiError &&
    appsQuery.error.status === 403 &&
    appsQuery.error.code === "forbidden"
  ) {
    return <AdminRequired />;
  }

  const filterItems: Array<{ id: AdminFilter; label: string; count: number }> = [
    { id: "all", label: "全部", count: counts.all },
    { id: "pending_review", label: "待审核", count: counts.pending_review },
    { id: "published", label: "已上架", count: counts.published },
    { id: "rejected", label: "已驳回", count: counts.rejected },
    { id: "draft", label: "草稿", count: counts.draft },
    { id: "official", label: "官方", count: counts.official },
    { id: "suspended", label: "已封禁", count: counts.suspended },
  ];

  return (
    <div className="admin-view">
      <div className="admin-heading">
        <div>
          <span className="platform-role-badge">
            <ShieldCheck size={ICON.xs} aria-hidden="true" />
            平台管理员
          </span>
          <p>
            审核社区开发者提审的外部 Web + MCP 服务并维护官方直发应用。通过审核后，工作空间所有者即可安装至群聊。
          </p>
        </div>
        <Button type="button" onClick={() => setCreatingOfficial(true)}>
          <Plus size={ICON.sm} /> 上架官方应用
        </Button>
      </div>

      {!marketplaceEnabled && (
        <Notice kind="info">
          平台当前已暂停应用写入与新会话，已注册目录与审核记录仍可查阅。
        </Notice>
      )}
      <Notice>{pending ? "" : error}</Notice>

      <section className="settings-form" aria-label="已注册应用列表">
        <div className="mp-filter-bar">
          <div>
            <h3>审核与治理队列（{filteredApps.length}）</h3>
            {counts.pending_review > 0 && (
              <small className="field-help">
                当前有 <b>{counts.pending_review}</b> 个开发者提审待处理
              </small>
            )}
          </div>
          <label className="member-search mp-admin-search">
            <Search size={ICON.sm} aria-hidden="true" />
            <input
              type="search"
              aria-label="搜索应用目录"
              value={searchQuery}
              onChange={(e) => {
                setSearchQuery(e.target.value);
                setVisibleLimit(ADMIN_MARKETPLACE_PAGE_SIZE);
              }}
              placeholder="搜索名称、Slug、发布者或 ID…"
            />
          </label>
        </div>

        <div className="theme-options mp-status-filters" role="group" aria-label="筛选应用状态">
          {filterItems.map((item) => {
            const active = filter === item.id;
            const highlightPending = item.id === "pending_review" && item.count > 0;
            return (
              <button
                key={item.id}
                type="button"
                aria-pressed={active}
                className={`theme-option ${active ? "active" : ""} ${highlightPending ? "mp-filter-attention" : ""}`}
                onClick={() => {
                  setFilter(item.id);
                  setVisibleLimit(ADMIN_MARKETPLACE_PAGE_SIZE);
                }}
              >
                {item.label} ({item.count})
              </button>
            );
          })}
        </div>

        {appsQuery.isLoading ? (
          <p className="muted" role="status">正在加载应用目录…</p>
        ) : appsQuery.isError ? (
          <div className="admin-empty" role="alert">
            <p>加载失败：{errorText(appsQuery.error)}</p>
            <Button variant="secondary" size="sm" onClick={() => void appsQuery.refetch()}>
              重新加载
            </Button>
          </div>
        ) : filteredApps.length === 0 ? (
          <EmptyState
            icon={<Blocks size={ICON.lg} />}
            title="当前筛选条件下暂无应用"
            description={
              searchQuery.trim()
                ? "请尝试更换关键词，或切换状态筛选查看其他应用。"
                : "当开发者提交应用审核或管理员上架官方应用后，记录将显示在这里。"
            }
          />
        ) : (
          <>
            <DataList label="平台应用目录" className="member-directory-list">
              {visibleApps.map((app) => {
                const status = reviewStatusMeta(app.reviewStatus, app.adminSuspended);
                const displayName = app.name || app.draftName;
                const displayWeb = app.webUrl || app.draftWebUrl;
                const displayMcp = app.mcpUrl || app.draftMcpUrl;
                const publisherLabel = getPublisherName(app);
                return (
                  <li key={app.id} className="member-directory-row">
                    <div className="member-directory-identity">
                      <div className="mp-admin-meta">
                        <b>
                          {displayName} <BadgePill>{app.slug}</BadgePill>{" "}
                          <BadgePill className={status.className}>{status.label}</BadgePill>{" "}
                          {app.isOfficial ? (
                            <BadgePill className="mp-badge-approved">官方</BadgePill>
                          ) : publisherLabel ? (
                            <BadgePill>开发者：{publisherLabel}</BadgePill>
                          ) : null}{" "}
                          <BadgePill className={app.enabled ? "" : "member-unavailable"}>
                            {app.enabled ? "已启用" : "已停用"}
                          </BadgePill>
                        </b>
                        {(app.description || app.draftDescription) && (
                          <p className="field-help">{app.description || app.draftDescription}</p>
                        )}
                        <small className="field-help">
                          ID: {app.id} · Web: {displayWeb || "未填写"} · MCP:{" "}
                          {displayMcp || "未填写"}
                        </small>
                      </div>
                    </div>
                    <div className="member-directory-actions">
                      <Button
                        type="button"
                        className="secondary"
                        disabled={!!busyAppId}
                        onClick={() => setReviewAppId(app.id)}
                        aria-label={`审核 ${displayName}`}
                      >
                        <Eye size={ICON.sm} /> 审核详情
                      </Button>
                      <Button
                        type="button"
                        className="secondary"
                        disabled={!!busyAppId}
                        busy={busyAppId === app.id}
                        onClick={() =>
                          void runAppMutation(app.id, async () => {
                            const nextEnabled = !app.enabled;
                            await api.updateAdminMarketplaceApp(app.id, {
                              enabled: nextEnabled,
                            });
                            return nextEnabled
                              ? `已启用「${displayName}」`
                              : `已停用「${displayName}」`;
                          })
                        }
                      >
                        {app.enabled ? "停用" : "启用"}
                      </Button>
                      <Button
                        type="button"
                        className="secondary"
                        disabled={!!busyAppId}
                        onClick={() => {
                          setError("");
                          setPending({ kind: "rotate", app });
                        }}
                        aria-label={`轮换 ${displayName} 密钥`}
                      >
                        <KeyRound size={ICON.sm} /> 轮换密钥
                      </Button>
                      <Button
                        type="button"
                        className="secondary danger-text"
                        disabled={!!busyAppId}
                        onClick={() => {
                          setError("");
                          setPending({ kind: "delete", app });
                        }}
                        aria-label={`删除 ${displayName}`}
                      >
                        <Trash2 size={ICON.sm} /> 删除
                      </Button>
                    </div>
                  </li>
                );
              })}
            </DataList>
            {remainingCount > 0 && (
              <div className="mp-pagination-more">
                <Button
                  type="button"
                  variant="secondary"
                  onClick={() =>
                    setVisibleLimit((prev) => prev + ADMIN_MARKETPLACE_PAGE_SIZE)
                  }
                >
                  加载更多（剩余 {remainingCount} 个）
                </Button>
              </div>
            )}
          </>
        )}
      </section>

      {creatingOfficial && (
        <AdminOfficialAppModal
          onClose={() => setCreatingOfficial(false)}
          onCreated={(issued) => {
            setCreatingOfficial(false);
            setIssuedSecret(issued);
          }}
        />
      )}
      {pending?.kind === "rotate" && (
        <RotateSecretConfirm
          appName={pending.app.name || pending.app.draftName}
          published={pending.app.isPublished}
          busy={busyAppId === pending.app.id}
          error={error}
          onCancel={() => setPending(null)}
          onConfirm={confirmPending}
        />
      )}
      {pending?.kind === "delete" && (
        <ConfirmDialog
          tone="danger"
          title={`删除应用「${pending.app.name || pending.app.draftName}」？`}
          impact={[
            "所有工作空间中该应用的安装都会被一并删除，各小组的应用面板与 Agent 工具随之消失。",
            "应用的目录信息、审核记录与签名密钥将被永久删除，无法恢复。",
            "如只想暂时下架，请改用「停用」或「封禁下架」。",
          ]}
          confirmLabel="永久删除"
          busy={busyAppId === pending.app.id}
          error={error}
          onCancel={() => setPending(null)}
          onConfirm={confirmPending}
        />
      )}
      {issuedSecret && (
        <IssuedSecretDialog
          appName={issuedSecret.appName}
          secret={issuedSecret.secret}
          onAcknowledge={() => setIssuedSecret(null)}
        />
      )}
      {reviewAppId && (
        <AdminReviewModal
          appId={reviewAppId}
          onClose={() => setReviewAppId("")}
        />
      )}
    </div>
  );
}
