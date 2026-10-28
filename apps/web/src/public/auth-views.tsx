import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import {
  Link,
  Navigate,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";
import { ArrowLeft, ArrowRight, Check, Users } from "lucide-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@teamie/api-client";
import type { Bootstrap } from "@teamie/api-client";
import { chatStore } from "../store";
import { useAuth } from "../auth";
import { clearSessionExpired, readSessionExpired } from "../session-notice";
import { errorText } from "../chat";
import { Avatar, Button, Logo, Notice, PublicHeader } from "../ui";
import { passwordPolicyError } from "../password-policy";
import { FieldError, PasswordField, fieldErrorProps } from "../password-field";
import { ICON } from "../icons";
import { resetPageScroll } from "../public";

type LoginField =
  | "inviteCode"
  | "displayName"
  | "email"
  | "password"
  | "confirmPassword";
const REGISTER_FIELDS: LoginField[] = [
  "inviteCode",
  "displayName",
  "email",
  "password",
  "confirmPassword",
];
const LOGIN_FIELDS: LoginField[] = ["email", "password"];
// Deliberately lenient, like the browser's type=email check; the server is
// authoritative.
const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+$/;
function loginFieldError(
  field: LoginField,
  data: FormData,
  register: boolean,
): string {
  const text = (name: string) => String(data.get(name) || "");
  switch (field) {
    case "inviteCode":
      return text("inviteCode").trim() ? "" : "请填写注册邀请码。";
    case "displayName":
      return text("displayName").trim() ? "" : "请填写显示名称。";
    case "email": {
      const email = text("email").trim();
      if (!email) return "请填写邮箱地址。";
      return EMAIL_PATTERN.test(email) ? "" : "请输入有效的邮箱地址。";
    }
    case "password":
      return register
        ? passwordPolicyError(text("password"))
        : text("password")
          ? ""
          : "请输入密码。";
    case "confirmPassword":
      if (!text("confirmPassword")) return "请再次输入密码。";
      return text("confirmPassword") !== text("password")
        ? "两次输入的密码不一致。"
        : "";
  }
}

export function Login() {
  const { user, loading, setSession, reconcileSession } = useAuth();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const register = params.get("mode") === "register";
  const inviteCode = params.get("inviteCode") || "";
  const registration = useQuery({
    queryKey: ["registrationPolicy"],
    queryFn: api.registration,
    enabled: register && !loading,
    staleTime: 0,
    retry: 1,
  });
  const redirectParam = params.get("next") || "/app";
  const next =
    redirectParam.startsWith("/") && !redirectParam.startsWith("//")
      ? redirectParam
      : "/app";
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const [invalidField, setInvalidField] = useState<LoginField | "">("");
  const [editingInvite, setEditingInvite] = useState(false);
  const inviteInput = useRef<HTMLInputElement>(null);
  const errorId = useId();
  const fieldId = useId();
  const pending = useRef(false);
  const active = useRef(false);
  const [sessionExpired] = useState(readSessionExpired);
  const inviteSummary = register && !!inviteCode.trim() && !editingInvite;
  const inviteLabel = registration.data?.bootstrapRequired
    ? "初始化邀请码"
    : "注册邀请码";
  const inviteHelp = registration.data?.bootstrapRequired
    ? "仅用于创建第一个管理员账户。"
    : "每个邀请码仅可使用一次，7 天内有效。";
  useEffect(() => {
    clearSessionExpired();
  }, []);
  useLayoutEffect(() => {
    resetPageScroll();
  }, [register]);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
    };
  }, []);
  useEffect(() => {
    if (editingInvite) inviteInput.current?.focus();
  }, [editingInvite]);
  function changeMode(mode: string) {
    setError("");
    setInvalidField("");
    setEditingInvite(false);
    setParams({
      ...(params.get("next") ? { next } : {}),
      ...(inviteCode ? { inviteCode } : {}),
      mode,
    });
  }
  const errorFor = (field: LoginField) =>
    invalidField === field ? error : undefined;
  const errorElementId = (field: LoginField) => `${fieldId}-${field}-error`;
  const fieldA11y = (field: LoginField) =>
    fieldErrorProps(errorElementId(field), errorFor(field));
  function revalidate(event: FormEvent<HTMLFormElement>) {
    // Validate on submit first, then live only for the field already in error.
    const target = event.target as HTMLInputElement;
    if (!invalidField || target.name !== invalidField) return;
    const message = loginFieldError(
      invalidField,
      new FormData(event.currentTarget),
      register,
    );
    if (!message) {
      setError("");
      setInvalidField("");
    } else if (message !== error) setError(message);
  }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending.current) return;
    setError("");
    setInvalidField("");
    const form = event.currentTarget;
    const data = new FormData(form);
    const password = String(data.get("password") || "");
    for (const field of register ? REGISTER_FIELDS : LOGIN_FIELDS) {
      const message = loginFieldError(field, data, register);
      if (!message) continue;
      setError(message);
      setInvalidField(field);
      if (field === "inviteCode" && inviteSummary) setEditingInvite(true);
      const input = form.elements.namedItem(field);
      if (input instanceof HTMLInputElement) input.focus();
      return;
    }
    pending.current = true;
    setBusy(true);
    const sessionToken = api.csrfToken;
    try {
      const session = register
        ? await api.register({
            email: String(data.get("email")).trim(),
            password,
            displayName: String(data.get("displayName")).trim(),
            inviteCode: String(data.get("inviteCode") || "").trim(),
          })
        : await api.login(String(data.get("email")).trim(), password);
      if (!active.current || sessionToken !== api.csrfToken) {
        reconcileSession();
        return;
      }
      form.reset();
      setSession(session);
      navigate(next, { replace: true });
    } catch (e) {
      if (active.current && sessionToken === api.csrfToken)
        setError(errorText(e));
    } finally {
      pending.current = false;
      if (active.current) setBusy(false);
    }
  }
  if (user && !loading) return <Navigate to={next} replace />;
  return (
    <div className="auth-page">
      <div className="auth-story">
        <Link to="/" aria-label="Teamie 首页">
          <Logo />
        </Link>
        <div>
          <h1>
            在一起，
            <br />
            做成事<span>。</span>
          </h1>
          <p>你的团队，你们的空间。</p>
        </div>
        <span className="story-bottom">
          TEAMIE © {new Date().getFullYear()}
        </span>
      </div>
      <main className="auth-content">
        <Link to="/" className="back-link">
          <ArrowLeft size={ICON.sm} />
          首页
        </Link>
        <div className="auth-form">
          <Link to="/" className="auth-mobile-brand" aria-label="Teamie 首页">
            <Logo />
          </Link>
          <h2>{register ? "受邀注册" : "登录 Teamie"}</h2>
          <div className="auth-tabs" aria-label="账户入口">
            <button
              className={!register ? "active" : ""}
              aria-pressed={!register}
              disabled={busy}
              onClick={() => changeMode("login")}
            >
              登录
            </button>
            <button
              className={register ? "active" : ""}
              aria-pressed={register}
              disabled={busy}
              onClick={() => changeMode("register")}
            >
              受邀注册
            </button>
          </div>
          {register && registration.data?.bootstrapRequired && (
            <Notice kind="info">
              首次注册将创建平台管理员，请使用初始化邀请码。
            </Notice>
          )}
          {register && registration.isError && (
            <Notice kind="info">
              暂时无法读取注册状态，请使用有效邀请码重试。
            </Notice>
          )}
          {!register && params.get("passwordChanged") === "1" && (
            <Notice kind="success">密码已修改，请重新登录。</Notice>
          )}
          {!register &&
            sessionExpired &&
            params.get("passwordChanged") !== "1" && (
              <Notice kind="info">登录已失效，请重新登录。</Notice>
            )}
          <form
            onSubmit={submit}
            onInput={revalidate}
            key={register ? `register:${inviteCode}` : "login"}
            aria-busy={busy}
            noValidate
          >
            <Notice id={errorId}>{error}</Notice>
            {register &&
              (inviteSummary ? (
                <div className="invite-summary">
                  <input type="hidden" name="inviteCode" value={inviteCode} />
                  <span className="invite-summary-text">
                    <span>{inviteLabel}</span>
                    <code>{inviteCode}</code>
                  </span>
                  <button
                    type="button"
                    className="invite-summary-edit"
                    aria-label={`修改${inviteLabel}`}
                    disabled={busy}
                    onClick={() => setEditingInvite(true)}
                  >
                    修改
                  </button>
                  <small className="field-help">
                    已从邀请链接填入。{inviteHelp}
                  </small>
                </div>
              ) : (
                <>
                  <label>
                    {inviteLabel}
                    <span className="optional">由管理员提供</span>
                    <input
                      ref={inviteInput}
                      name="inviteCode"
                      autoComplete="off"
                      autoCapitalize="none"
                      spellCheck={false}
                      defaultValue={inviteCode}
                      placeholder="输入邀请码"
                      required
                      readOnly={busy}
                      aria-disabled={busy || undefined}
                      {...fieldA11y("inviteCode")}
                    />
                    <small className="field-help">{inviteHelp}</small>
                  </label>
                  <FieldError
                    id={errorElementId("inviteCode")}
                    message={errorFor("inviteCode")}
                  />
                </>
              ))}
            {register && (
              <>
                <label>
                  显示名称
                  <input
                    name="displayName"
                    autoComplete="nickname"
                    placeholder="你的名字"
                    maxLength={80}
                    required
                    readOnly={busy}
                    aria-disabled={busy || undefined}
                    {...fieldA11y("displayName")}
                  />
                </label>
                <FieldError
                  id={errorElementId("displayName")}
                  message={errorFor("displayName")}
                />
              </>
            )}
            <label>
              邮箱地址
              <input
                type="email"
                name="email"
                autoComplete="email"
                inputMode="email"
                autoCapitalize="none"
                placeholder="you@team.com"
                required
                readOnly={busy}
                aria-disabled={busy || undefined}
                {...fieldA11y("email")}
              />
            </label>
            <FieldError
              id={errorElementId("email")}
              message={errorFor("email")}
            />
            <PasswordField
              name="password"
              label="密码"
              autoComplete={register ? "new-password" : "current-password"}
              placeholder={register ? "至少 12 个字符" : "输入密码"}
              error={errorFor("password")}
              readOnly={busy}
              showRules={register}
            />
            {register && (
              <PasswordField
                name="confirmPassword"
                label="确认密码"
                autoComplete="new-password"
                placeholder="再次输入密码"
                error={errorFor("confirmPassword")}
                readOnly={busy}
                confirmOf="password"
              />
            )}
            {!register && <Link to="/forgot-password" className="forgot-password-link">忘记密码？</Link>}
            <Button type="submit" busy={busy} className="full-width">
              {register ? "创建账户" : "登录"}
              <ArrowRight size={ICON.md} />
            </Button>
          </form>
        </div>
      </main>
    </div>
  );
}

