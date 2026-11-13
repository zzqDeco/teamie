import { useEffect, useId, useRef, useState, useSyncExternalStore } from "react";
import type { ReactNode, RefObject } from "react";
import { Link } from "react-router-dom";
import {
  Bell,
  Blocks,
  Bot,
  ChevronDown,
  Hash,
  Megaphone,
  PencilLine,
  Plus,
  Search,
  Users,
  X,
} from "lucide-react";
import type {
  AttentionUnreadDTO,
  Conversation,
  Member,
  User,
  Workspace,
} from "@teamie/api-client";
import { useAuth } from "./auth";
import type { Connection } from "./chat";
import { AccountMenu } from "./account-menu";
import { ConversationActions } from "./conversation-actions";
import { AgentAvatar, AgentBadge } from "./agent-ui";
import type { DialogKind } from "./dialogs";
import { conversationName } from "./messages";
import { readDraftPresence, subscribeDraftPresence } from "./composer-drafts";
import { chatStore } from "./store";
import { conversationUnreadSummary, UnreadBadge } from "./unread-badge";
import type { ChatSnapshot } from "./store";
import {
  formatPresenceLabel,
  presenceClassName,
  useUserPresence,
} from "./engagement-store";
import { Avatar, IconButton, Logo } from "./ui";
import { ICON } from "./icons";
import {
  computePageTitle,
  computeShellAttention,
  pickAdjacentConversation,
  resolveWorkspaceTarget,
  route,
  ShellManagementView,
  useShellViewportAndFocus,
} from "./shell/shell-navigation-helpers";
export {
  computePageTitle,
  computeShellAttention,
  pickAdjacentConversation,
  resolveWorkspaceTarget,
  route,
  ShellManagementView,
  useShellViewportAndFocus,
};

function ConversationItem({
  conversation,
  selected,
  members,
  snapshot,
  attentionProjection,
  presenceEnabled,
  onClick,
}: {
  conversation: Conversation;
  selected: boolean;
  members: Member[];
  snapshot?: ChatSnapshot;
  attentionProjection?: ReadonlyArray<AttentionUnreadDTO>;
  presenceEnabled?: boolean;
  onClick: () => void;
}) {
  const { user } = useAuth();
  const activeSnapshot = snapshot || chatStore.getSnapshot();
  const summary = conversationUnreadSummary(
    conversation,
    activeSnapshot,
    user!.id,
    attentionProjection,
  );
  const otherMember = members.find(
    (member) =>
      member.userId !== user!.id &&
      conversation.memberIds.includes(member.userId),
  );
  const otherHumanId =
    conversation.type === "direct" && otherMember?.kind === "human"
      ? otherMember.userId
      : undefined;
  const presenceState = useUserPresence(otherHumanId, !!presenceEnabled);
  const hasDraft = useSyncExternalStore(subscribeDraftPresence, readDraftPresence).has(
    conversation.id,
  );
  return (
    <Link
      to={route(conversation)}
      onClick={onClick}
      className={`conversation-link ${selected ? "active" : ""} ${summary.hasUnread ? "has-unread" : ""} ${summary.muted ? "is-muted" : ""}`}
      aria-current={selected ? "page" : undefined}
    >
      {conversation.type === "channel" ? (
        <Hash size={ICON.md} />
      ) : conversation.type === "group" ? (
        <span className="group-avatar">
          <Users size={ICON.sm} />
        </span>
      ) : otherMember?.kind === "agent" ? (
        <AgentAvatar size="small" seed={otherMember.userId} />
      ) : presenceEnabled && otherHumanId ? (
        <div className="avatar-with-presence">
          <Avatar
            size="small"
            name={conversationName(conversation, user!.id, members)}
            seed={conversation.memberIds.find((id) => id !== user!.id)}
          />
          <i
            className={`presence-dot ${presenceClassName(presenceState)}`}
            title={formatPresenceLabel(presenceState)}
            aria-label={formatPresenceLabel(presenceState)}
          />
        </div>
      ) : (
        <Avatar
          size="small"
          name={conversationName(conversation, user!.id, members)}
          seed={conversation.memberIds.find((id) => id !== user!.id)}
        />
      )}
      <span>{conversationName(conversation, user!.id, members)}</span>
      {conversation.type === "direct" && otherMember?.kind === "agent" && (
        <AgentBadge compact ownerName={otherMember.ownerName} />
      )}
      {hasDraft && !selected && (
        <span className="conversation-draft-marker" title="有未发送的草稿">
          <PencilLine size={ICON.xs} aria-hidden="true" />
          草稿
        </span>
      )}
      <UnreadBadge summary={summary} conversationType={conversation.type} />
    </Link>
  );
}

