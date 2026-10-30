import { useState } from "react";
import type { FormEvent } from "react";
import { Link } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowRight,
  BookOpen,
  CheckCheck,
  Code2,
  Copy,
  Plus,
  Trash2,
} from "lucide-react";
import { api } from "@teamie/api-client";
import type { DeveloperMarketplaceApp } from "@teamie/api-client";
import { useAuth } from "../auth";
import { errorText } from "../errors";
import { reviewStatusMeta } from "../marketplace";
import { BadgePill, Button, Notice } from "../ui";
import { ICON } from "../icons";

export function DeveloperAppsList() {
  const { user } = useAuth();
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [description, setDescription] = useState("");
  const [webUrl, setWebUrl] = useState("");
  const [mcpUrl, setMcpUrl] = useState("");
  const [busy, setBusy] = useState(false);
  const [deletingId, setDeletingId] = useState("");
  const [error, setError] = useState("");
  const [createdResult, setCreatedResult] = useState<{
    app: DeveloperMarketplaceApp;
    secret: string;
    copied: boolean;
  } | null>(null);

  const appsQuery = useQuery({
    queryKey: ["developerMarketplaceApps", user!.id],
    queryFn: () => api.developerMarketplaceApps(),
  });

  async function handleCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      const created = await api.createDeveloperMarketplaceApp({
        name: name.trim(),
        slug: slug.trim().toLowerCase(),
        description: description.trim(),
        webUrl: webUrl.trim() || undefined,
        mcpUrl: mcpUrl.trim() || undefined,
      });
      setName("");
      setSlug("");
      setDescription("");
      setWebUrl("");
      setMcpUrl("");
      setCreatedResult({
        app: created.app,
        secret: created.signingSecret,
        copied: false,
      });
      await queryClient.invalidateQueries({ queryKey: ["developerMarketplaceApps"] });
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  async function handleDelete(app: DeveloperMarketplaceApp) {
    if (deletingId) return;
    setDeletingId(app.id);
    setError("");
    try {
      await api.deleteDeveloperMarketplaceApp(app.id);
      await queryClient.invalidateQueries({ queryKey: ["developerMarketplaceApps"] });
    } catch (e) {
      setError(errorText(e));
    } finally {
      setDeletingId("");
    }
  }

  async function copySecret() {
    if (!createdResult) return;
    try {
      await navigator.clipboard.writeText(createdResult.secret);
      setCreatedResult({ ...createdResult, copied: true });
    } catch {
      setError("复制未成功，请手动选中密钥复制。");
    }
  }

  const apps = appsQuery.data?.apps || [];

  return (
    <div className="admin-view">
      <div className="admin-heading">
        <div>
          <h1>
            开发者平台
            <span className="platform-role-badge">
              <Code2 size={ICON.xs} />
              开放生态
            </span>
          </h1>
          <p>
            使用你的 Teamie 账号接入自部署的外部群聊应用（Web UI + Streamable HTTP MCP Server），支持人类成员与 Agent 在群聊中同场协作或同台博弈。
          </p>
        </div>
        <span className="admin-heading-icon">
          <Code2 size={ICON.xl} />
        </span>
      </div>

      <div className="dev-steps" aria-label="接入与提审四步流程">
        <div className="dev-step-card">
          <span className="dev-step-num">步骤 1 · 注册草稿</span>
          <b>获取 App ID 与密钥</b>
          <small className="field-help">
            先创建应用草稿（服务端地址可稍后填写），领取唯一 App ID 与 HS256 签名密钥。
          </small>
        </div>
        <div className="dev-step-card">
          <span className="dev-step-num">步骤 2 · 独立部署</span>
          <b>启动 Web UI 与 MCP</b>
          <small className="field-help">
            将签名密钥写入你的外部服务环境变量，配置跨源 HTTPS、iframe 嵌入与 Bearer 鉴权。
          </small>
        </div>
        <div className="dev-step-card">
          <span className="dev-step-num">步骤 3 · 拨测与自测</span>
          <b>协议验证 + 群聊实测</b>
          <small className="field-help">
            通过四阶段线上协议检测后，一键安装至你创建的工作空间与真实 Agent 联调。
          </small>
        </div>
        <div className="dev-step-card">
          <span className="dev-step-num">步骤 4 · 提交审核</span>
          <b>管理员审批上架</b>
          <small className="field-help">
            提交审核说明，通过后应用进入全站应用市场目录；后续升级草稿不影响线上版本。
          </small>
        </div>
      </div>

      <Notice>{error}</Notice>

      {createdResult && (
        <section className="settings-form" aria-label="新建应用签名密钥">
          <h3>
            已创建「{createdResult.app.draftName || createdResult.app.name}」· 请保存签名密钥
          </h3>
          <p className="field-help">
            App ID：<code>{createdResult.app.id}</code>。下方 HMAC-SHA256 密钥仅在创建或轮换时明文展示一次，请配置为外部服务的{" "}
            <code>TEAMIE_APP_SIGNING_SECRET</code>。
          </p>
          <div className="copy-input">
            <input
              value={createdResult.secret}
              readOnly
              aria-label="应用签名密钥"
              onFocus={(e) => e.target.select()}
            />
            <button type="button" onClick={() => void copySecret()} aria-label="复制签名密钥">
              {createdResult.copied ? <CheckCheck size={ICON.md} /> : <Copy size={ICON.md} />}
            </button>
          </div>
          <div className="dev-inline-actions">
            <Link
              to={`/app/developers/apps/${createdResult.app.id}`}
              className="button small-button"
            >
              进入应用配置与协议检测 <ArrowRight size={ICON.xs} />
            </Link>
            <Link to="/app/developers/docs" className="button secondary small-button">
              <BookOpen size={ICON.xs} /> 查看接入协议手册
            </Link>
          </div>
        </section>
      )}

      <form className="settings-form" onSubmit={handleCreate}>
        <div className="mp-section-header">
          <h3>创建新应用草稿</h3>
          <Link to="/app/developers/docs" className="button secondary small-button">
            <BookOpen size={ICON.xs} /> 阅读开发者手册
          </Link>
        </div>
        <label>
          应用名称
          <input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="例如：暗夜狼人杀法官台"
            maxLength={80}
            required
          />
        </label>
        <label>
          唯一标识（Slug）
          <input
            value={slug}
            onChange={(e) => setSlug(e.target.value)}
            placeholder="例如：werewolf-arena"
            maxLength={64}
            required
          />
          <small className="field-help">
            仅限小写字母、数字与连字符（如 werewolf-arena），群聊内 Agent 工具将自动命名为 mcp__slug__tool。
          </small>
        </label>
        <label>
          应用简介
          <input
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="说明应用玩法、适用场景及人类与 Agent 如何协同使用"
            maxLength={400}
          />
        </label>
        <label>
          Web UI 地址（可选，部署完成后可在详情页补充）
          <input
            type="url"
            value={webUrl}
            onChange={(e) => setWebUrl(e.target.value)}
            placeholder="https://werewolf.example.com/ui"
          />
        </label>
        <label>
          MCP Server 地址（可选，Streamable HTTP 端点）
          <input
            type="url"
            value={mcpUrl}
            onChange={(e) => setMcpUrl(e.target.value)}
            placeholder="https://werewolf.example.com/mcp"
          />
        </label>
        <Button type="submit" busy={busy} disabled={!name.trim() || !slug.trim()}>
          <Plus size={ICON.sm} /> 创建草稿并获取密钥
        </Button>
      </form>

      <section className="settings-form" aria-label="我的开发者应用">
        <h3>我的应用（{apps.length}）</h3>
        {appsQuery.isLoading ? (
          <p className="muted">正在加载你的应用列表…</p>
        ) : apps.length === 0 ? (
          <p className="muted">你还没有创建任何应用草稿，可先从上方创建并阅读开发者手册。</p>
        ) : (
          <ul className="member-directory-list" aria-label="开发者应用列表">
            {apps.map((app) => {
              const status = reviewStatusMeta(app.reviewStatus, app.adminSuspended);
              const displayName = app.draftName || app.name;
              const displayDesc = app.draftDescription || app.description;
              const displayWeb = app.draftWebUrl || app.webUrl;
              const displayMcp = app.draftMcpUrl || app.mcpUrl;
              return (
                <li key={app.id} className="member-directory-row">
                  <div className="member-directory-identity">
                    <div>
                      <b>
                        {displayName} <BadgePill>{app.slug}</BadgePill>{" "}
                        <BadgePill className={status.className}>{status.label}</BadgePill>{" "}
                        {app.publishedVersion > 0 && (
                          <BadgePill className="mp-badge-approved">
                            线上 v{app.publishedVersion}
                          </BadgePill>
                        )}
                      </b>
                      {displayDesc && <p className="field-help">{displayDesc}</p>}
                      <small className="field-help">
                        App ID: {app.id} · Web: {displayWeb || "待配置"} · MCP:{" "}
                        {displayMcp || "待配置"}
                      </small>
                    </div>
                  </div>
                  <div className="member-directory-actions">
                    <Link
                      to={`/app/developers/apps/${app.id}`}
                      className="button secondary small-button"
                    >
                      管理与提审 <ArrowRight size={ICON.xs} />
                    </Link>
                    {app.publishedVersion === 0 && app.reviewStatus !== "pending_review" && (
                      <Button
                        type="button"
                        className="secondary danger-text"
                        size="sm"
                        busy={deletingId === app.id}
                        disabled={!!deletingId}
                        onClick={() => void handleDelete(app)}
                        aria-label={`删除草稿 ${displayName}`}
                      >
                        <Trash2 size={ICON.sm} /> 删除草稿
                      </Button>
                    )}
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </section>
    </div>
  );
}
