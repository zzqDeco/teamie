import { useMemo, useState } from "react";
import type { FormEvent } from "react";
import { Link } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Ban,
  Blocks,
  CheckCheck,
  CheckCircle2,
  Copy,
  Eye,
  KeyRound,
  Plus,
  RefreshCw,
  ShieldCheck,
  Trash2,
} from "lucide-react";
import { api, ApiError } from "@teamie/api-client";
import type {
  DeveloperMarketplaceApp,
  MarketplaceEndpointVerification,
  User,
} from "@teamie/api-client";
import { useAuth } from "../auth";
import { errorText } from "../errors";
import { BadgePill, Button, Modal, Notice, SwitchField } from "../ui";
import { ICON } from "../icons";
import { probeStageLabel, reviewActionLabel, reviewStatusMeta } from "./marketplace-labels";

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

export function PlatformMarketplace() {
  const { user } = useAuth();
  if (user?.platformRole !== "admin") return <AdminRequired />;
  return <AdminMarketplaceCatalog key={`${user.id}:${user.platformRole}`} user={user} />;
}

type AdminFilter = "all" | "pending_review" | "suspended";

function AdminMarketplaceCatalog({ user }: { user: User }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [description, setDescription] = useState("");
  const [webUrl, setWebUrl] = useState("");
  const [mcpUrl, setMcpUrl] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [filter, setFilter] = useState<AdminFilter>("all");
  const [reviewAppId, setReviewAppId] = useState("");
  const [busy, setBusy] = useState(false);
  const [busyAppId, setBusyAppId] = useState("");
  const [error, setError] = useState("");
  const [issuedSecret, setIssuedSecret] = useState<{
    appName: string;
    secret: string;
    copied: boolean;
  } | null>(null);

  const appsQuery = useQuery({
    queryKey: ["adminMarketplaceApps", user.id],
    queryFn: () => api.adminMarketplaceApps(),
  });

  async function handleCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      const created = await api.createAdminMarketplaceApp({
        name: name.trim(),
        slug: slug.trim().toLowerCase(),
        description: description.trim(),
        webUrl: webUrl.trim(),
        mcpUrl: mcpUrl.trim(),
        enabled,
      });
      setName("");
      setSlug("");
      setDescription("");
      setWebUrl("");
      setMcpUrl("");
      setEnabled(true);
      setIssuedSecret({
        appName: created.app.name,
        secret: created.signingSecret,
        copied: false,
      });
      await queryClient.invalidateQueries({ queryKey: ["adminMarketplaceApps"] });
      await queryClient.invalidateQueries({ queryKey: ["marketplaceCatalog"] });
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  async function runAppMutation(appId: string, fn: () => Promise<void>) {
    if (busyAppId) return;
    setBusyAppId(appId);
    setError("");
    try {
      await fn();
      await queryClient.invalidateQueries({ queryKey: ["adminMarketplaceApps"] });
      await queryClient.invalidateQueries({ queryKey: ["marketplaceCatalog"] });
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusyAppId("");
    }
  }

  async function copySecret() {
    if (!issuedSecret) return;
    try {
      await navigator.clipboard.writeText(issuedSecret.secret);
      setIssuedSecret({ ...issuedSecret, copied: true });
    } catch {
      setError("复制未成功，请手动选中密钥复制。");
    }
  }

  const apps = useMemo(() => appsQuery.data?.apps || [], [appsQuery.data]);
  const pendingCount = useMemo(
    () => apps.filter((a) => a.reviewStatus === "pending_review").length,
    [apps],
  );
  const suspendedCount = useMemo(
    () =>
      apps.filter((a) => a.adminSuspended || a.reviewStatus === "suspended").length,
    [apps],
  );
  const filteredApps = useMemo(() => {
    if (filter === "pending_review") {
      return apps.filter((a) => a.reviewStatus === "pending_review");
    }
    if (filter === "suspended") {
      return apps.filter((a) => a.adminSuspended || a.reviewStatus === "suspended");
    }
    return apps;
  }, [apps, filter]);

  if (
    appsQuery.error instanceof ApiError &&
    appsQuery.error.status === 403 &&
    appsQuery.error.code === "forbidden"
  ) {
    return <AdminRequired />;
  }

  return (
    <div className="admin-view">
      <div className="admin-heading">
        <div>
          <h1>
            应用市场与提审治理
            <span className="platform-role-badge">
              <ShieldCheck size={ICON.xs} />
              平台管理员
            </span>
          </h1>
          <p>
            管理官方直发应用并审核社区开发者提审的外部 Web + MCP 服务。通过审核后，工作空间所有者即可安装至群聊。
          </p>
        </div>
        <span className="admin-heading-icon">
          <Blocks size={ICON.xl} />
        </span>
      </div>

      <Notice>{error}</Notice>

      {issuedSecret && (
        <section className="settings-form" aria-label="应用签名密钥">
          <h3>「{issuedSecret.appName}」的 Context Token 签名密钥</h3>
          <p className="field-help">
            此 HMAC-SHA256 密钥仅显示一次，请妥善保存并配置到对应的第三方 MCP Server 用于校验 Bearer Context Token。
          </p>
          <div className="copy-input">
            <input
              value={issuedSecret.secret}
              readOnly
              aria-label="应用签名密钥"
              onFocus={(e) => e.target.select()}
            />
            <button type="button" onClick={() => void copySecret()} aria-label="复制签名密钥">
              {issuedSecret.copied ? <CheckCheck size={ICON.md} /> : <Copy size={ICON.md} />}
            </button>
          </div>
        </section>
      )}

      <form className="settings-form" onSubmit={handleCreate}>
        <h3>上架官方应用</h3>
        <label>
          应用名称
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="例如：协作桌游面板"
            maxLength={80}
            required
          />
        </label>
        <label>
          唯一标识（Slug）
          <input
            value={slug}
            onChange={(e) => setSlug(e.target.value)}
            placeholder="例如：board-room"
            maxLength={64}
            required
          />
          <small className="field-help">
            仅限小写字母、数字与连字符，Agent 工具将暴露为 mcp__slug__tool。
          </small>
        </label>
        <label>
          应用简介
          <input
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="简要说明该群聊应用的用途与交互方式"
            maxLength={400}
          />
        </label>
        <label>
          Web UI 地址（HTTPS / HTTP）
          <input
            type="url"
            value={webUrl}
            onChange={(e) => setWebUrl(e.target.value)}
            placeholder="https://app.example.com/ui"
            required
          />
        </label>
        <label>
          MCP Server 地址（Streamable HTTP）
          <input
            type="url"
            value={mcpUrl}
            onChange={(e) => setMcpUrl(e.target.value)}
            placeholder="https://app.example.com/mcp"
            required
          />
        </label>
        <SwitchField
          checked={enabled}
          title="立即启用"
          description="启用后，工作空间所有者即可在目录中浏览并安装该应用。"
          onChange={setEnabled}
        />
        <Button
          type="submit"
          busy={busy}
          disabled={!name.trim() || !slug.trim() || !webUrl.trim() || !mcpUrl.trim()}
        >
          <Plus size={ICON.sm} /> 注册应用
        </Button>
      </form>

      <section className="settings-form" aria-label="已注册应用列表">
        <div className="mp-filter-bar">
          <h3>应用与审核队列（{filteredApps.length}）</h3>
          <div className="theme-options" role="group" aria-label="筛选应用状态">
            <button
              type="button"
              className={`theme-option ${filter === "all" ? "active" : ""}`}
              onClick={() => setFilter("all")}
            >
              全部 ({apps.length})
            </button>
            <button
              type="button"
              className={`theme-option ${filter === "pending_review" ? "active" : ""}`}
              onClick={() => setFilter("pending_review")}
            >
              待审核 ({pendingCount})
            </button>
            <button
              type="button"
              className={`theme-option ${filter === "suspended" ? "active" : ""}`}
              onClick={() => setFilter("suspended")}
            >
              已封禁 ({suspendedCount})
            </button>
          </div>
        </div>

        {appsQuery.isLoading ? (
          <p className="muted">正在加载应用目录…</p>
        ) : filteredApps.length === 0 ? (
          <p className="muted">当前筛选条件下暂无应用。</p>
        ) : (
          <ul className="member-directory-list" aria-label="平台应用目录">
            {filteredApps.map((app) => {
              const status = reviewStatusMeta(app.reviewStatus, app.adminSuspended);
              const displayName = app.name || app.draftName;
              const displayWeb = app.webUrl || app.draftWebUrl;
              const displayMcp = app.mcpUrl || app.draftMcpUrl;
              return (
                <li key={app.id} className="member-directory-row">
                  <div className="member-directory-identity">
                    <div>
                      <b>
                        {displayName} <BadgePill>{app.slug}</BadgePill>{" "}
                        <BadgePill className={status.className}>{status.label}</BadgePill>{" "}
                        {app.isOfficial ? (
                          <BadgePill className="mp-badge-approved">官方</BadgePill>
                        ) : app.ownerDisplayName ? (
                          <BadgePill>开发者：{app.ownerDisplayName}</BadgePill>
                        ) : null}{" "}
                        <BadgePill className={app.enabled ? "" : "member-unavailable"}>
                          {app.enabled ? "已启用" : "已停用"}
                        </BadgePill>
                      </b>
                      {(app.description || app.draftDescription) && (
                        <p className="field-help">{app.description || app.draftDescription}</p>
                      )}
                      <small className="field-help">
                        Web: {displayWeb || "未填写"} · MCP: {displayMcp || "未填写"}
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
                          await api.updateAdminMarketplaceApp(app.id, { enabled: !app.enabled });
                        })
                      }
                    >
                      {app.enabled ? "停用" : "启用"}
                    </Button>
                    <Button
                      type="button"
                      className="secondary"
                      disabled={!!busyAppId}
                      onClick={() =>
                        void runAppMutation(app.id, async () => {
                          const rotated = await api.rotateAdminMarketplaceAppSecret(app.id);
                          setIssuedSecret({
                            appName: rotated.app.name,
                            secret: rotated.signingSecret,
                            copied: false,
                          });
                        })
                      }
                      aria-label={`轮换 ${displayName} 密钥`}
                    >
                      <KeyRound size={ICON.sm} /> 轮换密钥
                    </Button>
                    <Button
                      type="button"
                      className="secondary danger-text"
                      disabled={!!busyAppId}
                      onClick={() =>
                        void runAppMutation(app.id, async () => {
                          await api.deleteAdminMarketplaceApp(app.id);
                        })
                      }
                      aria-label={`删除 ${displayName}`}
                    >
                      <Trash2 size={ICON.sm} /> 删除
                    </Button>
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </section>

      {reviewAppId && (
        <AdminReviewModal
          appId={reviewAppId}
          onClose={() => setReviewAppId("")}
        />
      )}
    </div>
  );
}

function AdminReviewModal({ appId, onClose }: { appId: string; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [note, setNote] = useState("");
  const [busyAction, setBusyAction] = useState("");
  const [error, setError] = useState("");
  const [liveReport, setLiveReport] = useState<MarketplaceEndpointVerification | null>(
    null,
  );

  const detailQuery = useQuery({
    queryKey: ["adminMarketplaceReview", appId],
    queryFn: () => api.adminMarketplaceApp(appId),
  });

  const app: DeveloperMarketplaceApp | undefined = detailQuery.data?.app;
  const reviews = detailQuery.data?.reviews || [];
  const report = liveReport || app?.verificationReport;

  async function runAction(action: string, fn: () => Promise<void>) {
    if (busyAction) return;
    setBusyAction(action);
    setError("");
    try {
      await fn();
      await queryClient.invalidateQueries({ queryKey: ["adminMarketplaceReview", appId] });
      await queryClient.invalidateQueries({ queryKey: ["adminMarketplaceApps"] });
      await queryClient.invalidateQueries({ queryKey: ["marketplaceCatalog"] });
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusyAction("");
    }
  }

  return (
    <Modal
      title={app ? `审核与治理 · ${app.draftName || app.name}` : "审核详情"}
      subtitle="核对线上发布快照与提审草稿变更、校验四阶段协议拨测报告并执行审批或治理操作。"
      className="mp-modal-wide"
      busy={!!busyAction}
      onClose={onClose}
    >
      <div>
        <Notice>{error}</Notice>
        {detailQuery.isLoading || !app ? (
          <p className="muted">正在加载审核详情…</p>
        ) : (
          <>
            <div className="dev-diff-grid">
              <div className="dev-diff-col">
                <b>线上已发布版本 (v{app.publishedVersion})</b>
                <p className="field-help">名称：{app.isPublished ? app.name : "尚未发布"}</p>
                <p className="field-help">Slug：{app.slug}</p>
                <p className="field-help">Web：{app.webUrl || "—"}</p>
                <p className="field-help">MCP：{app.mcpUrl || "—"}</p>
              </div>
              <div className="dev-diff-col">
                <b>本次提审草稿 (修订 #{app.submissionRevision})</b>
                <p className="field-help">名称：{app.draftName}</p>
                <p className="field-help">Slug：{app.slug}</p>
                <p className="field-help">Web：{app.draftWebUrl || "—"}</p>
                <p className="field-help">MCP：{app.draftMcpUrl || "—"}</p>
                {app.submissionNote && (
                  <p className="field-help">提审说明：{app.submissionNote}</p>
                )}
              </div>
            </div>

            <div className="mp-modal-section">
              <div className="mp-section-header">
                <b>协议联调与 MCP 工具快照</b>
                <Button
                  type="button"
                  size="sm"
                  variant="secondary"
                  busy={busyAction === "verify"}
                  onClick={() =>
                    void runAction("verify", async () => {
                      const res = await api.verifyAdminMarketplaceApp(app.id);
                      setLiveReport(res.verification);
                    })
                  }
                >
                  <RefreshCw size={ICON.xs} /> 重新检测线上服务
                </Button>
              </div>
              {report ? (
                <>
                  <ul className="dev-check-list">
                    {report.checks.map((chk) => (
                      <li key={chk.stage} className="dev-check-item">
                        <div className="dev-check-head">
                          <span>{probeStageLabel(chk.stage)}</span>
                          <BadgePill
                            className={chk.ok ? "mp-badge-approved" : "mp-badge-rejected"}
                          >
                            {chk.ok ? "通过" : "未通过"}
                          </BadgePill>
                        </div>
                        <small className="field-help">{chk.summary}</small>
                      </li>
                    ))}
                  </ul>
                  {report.tools.length > 0 && (
                    <ul className="dev-tool-list" aria-label="审核发现的 MCP 工具">
                      {report.tools.map((tool) => (
                        <li key={tool.name} className="dev-tool-card">
                          <b>{tool.exposedName}</b>
                          {tool.description && <p className="field-help">{tool.description}</p>}
                          {tool.inputSchemaJson && (
                            <pre className="dev-code-block">{tool.inputSchemaJson}</pre>
                          )}
                        </li>
                      ))}
                    </ul>
                  )}
                </>
              ) : (
                <p className="muted">尚无协议检测记录，请点击「重新检测线上服务」。</p>
              )}
            </div>

            <div className="mp-modal-section">
              <label>
                审批备注 / 驳回或封禁原因
                <input
                  value={note}
                  onChange={(e) => setNote(e.target.value)}
                  placeholder="驳回提审或封禁下架时必须填写具体原因"
                  maxLength={500}
                />
              </label>
              <div className="dev-inline-actions">
                {app.reviewStatus === "pending_review" && (
                  <>
                    <Button
                      type="button"
                      busy={busyAction === "approve"}
                      onClick={() =>
                        void runAction("approve", async () => {
                          await api.approveAdminMarketplaceApp(
                            app.id,
                            app.submissionRevision,
                            note.trim() || undefined,
                          );
                          setNote("");
                        })
                      }
                    >
                      <CheckCircle2 size={ICON.sm} /> 审批通过并上架
                    </Button>
                    <Button
                      type="button"
                      variant="danger"
                      busy={busyAction === "reject"}
                      disabled={!note.trim()}
                      onClick={() =>
                        void runAction("reject", async () => {
                          await api.rejectAdminMarketplaceApp(
                            app.id,
                            app.submissionRevision,
                            note.trim(),
                          );
                          setNote("");
                        })
                      }
                    >
                      驳回提审
                    </Button>
                  </>
                )}
                {!app.adminSuspended && app.reviewStatus !== "suspended" ? (
                  <Button
                    type="button"
                    variant="secondary"
                    className="danger-text"
                    busy={busyAction === "suspend"}
                    disabled={!note.trim()}
                    onClick={() =>
                      void runAction("suspend", async () => {
                        await api.suspendAdminMarketplaceApp(app.id, note.trim());
                        setNote("");
                      })
                    }
                  >
                    <Ban size={ICON.sm} /> 封禁下架
                  </Button>
                ) : (
                  <Button
                    type="button"
                    variant="secondary"
                    busy={busyAction === "unsuspend"}
                    onClick={() =>
                      void runAction("unsuspend", async () => {
                        await api.unsuspendAdminMarketplaceApp(app.id);
                        setNote("");
                      })
                    }
                  >
                    解除封禁
                  </Button>
                )}
              </div>
            </div>

            {reviews.length > 0 && (
              <div className="mp-modal-section">
                <b>审核与治理记录</b>
                <ul className="dev-timeline">
                  {reviews.map((item) => (
                    <li key={item.id} className="dev-timeline-item">
                      <b>{reviewActionLabel(item.action)}</b> ·{" "}
                      <small>{new Date(item.createdAt).toLocaleString("zh-CN")}</small>
                      {item.note && <p className="field-help">{item.note}</p>}
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </>
        )}
        <div className="modal-actions">
          <Button type="button" variant="secondary" onClick={onClose}>
            关闭
          </Button>
        </div>
      </div>
    </Modal>
  );
}
