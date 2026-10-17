import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import type { FormEvent, ReactNode } from "react";
import { Link, useLocation } from "react-router-dom";
import { ArrowLeft, ArrowRight, Check, KeyRound, LoaderCircle } from "lucide-react";
import { api, ApiError } from "@teamie/api-client";
import type { PasswordResetInspection } from "@teamie/api-client";
import { useAuth } from "./auth";
import { passwordPolicyError } from "./password-policy";
import { Button, Logo, Notice } from "./ui";

function RecoveryFrame({ children }: { children: ReactNode }) {
  return (
    <div className="auth-page recovery-page">
      <div className="auth-story">
        <Link to="/" aria-label="Teamie 首页"><Logo /></Link>
        <div>
          <h1>回到团队，<br />接着聊<span>。</span></h1>
          <p>找回你的账户，继续一起做成事。</p>
        </div>
        <span className="story-bottom">TEAMIE © {new Date().getFullYear()}</span>
      </div>
      <main className="auth-content">
        <Link to="/login" className="back-link"><ArrowLeft size={16} />返回登录</Link>
        <div className="auth-form recovery-form">
          <Link to="/" className="auth-mobile-brand" aria-label="Teamie 首页"><Logo /></Link>
          {children}
        </div>
      </main>
    </div>
  );
}

export function ForgotPassword() {
  return (
    <RecoveryFrame>
      <span className="recovery-symbol"><KeyRound size={23} /></span>
      <h2>忘记密码</h2>
      <p>请联系平台管理员，核实身份后获取一次性密码重置链接。</p>
      <div className="recovery-instructions">
        <h3>如何找回账户</h3>
        <ol>
          <li>将你的账号邮箱告知平台管理员。</li>
          <li>打开管理员提供的重置链接，设置新密码。</li>
          <li>使用新密码重新登录 Teamie。</li>
        </ol>
        <small>链接 1 小时内有效，仅可使用一次。管理员也无法登录时，请联系服务器维护者。</small>
      </div>
      <Link to="/login" className="button full-width">返回登录<ArrowRight size={17} /></Link>
    </RecoveryFrame>
  );
}

function fragmentToken(fragment: string) {
  // Pure read: StrictMode can call this initializer twice before Effects run.
  const params = new URLSearchParams(fragment.slice(1));
  const values = params.getAll("token");
  return values.length === 1 ? values[0] : "";
}
type ResetState = "checking" | "valid" | "invalid" | "expired" | "error" | "success";
type PasswordField = "newPassword" | "confirmPassword";

