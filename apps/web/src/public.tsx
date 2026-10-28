import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Link, Navigate } from "react-router-dom";
import {
  ArrowRight,
  Bell,
  Bot,
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
import { useAuth } from "./auth";
import { Avatar, Logo, PublicHeader } from "./ui";
import { ICON } from "./icons";
import { resetPageScroll } from "./public/scroll";
export { resetPageScroll };
export { Login, Join } from "./public/auth-views";

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

function isStickyStoryActive() {
  if (typeof window === "undefined") return false;
  if (
    typeof window.matchMedia === "function" &&
    window.matchMedia("(max-width: 780px), (max-height: 700px)").matches
  )
    return false;
  return window.innerWidth > 780 && window.innerHeight > 700;
}

export function Landing() {
  const { user, loading } = useAuth();
  const trackRef = useRef<HTMLDivElement>(null);
  const [activeScene, setActiveScene] = useState(0);
  const [scrollProgress, setScrollProgress] = useState(0);
  const [stickyStory, setStickyStory] = useState(() => isStickyStoryActive());

  useLayoutEffect(() => {
    resetPageScroll();
  }, []);

  useEffect(() => {
    if (user && !loading) return;
    function updateScrollStage() {
      const sticky = isStickyStoryActive();
      setStickyStory(sticky);
      const track = trackRef.current;
      if (!track || !sticky) return;
      const rect = track.getBoundingClientRect();
      const scrollable = rect.height - window.innerHeight;
      if (scrollable <= 0) return;
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
    if (!track || typeof window === "undefined" || !isStickyStoryActive())
      return;
    const rect = track.getBoundingClientRect();
    const scrollable = Math.max(0, rect.height - window.innerHeight);
    if (scrollable > 0 && typeof window.scrollTo === "function") {
      const targetTop =
        window.scrollY +
        rect.top +
        (index / Math.max(1, STORY_SCENES.length - 1)) * scrollable * 0.92;
      const reducedMotion =
        typeof window.matchMedia === "function" &&
        window.matchMedia("(prefers-reduced-motion: reduce)").matches;
      window.scrollTo({
        top: targetTop,
        behavior: reducedMotion ? "auto" : "smooth",
      });
    }
  }

  if (user && !loading) return <Navigate to="/app" replace />;
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
              在同一个空间
              <span className="hero-title-ending">
                <span className="hand-underline">做成事</span>
                <span className="purple">。</span>
              </span>
            </h1>
            <p>
              少一点工具切换，多一点心有灵犀。
              <br />
              让频道讨论、小组专项与专属 Agent，在 Teamie 自然配合。
            </p>
            <div className="hero-actions">
              <Link to="/login" className="button">
                登录 Teamie
                <ArrowRight size={ICON.md} />
              </Link>
              <Link className="landing-register" to="/login?mode=register">
                凭邀请码注册 <ChevronRight size={ICON.sm} />
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
                    <Hash size={ICON.xs} />
                    general
                  </span>
                  <span className="demo-channel">
                    <Hash size={ICON.xs} />
                    设计灵感
                  </span>
                  <small>小组与私信</small>
                  <span className="demo-channel">
                    <Users size={ICON.xs} />
                    前端体验专项
                  </span>
                  <span className="demo-channel">
                    <Avatar name="林" size="small" />
                    林小满
                  </span>
                  <span className="demo-channel">
                    <Bot size={ICON.sm} />
                    产品伙伴
                  </span>
                </div>
                <div className="demo-chat">
                  <div className="demo-chat-heading">
                    <Hash size={ICON.sm} />
                    <b>general</b>
                    <span className="demo-topic">日常灵感与版本节奏</span>
                    <Users size={ICON.sm} />
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
                      <Bot size={ICON.md} />
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
                    <SendHorizontal size={ICON.sm} />
                  </div>
                </div>
              </div>
              <div className="demo-label">Teamie 实时会话示意</div>
            </div>
            <div className="floating-note">
              <div className="floating-icon">
                <Sparkles size={ICON.md} />
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
                        aria-hidden={!isActive}
                      >
                        <span className="story-scene-eyebrow">{scene.eyebrow}</span>
                        <h3>{scene.title}</h3>
                        <p>{scene.description}</p>
                        <span className="story-scene-footnote">{scene.footnote}</span>
                      </article>
                    );
                  })}
                </div>

                <div
                  className="story-controls"
                  role="group"
                  aria-label="产品特性分幕切换"
                >
                  {STORY_SCENES.map((scene, index) => (
                    <button
                      key={scene.step}
                      type="button"
                      aria-pressed={index === activeScene}
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
                  {stickyStory ? "继续滑动切换下一幕" : "点击上方标签切换下一幕"} ·{" "}
                  {currentScene.step} / 0{STORY_SCENES.length}
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
                      <MessagesSquare size={ICON.lg} />
                    </span>
                    <div>
                      <b>公开频道与私密小组</b>
                      <small>产品研发空间 · 实时同步</small>
                    </div>
                    <span className="stage-pill lilac">01 / 04</span>
                  </div>
                  <div className="stage-channel-list">
                    <div className="stage-channel-row active">
                      <Hash size={ICON.sm} />
                      <div>
                        <b>general</b>
                        <small>全员公开频道 · 沉淀版本节奏与团队共识</small>
                      </div>
                      <span>24 人</span>
                    </div>
                    <div className="stage-channel-row">
                      <Users size={ICON.sm} />
                      <div>
                        <b>前端体验专项</b>
                        <small>受控私密小组 · 聚焦专项议题与设计走查</small>
                      </div>
                      <span>6 人</span>
                    </div>
                    <div className="stage-channel-row">
                      <Heart size={ICON.sm} />
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
                      <Bot size={ICON.lg} />
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
                        <Bot size={ICON.sm} />
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
                      <Bell size={ICON.lg} />
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
                      <CornerUpLeft size={ICON.sm} />
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
                      <Bot size={ICON.lg} />
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
                    <Lock size={ICON.xs} />
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
              <ArrowRight size={ICON.md} />
            </Link>
            <Link to="/login?mode=register" className="landing-register">
              凭邀请码注册 <ChevronRight size={ICON.sm} />
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
