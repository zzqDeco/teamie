import { useEffect, useId, useRef, useState } from "react";
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
  Bell,
  Bot,
  Check,
  ChevronRight,
  CornerUpLeft,
  Hash,
  Heart,
  Lock,
  MessagesSquare,
  SendHorizontal,
  Sparkles,
  Users,
} from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@teamie/api-client";
import { useAuth } from "./auth";
import { errorText } from "./chat";
import { Avatar, Button, Loading, Logo, Notice, PublicHeader } from "./ui";
import { passwordPolicyError } from "./password-policy";

const STORY_SCENES = [
  {
    step: "01",
    eyebrow: "01 · CHANNELS & GROUPS",
    label: "频道与小组",
    title: "对的讨论，在对的地方。",
    description:
      "公开频道沉淀团队共识，私密小组聚焦专项议题。每个项目的上下文都清晰可见，随时接着聊。",
    footnote: "频道、小组与私信 →",
  },
  {
    step: "02",
    eyebrow: "02 · PERSONAL AGENTS",
    label: "专属 Agent",
    title: "每位成员，都有专属伙伴。",
    description:
      "创建属于你的 Agent 并带入工作空间，像和同事对话一样随时 @提及或发起私聊，自然配合。",
    footnote: "个人拥有 · 团队可发现 →",
  },
  {
    step: "03",
    eyebrow: "03 · INBOX & CONTEXT",
    label: "收件箱溯源",
    title: "暂时离开，也能轻松接上。",
    description:
      "提及与回复自动归集到收件箱，点击任意引用即可一键回到原消息位置，关键脉络不再遗漏。",
    footnote: "提及收件箱与引用定位 →",
  },
  {
    step: "04",
    eyebrow: "04 · TRUST & PRIVACY",
    label: "权限边界",
    title: "像邀请一位新队友，安静可靠。",
    description:
      "无需繁琐的配置面板。你只需维护它的名字、简介与可见空间，Agent 便会遵循与成员一致的会话权限。",
    footnote: "受邀制准入 · 严格遵循会话权限 →",
  },
] as const;

