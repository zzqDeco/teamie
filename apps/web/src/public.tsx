import { useId, useRef, useState } from "react";
import type { FormEvent } from "react";
import {
  Link,
  Navigate,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";
import {
  ArrowLeft,
  ArrowRight,
  Bot,
  Check,
  ChevronRight,
  Hash,
  MessagesSquare,
  SendHorizontal,
  Users,
} from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@teamie/api-client";
import { useAuth } from "./auth";
import { errorText } from "./chat";
import { Avatar, Button, Logo, Notice, PublicHeader } from "./ui";
import { passwordPolicyError } from "./password-policy";

export function Landing() {
  const { user } = useAuth();
  return (
    <div className="landing">
      <PublicHeader />
      <main>
        <section className="hero">
          <div className="hero-copy">
            <h1>
              团队与 Agent，
              <br />
              聊在<span className="hand-underline">一起</span>
              <span className="purple">。</span>
            </h1>
            <p>频道、私聊与小组，让协作自然发生。</p>
            <div className="hero-actions">
              <Link to={user ? "/app" : "/login"} className="button">
                {user ? "进入工作空间" : "登录 Teamie"}
                <ArrowRight size={18} />
              </Link>
              {!user && (
                <Link className="landing-register" to="/login?mode=register">
                  凭邀请码注册 <ChevronRight size={16} />
                </Link>
              )}
            </div>
            <div className="landing-features" aria-label="Teamie 功能">
              <span>
                <Hash size={16} />
                团队频道
              </span>
              <span>
                <MessagesSquare size={16} />
                私信与小组
              </span>
              <span>
                <Bot size={16} />
                Agent 协作
              </span>
            </div>
          </div>
          <div className="hero-visual" aria-label="Teamie 团队沟通界面示意">
            <div className="visual-grid" />
            <div className="demo-window">
              <div className="demo-title">
                <Logo />
                <span>团队空间</span>
              </div>
              <div className="demo-body">
                <div className="demo-sidebar">
                  <span className="demo-workspace">设计小组</span>
                  <small>频道</small>
                  <span className="demo-channel selected">
                    <Hash size={15} />
                    general
                  </span>
                  <span className="demo-channel">
                    <Hash size={15} />
                    产品讨论
                  </span>
                  <small>私信</small>
                  <span className="demo-channel">
                    <Avatar name="林" size="small" />
                    林小满
                  </span>
                  <span className="demo-channel">
                    <Bot size={17} />
                    产品伙伴
                  </span>
                </div>
                <div className="demo-chat">
                  <div className="demo-chat-heading">
                    <Hash size={19} />
                    <b>general</b>
                    <Users size={16} />
                  </div>
                  <div className="demo-message">
                    <Avatar name="林" />
                    <div>
                      <b>
                        林小满 <small>10:24</small>
                      </b>
                      <p>大家看一下新版首页。</p>
                    </div>
                  </div>
                  <div className="demo-message">
                    <Avatar name="周" />
                    <div>
                      <b>
                        周一 <small>10:25</small>
                      </b>
                      <p>导航简洁了，手机上也很好用。</p>
                    </div>
                  </div>
                  <div className="demo-message">
                    <span className="avatar agent-avatar">
                      <Bot size={20} />
                    </span>
                    <div>
                      <b>
                        产品伙伴{" "}
                        <small className="demo-agent-label">Agent</small>
                      </b>
                      <p>讨论要点已整理好，接着一起推进。</p>
                    </div>
                  </div>
                  <div className="demo-input">
                    发送消息到 # general <SendHorizontal size={15} />
                  </div>
                </div>
              </div>
              <div className="demo-label">界面示意</div>
            </div>
          </div>
        </section>
      </main>
      <footer className="public-footer">
        <Logo />
        <small>© {new Date().getFullYear()} Teamie</small>
      </footer>
    </div>
  );
}

export function Login() {
  const { user, loading, setSession } = useAuth();
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
  const [invalidField, setInvalidField] = useState("");
  const errorId = useId();
  const pending = useRef(false);
  function changeMode(mode: string) {
    setError("");
    setInvalidField("");
    setParams({
      ...(params.get("next") ? { next } : {}),
      ...(inviteCode ? { inviteCode } : {}),
      mode,
    });
  }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending.current) return;
    setError("");
    setInvalidField("");
    const form = event.currentTarget;
    const data = new FormData(form);
    const password = String(data.get("password") || "");
    function fail(message: string, field: string) {
      setError(message);
      setInvalidField(field);
      const input = form.elements.namedItem(field);
      if (input instanceof HTMLInputElement) input.focus();
    }
    if (register) {
      const policyError = passwordPolicyError(password);
      if (policyError) {
        fail(policyError, "password");
        return;
      }
      if (password !== String(data.get("confirmPassword") || "")) {
        fail("两次输入的密码不一致。", "confirmPassword");
        return;
      }
      if (!String(data.get("inviteCode") || "").trim()) {
        fail("请填写注册邀请码。", "inviteCode");
        return;
      }
    }
    pending.current = true;
    setBusy(true);
    try {
      const session = register
        ? await api.register({
            email: String(data.get("email")).trim(),
            password,
            displayName: String(data.get("displayName")).trim(),
            inviteCode: String(data.get("inviteCode") || "").trim(),
          })
        : await api.login(String(data.get("email")).trim(), password);
      form.reset();
      setSession(session);
      navigate(next, { replace: true });
    } catch (e) {
      setError(errorText(e));
    } finally {
      pending.current = false;
      setBusy(false);
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
          <ArrowLeft size={16} />
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
          <form
            onSubmit={submit}
            key={register ? `register:${inviteCode}` : "login"}
            aria-busy={busy}
          >
            <Notice id={errorId}>{error}</Notice>
            {register && (
              <label>
                显示名称
                <input
                  name="displayName"
                  autoComplete="nickname"
                  placeholder="你的名字"
                  maxLength={80}
                  required
                  disabled={busy}
                />
              </label>
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
                disabled={busy}
              />
            </label>
            <label>
              密码
              <input
                type="password"
                name="password"
                autoComplete={register ? "new-password" : "current-password"}
                placeholder={register ? "至少 12 个字符" : "输入密码"}
                minLength={register ? 12 : undefined}
                aria-invalid={invalidField === "password" || undefined}
                aria-describedby={
                  invalidField === "password" ? errorId : undefined
                }
                required
                disabled={busy}
              />
            </label>
            {register && (
              <label>
                确认密码
                <input
                  type="password"
                  name="confirmPassword"
                  autoComplete="new-password"
                  placeholder="再次输入密码"
                  aria-invalid={invalidField === "confirmPassword" || undefined}
                  aria-describedby={
                    invalidField === "confirmPassword" ? errorId : undefined
                  }
                  required
                  disabled={busy}
                />
              </label>
            )}
            {register && (
              <label>
                {registration.data?.bootstrapRequired
                  ? "初始化邀请码"
                  : "注册邀请码"}
                <span className="optional">由管理员提供</span>
                <input
                  name="inviteCode"
                  autoComplete="off"
                  autoCapitalize="none"
                  spellCheck={false}
                  defaultValue={inviteCode}
                  aria-invalid={invalidField === "inviteCode" || undefined}
                  aria-describedby={
                    invalidField === "inviteCode" ? errorId : undefined
                  }
                  placeholder="输入邀请码"
                  required
                  disabled={busy}
                />
                <small className="field-help">
                  {registration.data?.bootstrapRequired
                    ? "仅用于创建第一个管理员账户。"
                    : "每个邀请码仅可使用一次，7 天内有效。"}
                </small>
              </label>
            )}
            <Button type="submit" busy={busy} className="full-width">
              {register ? "创建账户" : "登录"}
              <ArrowRight size={17} />
            </Button>
          </form>
        </div>
      </main>
    </div>
  );
}

export function Join() {
  const { inviteCode = "" } = useParams();
  const { user, loading } = useAuth();
  const navigate = useNavigate();
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
  return (
    <div className="join-page">
      <PublicHeader />
      <main className="join-card">
        <div className="invitation-icon">
          <Users size={34} />
        </div>
        <h1>
          {query.data
            ? `加入「${query.data.workspace.name}」`
            : query.isLoading || loading
              ? "正在打开邀请…"
              : "邀请已失效"}
        </h1>
        <p>
          {query.data
            ? "接受邀请后，即可参与团队对话。"
            : query.isLoading || loading
              ? "正在加载…"
              : "请联系团队管理员获取新邀请。"}
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
                  <Check size={17} />
                </div>
                <Button
                  busy={busy || loading}
                  onClick={accept}
                  className="full-width"
                >
                  接受邀请
                  <ArrowRight size={17} />
                </Button>
              </>
            ) : (
              <Link
                className="button full-width"
                to={`/login?next=${encodeURIComponent(`/join/${inviteCode}`)}`}
              >
                登录并接受邀请
                <ArrowRight size={17} />
              </Link>
            )}
            <small className="invite-expiry">
              有效期至 {new Date(query.data.expiresAt).toLocaleString("zh-CN")}
            </small>
          </>
        )}
        {!query.data && !query.isLoading && !loading && (
          <Link to="/" className="button secondary">
            返回首页
          </Link>
        )}
      </main>
    </div>
  );
}
