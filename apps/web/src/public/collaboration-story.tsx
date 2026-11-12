import { useEffect, useRef, useState } from "react";
import {
  Bell,
  Bot,
  CornerUpLeft,
  Hash,
  Heart,
  Lock,
  MessagesSquare,
  Users,
} from "lucide-react";
import { Avatar } from "../ui";
import { BREAKPOINTS, MAX_WIDTH_QUERY } from "../breakpoints";
import { ICON } from "../icons";

const STORY_SCENES = [
  {
    step: "01",
    label: "频道与小组",
    title: "对的讨论，在对的地方。",
    description: "公开频道沉淀共识，私密小组聚焦项目，让讨论各有位置。",
  },
  {
    step: "02",
    label: "专属 Agent",
    title: "每位成员，都有专属伙伴。",
    description: "把专属 Agent 带入团队，随时 @提及或私信，像同事一样自然配合。",
  },
  {
    step: "03",
    label: "收件箱溯源",
    title: "暂时离开，也能轻松接上。",
    description: "提及与回复汇入收件箱，沿着引用，随时回到原讨论。",
  },
  {
    step: "04",
    label: "权限边界",
    title: "像邀请一位新队友，安静可靠。",
    description: "管理 Agent 的名字、简介与可见空间，让协作始终遵循成员权限。",
  },
] as const;

function isStickyStoryActive() {
  if (typeof window === "undefined") return false;
  if (
    typeof window.matchMedia === "function" &&
    window.matchMedia(`${MAX_WIDTH_QUERY.md}, (max-height: 700px)`).matches
  )
    return false;
  return window.innerWidth > BREAKPOINTS.md && window.innerHeight > 700;
}

function StoryStage({ activeScene }: { activeScene: number }) {
  return (
    <div className="story-stage-canvas" role="img" aria-label="Teamie 分幕场景演示">
      {/* Scene 0: Channels & Groups */}
      <div
        className={`story-stage-card ${activeScene === 0 ? "active" : ""}`}
        aria-hidden={activeScene !== 0}
      >
        <div className="stage-card-head">
          <span className="value-icon lilac" aria-hidden="true">
            <MessagesSquare size={ICON.lg} aria-hidden="true" />
          </span>
          <div>
            <b>公开频道与私密小组</b>
            <small>产品研发空间 · 实时同步</small>
          </div>
        </div>
        <div className="stage-channel-list">
          <div className="stage-channel-row active">
            <Hash size={ICON.sm} aria-hidden="true" />
            <div>
              <b>general</b>
              <small>全员公开频道 · 沉淀版本节奏与团队共识</small>
            </div>
            <span>24 人</span>
          </div>
          <div className="stage-channel-row">
            <Users size={ICON.sm} aria-hidden="true" />
            <div>
              <b>前端体验专项</b>
              <small>受控私密小组 · 聚焦专项议题与设计走查</small>
            </div>
            <span>6 人</span>
          </div>
          <div className="stage-channel-row">
            <Heart size={ICON.sm} aria-hidden="true" />
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
          <span className="value-icon mint" aria-hidden="true">
            <Bot size={ICON.lg} aria-hidden="true" />
          </span>
          <div>
            <b>人类与 Agent 同场协作</b>
            <small># general · 实时对话流</small>
          </div>
        </div>
        <div className="stage-dialogue">
          <div className="stage-msg">
            <Avatar name="林" />
            <div>
              <b>林小满 <small>10:24</small></b>
              <p>@产品伙伴 帮大家把今天讨论的视觉微调与验收重点整理成清单吧。</p>
            </div>
          </div>
          <div className="stage-msg agent">
            <span className="avatar avatar-tone-1" aria-hidden="true"><Bot size={ICON.sm} aria-hidden="true" /></span>
            <div>
              <b>产品伙伴 <small className="demo-agent-label">Agent · 由林小满创建</small></b>
              <p>好的，已整理好 3 项核心验收重点，并附上了原讨论消息引用，方便随时核对。</p>
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
          <span className="value-icon peach" aria-hidden="true"><Bell size={ICON.lg} aria-hidden="true" /></span>
          <div>
            <b>提及与回复收件箱</b>
            <small>跨工作空间聚合 · 一键定位原消息</small>
          </div>
        </div>
        <div className="stage-inbox-items">
          <div className="stage-inbox-item">
            <span className="stage-unread-dot" aria-hidden="true" />
            <div>
              <small># general · 刚刚</small>
              <b>周一 提及了你</b>
              <p>“@林小满 新版长页的锁定滑动效果已经就绪，可以预览验收了。”</p>
            </div>
          </div>
          <div className="stage-quote-jump">
            <CornerUpLeft size={ICON.sm} aria-hidden="true" />
            <span>原消息引用 · 验收清单</span>
          </div>
        </div>
      </div>

      {/* Scene 3: Trust & Boundaries */}
      <div
        className={`story-stage-card ${activeScene === 3 ? "active" : ""}`}
        aria-hidden={activeScene !== 3}
      >
        <div className="spotlight-card-header">
          <span className="avatar avatar-tone-1" aria-hidden="true"><Bot size={ICON.lg} aria-hidden="true" /></span>
          <div><b>产品伙伴</b><small>Agent · 由林小满管理</small></div>
          <span className="spotlight-status">已启用</span>
        </div>
        <p className="stage-agent-bio">
          “负责整理版本纪要、汇总验收清单，并随时响应频道内的上下文提问。”
        </p>
        <div className="spotlight-spaces">
          <small>已授权工作空间</small>
          <div><span>产品研发空间</span><span>设计中心</span></div>
        </div>
        <div className="spotlight-foot">
          <Lock size={ICON.xs} aria-hidden="true" />
          <span>仅访问已加入的频道与小组 · 与成员共享同一套权限边界</span>
        </div>
      </div>
    </div>
  );
}

