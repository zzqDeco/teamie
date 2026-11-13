import { useLayoutEffect } from "react";
import { Link, Navigate } from "react-router-dom";
import {
  ArrowRight,
  Asterisk,
  Bot,
  ChevronRight,
  Hash,
  SendHorizontal,
  Sparkles,
  Users,
} from "lucide-react";
import { useAuth } from "./auth";
import { Avatar, Logo, PublicHeader } from "./ui";
import { ICON } from "./icons";
import { resetPageScroll } from "./public/scroll";
import { CollaborationStory } from "./public/collaboration-story";
export { resetPageScroll };

export function Landing() {
  const { user, loading } = useAuth();

  useLayoutEffect(() => {
    resetPageScroll();
  }, []);

  if (user && !loading) return <Navigate to="/app" replace />;

  return (
    <div className="landing">
      <PublicHeader />
      <main>
        <section className="hero">
          <div className="hero-copy">
            <h1>
              团队与 Agent，
              <br />
              在同一个空间
              <span className="hero-title-ending">
                <span className="hand-underline">做成事</span>
                <span className="purple">。</span>
              </span>
            </h1>
            <p>频道、小组与专属 Agent，在 Teamie 自然配合。</p>
            <div className="hero-actions">
              <Link to="/login" className="button">
                登录 Teamie
                <ArrowRight size={ICON.md} />
              </Link>
              <Link className="landing-register" to="/login?mode=register">
                凭邀请码注册 <ChevronRight size={ICON.sm} />
              </Link>
            </div>
          </div>
          <div className="hero-visual" role="img" aria-label="Teamie 团队沟通界面示意">
            <div className="visual-grid" />
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
                    <span className="avatar avatar-tone-1">
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
              <Sparkles className="note-spark" size={ICON.md} aria-hidden="true" />
            </div>
            <Asterisk className="hero-asterisk" size={ICON.xl} aria-hidden="true" />
          </div>
        </section>

        <CollaborationStory />

        <section className="landing-cta" aria-label="开始使用 Teamie">
          <div>
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
          <Asterisk className="cta-decoration" size={ICON.xl} aria-hidden="true" />
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