function inviteFailure(error: unknown): "invalid" | "unavailable" {
  return error instanceof ApiError && [400, 404, 410].includes(error.status)
    ? "invalid"
    : "unavailable";
}

export function Join() {
  const { inviteCode = "" } = useParams();
  const { user, loading } = useAuth();
  const navigate = useNavigate();
  const client = useQueryClient();
  const query = useQuery({
    queryKey: ["invite", inviteCode],
    queryFn: () => api.invite(inviteCode),
    enabled: !loading,
    retry: false,
  });
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  async function accept() {
    setBusy(true);
    setError("");
    try {
      const result = await api.acceptInvite(inviteCode);
      result.conversations.forEach((c) => chatStore.addConversation(c));
      if (user) {
        client.setQueryData<Bootstrap>(["bootstrap", user.id], (old) =>
          old
            ? {
                ...old,
                workspaces: old.workspaces.some(
                  (w) => w.id === result.workspace.id,
                )
                  ? old.workspaces
                  : [...old.workspaces, result.workspace],
                conversations: [
                  ...old.conversations.filter(
                    (c) =>
                      !result.conversations.some((added) => added.id === c.id),
                  ),
                  ...result.conversations,
                ],
              }
            : old,
        );
      }
      await client.invalidateQueries({
        queryKey: ["bootstrap"],
        refetchType: "all",
      });
      navigate(
        result.conversations[0]
          ? `/app/w/${result.workspace.id}/c/${result.conversations[0].id}`
          : "/app",
        { replace: true },
      );
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  const failure = query.error ? inviteFailure(query.error) : null;
  const pendingInvite = query.isLoading || loading;
  const joinPath = `/join/${inviteCode}`;
  return (
    <div className="join-page">
      <PublicHeader />
      <main className="join-card">
        <div className="invitation-icon">
          <Users size={ICON.xl} />
        </div>
        <h1>
          {query.data
            ? `加入「${query.data.workspace.name}」`
            : pendingInvite
              ? "正在打开邀请…"
              : failure === "unavailable"
                ? "暂时无法打开邀请"
                : "邀请已失效"}
        </h1>
        <p>
          {query.data
            ? user
              ? "接受邀请后，即可参与团队对话。"
              : "加入工作空间需要 Teamie 账号。请先登录，再接受邀请。"
            : pendingInvite
              ? "正在加载…"
              : failure === "unavailable"
                ? "网络或服务暂时不可用，邀请本身可能仍然有效，请稍后重试。"
                : "邀请链接可能已过期或被撤销，请联系工作空间管理员获取新邀请。"}
        </p>
        <Notice>{error}</Notice>
        {query.data && (
          <>
            {user ? (
              <>
                <div className="invite-identity">
                  <Avatar name={user.displayName} />
                  <span>
                    以 <b>{user.displayName}</b> 的身份加入
                    <small>{user.email}</small>
                  </span>
                  <Check size={ICON.md} />
                </div>
                <Button
                  busy={busy || loading}
                  onClick={accept}
                  className="full-width"
                >
                  接受邀请
                  <ArrowRight size={ICON.md} />
                </Button>
              </>
            ) : (
              <>
                <Link
                  className="button full-width"
                  to={`/login?next=${encodeURIComponent(joinPath)}`}
                >
                  登录并接受邀请
                  <ArrowRight size={ICON.md} />
                </Link>
                <Link
                  className="join-register-link"
                  to={`/login?mode=register&next=${encodeURIComponent(joinPath)}`}
                >
                  还没有账号？使用注册邀请码注册
                </Link>
              </>
            )}
            <small className="invite-expiry">
              有效期至 {new Date(query.data.expiresAt).toLocaleString("zh-CN")}
            </small>
          </>
        )}
        {!query.data && !pendingInvite && failure === "unavailable" && (
          <Button
            busy={query.isFetching}
            onClick={() => void query.refetch()}
            className="full-width"
          >
            重新加载邀请
          </Button>
        )}
        {!query.data && !pendingInvite && (
          <Link to="/" className="button secondary">
            返回首页
          </Link>
        )}
      </main>
    </div>
  );
}
