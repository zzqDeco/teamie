import { useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import { Link } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowLeft,
  CheckCheck,
  CheckCircle2,
  Copy,
  KeyRound,
  Play,
  RefreshCw,
  Send,
  Undo2,
} from "lucide-react";
import { api } from "@teamie/api-client";
import type {
  DeveloperMarketplaceApp,
  MarketplaceEndpointVerification,
  Workspace,
} from "@teamie/api-client";
import { useAuth } from "../auth";
import { errorText } from "../errors";
import {
  probeStageLabel,
  reviewActionLabel,
  reviewStatusMeta,
  sanitizeMcpToolSegment,
} from "../marketplace";
import { BadgePill, Button, Notice, SwitchField } from "../ui";
import { ICON } from "../icons";

export function DeveloperAppDetail({
  appId,
  workspaces,
}: {
  appId: string;
  workspaces?: Workspace[];
}) {
  const { user } = useAuth();
  const queryClient = useQueryClient();

  const detailQuery = useQuery({
    queryKey: ["developerMarketplaceApp", appId],
    queryFn: () => api.developerMarketplaceApp(appId),
  });

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
  const [enabled, setEnabled] = useState(true);
  const [submissionNote, setSubmissionNote] = useState("");
  const [hydratedId, setHydratedId] = useState("");

  useEffect(() => {
    if (app && hydratedId !== `${app.id}:${app.updatedAt}`) {
      setName(app.draftName);
      setSlug(app.slug);
      setDescription(app.draftDescription);
      setWebUrl(app.draftWebUrl);
      setMcpUrl(app.draftMcpUrl);
      setEnabled(app.enabled);
      setSubmissionNote(app.submissionNote || "");
      setHydratedId(`${app.id}:${app.updatedAt}`);
    }
  }, [app, hydratedId]);

  const [busyAction, setBusyAction] = useState("");
  const [error, setError] = useState("");
  const [successMsg, setSuccessMsg] = useState("");
  const [liveReport, setLiveReport] = useState<MarketplaceEndpointVerification | null>(
    null,
  );
  const [issuedSecret, setIssuedSecret] = useState<{
    secret: string;
    copied: boolean;
  } | null>(null);
  const [copiedAppId, setCopiedAppId] = useState(false);

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
      setError(errorText(e));
    } finally {
      setBusyAction("");
    }
  }

  async function handleSaveDraft(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    await runAction("save", async () => {
      setLiveReport(null);
      await api.updateDeveloperMarketplaceApp(appId, {
        name: name.trim(),
        slug: slug.trim().toLowerCase(),
        description: description.trim(),
        webUrl: webUrl.trim(),
        mcpUrl: mcpUrl.trim(),
        enabled,
      });
      setSuccessMsg("草稿配置已保存。若修改了线上地址或工具定义，请重新执行协议检测。");
    });
  }

  if (detailQuery.isLoading) {
    return (
      <div className="admin-view">
        <p className="muted">正在加载应用详情…</p>
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
  const status = reviewStatusMeta(app.reviewStatus, app.adminSuspended);
  const report = liveReport || app.verificationReport;
  const hasEndpoints = Boolean(app.draftWebUrl && app.draftMcpUrl);
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
          <h1>{app.draftName || app.name}</h1>
          <p>
            Slug：<code>{app.slug}</code> · Agent 工具前缀：{" "}
            <code>mcp__{sanitizeMcpToolSegment(app.slug)}__*</code>
          </p>
        </div>
      </div>

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
          <b>{status.label}</b>
          <small className="field-help">
            {app.publishedVersion > 0
              ? `当前线上版本 v${app.publishedVersion}`
              : "审批通过后全站可安装"}
          </small>
        </div>
      </div>

      <Notice>{error}</Notice>
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

      <section className="settings-form" aria-label="凭证与签名密钥">
        <div className="mp-section-header">
          <h3>应用凭证（App ID & Signing Secret）</h3>
          <Button
            type="button"
            variant="secondary"
            size="sm"
            disabled={isLocked || !!busyAction}
            busy={busyAction === "rotate"}
            onClick={() =>
              void runAction("rotate", async () => {
                const res = await api.rotateDeveloperMarketplaceAppSecret(app.id);
                setIssuedSecret({ secret: res.signingSecret, copied: false });
              })
            }
          >
            <KeyRound size={ICON.xs} /> 轮换签名密钥
          </Button>
        </div>
        <label>
          App ID（对应 JWT aud 声明）
          <div className="copy-input">
            <input value={app.id} readOnly aria-label="应用 App ID" />
            <button
              type="button"
              aria-label="复制 App ID"
              onClick={() => {
                void navigator.clipboard?.writeText(app.id).then(() => setCopiedAppId(true));
              }}
            >
              {copiedAppId ? <CheckCheck size={ICON.md} /> : <Copy size={ICON.md} />}
            </button>
          </div>
        </label>
        {issuedSecret && (
          <label>
            最新生成的 HMAC-SHA256 签名密钥（仅显示一次）
            <div className="copy-input">
              <input
                value={issuedSecret.secret}
                readOnly
                aria-label="应用签名密钥"
                onFocus={(e) => e.target.select()}
              />
              <button
                type="button"
                aria-label="复制签名密钥"
                onClick={() => {
                  void navigator.clipboard?.writeText(issuedSecret.secret).then(() =>
                    setIssuedSecret({ ...issuedSecret, copied: true }),
                  );
                }}
              >
                {issuedSecret.copied ? (
                  <CheckCheck size={ICON.md} />
                ) : (
                  <Copy size={ICON.md} />
                )}
              </button>
            </div>
          </label>
        )}
      </section>

      {app.publishedVersion > 0 && (
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
      )}

      <form className="settings-form" onSubmit={handleSaveDraft}>
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
            disabled={isLocked}
            required
          />
        </label>
        <label>
          唯一标识（Slug）
          <input
            value={slug}
            onChange={(e) => setSlug(e.target.value)}
            maxLength={64}
            disabled={isLocked || app.isPublished}
            required
          />
          {app.isPublished && (
            <small className="field-help">
              已发布应用的 Slug 保持恒定，确保已接入群聊中的 Agent 工具名稳定。
            </small>
          )}
        </label>
        <label>
          应用简介
          <input
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            maxLength={400}
            disabled={isLocked}
          />
        </label>
        <label>
          外部 Web UI 地址（独立跨源域名）
          <input
            type="url"
            value={webUrl}
            onChange={(e) => setWebUrl(e.target.value)}
            placeholder="https://werewolf.example.com/ui"
            disabled={isLocked}
          />
        </label>
        <label>
          外部 MCP Server 地址（Streamable HTTP）
          <input
            type="url"
            value={mcpUrl}
            onChange={(e) => setMcpUrl(e.target.value)}
            placeholder="https://werewolf.example.com/mcp"
            disabled={isLocked}
          />
        </label>
        <SwitchField
          checked={enabled}
          disabled={isLocked}
          title="应用启用状态"
          description="关闭后，已安装该应用的工作空间将暂停挂载其 Web UI 与 MCP 工具。"
          onChange={setEnabled}
        />
        <Button
          type="submit"
          busy={busyAction === "save"}
          disabled={isLocked || !name.trim() || !slug.trim()}
        >
          保存草稿配置
        </Button>
      </form>

      <section className="settings-form" aria-label="线上服务协议验证">
        <div className="mp-section-header">
          <div>
            <h3>步骤 3A · 线上服务四阶段协议拨测</h3>
            <p className="field-help">
              Teamie 后端将主动拨测你的 Web UI 与 MCP Server，验证跨源隔离、无 Token 401 拒绝、伪造签名拒绝以及真实 HS256 Context Token 下的 tools/list 工具暴露。
            </p>
          </div>
          <Button
            type="button"
            busy={busyAction === "verify"}
            disabled={!hasEndpoints || isLocked || !!busyAction}
            onClick={() =>
              void runAction("verify", async () => {
                const res = await api.verifyDeveloperMarketplaceApp(app.id);
                setLiveReport(res.verification);
                if (res.verification.ok) {
                  setSuccessMsg("四阶段线上协议检测全部通过！现在可以自测或提交平台审核。");
                }
              })
            }
          >
            <Play size={ICON.xs} /> 立即检测线上服务
          </Button>
        </div>

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

      <section className="settings-form" aria-label="自有工作空间联调自测">
        <h3>步骤 3B · 安装到自有工作空间实测</h3>
        <p className="field-help">
          无需等待审核，你可将当前草稿直接安装到自己创建的工作空间，在真实群聊中验证人类 Web UI 面板与 Agent MCP 工具同场交互。
        </p>
        {ownedWorkspaces.length === 0 ? (
          <p className="muted">你尚未创建工作空间，请先在左侧栏创建一个用于联调的工作空间。</p>
        ) : (
          <div className="dev-inline-actions">
            <select
              aria-label="选择联调工作空间"
              value={selectedWsId}
              onChange={(e) => setSelectedWsId(e.target.value)}
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
              disabled={!hasEndpoints || !selectedWsId || !!busyAction}
              busy={busyAction === "sandboxInstall"}
              onClick={() =>
                void runAction("sandboxInstall", async () => {
                  await api.installWorkspaceApp(selectedWsId, app.id);
                  await queryClient.invalidateQueries({
                    queryKey: ["workspaceApps", selectedWsId],
                  });
                  await queryClient.invalidateQueries({ queryKey: ["conversationApps"] });
                  setSuccessMsg("已安装草稿到所选工作空间！进入该空间的任意小组对话点击右上角「群聊应用」即可联调。");
                })
              }
            >
              <RefreshCw size={ICON.xs} /> 安装草稿到该空间自测
            </Button>
          </div>
        )}
      </section>

      <section className="settings-form" aria-label="提交平台审核">
        <h3>步骤 4 · 提交平台审核</h3>
        {app.reviewStatus === "pending_review" ? (
          <>
            <p className="field-help">
              你的应用已提交至平台管理员审核队列。若需继续调整代码或地址，可随时撤回提审。
            </p>
            <div className="dev-inline-actions">
              <Button
                type="button"
                variant="secondary"
                busy={busyAction === "withdraw"}
                onClick={() =>
                  void runAction("withdraw", async () => {
                    await api.withdrawDeveloperMarketplaceApp(app.id);
                    setSuccessMsg("已撤回提审，草稿已恢复可编辑状态。");
                  })
                }
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
                onChange={(e) => setSubmissionNote(e.target.value)}
                placeholder="例如：支持 6-12 人局暗夜狼人杀（人类 + Agent 混排），已开启角色视野隔离与动作幂等锁。"
                maxLength={500}
                disabled={isSuspended}
              />
            </label>
            <Button
              type="button"
              busy={busyAction === "submit"}
              disabled={!verifiedOk || isSuspended || !!busyAction}
              onClick={() =>
                void runAction("submit", async () => {
                  await api.submitDeveloperMarketplaceApp(
                    app.id,
                    submissionNote.trim() || undefined,
                  );
                  setSuccessMsg("已成功提交审核！平台管理员审批通过后将自动上架。");
                })
              }
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
                  <small>{new Date(item.createdAt).toLocaleString("zh-CN")}</small>
                  {item.note && <p className="field-help">{item.note}</p>}
                </li>
              ))}
            </ul>
          </div>
        )}
      </section>
    </div>
  );
}
