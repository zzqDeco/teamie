import { useCallback, useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import { Link } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft } from "lucide-react";
import { api, ApiError } from "@teamie/api-client";
import type {
  DeveloperMarketplaceApp,
  MarketplaceEndpointVerification,
  Workspace,
} from "@teamie/api-client";
import { useAuth } from "../auth";
import { errorText } from "../errors";
import { ICON } from "../icons";
import {
  MARKETPLACE_DESCRIPTION_MAX_LENGTH,
  MARKETPLACE_SLUG_HELP,
  reviewStatusMeta,
  sanitizeMcpToolSegment,
  validateMarketplaceSlug,
} from "../marketplace/marketplace-labels";
import { usePageTitleDetail } from "../shell/page-title";
import { toast } from "../toast";
import { BadgePill, Button, ConfirmDialog, Notice, SwitchField } from "../ui";
import {
  DeveloperAppCredentialsSection,
  DeveloperAppPublishedDiff,
  DeveloperAppSandboxSection,
  DeveloperAppSteps,
  DeveloperAppSubmissionSection,
  DeveloperAppVerificationSection,
} from "./developer-app-sections";

interface DraftBaseline {
  id: string;
  updatedAt: string;
  name: string;
  slug: string;
  description: string;
  webUrl: string;
  mcpUrl: string;
}

function toDraftBaseline(app: DeveloperMarketplaceApp): DraftBaseline {
  return {
    id: app.id,
    updatedAt: app.updatedAt,
    name: app.draftName,
    slug: app.slug,
    description: app.draftDescription,
    webUrl: app.draftWebUrl,
    mcpUrl: app.draftMcpUrl,
  };
}

