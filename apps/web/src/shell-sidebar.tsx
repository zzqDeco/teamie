import type { RefObject } from "react";
import { Link } from "react-router-dom";
import {
  AtSign,
  Bell,
  BellOff,
  Bot,
  ChevronDown,
  Hash,
  Plus,
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
import {
  chatStore,
  summarizeConversationUnread,
} from "./store";
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
  const summary = summarizeConversationUnread(
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
        <AgentAvatar size="small" />
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
      {summary.muted ? (
        <BellOff
          size={ICON.xs}
          className="conversation-muted-icon"
          aria-label="已静音"
        />
      ) : summary.badgeCount > 0 ? (
        <span
          className={`unread-badge ${summary.badgePrefix ? "mention-badge" : ""}`}
          aria-label={`${summary.badgeCount} 条未读`}
          title={conversation.type === "channel" && summary.projectionStale ? "截至最近同步的未读提醒" : undefined}
        >
          {summary.badgePrefix}
          {summary.badgeCount > 99 ? "99+" : summary.badgeCount}
        </span>
      ) : summary.level === "mentions" ? (
        <AtSign
          size={ICON.xs}
          className="conversation-mentions-icon"
          aria-label="仅提及通知"
        />
      ) : null}
    </Link>
  );
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
}) {
  const activeSnapshot = snapshot || chatStore.getSnapshot();
  const presenceEnabled = !!caps["im.presence.publish"];
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
                summarizeConversationUnread(
                  c,
                  activeSnapshot,
                  user.id,
                  attentionProjection,
                ).contributesToWorkspaceDot,
            );
            return (
              <button
                key={space.id}
                className={`workspace-button ${space.id === workspace?.id ? "selected" : ""}`}
                title={hasUnread ? `${space.name}（有未读消息）` : space.name}
                aria-label={
                  hasUnread
                    ? `打开工作空间 ${space.name}（有未读消息）`
                    : `打开工作空间 ${space.name}`
                }
                onClick={() => onSelectWorkspace(space)}
              >
                <span>{Array.from(space.name).slice(0, 2).join("")}</span>
                {space.id === workspace?.id && <i />}
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
          connection={connection}
          connectionLabel={connectionLabel}
          onSignOut={onSignOut}
          onNavigate={onCloseSidebar}
          onShortcuts={onShortcuts}
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
        <div className="sidebar-scroll">
          <div className="nav-section">
            <div className="nav-section-title">
              <span>
                <ChevronDown size={ICON.xs} /> 团队频道
              </span>
              {workspace && owner && caps["im.channel.create"] && (
                <IconButton
                  label="创建频道"
                  onClick={() => onOpenDialog("channel")}
                >
                  <Plus size={ICON.sm} />
                </IconButton>
              )}
            </div>
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
          </div>
          <div className="nav-section">
            <div className="nav-section-title">
              <span>
                <ChevronDown size={ICON.xs} /> 私信与小组
              </span>
            </div>
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
          </div>
        </div>
      </aside>
    </div>
  );
}

