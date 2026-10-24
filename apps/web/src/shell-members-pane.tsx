import type { RefObject } from "react";
import { Link } from "react-router-dom";
import {
  LoaderCircle,
  MessageCircle,
  Plus,
  ShieldCheck,
  UserPlus,
  Users,
  X,
} from "lucide-react";
import type { Conversation, Member, Workspace } from "@teamie/api-client";
import { AgentAvatar, AgentBadge } from "./agent-ui";
import type { Participant } from "./participants";
import { conversationName } from "./messages";
import { Avatar, IconButton, Notice } from "./ui";
import { ICON } from "./icons";

export function ShellMembersPane({
  membersRef,
  overlayMembers,
  mobile,
  sidebar,
  workspace,
  conversation,
  userId,
  owner,
  caps,
  busy,
  members,
  visibleMembers,
  isLoading,
  hasError,
  onClose,
  onStartDirect,
  onRequestRemove,
  onManageGroup,
  onInvite,
}: {
  membersRef: RefObject<HTMLElement | null>;
  overlayMembers: boolean;
  mobile: boolean;
  sidebar: boolean;
  workspace: Workspace;
  conversation: Conversation;
  userId: string;
  owner: boolean;
  caps: Record<string, boolean>;
  busy: boolean;
  members: Participant[];
  visibleMembers: Participant[];
  isLoading: boolean;
  hasError: boolean;
  onClose: () => void;
  onStartDirect: (member: Participant) => void;
  onRequestRemove: (member: Member, fromGroup: boolean) => void;
  onManageGroup: () => void;
  onInvite: () => void;
}) {
  return (
    <>
      {overlayMembers && !sidebar && (
        <button
          className="members-shade"
          aria-label="关闭成员列表"
          onClick={onClose}
        />
      )}
      <aside
        className="members-pane"
        ref={membersRef}
        role={overlayMembers ? "dialog" : undefined}
        aria-modal={overlayMembers ? true : undefined}
        aria-label="对话成员"
        tabIndex={-1}
        inert={mobile && sidebar}
      >
        <header>
          <span>
            对话成员 <b>{visibleMembers.length}</b>
          </span>
          <IconButton label="关闭成员列表" onClick={onClose}>
            <X size={ICON.md} />
          </IconButton>
        </header>
        <div className="members-description">
          <span className="member-cover">
            <Users size={ICON.xl} />
          </span>
          <h3>
            {conversation
              ? conversationName(conversation, userId, members)
              : workspace.name}
          </h3>
        </div>
        <div className="members-list">
          <span className="members-list-label">
            {conversation.type === "channel"
              ? "工作空间成员"
              : "当前对话成员"}
          </span>
          {isLoading && (
            <div className="member-loading">
              <LoaderCircle size={ICON.sm} className="spin" />
              加载成员…
            </div>
          )}
          {hasError && <Notice>成员暂时无法加载。</Notice>}
          {visibleMembers.map((member) => (
            <div className="member-row" key={member.userId}>
              {member.kind === "agent" ? (
                <AgentAvatar />
              ) : (
                <Avatar name={member.displayName} seed={member.userId} />
              )}
              <div>
                <b>
                  {member.displayName}
                  {member.userId === userId && <small>你</small>}
                  {member.kind === "agent" && (
                    <AgentBadge compact ownerName={member.ownerName} />
                  )}
                </b>
                <span>
                  {member.kind === "agent"
                    ? member.ownerName
                      ? `由 ${member.ownerName} 创建`
                      : "Agent"
                    : member.role === "owner"
                      ? "空间所有者"
                      : "团队成员"}
                </span>
              </div>
              {member.role === "owner" && (
                <ShieldCheck size={ICON.xs} className="owner-shield" />
              )}
              {member.userId !== userId &&
                caps["im.dm.create"] &&
                member.selectable !== false && (
                  <IconButton
                    className="member-dm"
                    label={`与 ${member.displayName} 发起私聊`}
                    disabled={busy}
                    onClick={() => onStartDirect(member)}
                  >
                    <MessageCircle size={ICON.xs} />
                  </IconButton>
                )}
              {((conversation.type === "group" &&
                conversation.canManageMembers) ||
                (conversation.type === "channel" &&
                  owner &&
                  member.role !== "owner")) &&
                member.userId !== userId && (
                  <IconButton
                    className="remove-member"
                    label={`${conversation.type === "group" ? "从小组" : "从空间"}移除 ${member.displayName}`}
                    onClick={() =>
                      onRequestRemove(member, conversation.type === "group")
                    }
                  >
                    <X size={ICON.xs} />
                  </IconButton>
                )}
            </div>
          ))}
        </div>
        <Link
          className="members-directory-link"
          to={`/app/w/${workspace.id}/members`}
          onClick={onClose}
        >
          <Users size={ICON.sm} />
          查看全部成员
        </Link>
        {conversation.type === "group" && conversation.canManageMembers && (
          <button className="members-invite" onClick={onManageGroup}>
            <Users size={ICON.sm} />
            管理小组成员
            <Plus size={ICON.xs} />
          </button>
        )}
        {owner && conversation.type === "channel" && (
          <button className="members-invite" onClick={onInvite}>
            <UserPlus size={ICON.sm} />
            邀请成员
            <Plus size={ICON.xs} />
          </button>
        )}
      </aside>
    </>
  );
}