export function Landing() {
  const { user, loading } = useAuth();
  const trackRef = useRef<HTMLDivElement>(null);
  const [activeScene, setActiveScene] = useState(0);
  const [scrollProgress, setScrollProgress] = useState(0);

  useEffect(() => {
    if (user || loading) return;
    function updateScrollStage() {
      const track = trackRef.current;
      if (!track) return;
      const rect = track.getBoundingClientRect();
      const scrollable = Math.max(1, rect.height - window.innerHeight);
      const raw = Math.min(1, Math.max(0, -rect.top / scrollable));
      setScrollProgress(raw);
      const index = Math.min(
        STORY_SCENES.length - 1,
        Math.floor(raw * STORY_SCENES.length),
      );
      setActiveScene(index);
    }
    updateScrollStage();
    window.addEventListener("scroll", updateScrollStage, { passive: true });
    window.addEventListener("resize", updateScrollStage);
    return () => {
      window.removeEventListener("scroll", updateScrollStage);
      window.removeEventListener("resize", updateScrollStage);
    };
  }, [user, loading]);

  function selectScene(index: number) {
    setActiveScene(index);
    setScrollProgress((index + 0.5) / STORY_SCENES.length);
    const track = trackRef.current;
    if (!track || typeof window === "undefined") return;
    const rect = track.getBoundingClientRect();
    const scrollable = Math.max(0, rect.height - window.innerHeight);
    if (scrollable > 0 && typeof window.scrollTo === "function") {
      const targetTop =
        window.scrollY +
        rect.top +
        (index / Math.max(1, STORY_SCENES.length - 1)) * scrollable * 0.92;
      window.scrollTo({ top: targetTop, behavior: "smooth" });
    }
  }

  if (loading) return <Loading />;
  if (user) return <Navigate to="/app" replace />;
  const currentScene = STORY_SCENES[activeScene];

  return (
    <div className="landing">
      <PublicHeader />
      <main>
        <section className="hero">
          <div className="hero-copy">
            <span className="eyebrow">
              <span className="tiny-dot" />
              为默契的团队与 Agent，留一个空间
            </span>
            <h1>
              团队与 Agent，
              <br />
              在同一个空间<span className="hand-underline">做成事</span>
              <span className="purple">。</span>
            </h1>
            <p>
              少一点工具切换，多一点心有灵犀。
              <br />
              让频道讨论、小组专项与专属 Agent，在 Teamie 自然配合。
            </p>
            <div className="hero-actions">
              <Link to="/login" className="button">
                登录 Teamie
                <ArrowRight size={18} />
              </Link>
              <Link className="landing-register" to="/login?mode=register">
                凭邀请码注册 <ChevronRight size={16} />
              </Link>
            </div>
            <div className="hero-footnote">
              <div className="mini-avatars" aria-hidden="true">
                <Avatar name="林" />
                <Avatar name="周" />
                <span className="avatar lilac">AI</span>
              </div>
              <span>向下滑动，探索 Teamie 的协作空间 ↓</span>
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
                  <span className="tiny-dot" />
                  产品研发空间
                </span>
              </div>
              <div className="demo-body">
                <div className="demo-sidebar">
                  <span className="demo-workspace">产品研发空间</span>
                  <small>公开频道</small>
                  <span className="demo-channel selected">
                    <Hash size={14} />
                    general
                  </span>
                  <span className="demo-channel">
                    <Hash size={14} />
                    设计灵感
                  </span>
                  <small>小组与私信</small>
                  <span className="demo-channel">
                    <Users size={14} />
                    前端体验专项
                  </span>
                  <span className="demo-channel">
                    <Avatar name="林" size="small" />
                    林小满
                  </span>
                  <span className="demo-channel">
                    <Bot size={15} />
                    产品伙伴
                  </span>
                </div>
                <div className="demo-chat">
                  <div className="demo-chat-heading">
                    <Hash size={16} />
                    <b>general</b>
                    <span className="demo-topic">日常灵感与版本节奏</span>
                    <Users size={15} />
                  </div>
                  <div className="demo-message">
                    <Avatar name="林" />
                    <div>
                      <b>
                        林小满 <small>10:24</small>
                      </b>
                      <p>
                        新版导航与登录直达已经就绪，@产品伙伴 帮大家理好了验收清单。
                      </p>
                    </div>
                  </div>
                  <div className="demo-message">
                    <Avatar name="周" />
                    <div>
                      <b>
                        周一 <small>10:25</small>
                      </b>
                      <p>收到，阅读对比度舒服多了，下午我们直接按清单过一遍。</p>
                    </div>
                  </div>
                  <div className="demo-message">
                    <span className="avatar lilac">
                      <Bot size={17} />
                    </span>
                    <div>
                      <b>
                        产品伙伴 <small className="demo-agent-label">Agent</small>
                      </b>
                      <p>已整理好 3 项核心改动与上下文引用，随时可以查看。</p>
                    </div>
                  </div>
                  <div className="demo-input">
                    <span>写下你的想法，或输入 @ 提及伙伴…</span>
                    <SendHorizontal size={15} />
                  </div>
                </div>
              </div>
              <div className="demo-label">Teamie 实时会话示意</div>
            </div>
            <div className="floating-note">
              <div className="floating-icon">
                <Sparkles size={18} />
              </div>
              <span>
                想法，接住了。
                <small>提及与回复实时归集</small>
              </span>
              <span className="note-spark">✦</span>
            </div>
            <span className="hero-asterisk">✳</span>
          </div>
        </section>

        <section
          ref={trackRef}
          className="scroll-story-track"
          aria-labelledby="features-heading"
          data-active-scene={activeScene}
        >
          <div className="scroll-story-sticky">
            <div className="scroll-story-inner">
              <div className="story-copy-column">
                <div className="story-header">
                  <span className="eyebrow">LESS FRICTION · MORE FLOW</span>
                  <h2 id="features-heading">留给协作的，应该是专注。</h2>
                </div>

                <div className="story-scene-list">
                  {STORY_SCENES.map((scene, index) => {
                    const isActive = index === activeScene;
                    return (
                      <article
                        key={scene.step}
                        className={`story-scene-item ${isActive ? "active" : ""}`}
                        aria-current={isActive ? "step" : undefined}
                      >
                        <span className="story-scene-eyebrow">{scene.eyebrow}</span>
                        <h3>{scene.title}</h3>
                        <p>{scene.description}</p>
                        <span className="story-scene-footnote">{scene.footnote}</span>
                      </article>
                    );
                  })}
                </div>

                <div className="story-controls" role="tablist" aria-label="产品特性分幕切换">
                  {STORY_SCENES.map((scene, index) => (
                    <button
                      key={scene.step}
                      type="button"
                      role="tab"
                      aria-selected={index === activeScene}
                      className={`story-step-pill ${index === activeScene ? "active" : ""}`}
                      onClick={() => selectScene(index)}
                    >
                      <b>{scene.step}</b>
                      <span>{scene.label}</span>
                    </button>
                  ))}
                </div>
                <div className="story-progress-bar" aria-hidden="true">
                  <span
                    style={{
                      width: `${Math.max(
                        ((activeScene + 1) / STORY_SCENES.length) * 100,
                        Math.round(scrollProgress * 100),
                      )}%`,
                    }}
                  />
                </div>
                <small className="story-scroll-hint" aria-hidden="true">
                  继续滑动切换下一幕 · {currentScene.step} / 0{STORY_SCENES.length}
                </small>
              </div>

              <div className="story-stage-canvas" aria-label="Teamie 分幕场景演示">
                {/* Scene 0: Channels & Groups */}
                <div
                  className={`story-stage-card ${activeScene === 0 ? "active" : ""}`}
                  aria-hidden={activeScene !== 0}
                >
                  <div className="stage-card-head">
                    <span className="value-icon lilac">
                      <MessagesSquare size={20} />
                    </span>
                    <div>
                      <b>公开频道与私密小组</b>
                      <small>产品研发空间 · 实时同步</small>
                    </div>
                    <span className="stage-pill lilac">01 / 04</span>
                  </div>
                  <div className="stage-channel-list">
                    <div className="stage-channel-row active">
                      <Hash size={16} />
                      <div>
                        <b>general</b>
                        <small>全员公开频道 · 沉淀版本节奏与团队共识</small>
                      </div>
                      <span>24 人</span>
                    </div>
                    <div className="stage-channel-row">
                      <Users size={16} />
                      <div>
                        <b>前端体验专项</b>
                        <small>受控私密小组 · 聚焦专项议题与设计走查</small>
                      </div>
                      <span>6 人</span>
                    </div>
                    <div className="stage-channel-row">
                      <Heart size={16} />
                      <div>
                        <b>1v1 直接私信</b>
                        <small>与人类成员或专属 Agent 随时快速对齐细节</small>
                      </div>
                      <span>私密</span>
                    </div>
                  </div>
                </div>

                {/* Scene 1: Personal Agents */}
                <div
                  className={`story-stage-card ${activeScene === 1 ? "active" : ""}`}
                  aria-hidden={activeScene !== 1}
                >
                  <div className="stage-card-head">
                    <span className="value-icon mint">
                      <Bot size={20} />
                    </span>
                    <div>
                      <b>人类与 Agent 同场协作</b>
                      <small># general · 实时对话流</small>
                    </div>
                    <span className="stage-pill mint">02 / 04</span>
                  </div>
                  <div className="stage-dialogue">
                    <div className="stage-msg">
                      <Avatar name="林" />
                      <div>
                        <b>
                          林小满 <small>10:24</small>
                        </b>
                        <p>
                          @产品伙伴 帮大家把今天讨论的视觉微调与验收重点整理成清单吧。
                        </p>
                      </div>
                    </div>
                    <div className="stage-msg agent">
                      <span className="avatar lilac">
                        <Bot size={16} />
                      </span>
                      <div>
                        <b>
                          产品伙伴 <small className="demo-agent-label">Agent · 由林小满创建</small>
                        </b>
                        <p>
                          好的，已整理好 3 项核心验收重点，并附上了原讨论消息引用，方便随时核对。
                        </p>
                      </div>
                    </div>
                  </div>
                </div>

                {/* Scene 2: Inbox & Context */}
                <div
                  className={`story-stage-card ${activeScene === 2 ? "active" : ""}`}
                  aria-hidden={activeScene !== 2}
                >
                  <div className="stage-card-head">
                    <span className="value-icon peach">
                      <Bell size={20} />
                    </span>
                    <div>
                      <b>提及与回复收件箱</b>
                      <small>跨工作空间聚合 · 一键定位原消息</small>
                    </div>
                    <span className="stage-pill peach">03 / 04</span>
                  </div>
                  <div className="stage-inbox-items">
                    <div className="stage-inbox-item">
                      <span className="stage-unread-dot" />
                      <div>
                        <small># general · 刚刚</small>
                        <b>周一 提及了你</b>
                        <p>“@林小满 新版长页的锁定滑动效果已经就绪，可以预览验收了。”</p>
                      </div>
                    </div>
                    <div className="stage-quote-jump">
                      <CornerUpLeft size={15} />
                      <span>点击任意引用预览，跨越长历史直接高亮定位至原消息位置</span>
                    </div>
                  </div>
                </div>

                {/* Scene 3: Trust & Boundaries */}
                <div
                  className={`story-stage-card ${activeScene === 3 ? "active" : ""}`}
                  aria-hidden={activeScene !== 3}
                >
                  <div className="spotlight-card-header">
                    <span className="avatar lilac">
                      <Bot size={22} />
                    </span>
                    <div>
                      <b>产品伙伴</b>
                      <small>Agent · 由林小满管理</small>
                    </div>
                    <span className="spotlight-status">已启用</span>
                  </div>
                  <p className="stage-agent-bio">
                    “负责整理版本纪要、汇总验收清单，并随时响应频道内的上下文提问。”
                  </p>
                  <div className="spotlight-spaces">
                    <small>已授权工作空间</small>
                    <div>
                      <span>产品研发空间</span>
                      <span>设计中心</span>
                    </div>
                  </div>
                  <div className="spotlight-foot">
                    <Lock size={13} />
                    <span>仅访问已加入的频道与小组 · 与成员共享同一套权限边界</span>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </section>

        <section className="landing-cta" aria-label="开始使用 Teamie">
          <div>
            <span className="eyebrow">A LITTLE SPACE. A LOT OF POSSIBILITIES.</span>
            <h2>下一件好事，一起开始。</h2>
          </div>
          <div className="landing-cta-actions">
            <Link to="/login" className="button">
              进入 Teamie
              <ArrowRight size={17} />
            </Link>
            <Link to="/login?mode=register" className="landing-register">
              凭邀请码注册 <ChevronRight size={16} />
            </Link>
          </div>
          <span className="cta-decoration" aria-hidden="true">
            ✳
          </span>
        </section>
      </main>
      <footer className="public-footer">
        <Logo />
        <span>为默契的团队，留一个空间</span>
        <small>© {new Date().getFullYear()} Teamie</small>
      </footer>
    </div>
  );
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
  const [invalidField, setInvalidField] = useState("");
  const errorId = useId();
  const pending = useRef(false);
  const active = useRef(false);
  useEffect(() => {
    active.current = true;
    return () => {
      active.current = false;
    };
  }, []);
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
            {!register && <Link to="/forgot-password" className="forgot-password-link">忘记密码？</Link>}
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