export function ResetPassword() {
  const location = useLocation();
  const [entry, setEntry] = useState({ location, fragment: location.hash, revision: 0 });
  if (entry.location !== location)
    setEntry({ location, fragment: location.hash, revision: entry.revision + 1 });
  useEffect(() => {
    // Browser hash-only navigation can emit hashchange without a router
    // popstate. Read newURL because a layout Effect may already have removed
    // the live fragment by the time this event is delivered.
    const onHashChange = (event: HashChangeEvent) => {
      const fragment = new URL(event.newURL).hash;
      setEntry((current) => ({ ...current, fragment, revision: current.revision + 1 }));
    };
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);
  // Native fragment navigations may reuse a history key, including reopening
  // the same already-consumed link. Every new location gets a fresh form.
  return <ResetPasswordForm key={entry.revision} fragment={entry.fragment} />;
}

function ResetPasswordForm({ fragment }: { fragment: string }) {
  const { user, loading, resetPassword } = useAuth();
  const [token] = useState(() => fragmentToken(fragment));
  const [state, setState] = useState<ResetState>(token ? "checking" : "invalid");
  const [inspection, setInspection] = useState<PasswordResetInspection | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [invalidField, setInvalidField] = useState<PasswordField>();
  const formRef = useRef<HTMLFormElement>(null);
  const pending = useRef(false);
  const mounted = useRef(false);
  const errorId = useId();

  useLayoutEffect(() => {
    // Keep the capability in this component's memory only. Do not put it in
    // navigation state, query keys, storage, the page query string or referrers.
    if (window.location.hash)
      window.history.replaceState(window.history.state, "", window.location.pathname + window.location.search);
  }, []);
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; };
  }, []);
  useEffect(() => {
    if (!token) return;
    let active = true;
    setState("checking");
    api.inspectPasswordReset(token).then((result) => {
      if (!active) return;
      setInspection(result);
      setState(Date.parse(result.expiresAt) <= Date.now() ? "expired" : "valid");
    }).catch((error: unknown) => {
      if (!active) return;
      setInspection(null);
      setState(error instanceof ApiError && error.code === "invalid_reset_link" ? "invalid" : "error");
    });
    return () => { active = false; };
  }, [token, attempt]);
  useEffect(() => {
    if (state !== "valid" || !inspection) return;
    const remaining = Date.parse(inspection.expiresAt) - Date.now();
    const timer = window.setTimeout(() => setState("expired"), Math.max(0, Math.min(remaining, 2_147_483_647)));
    return () => window.clearTimeout(timer);
  }, [inspection, state]);
  useEffect(() => {
    if (!busy && invalidField) {
      const input = formRef.current?.elements.namedItem(invalidField);
      if (input instanceof HTMLInputElement) input.focus();
    }
  }, [busy, error, invalidField]);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending.current || loading || state !== "valid" || !inspection) return;
    if (Date.parse(inspection.expiresAt) <= Date.now()) {
      setState("expired");
      return;
    }
    const form = event.currentTarget;
    const data = new FormData(form);
    const newPassword = String(data.get("newPassword") || "");
    const confirmation = String(data.get("confirmPassword") || "");
    const policyError = passwordPolicyError(newPassword);
    if (policyError || newPassword !== confirmation) {
      setError(policyError || "两次输入的新密码不一致。");
      const field = policyError ? "newPassword" : "confirmPassword";
      setInvalidField(field);
      const input = form.elements.namedItem(field);
      if (input instanceof HTMLInputElement) input.focus();
      return;
    }
    pending.current = true;
    setBusy(true);
    setError("");
    setInvalidField(undefined);
    try {
      await resetPassword(token, newPassword, inspection.account.id);
      if (!mounted.current) return;
      form.reset();
      setInspection(null);
      setState("success");
    } catch (error) {
      if (!mounted.current) return;
      if (error instanceof ApiError && error.code === "invalid_reset_link") {
        form.reset();
        setInspection(null);
        setState("invalid");
      } else if (error instanceof ApiError && ["invalid_password", "password_unchanged"].includes(error.code)) {
        setError(error.code === "password_unchanged" ? "新密码不能与原密码相同，请换一个密码。" : "密码需至少 12 个字符，且不超过 72 字节。");
        setInvalidField("newPassword");
      } else {
        setError(error instanceof ApiError && error.status === 429 ? "操作过于频繁，请稍后再试。" : "重置失败，请检查网络后重试。");
      }
    } finally {
      pending.current = false;
      if (mounted.current) setBusy(false);
    }
  }

  return (
    <RecoveryFrame>
      <span className={`recovery-symbol ${state === "success" ? "success" : ""}`}>
        {state === "success" ? <Check size={23} /> : <KeyRound size={23} />}
      </span>
      <h2>{state === "success" ? "密码已重置" : state === "expired" ? "链接已过期" : state === "invalid" ? "链接无法使用" : "设置新密码"}</h2>
      {state === "checking" ? (
        <div className="recovery-loading" role="status"><LoaderCircle className="spin" size={20} />正在验证重置链接…</div>
      ) : state === "invalid" || state === "expired" ? (
        <>
          <p>{state === "expired" ? "重置链接的有效期已结束。" : "链接可能已过期、已使用，或被新链接替代。"}请联系平台管理员获取新链接。</p>
          <Link to="/forgot-password" className="button full-width">联系管理员<ArrowRight size={17} /></Link>
        </>
      ) : state === "error" ? (
        <>
          <Notice>暂时无法验证链接，请检查网络后重试。</Notice>
          <Button className="full-width" onClick={() => setAttempt((value) => value + 1)}>重新验证链接</Button>
        </>
      ) : state === "success" ? (
        <>
          <p>密码已更新，你可以使用新密码登录。</p>
          {user ? (
            <>
              <Notice kind="info">当前账号仍保持登录。切换账号时，请先从账号设置退出。</Notice>
              <Link to="/app" className="button full-width">返回当前账号<ArrowRight size={17} /></Link>
            </>
          ) : <Link to="/login" className="button full-width">使用新密码登录<ArrowRight size={17} /></Link>}
        </>
      ) : inspection && (
        <>
          <div className="reset-account"><b>{inspection.account.displayName}</b><span>{inspection.account.emailMasked}</span></div>
          <p className="reset-expiry">链接有效期至 {new Date(inspection.expiresAt).toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false })}</p>
          <form ref={formRef} onSubmit={submit} aria-busy={busy}>
            <Notice id={errorId}>{error}</Notice>
            <label>新密码<input type="password" name="newPassword" autoComplete="new-password" placeholder="至少 12 个字符" minLength={12} required disabled={busy} aria-invalid={invalidField === "newPassword" || undefined} aria-describedby={invalidField === "newPassword" ? errorId : undefined} /></label>
            <label>确认新密码<input type="password" name="confirmPassword" autoComplete="new-password" placeholder="再次输入新密码" required disabled={busy} aria-invalid={invalidField === "confirmPassword" || undefined} aria-describedby={invalidField === "confirmPassword" ? errorId : undefined} /></label>
            <small className="field-help reset-session-help">重置后，该账号原有的登录会话将失效。</small>
            <Button type="submit" className="full-width" busy={busy} disabled={loading}>重置密码<ArrowRight size={17} /></Button>
          </form>
        </>
      )}
    </RecoveryFrame>
  );
}