const sectionStorageKey = (userId: string) => `teamie.sidebarSections.${userId}`;

function readCollapsedSections(userId: string): Record<string, boolean> {
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(sectionStorageKey(userId)) || "{}");
    return parsed && typeof parsed === "object" ? (parsed as Record<string, boolean>) : {};
  } catch {
    return {};
  }
}

function writeCollapsedSection(userId: string, section: string, collapsed: boolean) {
  try {
    const next = { ...readCollapsedSections(userId), [section]: collapsed };
    localStorage.setItem(sectionStorageKey(userId), JSON.stringify(next));
  } catch {
    /* Ignore storage access errors. */
  }
}

function ConversationSection({
  title,
  sectionId,
  userId,
  count,
  hasUnread,
  selectedConversationId,
  action,
  children,
}: {
  title: string;
  sectionId: string;
  userId: string;
  count: number;
  hasUnread: boolean;
  selectedConversationId?: string;
  action?: ReactNode;
  children: ReactNode;
}) {
  const [expanded, setExpanded] = useState(
    () => readCollapsedSections(userId)[sectionId] !== true,
  );
  const id = useId();

  useEffect(() => {
    if (selectedConversationId) setExpanded(true);
  }, [selectedConversationId]);

  return (
    <section className="nav-section" aria-label={title}>
      <div className="nav-section-title">
        <button
          type="button"
          className="nav-section-toggle"
          aria-label={`${title}${hasUnread ? "（有未读消息）" : ""}`}
          aria-expanded={expanded}
          aria-controls={id}
          onClick={() => {
            writeCollapsedSection(userId, sectionId, expanded);
            setExpanded(!expanded);
          }}
        >
          <ChevronDown size={ICON.xs} aria-hidden="true" />
          <span>{title}</span>
          <span className="nav-section-count" aria-hidden="true">{count}</span>
          {hasUnread && <i className="nav-section-unread" aria-hidden="true" />}
        </button>
        {action}
      </div>
      <div id={id} hidden={!expanded}>{children}</div>
    </section>
  );
}

/**
 * Number of visible unread conversation rows scrolled below the sidebar's
 * viewport, re-measured on scroll/resize and whenever `signature` changes.
 */
function useUnreadBelow(scrollRef: RefObject<HTMLDivElement | null>, signature: string) {
  const [below, setBelow] = useState(0);
  useEffect(() => {
    const scroller = scrollRef.current;
    if (!scroller) return;
    const measure = () => {
      if (!scroller.clientHeight) {
        setBelow(0);
        return;
      }
      const bottom = scroller.getBoundingClientRect().bottom;
      const hidden = Array.from(
        scroller.querySelectorAll<HTMLElement>(".conversation-link.has-unread"),
      ).filter(
        (link) => !link.closest("[hidden]") && link.getBoundingClientRect().top >= bottom - 4,
      );
      setBelow(hidden.length);
    };
    measure();
    scroller.addEventListener("scroll", measure, { passive: true });
    window.addEventListener("resize", measure);
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure);
    observer?.observe(scroller);
    return () => {
      scroller.removeEventListener("scroll", measure);
      window.removeEventListener("resize", measure);
      observer?.disconnect();
    };
  }, [scrollRef, signature]);
  return below;
}

