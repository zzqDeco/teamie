import { useState } from "react";
import { Link } from "react-router-dom";
import {
  Blocks,
  CheckCheck,
  CheckCircle2,
  Copy,
  KeyRound,
  Play,
  Send,
  ShieldCheck,
  Undo2,
} from "lucide-react";
import type {
  DeveloperMarketplaceApp,
  MarketplaceAppReview,
  MarketplaceEndpointVerification,
  User,
  Workspace,
} from "@teamie/api-client";
import { canConfigureModelGateway } from "../agent-management";
import { copyToClipboard } from "../clipboard";
import { ICON } from "../icons";
import { formatDateTime } from "../format-datetime";
import {
  probeStageLabel,
  reviewActionLabel,
} from "../marketplace/marketplace-labels";
import {
  IssuedSecretDialog,
  RotateSecretConfirm,
} from "../marketplace/secret-dialogs";
import { toast } from "../toast";
import { BadgePill, Button } from "../ui";

export function DeveloperAppSteps({
  app,
  hasEndpoints,
  verifiedOk,
  isSuspended,
  statusLabel,
}: {
  app: DeveloperMarketplaceApp;
  hasEndpoints: boolean;
  verifiedOk: boolean;
  isSuspended: boolean;
  statusLabel: string;
}) {
  return (
    <div className="dev-steps" aria-label="当前应用提审进度">
      <div className="dev-step-card done">
        <span className="dev-step-num">1 · 凭证就绪</span>
        <b>App ID 已分配</b>
        <small className="field-help">已生成独立 HS256 签名密钥。</small>
      </div>
      <div className={`dev-step-card ${hasEndpoints ? "done" : ""}`}>
        <span className="dev-step-num">2 · 服务地址</span>
        <b>{hasEndpoints ? "Web + MCP 已填写" : "待填写线上地址"}</b>
        <small className="field-help">需部署在独立跨源域名上。</small>
      </div>
      <div className={`dev-step-card ${verifiedOk ? "done" : ""}`}>
        <span className="dev-step-num">3 · 协议拨测</span>
        <b>{verifiedOk ? "四阶段检测已通过" : "待执行线上检测"}</b>
        <small className="field-help">校验跨源头、鉴权拦截与 MCP 工具。</small>
      </div>
      <div
        className={`dev-step-card ${app.reviewStatus === "approved" && !isSuspended ? "done" : ""}`}
      >
        <span className="dev-step-num">4 · 审核上架</span>
        <b>{statusLabel}</b>
        <small className="field-help">
          {app.publishedVersion > 0
            ? `当前线上版本 v${app.publishedVersion}`
            : "审批通过后全站可安装"}
        </small>
      </div>
    </div>
  );
}

export function DeveloperAppCredentialsSection({
  app,
  isLocked,
  marketplaceEnabled,
  busyAction,
  confirmingRotate,
  issuedSecret,
  error,
  onRequestRotate,
  onCancelRotate,
  onConfirmRotate,
  onAcknowledgeSecret,
}: {
  app: DeveloperMarketplaceApp;
  isLocked: boolean;
  marketplaceEnabled: boolean;
  busyAction: string;
  confirmingRotate: boolean;
  issuedSecret: string;
  error: string;
  onRequestRotate: () => void;
  onCancelRotate: () => void;
  onConfirmRotate: () => void;
  onAcknowledgeSecret: () => void;
}) {
  const [copiedAppId, setCopiedAppId] = useState(false);

  async function handleCopyAppId() {
    try {
      await copyToClipboard(app.id);
      setCopiedAppId(true);
      toast.success("已复制 App ID");
    } catch {
      toast.error("复制失败，请手动选中 App ID 复制。");
    }
  }

  return (
    <>
      <section className="settings-form" aria-label="凭证与签名密钥">
        <div className="mp-section-header">
          <h3>应用凭证（App ID & Signing Secret）</h3>
          <Button
            type="button"
            variant="secondary"
            size="sm"
            disabled={!marketplaceEnabled || isLocked || !!busyAction}
            busy={busyAction === "rotate"}
            onClick={onRequestRotate}
          >
            <KeyRound size={ICON.xs} /> 轮换签名密钥
          </Button>
        </div>
        <label>
          App ID（对应 JWT aud 声明）
          <div className="copy-input">
            <input
              value={app.id}
              readOnly
              aria-label="应用 App ID"
              onFocus={(e) => e.target.select()}
            />
            <button
              type="button"
              aria-label="复制 App ID"
              onClick={() => void handleCopyAppId()}
            >
              {copiedAppId ? <CheckCheck size={ICON.md} /> : <Copy size={ICON.md} />}
            </button>
          </div>
        </label>
      </section>
      {confirmingRotate && (
        <RotateSecretConfirm
          appName={app.draftName || app.name}
          published={app.publishedVersion > 0}
          busy={busyAction === "rotate"}
          error={error}
          onCancel={onCancelRotate}
          onConfirm={onConfirmRotate}
        />
      )}
      {issuedSecret && (
        <IssuedSecretDialog
          appName={app.draftName || app.name}
          secret={issuedSecret}
          onAcknowledge={onAcknowledgeSecret}
        />
      )}
    </>
  );
}

