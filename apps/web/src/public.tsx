import { useState } from "react";
import type { FormEvent } from "react";
import {
  Link,
  Navigate,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";
import {
  ArrowRight,
  Check,
  CheckCheck,
  ChevronRight,
  Hash,
  LockKeyhole,
  MessageCircle,
  MessagesSquare,
  ShieldCheck,
  Sparkles,
  Users,
  Zap,
} from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@teamie/api-client";
import { useAuth } from "./auth";
import { errorText } from "./chat";
import { Avatar, Button, Logo, Notice, PublicHeader } from "./ui";

export function Landing() {
  const { user } = useAuth();
  return (
    <div className="landing">
      <PublicHeader />
      <main>
        <section className="hero">
          <div className="hero-copy">
            <div className="eyebrow">
              <span className="tiny-dot" /> 为默契的团队，留一个空间
            </div>
            <h1>
              好想法，
              <br />从<span className="hand-underline">聊在一起</span>开始
              <span className="purple">。</span>
            </h1>
            <p>
              少一点信息散落，多一点心有灵犀。
              <br />
              让团队、对话和灵感，在 Teamie 自然相遇。
            </p>
            <div className="hero-actions">
              <Link
                to={user ? "/app" : "/login?mode=register"}
                className="button"
              >
                {user ? "回到工作空间" : "开启团队新对话"}
                <ArrowRight size={18} />
              </Link>
              <span>一个空间，一起向前。</span>
            </div>
            <div className="hero-footnote">
              <span className="mini-avatars">
                <Avatar name="林" />
                <Avatar name="周" />
                <Avatar name="陈" />
              </span>
              <span>为每一位认真协作的伙伴而造</span>
            </div>
          </div>
          <div className="hero-visual" aria-label="Teamie 团队沟通界面示意">
            <div className="visual-grid" />
            <span className="orbit-caption">
              GOOD THINGS
              <br />
              HAPPEN TOGETHER.
            </span>
            <div className="demo-window">
              <div className="demo-title">
                <Logo />
                <span>
                  <span className="tiny-dot" /> 团队空间
                </span>
              </div>
              <div className="demo-body">
                <div className="demo-sidebar">
                  <span className="demo-workspace">
                    灵感工作室 <span>⌄</span>
                  </span>
                  <small>团队频道</small>
                  <span className="demo-channel selected">
                    <Hash size={15} /> general
                  </span>
                  <span className="demo-channel">
                    <Hash size={15} /> 一起做点什么
                  </span>
                  <span className="demo-channel">
                    <Hash size={15} /> 灵感随手记
                  </span>
                  <small>私信</small>
                  <span className="demo-channel">
                    <Avatar name="林" size="small" /> 林小满
                  </span>
                  <span className="demo-channel">
                    <Avatar name="周" size="small" /> 周一
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
                      <p>新的一天，有什么好点子？ ☀️</p>
                    </div>
                  </div>
                  <div className="demo-message">
                    <Avatar name="周" />
                    <div>
                      <b>
                        周一 <small>10:25</small>
                      </b>
                      <p>把我们的想法，变成下一件好事。</p>
                      <span className="idea-card">
                        <Sparkles size={18} />
                        <span>
                          从一句「要不，我们试试？」开始
                          <small>让灵感在对话里生长</small>
                        </span>
                      </span>
                    </div>
                  </div>
                  <div className="demo-message">
                    <Avatar name="陈" />
                    <div>
                      <b>
                        陈可 <small>10:26</small>
                      </b>
                      <p>一起！已经开始期待了 🙌</p>
                    </div>
                  </div>
                  <div className="demo-input">
                    发送消息到 # general <span>↵</span>
                  </div>
                </div>
              </div>
              <div className="demo-label">
                界面示意 · 真实协作，从你的团队开始
              </div>
            </div>
            <div className="floating-note">
              <span className="floating-icon">
                <CheckCheck size={22} />
              </span>
              <span>
                想法，接住了。<small>每一条对话，都有回响</small>
              </span>
              <span className="note-spark">✧</span>
            </div>
            <span className="hero-asterisk">✳</span>
          </div>
        </section>
        <section className="values" id="why">
          <div className="section-intro">
            <span className="eyebrow">LESS FRICTION. MORE CONNECTION.</span>
            <h2>留给协作的，应该是专注。</h2>
            <p>把沟通安排妥当，好让你们去做重要的事。</p>
          </div>
          <div className="value-grid">
            <article>
              <div className="value-icon lilac">
                <Hash size={24} />
              </div>
              <h3>对的讨论，在对的地方</h3>
              <p>
                用频道组织团队话题，让每个项目的上下文清晰可见，随时接着聊。
              </p>
              <span>
                频道与工作空间 <ArrowRight size={15} />
              </span>
            </article>
            <article>
              <div className="value-icon mint">
                <MessagesSquare size={24} />
              </div>
              <h3>一句私聊，一群默契</h3>
              <p>
                从一对一沟通到小组讨论，自然切换。让每个想法，都能找到合适的人。
              </p>
              <span>
                私信与小组对话 <ArrowRight size={15} />
              </span>
            </article>
            <article>
              <div className="value-icon peach">
                <Zap size={24} />
              </div>
              <h3>暂时离开，也能接上</h3>
              <p>
                消息妥善保存，未读清楚呈现。再次回来时，重要对话依然在这里。
              </p>
              <span>
                可靠消息与未读同步 <ArrowRight size={15} />
              </span>
            </article>
          </div>
        </section>
        <section className="landing-cta">
          <div>
            <span className="eyebrow">
              A LITTLE SPACE. A LOT OF POSSIBILITIES.
            </span>
            <h2>下一件好事，一起开始。</h2>
          </div>
          <Link to={user ? "/app" : "/login?mode=register"} className="button">
            进入 Teamie <ArrowRight size={17} />
          </Link>
          <span className="cta-decoration">✳</span>
        </section>
      </main>
      <footer className="public-footer">
        <Logo />
        <span>在一起，做成事。</span>
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
  const redirectParam = params.get("next") || "/app";
  const next =
    redirectParam.startsWith("/") && !redirectParam.startsWith("//")
      ? redirectParam
      : "/app";
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError("");
    const data = new FormData(event.currentTarget);
    try {
      const session = register
        ? await api.register({
            email: String(data.get("email")).trim(),
            password: String(data.get("password")),
            displayName: String(data.get("displayName")).trim(),
            inviteCode: String(data.get("inviteCode") || "").trim(),
          })
        : await api.login(
            String(data.get("email")).trim(),
            String(data.get("password")),
          );
      setSession(session);
      navigate(next, { replace: true });
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  if (user && !loading) return <Navigate to={next} replace />;
  return (
    <div className="auth-page">
      <div className="auth-story">
        <Link to="/">
          <Logo />
        </Link>
        <div>
          <span className="eyebrow">YOUR PEOPLE. YOUR SPACE.</span>
          <h1>
            欢迎来到
            <br />
            你们的<span>下一站。</span>
          </h1>
          <p>
            一点灵感，一些默契，
            <br />
            和一群想把事情做好的人。
          </p>
          <div className="auth-art">
            <div className="art-circle" />
            <span className="art-bubble first">
              <MessageCircle size={35} />
              <span>很高兴，和你在一起。</span>
            </span>
            <span className="art-bubble second">
              <span>一起做点好事</span>
              <span>✳</span>
            </span>
            <span className="art-star">✧</span>
          </div>
        </div>
        <span className="story-bottom">
          在一起，做成事。 <span>TEAMIE © {new Date().getFullYear()}</span>
        </span>
      </div>
      <div className="auth-content">
        <Link to="/" className="back-link">
          返回首页 <ChevronRight size={15} />
        </Link>
        <div className="auth-form">
          <span className="auth-kicker">LET'S GET TOGETHER</span>
          <h2>{register ? "认识一下，新伙伴。" : "回来啦，老朋友。"}</h2>
          <p>
            {register
              ? "创建你的账户，加入下一场好对话。"
              : "登录 Teamie，继续你们的好点子。"}
          </p>
          <div className="auth-tabs">
            <button
              className={!register ? "active" : ""}
              onClick={() => {
                setError("");
                setParams({
                  ...(params.get("next") ? { next } : {}),
                  mode: "login",
                });
              }}
            >
              登录
            </button>
            <button
              className={register ? "active" : ""}
              onClick={() => {
                setError("");
                setParams({
                  ...(params.get("next") ? { next } : {}),
                  mode: "register",
                });
              }}
            >
              创建账户
            </button>
          </div>
          <form onSubmit={submit} key={register ? "register" : "login"}>
            <Notice>{error}</Notice>
            {register && (
              <label>
                怎么称呼你
                <input
                  name="displayName"
                  autoComplete="nickname"
                  placeholder="团队里的名字"
                  maxLength={80}
                  required
                />
              </label>
            )}
            <label>
              邮箱地址
              <input
                type="email"
                name="email"
                autoComplete="email"
                placeholder="you@team.com"
                required
              />
            </label>
            <label>
              密码
              <input
                type="password"
                name="password"
                autoComplete={register ? "new-password" : "current-password"}
                placeholder={register ? "至少 12 个字符" : "输入你的密码"}
                minLength={register ? 12 : undefined}
                required
              />
            </label>
            {register && (
              <label>
                注册邀请码{" "}
                <span className="optional">由 Teamie 管理员提供</span>
                <input
                  name="inviteCode"
                  autoComplete="off"
                  placeholder="输入部署注册邀请码（如有）"
                />
                <small className="field-help">
                  当前默认采用邀请注册。此邀请码与工作空间邀请链接不同；开放注册时可留空。
                </small>
              </label>
            )}
            <Button type="submit" busy={busy} className="full-width">
              {register ? "创建账户，加入 Teamie" : "登录 Teamie"}
              <ArrowRight size={17} />
            </Button>
          </form>
          <div className="auth-security">
            <LockKeyhole size={13} />
            <span>你的对话，只属于你和你的团队。</span>
          </div>
        </div>
        <div className="auth-bottom">一切好的协作，从一句「你好」开始。</div>
      </div>
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
          <span>✧</span>
        </div>
        <span className="eyebrow">BETTER TOGETHER</span>
        <h1>
          {query.data
            ? `加入「${query.data.workspace.name}」`
            : query.isLoading
              ? "正在打开邀请…"
              : "这份邀请暂时无法打开"}
        </h1>
        <p>
          {query.data
            ? "有人为你留了一个位置。加入工作空间，和伙伴们接着聊。"
            : query.isLoading
              ? "马上就好。"
              : "邀请可能已过期或已被撤销，请联系工作空间管理员获取新的邀请。"}
        </p>
        <Notice>{error}</Notice>
        {query.data && (
          <>
            <div className="invite-benefits">
              <span>
                <Hash size={17} /> 团队频道
              </span>
              <span>
                <MessageCircle size={17} /> 直接对话
              </span>
              <span>
                <ShieldCheck size={17} /> 受邀访问
              </span>
            </div>
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
                  接受邀请，进入工作空间
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
              邀请有效期至{" "}
              {new Date(query.data.expiresAt).toLocaleString("zh-CN")}
            </small>
          </>
        )}
        {!query.data && !query.isLoading && (
          <Link to="/" className="button secondary">
            返回首页
          </Link>
        )}
      </main>
      <div className="join-bottom">
        <Logo />
        <span>为每一次相遇，留一个空间。</span>
      </div>
    </div>
  );
}
