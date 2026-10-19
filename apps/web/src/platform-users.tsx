import { useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { Link } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { CheckCheck, Copy, KeyRound, LoaderCircle, Search, ShieldCheck, Users } from "lucide-react";
import { api, ApiError } from "@teamie/api-client";
import type { CreatedPasswordResetLink, User } from "@teamie/api-client";
import { useAuth } from "./auth";
import { Avatar, Button, Modal, Notice } from "./ui";
import { ICON } from "./icons";

type SessionScope = ReturnType<ReturnType<typeof useAuth>["captureSession"]>;
type ResetDialog = {
  account: User;
  scope: SessionScope;
  busy: boolean;
  created: CreatedPasswordResetLink | null;
  copied: boolean;
  error: string;
};

function AdminRequired() {
  return (
    <div className="unavailable-view">
      <span className="large-symbol"><ShieldCheck size={ICON.xl} /></span>
      <h2>需要平台管理员权限</h2>
      <p>用户与密码重置链接由平台管理员管理。</p>
      <Link to="/app" className="button">回到我的工作空间</Link>
    </div>
  );
}

export function PlatformUsers() {
  const { user } = useAuth();
  if (user?.platformRole !== "admin") return <AdminRequired />;
  // Remount all local state when the subject or its platform role changes.
  return <AdminUsers key={`${user.id}:${user.platformRole}`} user={user} />;
}

function AdminUsers({ user }: { user: User }) {
  const { captureSession } = useAuth();
  const [draft, setDraft] = useState("");
  const [q, setQ] = useState("");
  const [pages, setPages] = useState<string[]>([]);
  const [permissionDenied, setPermissionDenied] = useState(false);
  const [dialog, setDialog] = useState<ResetDialog | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const mounted = useRef(false);
  const requestSequence = useRef(0);
  const cursor = pages.at(-1) || "";
  const query = useQuery({
    queryKey: ["platformUsers", user.id, user.platformRole, q, cursor],
    queryFn: () => api.platformUsers({ limit: 20, q, cursor }),
    enabled: !permissionDenied,
    retry: (count, error) => !(error instanceof ApiError && [401, 403].includes(error.status)) && count < 1,
    staleTime: 0,
  });
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      requestSequence.current += 1;
    };
  }, []);
  useEffect(() => {
    if (!dialog?.created) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1_000);
    return () => window.clearInterval(timer);
  }, [dialog?.created]);
  const close = useCallback(() => {
    requestSequence.current += 1;
    setDialog(null);
  }, []);

  async function generate() {
    if (!dialog || dialog.busy || !dialog.scope.isCurrent()) return;
    const sequence = ++requestSequence.current;
    const scope = captureSession();
    const current = () => mounted.current && scope.isCurrent() && sequence === requestSequence.current;
    setDialog({ ...dialog, busy: true, created: null, copied: false, error: "" });
    try {
      const created = await api.createPasswordResetLink(dialog.account.id);
      if (!current()) return;
      const parsed = new URL(created.url, location.origin);
      if (parsed.origin !== location.origin || parsed.pathname !== "/reset-password" || parsed.search || !new URLSearchParams(parsed.hash.slice(1)).get("token"))
        throw new Error("invalid response");
      setNow(Date.now());
      setDialog((value) => value ? { ...value, busy: false, created } : value);
    } catch (error) {
      if (!current()) return;
      if (error instanceof ApiError && error.status === 403 && error.code === "forbidden") {
        setPermissionDenied(true);
        setDialog(null);
        return;
      }
      setDialog((value) => value ? {
        ...value, busy: false,
        ...(error instanceof ApiError && error.code === "feature_disabled"
          ? { created: dialog.created, copied: dialog.copied }
          : {}),
        error: error instanceof ApiError && error.code === "feature_disabled"
          ? "密码重置暂时关闭，请联系服务器维护者。"
          : error instanceof ApiError && error.status === 429
            ? "操作过于频繁，请稍后重试。"
            : "生成失败，请重试。此前的链接可能已失效。",
      } : value);
    }
  }
  async function copy(link: string) {
    if (!dialog?.scope.isCurrent()) return;
    const sequence = requestSequence.current;
    const scope = captureSession();
    const current = () => mounted.current && scope.isCurrent() && sequence === requestSequence.current;
    try {
      await navigator.clipboard.writeText(link);
      if (current()) setDialog((value) => value ? { ...value, copied: true, error: "" } : value);
    } catch {
      if (current()) setDialog((value) => value ? { ...value, error: "复制未成功，请选中链接后手动复制。" } : value);
    }
  }
  function search(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setQ(draft.trim());
    setPages([]);
  }

  const denied = permissionDenied || (query.error instanceof ApiError && query.error.status === 403 && query.error.code === "forbidden");
  if (denied) return <AdminRequired />;
  const users = query.data?.users || [];
  const currentDialog = dialog?.scope.isCurrent() ? dialog : null;
  const expired = !!currentDialog?.created && Date.parse(currentDialog.created.reset.expiresAt) <= now;
  const link = currentDialog?.created && !expired ? new URL(currentDialog.created.url, location.origin).href : "";
  return (
    <div className="admin-view platform-users-view">
      <div className="admin-heading">
        <div>
          <h1>用户管理<span className="platform-role-badge"><ShieldCheck size={ICON.xs} />平台管理员</span></h1>
          <p>为已核实身份的用户提供一次性密码重置链接。</p>
        </div>
        <span className="admin-heading-icon"><Users size={ICON.xl} /></span>
      </div>
      <form className="platform-user-search" onSubmit={search} role="search">
        <label><Search size={ICON.md} aria-hidden="true" /><span className="sr-only">搜索用户名称或邮箱</span><input type="search" value={draft} onChange={(event) => setDraft(event.target.value)} placeholder="搜索名称或邮箱" maxLength={200} /></label>
        <Button type="submit" className="secondary">搜索</Button>
      </form>
      <section className="platform-user-list" aria-label="平台用户">
        {query.isPending ? (
          <div className="admin-empty" role="status"><LoaderCircle className="spin" size={ICON.lg} /><p>正在加载用户…</p></div>
        ) : query.isError ? (
          <div className="admin-empty"><Notice>用户加载失败，请重新加载。</Notice><Button className="secondary" onClick={() => void query.refetch()}>重新加载</Button></div>
        ) : users.length === 0 ? (
          <div className="admin-empty"><Users size={ICON.xl} /><h3>{q ? "没有匹配的用户" : "暂无用户"}</h3><p>{q ? "试试其他名称或邮箱。" : "已注册的用户会显示在这里。"}</p></div>
        ) : (
          <ul>{users.map((account) => (
            <li key={account.id} className="platform-user-row">
              <div className="platform-user-identity"><Avatar name={account.displayName} seed={account.id} /><div><b>{account.displayName}{account.id === user.id && <small>你</small>}</b><span>{account.email}</span></div></div>
              <div className="platform-user-actions"><span className="platform-user-role">{account.platformRole === "admin" ? "管理员" : "用户"}</span><Button className="secondary" aria-label={`为 ${account.displayName} 重置密码`} onClick={() => {
                requestSequence.current += 1;
                setDialog({ account, scope: captureSession(), busy: false, created: null, copied: false, error: "" });
              }}><KeyRound size={ICON.xs} />重置密码</Button></div>
            </li>
          ))}</ul>
        )}
      </section>
      {(pages.length > 0 || query.data?.cursor) && <nav className="platform-users-pagination" aria-label="用户列表分页"><Button className="secondary" disabled={!pages.length || query.isFetching} onClick={() => setPages((value) => value.slice(0, -1))}>上一页</Button><span>第 {pages.length + 1} 页</span><Button className="secondary" disabled={!query.data?.cursor || query.isFetching} onClick={() => { if (query.data?.cursor) setPages((value) => [...value, query.data!.cursor!]); }}>下一页</Button></nav>}
      <p className="admin-footnote"><KeyRound size={ICON.xs} />链接 1 小时内有效。生成新链接会使该用户此前未使用的链接失效。</p>
      {currentDialog && (
        <Modal title={currentDialog.created ? expired ? "重置链接已过期" : "重置链接已生成" : "生成密码重置链接"} subtitle="请先核实用户身份，再将链接私下交给本人。" onClose={close}>
          <div className="reset-target"><Avatar name={currentDialog.account.displayName} seed={currentDialog.account.id} /><div><b>{currentDialog.account.displayName}</b><span>{currentDialog.account.email}</span></div></div>
          <Notice>{currentDialog.error}</Notice>
          {currentDialog.created ? expired ? (
            <Notice kind="info">链接已过期，请重新生成后交给用户。</Notice>
          ) : (
            <div className="created-reset-link">
              <label>一次性重置链接<div className="copy-input"><input aria-label="一次性重置链接" value={link} readOnly onFocus={(event) => event.target.select()} /><button aria-label="复制重置链接" onClick={() => void copy(link)}>{currentDialog.copied ? <CheckCheck size={ICON.md} /> : <Copy size={ICON.md} />}</button></div></label>
              <small>有效期至 {new Date(currentDialog.created.reset.expiresAt).toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false })} · 仅显示这一次</small>
              {currentDialog.copied && <Notice kind="success">链接已复制。</Notice>}
            </div>
          ) : <p className="reset-dialog-help">链接有效期为 1 小时，仅可使用一次。生成后，用户此前未使用的重置链接立即失效。</p>}
          <p className="reset-dialog-help">用户完成重置后，原有登录会话将失效。密码不会展示给管理员。</p>
          <div className="modal-actions">
            <Button className="secondary" onClick={close}>{currentDialog.created ? "关闭" : "取消"}</Button>
            <Button busy={currentDialog.busy} onClick={() => void generate()}>{currentDialog.created ? "重新生成链接" : "生成重置链接"}</Button>
          </div>
        </Modal>
      )}
    </div>
  );
}
