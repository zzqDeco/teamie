import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Ban,
  CheckCheck,
  ChevronRight,
  Copy,
  KeyRound,
  LoaderCircle,
  Plus,
  RefreshCw,
  ShieldCheck,
  UserPlus,
} from "lucide-react";
import { api, ApiError } from "@teamie/api-client";
import type {
  CreatedRegistrationInvite,
  RegistrationInvite,
} from "@teamie/api-client";
import { useAuth } from "./auth";
import { errorText } from "./chat";
import { Button, IconButton, Modal, Notice } from "./ui";
import { toast } from "./toast";
import { ICON } from "./icons";
export type InviteStatus = "available" | "used" | "revoked" | "expired";
export function registrationInviteStatus(
  invite: RegistrationInvite,
  now: number,
): InviteStatus {
  if (invite.usedAt) return "used";
  if (invite.revokedAt) return "revoked";
  return new Date(invite.expiresAt).getTime() <= now ? "expired" : "available";
}
const statusLabels: Record<InviteStatus, string> = {
  available: "可使用",
  used: "已使用",
  revoked: "已撤销",
  expired: "已过期",
};
const formatDate = (value: string) =>
  new Date(value).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
export function PlatformInvitations() {
  const { user } = useAuth();
  const client = useQueryClient();
  const admin = user?.platformRole === "admin";
  const [permissionDenied, setPermissionDenied] = useState(false);
  const key = ["registrationInvites", user?.id];
  const query = useQuery({
    queryKey: key,
    queryFn: api.registrationInvites,
    enabled: admin && !permissionDenied,
    refetchInterval: 30_000,
    retry: (count, error) =>
      !(error instanceof ApiError && [401, 403].includes(error.status)) &&
      count < 1,
  });
  const [created, setCreated] = useState<CreatedRegistrationInvite | null>(
    null,
  );
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [copied, setCopied] = useState("");
  const [revoking, setRevoking] = useState<RegistrationInvite | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const denied =
    permissionDenied ||
    (query.error instanceof ApiError && query.error.status === 403);
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(timer);
  }, []);
  useEffect(() => {
    if (!admin || denied) {
      setCreated(null);
      setRevoking(null);
    }
  }, [admin, denied]);
  const closeRevoke = useCallback(() => setRevoking(null), []);
  async function generate() {
    setBusy(true);
    setError("");
    setCopied("");
    const sessionToken = api.csrfToken;
    try {
      const result = await api.createRegistrationInvite();
      if (sessionToken !== api.csrfToken) return;
      setCreated(result);
      await client.invalidateQueries({ queryKey: key });
    } catch (e) {
      if (sessionToken === api.csrfToken) {
        if (e instanceof ApiError && e.status === 403)
          setPermissionDenied(true);
        setError(errorText(e));
      }
    } finally {
      setBusy(false);
    }
  }
  async function revoke() {
    if (!revoking) return;
    setBusy(true);
    setError("");
    const sessionToken = api.csrfToken;
    try {
      await api.revokeRegistrationInvite(revoking.id);
      if (sessionToken !== api.csrfToken) return;
      if (created?.invite.id === revoking.id) setCreated(null);
      setRevoking(null);
      toast.success(`已撤销邀请码 #${revoking.id.slice(-8)}`);
      await client.invalidateQueries({ queryKey: key });
    } catch (e) {
      if (sessionToken === api.csrfToken) {
        if (e instanceof ApiError && e.status === 403)
          setPermissionDenied(true);
        setError(errorText(e));
      }
    } finally {
      setBusy(false);
    }
  }
  async function copy(value: string, target: string) {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(target);
      toast.success(target === "code" ? "邀请码已复制" : "邀请链接已复制");
    } catch {
      setError("复制未成功，请选中内容后手动复制。");
    }
  }
  if (!admin || denied)
    return (
      <div className="unavailable-view">
        <span className="large-symbol">
          <ShieldCheck size={ICON.xl} />
        </span>
        <h2>需要平台管理员权限</h2>
        <p>注册邀请由平台管理员管理。请联系管理员获取邀请码。</p>
        <Link to="/app" className="button">
          回到我的工作空间
        </Link>
      </div>
    );
  const invites = query.data?.invites || [];
  const link = created ? new URL(created.url, location.origin).href : "";
  return (
    <div className="admin-view">
      <div className="admin-heading">
        <div>
          <h1>
            注册邀请{" "}
            <span className="platform-role-badge">
              <ShieldCheck size={ICON.xs} />
              平台管理员
            </span>
          </h1>
          <p>一次性邀请码，7 天内有效。</p>
        </div>
        <span className="admin-heading-icon">
          <KeyRound size={ICON.xl} />
        </span>
      </div>
      <Notice>{!revoking ? error : ""}</Notice>
      <div className="admin-generate">
        <div>
          <h2>创建注册邀请</h2>
          <p>邀请码仅显示一次。</p>
        </div>
        <Button
          onClick={generate}
          busy={busy}
          disabled={!!created || query.isLoading || denied}
        >
          <Plus size={ICON.sm} />
          生成邀请码
        </Button>
      </div>
      {created && (
        <section className="created-invite" aria-label="新生成的一次性注册邀请">
          <div className="created-invite-heading">
            <span>
              <CheckCheck size={ICON.md} />
              邀请码已生成
            </span>
            <small>仅显示一次 · 请及时保存</small>
          </div>
          <p>离开此页面后无法查看原始邀请码，请先复制保存。</p>
          <label>
            一次性注册邀请码
            <div className="copy-input">
              <input
                value={created.invite.code}
                readOnly
                onFocus={(e) => e.target.select()}
                aria-label="一次性注册邀请码"
              />
              <button
                onClick={() => void copy(created.invite.code, "code")}
                title="复制注册邀请码"
                aria-label="复制注册邀请码"
              >
                {copied === "code" ? (
                  <CheckCheck size={ICON.md} />
                ) : (
                  <Copy size={ICON.md} />
                )}
              </button>
            </div>
          </label>
          <label>
            注册邀请链接
            <div className="copy-input">
              <input
                value={link}
                readOnly
                onFocus={(e) => e.target.select()}
                aria-label="注册邀请链接"
              />
              <button
                onClick={() => void copy(link, "link")}
                title="复制注册邀请链接"
                aria-label="复制注册邀请链接"
              >
                {copied === "link" ? (
                  <CheckCheck size={ICON.md} />
                ) : (
                  <Copy size={ICON.md} />
                )}
              </button>
            </div>
          </label>
          <div className="created-invite-footer">
            <small>
              有效期至{" "}
              {new Date(created.invite.expiresAt).toLocaleString("zh-CN")}
            </small>
            <button
              onClick={() => {
                setCreated(null);
                setCopied("");
              }}
            >
              已保存，关闭
              <ChevronRight size={ICON.xs} />
            </button>
          </div>
          {copied && (
            <span className="copy-confirmation">
              {copied === "code" ? "邀请码" : "邀请链接"}已复制。
            </span>
          )}
        </section>
      )}
      <section className="admin-invite-history">
        <header>
          <div>
            <h2>邀请码记录</h2>
            <span>{invites.length} 个邀请 · 原始邀请码不再展示</span>
          </div>
          <IconButton
            label="刷新邀请码记录"
            onClick={() => {
              setNow(Date.now());
              void query.refetch();
            }}
          >
            <RefreshCw size={ICON.sm} className={query.isFetching ? "spin" : ""} />
          </IconButton>
        </header>
        {query.isLoading ? (
          <div className="admin-empty">
            <LoaderCircle className="spin" size={ICON.lg} />
            <p>正在加载邀请码记录…</p>
          </div>
        ) : query.error ? (
          <div className="admin-empty">
            <Notice>邀请码记录加载失败，请重新刷新。</Notice>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => void query.refetch()}
            >
              重新加载
            </Button>
          </div>
        ) : invites.length === 0 ? (
          <div className="admin-empty">
            <span>
              <UserPlus size={ICON.xl} />
            </span>
            <h3>暂无邀请码</h3>
            <p>点击上方按钮创建注册邀请。</p>
          </div>
        ) : (
          <div className="admin-table-wrap">
            <table className="admin-invites-table">
              <thead>
                <tr>
                  <th>创建时间 / 编号</th>
                  <th>有效期至</th>
                  <th>状态</th>
                  <th>
                    <span className="sr-only">操作</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {[...invites]
                  .sort(
                    (a, b) =>
                      Date.parse(b.createdAt) - Date.parse(a.createdAt) ||
                      b.id.localeCompare(a.id),
                  )
                  .map((invite) => {
                    const status = registrationInviteStatus(invite, now);
                    return (
                      <tr key={invite.id}>
                        <td data-label="创建时间">
                          <span>{formatDate(invite.createdAt)}</span>
                          <small title={invite.id}>
                            #{invite.id.slice(-8)}
                          </small>
                        </td>
                        <td data-label="有效期至">
                          <span>{formatDate(invite.expiresAt)}</span>
                          {invite.usedAt && (
                            <small>使用于 {formatDate(invite.usedAt)}</small>
                          )}
                        </td>
                        <td>
                          <span className={`invite-status ${status}`}>
                            <i />
                            {statusLabels[status]}
                          </span>
                        </td>
                        <td>
                          {status === "available" && (
                            <button
                              className="revoke-invite"
                              disabled={busy}
                              onClick={() => {
                                setRevoking(invite);
                                setError("");
                              }}
                              aria-label={`撤销邀请码 ${invite.id.slice(-8)}`}
                            >
                              <Ban size={ICON.xs} />
                              撤销
                            </button>
                          )}
                        </td>
                      </tr>
                    );
                  })}
              </tbody>
            </table>
          </div>
        )}
      </section>
      <p className="admin-footnote">
        <KeyRound size={ICON.xs} />
        已使用、撤销或过期的邀请码均不能再次注册账户。
      </p>
      {revoking && (
        <Modal
          title="撤销这个注册邀请？"
          subtitle="撤销后，这个邀请码与对应注册链接将立即失效。已注册的账户不会受影响。"
          onClose={closeRevoke}
          dismissible={!busy}
        >
          <div className="revoke-details">
            <span>邀请码编号</span>
            <b>#{revoking.id.slice(-8)}</b>
            <small>创建于 {formatDate(revoking.createdAt)}</small>
          </div>
          <Notice>{error}</Notice>
          <div className="modal-actions">
            <Button
              variant="secondary"
              onClick={closeRevoke}
              disabled={busy}
              data-autofocus
            >
              保留邀请
            </Button>
            <Button variant="danger" busy={busy} onClick={revoke}>
              确认撤销
            </Button>
          </div>
        </Modal>
      )}
    </div>
  );
}