function scrollToNextUnread(scroller: HTMLDivElement | null) {
  if (!scroller) return;
  const bottom = scroller.getBoundingClientRect().bottom;
  const next = Array.from(
    scroller.querySelectorAll<HTMLElement>(".conversation-link.has-unread"),
  ).find((link) => !link.closest("[hidden]") && link.getBoundingClientRect().top >= bottom - 4);
  next?.scrollIntoView?.({ block: "center", behavior: "smooth" });
  next?.focus({ preventScroll: true });
}

export function ShellSidebar({
  navigationRef,
  mobile,
  sidebar,
  hasOverlayModal,
  user,
  busy,
  connection,
  connectionLabel,
  isAttentionPage,
  isAgentsPage,
  isAccountPage = false,
  isAppsPage = false,
  attentionUnreadCount,
  attentionProjection,
  snapshot,
  workspaces,
  workspace,
  owner,
  caps,
  allAuthorizedConversations,
  channels,
  direct,
  conversationId,
  members,
  onCloseSidebar,
  onSelectWorkspace,
  onOpenDialog,
  onSignOut,
  onShortcuts,
  onOpenQuickSwitcher,
  onOpenAnnouncements,
  hasUnreadAnnouncement = false,
}: {
  navigationRef: RefObject<HTMLDivElement | null>;
  mobile: boolean;
  sidebar: boolean;
  hasOverlayModal: boolean;
  user: User;
  busy: boolean;
  connection: Connection;
  connectionLabel: string;
  isAttentionPage: boolean;
  isAgentsPage: boolean;
  /** Settings, developer platform and admin pages (reached from the avatar menu). */
  isAccountPage?: boolean;
  isAppsPage?: boolean;
  attentionUnreadCount?: number;
  attentionProjection?: ReadonlyArray<AttentionUnreadDTO>;
  snapshot?: ChatSnapshot;
  workspaces: Workspace[];
  workspace?: Workspace;
  owner: boolean;
  caps: Record<string, boolean>;
  allAuthorizedConversations: Conversation[];
  channels: Conversation[];
  direct: Conversation[];
  conversationId?: string;
  members: Member[];
  onCloseSidebar: () => void;
  onSelectWorkspace: (space: Workspace) => void;
  onOpenDialog: (kind: DialogKind) => void;
  onSignOut: () => void;
  onShortcuts: () => void;
  onOpenQuickSwitcher: () => void;
  onOpenAnnouncements?: () => void;
  hasUnreadAnnouncement?: boolean;
}) {
  const activeSnapshot = snapshot || chatStore.getSnapshot();
  const presenceEnabled = !!caps["im.presence.publish"];
  const scrollRef = useRef<HTMLDivElement>(null);
  const unreadOf = (item: Conversation) =>
    conversationUnreadSummary(item, activeSnapshot, user.id, attentionProjection).hasUnread;
  const unreadSignature = [...channels, ...direct]
    .filter(unreadOf)
    .map((item) => item.id)
    .join("|");
  const unreadBelow = useUnreadBelow(scrollRef, `${workspace?.id}:${unreadSignature}`);
  return (
    <div
      className="app-navigation"
      ref={navigationRef}
      role={mobile && sidebar ? "dialog" : undefined}
      aria-modal={mobile && sidebar ? true : undefined}
      aria-label={mobile ? "对话与团队导航" : undefined}
      tabIndex={-1}
      inert={mobile && !sidebar}
    >
      <aside className="workspace-rail" aria-label="工作空间切换">
        <Link
          className="rail-home"
          to="/app"
          title="回到聊天"
          aria-label="Teamie 工作空间"
          onClick={onCloseSidebar}
        >
          <Logo compact />
        </Link>
        <div className="rail-divider" />
        <Link
          to="/app/attention"
          className={`rail-attention ${isAttentionPage ? "selected" : ""}`}
          title="提及与回复"
          aria-label="提及与回复"
          aria-current={isAttentionPage ? "page" : undefined}
          onClick={onCloseSidebar}
        >
          <Bell size={ICON.lg} />
          {!!attentionUnreadCount && (
            <span
              className="rail-attention-count"
              aria-label={`${attentionUnreadCount} 条未读提醒`}
            >
              {attentionUnreadCount > 99 ? "99+" : attentionUnreadCount}
            </span>
          )}
        </Link>
        <div className="workspace-buttons">
          {workspaces.map((space) => {
            const hasUnread = allAuthorizedConversations.some(
              (c) =>
                c.workspaceId === space.id &&
                conversationUnreadSummary(
                  c,
                  activeSnapshot,
                  user.id,
                  attentionProjection,
                ).contributesToWorkspaceDot,
            );
            // Account pages own the selection; the sidebar still shows the
            // current workspace so returning keeps its context.
            const selected = space.id === workspace?.id && !isAccountPage;
            return (
              <button
                key={space.id}
                className={`workspace-button ${selected ? "selected" : ""}`}
                aria-current={selected ? "true" : undefined}
                title={hasUnread ? `${space.name}（有未读消息）` : space.name}
                aria-label={
                  hasUnread
                    ? `打开工作空间 ${space.name}（有未读消息）`
                    : `打开工作空间 ${space.name}`
                }
                onClick={() => onSelectWorkspace(space)}
              >
                <span>{Array.from(space.name).slice(0, 2).join("")}</span>
                {selected && <i />}
                {hasUnread && (
                  <em className="workspace-unread-dot" aria-hidden="true" />
                )}
              </button>
            );
          })}
        </div>
        <IconButton
          label="创建工作空间"
          className="rail-add"
          onClick={() => onOpenDialog("workspace")}
        >
          <Plus size={ICON.xl} />
        </IconButton>
        <span className="rail-spacer" />
        {onOpenAnnouncements && (
          <button
            type="button"
            className="rail-settings rail-announcements"
            title={hasUnreadAnnouncement ? "平台公告（有新公告）" : "平台公告"}
            aria-label={
              hasUnreadAnnouncement ? "平台公告（有新公告）" : "平台公告"
            }
            onClick={() => {
              onCloseSidebar();
              onOpenAnnouncements();
            }}
          >
            <Megaphone size={ICON.lg} aria-hidden="true" />
            {hasUnreadAnnouncement && (
              <span className="rail-announcement-dot" aria-hidden="true" />
            )}
          </button>
        )}
        <Link
          to="/app/agents"
          className={`rail-settings rail-agents ${isAgentsPage ? "selected" : ""}`}
          title="个人 Agent"
          aria-label="个人 Agent"
          aria-current={isAgentsPage ? "page" : undefined}
          onClick={onCloseSidebar}
        >
          <Bot size={ICON.lg} aria-hidden="true" />
        </Link>
        <AccountMenu
          user={user}
          busy={busy}
          active={(!mobile || sidebar) && !hasOverlayModal}
          selected={isAccountPage}
          connection={connection}
          connectionLabel={connectionLabel}
          onSignOut={onSignOut}
          onNavigate={onCloseSidebar}
          onShortcuts={onShortcuts}
          onAnnouncements={onOpenAnnouncements}
          hasUnreadAnnouncement={hasUnreadAnnouncement}
        />
      </aside>
      <aside className="navigation-sidebar">
        <div className="workspace-header">
          <div className="workspace-title" title={workspace?.name}>
            <b>{workspace?.name || "我的 Teamie"}</b>
          </div>
          {workspace && (caps["im.dm.create"] || caps["im.group.create"]) && (
            <ConversationActions
              key={workspace.id}
              active={(!mobile || sidebar) && !hasOverlayModal}
              canDirect={!!caps["im.dm.create"]}
              canGroup={!!caps["im.group.create"]}
              onSelect={onOpenDialog}
            />
          )}
          <IconButton
            className="mobile-close"
            label="关闭频道列表"
            onClick={onCloseSidebar}
          >
            <X size={ICON.md} />
          </IconButton>
        </div>
        <button
          type="button"
          className="sidebar-quick-switcher"
          aria-label="搜索与快速切换"
          aria-haspopup="dialog"
          aria-keyshortcuts="Control+K Meta+K"
          onClick={onOpenQuickSwitcher}
        >
          <Search size={ICON.sm} aria-hidden="true" />
          <span>搜索与切换</span>
          <kbd aria-hidden="true">{/Mac|iPhone|iPad/.test(navigator.platform) ? "⌘ K" : "Ctrl K"}</kbd>
        </button>
        {workspace && (
          <Link
            to={`/app/w/${workspace.id}/apps`}
            className={`sidebar-page-link ${isAppsPage ? "active" : ""}`}
            aria-current={isAppsPage ? "page" : undefined}
            onClick={onCloseSidebar}
          >
            <Blocks size={ICON.sm} aria-hidden="true" />
            <span>应用</span>
          </Link>
        )}
        <div className="sidebar-scroll" ref={scrollRef}>
          <ConversationSection
            key={`${user.id}:${workspace?.id}:channels`}
            title="团队频道"
            sectionId="channels"
            userId={user.id}
            count={channels.length}
            hasUnread={channels.some(unreadOf)}
            selectedConversationId={channels.find((item) => item.id === conversationId)?.id}
            action={workspace && owner && caps["im.channel.create"] && (
              <IconButton label="创建频道" onClick={() => onOpenDialog("channel")}>
                <Plus size={ICON.sm} />
              </IconButton>
            )}
          >
            {channels.map((c) => (
              <ConversationItem
                key={c.id}
                conversation={c}
                selected={c.id === conversationId}
                members={members}
                snapshot={activeSnapshot}
                attentionProjection={attentionProjection}
                presenceEnabled={presenceEnabled}
                onClick={onCloseSidebar}
              />
            ))}
            {channels.length === 0 && (
              <div className="sidebar-empty">
                {workspace ? "暂无频道" : "暂无团队"}
              </div>
            )}
          </ConversationSection>
          <ConversationSection
            key={`${user.id}:${workspace?.id}:direct`}
            title="私信与小组"
            sectionId="direct"
            userId={user.id}
            count={direct.length}
            hasUnread={direct.some(unreadOf)}
            selectedConversationId={direct.find((item) => item.id === conversationId)?.id}
          >
            {direct.map((c) => (
              <ConversationItem
                key={c.id}
                conversation={c}
                selected={c.id === conversationId}
                members={members}
                snapshot={activeSnapshot}
                attentionProjection={attentionProjection}
                presenceEnabled={presenceEnabled}
                onClick={onCloseSidebar}
              />
            ))}
            {direct.length === 0 && (
              <div className="sidebar-empty">
                {workspace ? "暂无私信或小组" : "加入工作空间后可发起私信"}
                {workspace && caps["im.dm.create"] && (
                  <button
                    type="button"
                    className="sidebar-empty-action"
                    onClick={() => onOpenDialog("direct")}
                  >
                    <Plus size={ICON.xs} aria-hidden="true" />
                    发起私信
                  </button>
                )}
              </div>
            )}
          </ConversationSection>
          {unreadBelow > 0 && (
            <button
              type="button"
              className="sidebar-unread-below"
              onClick={() => scrollToNextUnread(scrollRef.current)}
            >
              <ChevronDown size={ICON.xs} aria-hidden="true" />
              下方有 {unreadBelow} 个未读
            </button>
          )}
        </div>
      </aside>
    </div>
  );
}