export function DeveloperAppPublishedDiff({
  app,
}: {
  app: DeveloperMarketplaceApp;
}) {
  if (app.publishedVersion <= 0) return null;
  return (
    <section className="settings-form" aria-label="线上与草稿版本对比">
      <h3>线上版本与草稿隔离</h3>
      <p className="field-help">
        在草稿未通过新一轮审核前，其他工作空间仍继续使用线上已发布版本（v{app.publishedVersion}）。
      </p>
      <div className="dev-diff-grid">
        <div className="dev-diff-col">
          <b>线上运行版本 (v{app.publishedVersion})</b>
          <p className="field-help">名称：{app.name}</p>
          <p className="field-help">Web：{app.webUrl}</p>
          <p className="field-help">MCP：{app.mcpUrl}</p>
        </div>
        <div className="dev-diff-col">
          <b>当前编辑草稿</b>
          <p className="field-help">名称：{app.draftName}</p>
          <p className="field-help">Web：{app.draftWebUrl || "未配置"}</p>
          <p className="field-help">MCP：{app.draftMcpUrl || "未配置"}</p>
        </div>
      </div>
    </section>
  );
}

export function DeveloperAppVerificationSection({
  report,
  hasEndpoints,
  hasUnsavedEndpointEdits,
  canSaveAndVerify,
  isLocked,
  marketplaceEnabled,
  busyAction,
  onVerify,
  onSaveAndVerify,
}: {
  report?: MarketplaceEndpointVerification | null;
  hasEndpoints: boolean;
  hasUnsavedEndpointEdits: boolean;
  canSaveAndVerify: boolean;
  isLocked: boolean;
  marketplaceEnabled: boolean;
  busyAction: string;
  onVerify: () => void;
  onSaveAndVerify: () => void;
}) {
  return (
    <section className="settings-form" aria-label="线上服务协议验证">
      <div className="mp-section-header">
        <div>
          <h3>步骤 3A · 线上服务四阶段协议拨测</h3>
          <p className="field-help">
            检测将连接已保存的草稿地址，由 Teamie 后端主动验证跨源隔离、无 Token 401 拒绝、伪造签名拒绝以及真实 HS256 Context Token 下的 tools/list 工具暴露。
          </p>
        </div>
        <Button
          type="button"
          busy={busyAction === "verify"}
          disabled={!marketplaceEnabled || !hasEndpoints || isLocked || !!busyAction}
          onClick={onVerify}
        >
          <Play size={ICON.xs} /> 立即检测线上服务
        </Button>
      </div>

      {hasUnsavedEndpointEdits && !isLocked && marketplaceEnabled && (
        <div className="dev-probe-hint" role="status">
          <span>当前地址修改尚未保存，检测仍会使用上次保存的地址。</span>
          <Button
            type="button"
            variant="secondary"
            size="sm"
            busy={busyAction === "saveAndVerify"}
            disabled={!canSaveAndVerify || !!busyAction}
            onClick={onSaveAndVerify}
          >
            <Play size={ICON.xs} /> 保存并检测
          </Button>
        </div>
      )}

      {report ? (
        <>
          <ul className="dev-check-list" aria-label="协议检测结果列表">
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
            <ul className="dev-tool-list" aria-label="已发现的 MCP 工具">
              {report.tools.map((tool) => (
                <li key={tool.name} className="dev-tool-card">
                  <b>
                    <CheckCircle2 size={ICON.xs} /> {tool.exposedName}
                  </b>
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
        <p className="muted">保存 Web UI 与 MCP Server 地址后，点击「立即检测线上服务」开始验证。</p>
      )}
    </section>
  );
}

export function DeveloperAppSandboxSection({
  ownedWorkspaces,
  selectedWsId,
  hasEndpoints,
  marketplaceEnabled,
  busyAction,
  onSelectWs,
  onInstallSandbox,
}: {
  ownedWorkspaces: Workspace[];
  selectedWsId: string;
  hasEndpoints: boolean;
  marketplaceEnabled: boolean;
  busyAction: string;
  onSelectWs: (id: string) => void;
  onInstallSandbox: () => void;
}) {
  return (
    <section className="settings-form" aria-label="自有工作空间联调自测">
      <h3>步骤 3B · 安装到自有工作空间实测</h3>
      <p className="field-help">
        无需等待审核，你可将当前草稿直接安装到自己创建的工作空间，在真实小组中验证人类 Web UI 面板与 Agent MCP 工具同场交互。
      </p>
      {ownedWorkspaces.length === 0 ? (
        <p className="muted">你尚未创建工作空间，请先在左侧栏创建一个用于联调的工作空间。</p>
      ) : (
        <div className="dev-inline-actions">
          <select
            aria-label="选择联调工作空间"
            value={selectedWsId}
            disabled={!marketplaceEnabled}
            onChange={(e) => onSelectWs(e.target.value)}
          >
            {ownedWorkspaces.map((ws) => (
              <option key={ws.id} value={ws.id}>
                {ws.name}
              </option>
            ))}
          </select>
          <Button
            type="button"
            variant="secondary"
            disabled={!marketplaceEnabled || !hasEndpoints || !selectedWsId || !!busyAction}
            busy={busyAction === "sandboxInstall"}
            onClick={onInstallSandbox}
          >
            <Blocks size={ICON.xs} /> 安装草稿到该空间自测
          </Button>
        </div>
      )}
    </section>
  );
}

export function DeveloperAppSubmissionSection({
  app,
  user,
  reviews,
  submissionNote,
  verifiedOk,
  isSuspended,
  marketplaceEnabled,
  busyAction,
  onChangeNote,
  onSubmitReview,
  onWithdrawReview,
}: {
  app: DeveloperMarketplaceApp;
  user: User | null;
  reviews: MarketplaceAppReview[];
  submissionNote: string;
  verifiedOk: boolean;
  isSuspended: boolean;
  marketplaceEnabled: boolean;
  busyAction: string;
  onChangeNote: (next: string) => void;
  onSubmitReview: () => void;
  onWithdrawReview: () => void;
}) {
  return (
    <section className="settings-form" aria-label="提交平台审核">
      <h3>步骤 4 · 提交平台审核</h3>
      {app.reviewStatus === "pending_review" ? (
        <>
          <p className="field-help">
            你的应用已提交至平台管理员审核队列。若需继续调整代码或地址，可随时撤回提审。
          </p>
          <div className="dev-inline-actions">
            {canConfigureModelGateway(user) && (
              <Link to="/app/admin/marketplace" className="button">
                <ShieldCheck size={ICON.xs} /> 前往平台审核队列
              </Link>
            )}
            <Button
              type="button"
              variant="secondary"
              disabled={!marketplaceEnabled || !!busyAction}
              busy={busyAction === "withdraw"}
              onClick={onWithdrawReview}
            >
              <Undo2 size={ICON.xs} /> 撤回提审
            </Button>
          </div>
        </>
      ) : (
        <>
          <label>
            提审说明（供平台审核员查阅玩法、测试方法或版本变更摘要）
            <textarea
              rows={3}
              value={submissionNote}
              onChange={(e) => onChangeNote(e.target.value)}
              placeholder="例如：支持 6-12 人局暗夜狼人杀（人类 + Agent 混排），已开启角色视野隔离与动作幂等锁。"
              maxLength={500}
              disabled={!marketplaceEnabled || isSuspended}
            />
          </label>
          <Button
            type="button"
            busy={busyAction === "submit"}
            disabled={!marketplaceEnabled || !verifiedOk || isSuspended || !!busyAction}
            onClick={onSubmitReview}
          >
            <Send size={ICON.xs} /> 提交平台审核
          </Button>
          {!verifiedOk && (
            <small className="field-help">
              提交审核前，当前草稿配置必须先通过上方的「线上服务四阶段协议拨测」。
            </small>
          )}
        </>
      )}

      {reviews.length > 0 && (
        <div className="mp-modal-section">
          <b>提审与审核历史</b>
          <ul className="dev-timeline" aria-label="提审历史时间线">
            {reviews.map((item) => (
              <li key={item.id} className="dev-timeline-item">
                <b>{reviewActionLabel(item.action)}</b> ·{" "}
                <small>{formatDateTime(item.createdAt, "stamp")}</small>
                {item.note && <p className="field-help">{item.note}</p>}
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  );
}