export function DeveloperAppDetail({
  appId,
  workspaces,
  marketplaceEnabled = true,
}: {
  appId: string;
  workspaces?: Workspace[];
  marketplaceEnabled?: boolean;
}) {
  const { user } = useAuth();
  const queryClient = useQueryClient();

  const detailQuery = useQuery({
    queryKey: ["developerMarketplaceApp", appId],
    queryFn: () => api.developerMarketplaceApp(appId),
  });

  const detailApp = detailQuery.data?.app;
  usePageTitleDetail(detailApp ? detailApp.draftName || detailApp.name : undefined);

  const fallbackBootstrap = useQuery({
    queryKey: ["developerBootstrapWorkspaces", user!.id],
    queryFn: () =>
      api
        .bootstrap()
        .then((res) => res.workspaces)
        .catch(() => [] as Workspace[]),
    enabled: !workspaces,
  });

  const ownedWorkspaces = useMemo(() => {
    const list = workspaces || fallbackBootstrap.data || [];
    return list.filter((w) => w.createdBy === user!.id);
  }, [fallbackBootstrap.data, user, workspaces]);

  const [selectedWsId, setSelectedWsId] = useState("");
  useEffect(() => {
    if (!selectedWsId && ownedWorkspaces[0]) {
      setSelectedWsId(ownedWorkspaces[0].id);
    }
  }, [ownedWorkspaces, selectedWsId]);

  const app: DeveloperMarketplaceApp | undefined = detailQuery.data?.app;
  const reviews = detailQuery.data?.reviews || [];

  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [description, setDescription] = useState("");
  const [webUrl, setWebUrl] = useState("");
  const [mcpUrl, setMcpUrl] = useState("");
  const [submissionNote, setSubmissionNote] = useState("");
  const [baseline, setBaseline] = useState<DraftBaseline | null>(null);
  const [remoteUpdated, setRemoteUpdated] = useState(false);

  const syncDraftFromApp = useCallback((nextApp: DeveloperMarketplaceApp) => {
    setName(nextApp.draftName);
    setSlug(nextApp.slug);
    setDescription(nextApp.draftDescription);
    setWebUrl(nextApp.draftWebUrl);
    setMcpUrl(nextApp.draftMcpUrl);
    setSubmissionNote(nextApp.submissionNote || "");
    setBaseline(toDraftBaseline(nextApp));
    setRemoteUpdated(false);
  }, []);

  const isDraftDirty = Boolean(
    baseline &&
      app &&
      baseline.id === app.id &&
      (name !== baseline.name ||
        slug !== baseline.slug ||
        description !== baseline.description ||
        webUrl !== baseline.webUrl ||
        mcpUrl !== baseline.mcpUrl),
  );

  useEffect(() => {
    if (!app) return;
    if (!baseline || baseline.id !== app.id) {
      syncDraftFromApp(app);
      return;
    }
    if (app.updatedAt !== baseline.updatedAt) {
      if (!isDraftDirty) {
        syncDraftFromApp(app);
      } else {
        setRemoteUpdated(true);
      }
    }
  }, [app, baseline, isDraftDirty, syncDraftFromApp]);

  const [busyAction, setBusyAction] = useState("");
  const [error, setError] = useState("");
  const [successMsg, setSuccessMsg] = useState("");
  const [liveReport, setLiveReport] = useState<MarketplaceEndpointVerification | null>(
    null,
  );
  const [issuedSecret, setIssuedSecret] = useState("");
  const [confirmingRotate, setConfirmingRotate] = useState(false);
  const [confirmingDisablePublished, setConfirmingDisablePublished] = useState(false);

  async function refreshApp() {
    await queryClient.invalidateQueries({ queryKey: ["developerMarketplaceApp", appId] });
    await queryClient.invalidateQueries({ queryKey: ["developerMarketplaceApps"] });
  }

  async function runAction(action: string, fn: () => Promise<void>) {
    if (busyAction) return;
    setBusyAction(action);
    setError("");
    setSuccessMsg("");
    try {
      await fn();
      await refreshApp();
    } catch (e) {
      const msg = errorText(e);
      setError(msg);
      toast.error(msg);
      if (e instanceof ApiError && e.code === "endpoint_verification_failed")
        await refreshApp().catch(() => {});
    } finally {
      setBusyAction("");
    }
  }

  function validateDraftInputs(): string | null {
    if (!name.trim()) return "请填写应用名称。";
    if (!app?.isPublished) {
      const slugErr = validateMarketplaceSlug(slug);
      if (slugErr) return slugErr;
    }
    return null;
  }

  async function handleSaveDraft(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!marketplaceEnabled || !app) return;
    const validationErr = validateDraftInputs();
    if (validationErr) {
      setError(validationErr);
      return;
    }
    await runAction("save", async () => {
      setLiveReport(null);
      const updated = await api.updateDeveloperMarketplaceApp(appId, {
        name: name.trim(),
        slug: slug.trim().toLowerCase(),
        description: description.trim(),
        webUrl: webUrl.trim(),
        mcpUrl: mcpUrl.trim(),
      });
      syncDraftFromApp(updated.app);
      setSuccessMsg("草稿配置已保存。若修改了线上地址或工具定义，请重新执行协议检测。");
      toast.success("草稿配置已保存");
    });
  }

  async function handleSaveAndVerify() {
    if (!marketplaceEnabled || !app) return;
    const validationErr = validateDraftInputs();
    if (validationErr) {
      setError(validationErr);
      return;
    }
    await runAction("saveAndVerify", async () => {
      setLiveReport(null);
      const updated = await api.updateDeveloperMarketplaceApp(app.id, {
        name: name.trim(),
        slug: slug.trim().toLowerCase(),
        description: description.trim(),
        webUrl: webUrl.trim(),
        mcpUrl: mcpUrl.trim(),
      });
      syncDraftFromApp(updated.app);
      const res = await api.verifyDeveloperMarketplaceApp(app.id);
      setLiveReport(res.verification);
      if (res.verification.ok) {
        setSuccessMsg("草稿已保存，且四阶段线上协议检测全部通过！");
        toast.success("四阶段线上协议检测通过");
      } else {
        toast.info("草稿已保存，请根据协议检测报告修复未通过项");
      }
    });
  }

  async function executeToggleEnabled(nextEnabled: boolean) {
    if (!marketplaceEnabled || !app) return;
    await runAction("toggleEnabled", async () => {
      const updated = await api.updateDeveloperMarketplaceApp(app.id, {
        enabled: nextEnabled,
      });
      setConfirmingDisablePublished(false);
      setBaseline((prev) =>
        prev && prev.id === updated.app.id
          ? { ...prev, updatedAt: updated.app.updatedAt }
          : prev,
      );
      const msg = nextEnabled ? "应用已启用。" : "应用已停用。";
      setSuccessMsg(msg);
      toast.success(msg);
    });
  }

  if (detailQuery.isLoading) {
    return (
      <div className="admin-view">
        <p className="muted">正在加载应用详情…</p>
      </div>
    );
  }

  const missing =
    detailQuery.error instanceof ApiError &&
    [403, 404].includes(detailQuery.error.status);
  if (!app && detailQuery.isError && !missing) {
    return (
      <div className="unavailable-view" role="alert">
        <h2>加载失败</h2>
        <p>{errorText(detailQuery.error)}</p>
        <Button onClick={() => void detailQuery.refetch()}>重新加载</Button>
      </div>
    );
  }

  if (!app) {
    return (
      <div className="unavailable-view">
        <h2>未找到该开发者应用</h2>
        <p>请确认你是该应用的创建者，或返回开发者应用列表重试。</p>
        <Link to="/app/developers" className="button">
          返回我的应用
        </Link>
      </div>
    );
  }

  const isSuspended = app.adminSuspended || app.reviewStatus === "suspended";
  const isLocked = app.reviewStatus === "pending_review" || isSuspended;
  const formDisabled = !marketplaceEnabled || isLocked;
  const status = reviewStatusMeta(app.reviewStatus, app.adminSuspended);
  const report = liveReport || app.verificationReport;
  const hasEndpoints = Boolean(app.draftWebUrl && app.draftMcpUrl);
  const hasUnsavedEndpointEdits =
    webUrl.trim() !== (app.draftWebUrl || "") ||
    mcpUrl.trim() !== (app.draftMcpUrl || "");
  const canSaveAndVerify = Boolean(
    name.trim() && slug.trim() && webUrl.trim() && mcpUrl.trim(),
  );
  const verifiedOk = Boolean(report?.ok);

  return (
    <div className="admin-view">
      <div className="mp-section-header">
        <div>
          <Link to="/app/developers" className="button secondary small-button">
            <ArrowLeft size={ICON.xs} /> 返回我的应用
          </Link>
        </div>
        <div className="mp-header-actions">
          <BadgePill className={status.className}>{status.label}</BadgePill>
          {app.publishedVersion > 0 && (
            <BadgePill className="mp-badge-approved">已上架 v{app.publishedVersion}</BadgePill>
          )}
        </div>
      </div>

      <div className="admin-heading">
        <div>
          <h2>{app.draftName || app.name}</h2>
          <p>
            Slug：<code>{app.slug}</code> · Agent 工具前缀：{" "}
            <code>mcp__{sanitizeMcpToolSegment(app.slug)}__*</code>
          </p>
        </div>
      </div>

      {!marketplaceEnabled && (
        <Notice kind="info">
          平台当前已暂停应用写入与新会话，应用配置、拨测报告与审核记录仅供只读查看。
        </Notice>
      )}

      {remoteUpdated && (
        <div className="dev-remote-update-banner" role="status">
          <span>服务端草稿已更新，当前保留了你尚未保存的本地修改。</span>
          <Button
            type="button"
            variant="secondary"
            size="sm"
            onClick={() => syncDraftFromApp(app)}
          >
            加载最新内容
          </Button>
        </div>
      )}

      <DeveloperAppSteps
        app={app}
        hasEndpoints={hasEndpoints}
        verifiedOk={verifiedOk}
        isSuspended={isSuspended}
        statusLabel={status.label}
      />

      <Notice>{confirmingRotate || confirmingDisablePublished ? "" : error}</Notice>
      <Notice kind="success">{successMsg}</Notice>

      {app.reviewStatus === "rejected" && app.reviewNote && (
        <Notice>
          上次提审未通过：{app.reviewNote}。请按要求调整配置并重新通过协议检测后再次提交。
        </Notice>
      )}
      {isSuspended && (
        <Notice>
          该应用已被平台封禁下架：{app.suspendedReason || app.reviewNote || "违反平台应用治理规范"}。
        </Notice>
      )}

      <DeveloperAppCredentialsSection
        app={app}
        isLocked={isLocked}
        marketplaceEnabled={marketplaceEnabled}
        busyAction={busyAction}
        confirmingRotate={confirmingRotate}
        issuedSecret={issuedSecret}
        error={error}
        onRequestRotate={() => {
          setError("");
          setConfirmingRotate(true);
        }}
        onCancelRotate={() => setConfirmingRotate(false)}
        onConfirmRotate={() =>
          void runAction("rotate", async () => {
            const res = await api.rotateDeveloperMarketplaceAppSecret(app.id);
            setConfirmingRotate(false);
            setIssuedSecret(res.signingSecret);
            toast.success("已轮换应用签名密钥");
          })
        }
        onAcknowledgeSecret={() => setIssuedSecret("")}
      />

      <section className="settings-form" aria-label="应用启用状态">
        <SwitchField
          checked={app.enabled}
          disabled={!marketplaceEnabled || isSuspended || !!busyAction}
          title="应用启用状态"
          description="关闭后，已安装该应用的工作空间将暂停挂载其 Web UI 与 MCP 工具（审核排队期间仍可独立开关）。"
          onChange={(nextEnabled) => {
            setError("");
            if (!nextEnabled && app.publishedVersion > 0) {
              setConfirmingDisablePublished(true);
              return;
            }
            void executeToggleEnabled(nextEnabled);
          }}
        />
      </section>
      {confirmingDisablePublished && (
        <ConfirmDialog
          tone="danger"
          title={`停用已上架应用「${app.name}」？`}
          impact={[
            "所有已安装该应用的工作空间将立即暂停展示小组 Web UI 面板，并停止向 Agent 暴露 MCP 工具。",
            "工作空间中的已安装关系与配置会保留，重新启用后自动恢复。",
          ]}
          confirmLabel="确认停用"
          busy={busyAction === "toggleEnabled"}
          error={error}
          onCancel={() => setConfirmingDisablePublished(false)}
          onConfirm={() => void executeToggleEnabled(false)}
        />
      )}

      <DeveloperAppPublishedDiff app={app} />

      <form className="settings-form" noValidate onSubmit={handleSaveDraft}>
        <h3>应用草稿配置</h3>
        {app.reviewStatus === "pending_review" && (
          <Notice kind="info">当前处于审核排队中，如需修改地址或元数据请先撤回提审。</Notice>
        )}
        <label>
          应用名称
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={80}
            disabled={formDisabled}
            required
          />
        </label>
        <label>
          唯一标识（Slug）
          <input
            value={slug}
            onChange={(e) => setSlug(e.target.value)}
            maxLength={64}
            disabled={formDisabled || app.isPublished}
            required
          />
          <small className="field-help">
            {app.isPublished
              ? "已发布应用的 Slug 保持恒定，确保已接入小组中的 Agent 工具名稳定。"
              : MARKETPLACE_SLUG_HELP}
          </small>
        </label>
        <label>
          应用简介
          <input
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            maxLength={MARKETPLACE_DESCRIPTION_MAX_LENGTH}
            disabled={formDisabled}
          />
        </label>
        <label>
          外部 Web UI 地址（独立跨源域名）
          <input
            type="url"
            value={webUrl}
            onChange={(e) => setWebUrl(e.target.value)}
            placeholder="https://werewolf.example.com/ui"
            disabled={formDisabled}
          />
        </label>
        <label>
          外部 MCP Server 地址（Streamable HTTP）
          <input
            type="url"
            value={mcpUrl}
            onChange={(e) => setMcpUrl(e.target.value)}
            placeholder="https://werewolf.example.com/mcp"
            disabled={formDisabled}
          />
        </label>
        <Button
          type="submit"
          busy={busyAction === "save"}
          disabled={formDisabled || !name.trim() || !slug.trim() || !!busyAction}
        >
          保存草稿配置
        </Button>
      </form>

      <DeveloperAppVerificationSection
        report={report}
        hasEndpoints={hasEndpoints}
        hasUnsavedEndpointEdits={hasUnsavedEndpointEdits}
        canSaveAndVerify={canSaveAndVerify}
        isLocked={isLocked}
        marketplaceEnabled={marketplaceEnabled}
        busyAction={busyAction}
        onVerify={() =>
          void runAction("verify", async () => {
            const res = await api.verifyDeveloperMarketplaceApp(app.id);
            setLiveReport(res.verification);
            if (res.verification.ok) {
              setSuccessMsg("四阶段线上协议检测全部通过！现在可以自测或提交平台审核。");
              toast.success("四阶段线上协议检测通过");
            }
          })
        }
        onSaveAndVerify={() => void handleSaveAndVerify()}
      />

      <DeveloperAppSandboxSection
        ownedWorkspaces={ownedWorkspaces}
        selectedWsId={selectedWsId}
        hasEndpoints={hasEndpoints}
        marketplaceEnabled={marketplaceEnabled}
        busyAction={busyAction}
        onSelectWs={setSelectedWsId}
        onInstallSandbox={() =>
          void runAction("sandboxInstall", async () => {
            await api.installWorkspaceApp(selectedWsId, app.id);
            await queryClient.invalidateQueries({
              queryKey: ["workspaceApps", selectedWsId],
            });
            await queryClient.invalidateQueries({ queryKey: ["conversationApps"] });
            const msg =
              "已安装草稿到所选工作空间！进入该空间的任意小组对话点击右上角「小组应用」即可联调。";
            setSuccessMsg(msg);
            toast.success("已安装草稿到工作空间");
          })
        }
      />

      <DeveloperAppSubmissionSection
        app={app}
        user={user}
        reviews={reviews}
        submissionNote={submissionNote}
        verifiedOk={verifiedOk}
        isSuspended={isSuspended}
        marketplaceEnabled={marketplaceEnabled}
        busyAction={busyAction}
        onChangeNote={setSubmissionNote}
        onSubmitReview={() =>
          void runAction("submit", async () => {
            await api.submitDeveloperMarketplaceApp(
              app.id,
              submissionNote.trim() || undefined,
            );
            setSuccessMsg("已成功提交审核！平台管理员审批通过后将自动上架。");
            toast.success("已提交平台审核");
          })
        }
        onWithdrawReview={() =>
          void runAction("withdraw", async () => {
            await api.withdrawDeveloperMarketplaceApp(app.id);
            setSuccessMsg("已撤回提审，草稿已恢复可编辑状态。");
            toast.success("已撤回提审");
          })
        }
      />
    </div>
  );
}
