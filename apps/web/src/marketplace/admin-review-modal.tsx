import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Ban, CheckCircle2, RefreshCw } from "lucide-react";
import { api } from "@teamie/api-client";
import type {
  DeveloperMarketplaceApp,
  MarketplaceEndpointVerification,
} from "@teamie/api-client";
import { errorText } from "../errors";
import { BadgePill, Button, Modal, ModalCloseButton, Notice } from "../ui";
import { toast } from "../toast";
import { ICON } from "../icons";
import { formatDateTime } from "../format-datetime";
import { probeStageLabel, reviewActionLabel } from "./marketplace-labels";

export function AdminReviewModal({
  appId,
  onClose,
}: {
  appId: string;
  onClose: () => void;
}) {
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

  async function runAction(action: string, fn: () => Promise<string | void>) {
    if (busyAction) return;
    setBusyAction(action);
    setError("");
    try {
      const successMsg = await fn();
      await queryClient.invalidateQueries({ queryKey: ["adminMarketplaceReview", appId] });
      await queryClient.invalidateQueries({ queryKey: ["adminMarketplaceApps"] });
      await queryClient.invalidateQueries({ queryKey: ["marketplaceCatalog"] });
      await queryClient.invalidateQueries({ queryKey: ["workspaceApps"] });
      await queryClient.invalidateQueries({ queryKey: ["conversationApps"] });
      if (successMsg) toast.success(successMsg);
    } catch (e) {
      const msg = errorText(e);
      setError(msg);
      toast.error(msg);
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
        {detailQuery.isLoading ? (
          <p className="muted" role="status">正在加载审核详情…</p>
        ) : detailQuery.isError || !app ? (
          <div className="admin-empty" role="alert">
            <p>加载失败：{errorText(detailQuery.error)}</p>
            <Button
              type="button"
              size="sm"
              variant="secondary"
              onClick={() => void detailQuery.refetch()}
            >
              重新加载
            </Button>
          </div>
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
                      return res.verification.ok
                        ? "线上协议检测已全部通过"
                        : "线上协议检测完成，部分检查未通过";
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
                          const label = app.draftName || app.name;
                          await api.approveAdminMarketplaceApp(
                            app.id,
                            app.submissionRevision,
                            note.trim() || undefined,
                          );
                          setNote("");
                          return `已审批通过并上架「${label}」`;
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
                          const label = app.draftName || app.name;
                          await api.rejectAdminMarketplaceApp(
                            app.id,
                            app.submissionRevision,
                            note.trim(),
                          );
                          setNote("");
                          return `已驳回「${label}」的提审`;
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
                    variant="danger"
                    busy={busyAction === "suspend"}
                    disabled={!note.trim()}
                    onClick={() =>
                      void runAction("suspend", async () => {
                        const label = app.name || app.draftName;
                        await api.suspendAdminMarketplaceApp(app.id, note.trim());
                        setNote("");
                        return `已封禁并下架「${label}」`;
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
                        const label = app.name || app.draftName;
                        await api.unsuspendAdminMarketplaceApp(app.id);
                        setNote("");
                        return `已解除「${label}」的封禁`;
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
                      <small>{formatDateTime(item.createdAt, "stamp")}</small>
                      {item.note && <p className="field-help">{item.note}</p>}
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </>
        )}
        <div className="modal-actions">
          <ModalCloseButton>关闭</ModalCloseButton>
        </div>
      </div>
    </Modal>
  );
}