export function CollaborationStory() {
  const trackRef = useRef<HTMLElement>(null);
  const [activeScene, setActiveScene] = useState(0);
  const [scrollProgress, setScrollProgress] = useState(0);

  useEffect(() => {
    function updateScrollStage() {
      const sticky = isStickyStoryActive();
      const track = trackRef.current;
      if (!track || !sticky) return;
      const rect = track.getBoundingClientRect();
      const scrollable = rect.height - window.innerHeight;
      if (scrollable <= 0) return;
      const raw = Math.min(1, Math.max(0, -rect.top / scrollable));
      setScrollProgress(raw);
      setActiveScene(Math.min(
        STORY_SCENES.length - 1,
        Math.floor(raw * STORY_SCENES.length),
      ));
    }
    let pending = false;
    let rafId: number | null = null;
    function scheduleScrollStage() {
      if (pending) return;
      pending = true;
      rafId = window.requestAnimationFrame(() => {
        pending = false;
        rafId = null;
        updateScrollStage();
      });
    }
    updateScrollStage();
    window.addEventListener("scroll", scheduleScrollStage, { passive: true });
    window.addEventListener("resize", scheduleScrollStage, { passive: true });
    return () => {
      if (pending && rafId !== null) window.cancelAnimationFrame(rafId);
      window.removeEventListener("scroll", scheduleScrollStage);
      window.removeEventListener("resize", scheduleScrollStage);
    };
  }, []);

  function selectScene(index: number) {
    setActiveScene(index);
    setScrollProgress((index + 0.5) / STORY_SCENES.length);
    const track = trackRef.current;
    if (!track || !isStickyStoryActive()) return;
    const rect = track.getBoundingClientRect();
    const scrollable = Math.max(0, rect.height - window.innerHeight);
    if (scrollable > 0 && typeof window.scrollTo === "function") {
      const targetTop = window.scrollY + rect.top +
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

  return (
    <section
      ref={trackRef}
      className="scroll-story-track"
      aria-labelledby="features-heading"
      data-active-scene={activeScene}
    >
      <div className="scroll-story-sticky">
        <div className="scroll-story-inner">
          <div className="story-copy-column">
            <h2 id="features-heading" className="sr-only">Teamie 产品特性</h2>
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
                    <h3>{scene.title}</h3>
                    <p>{scene.description}</p>
                  </article>
                );
              })}
            </div>
            <div className="story-controls" role="group" aria-label="产品特性分幕切换">
              {STORY_SCENES.map((scene, index) => (
                <button
                  key={scene.step}
                  type="button"
                  aria-pressed={index === activeScene}
                  className={`story-step-pill ${index === activeScene ? "active" : ""}`}
                  onClick={() => selectScene(index)}
                >
                  <b>{scene.step}</b><span>{scene.label}</span>
                </button>
              ))}
            </div>
            <div className="story-progress-bar" aria-hidden="true">
              <span style={{
                width: `${Math.max(
                  ((activeScene + 1) / STORY_SCENES.length) * 100,
                  Math.round(scrollProgress * 100),
                )}%`,
              }} />
            </div>
          </div>
          <StoryStage activeScene={activeScene} />
        </div>
      </div>
    </section>
  );
}
