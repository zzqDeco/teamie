import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import {
  ArrowLeft,
  Bot,
  Hash,
  Home,
  KeyRound,
  Menu,
  Plus,
  Settings,
  ShieldCheck,
  Users,
} from "lucide-react";
import type { Agent, Member, User, Workspace } from "@teamie/api-client";
import { ProfileSettings } from "./dialogs";
import { PlatformInvitations } from "./admin";
import { PlatformUsers } from "./platform-users";
import { ModelGatewaySettings } from "./model-gateway";
import { AgentDetail, MyAgents } from "./agents";
import { WorkspaceMembers } from "./members";
import type { Participant } from "./participants";
import { Button, IconButton, Modal, Notice } from "./ui";
import { ICON } from "./icons";

export function parseInviteCode(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed) return "";
  const match = trimmed.match(/\/join\/([^/?#\s]+)/i);
  if (match?.[1]) {
    try {
      return decodeURIComponent(match[1]);
    } catch {
      return match[1];
    }
  }
  if (trimmed.includes("://") || trimmed.includes("/")) return "";
  return trimmed;
}

export function useDocumentTitle(
  pageTitle: string,
  urgentCount: number,
  hasChannelUnread: boolean,
) {
  useEffect(() => {
    const base = pageTitle ? `${pageTitle} · Teamie` : "Teamie";
    const prefix =
      urgentCount > 0
        ? `(${urgentCount > 99 ? "99+" : urgentCount}) `
        : hasChannelUnread
          ? "• "
          : "";
    document.title = `${prefix}${base}`;
  }, [pageTitle, urgentCount, hasChannelUnread]);
  useEffect(
    () => () => {
      document.title = "Teamie";
    },
    [],
  );
}

export function useMediaQuery(query: string) {
  const [matches, setMatches] = useState(
    () => window.matchMedia(query).matches,
  );
  useEffect(() => {
    const media = window.matchMedia(query);
    const change = () => setMatches(media.matches);
    change();
    media.addEventListener("change", change);
    return () => media.removeEventListener("change", change);
  }, [query]);
  return matches;
}

export function ShellManagementView({
  user,
  isGatewayPage,
  isUsersPage,
  isAdminPage,
  isAgentsPage,
  isDirectoryPage,
  agentId,
  backChatRoute,
  workspace,
  members,
  workspaceAgents,
  membersLoading,
  membersLoadError,
  onOpenSidebar,
  onRetryMembers,
}: {
  user: User;
  isGatewayPage: boolean;
  isUsersPage: boolean;
  isAdminPage: boolean;
  isAgentsPage: boolean;
  isDirectoryPage: boolean;
  agentId?: string;
  backChatRoute: string;
  workspace?: Workspace;
  members: Participant[];
  workspaceAgents: Agent[];
  membersLoading: boolean;
  membersLoadError: boolean;
  onOpenSidebar: () => void;
  onRetryMembers: () => void;
}) {
  return (
    <>
      <header className="settings-header">
        <IconButton
          label="打开频道列表"
          className="mobile-menu"
          onClick={onOpenSidebar}
        >
          <Menu size={ICON.lg} />
        </IconButton>
        {isGatewayPage ? (
          <KeyRound size={ICON.md} />
        ) : isDirectoryPage || isUsersPage ? (
          <Users size={ICON.md} />
        ) : isAgentsPage ? (
          <Bot size={ICON.md} />
        ) : isAdminPage ? (
          <ShieldCheck size={ICON.md} />
        ) : (
          <Settings size={ICON.md} />
        )}
        <span>
          {isGatewayPage
            ? "模型网关"
            : isUsersPage
              ? "用户管理"
              : isAdminPage
                ? "注册邀请"
                : isAgentsPage
                  ? "个人 Agent"
                  : isDirectoryPage
                    ? "成员"
                    : "账号设置"}
        </span>
        <Link to={backChatRoute}>
          <ArrowLeft size={ICON.sm} />
          <span>对话</span>
        </Link>
      </header>
      {(isAdminPage || isGatewayPage || isUsersPage) &&
        user.platformRole === "admin" && (
          <nav className="admin-section-nav">
            <Link to="/app/admin" className={isAdminPage ? "active" : ""}>
              <ShieldCheck size={ICON.xs} />
              注册邀请
            </Link>
            <Link
              to="/app/admin/users"
              className={isUsersPage ? "active" : ""}
            >
              <Users size={ICON.xs} />
              用户管理
            </Link>
            <Link
              to="/app/admin/gateway"
              className={isGatewayPage ? "active" : ""}
            >
              <KeyRound size={ICON.xs} />
              模型网关
            </Link>
          </nav>
        )}
      {isUsersPage ? (
        <PlatformUsers />
      ) : isGatewayPage ? (
        <ModelGatewaySettings key={user.id} />
      ) : isAdminPage ? (
        <PlatformInvitations key={user.id} />
      ) : isAgentsPage ? (
        agentId ? (
          <AgentDetail key={`${user.id}:${agentId}`} agentId={agentId} />
        ) : (
          <MyAgents key={user.id} />
        )
      ) : isDirectoryPage ? (
        workspace ? (
          <WorkspaceMembers
            key={`${user.id}:${workspace.id}`}
            workspace={workspace}
            members={members}
            agents={workspaceAgents}
            loading={membersLoading}
            loadError={membersLoadError}
            onRetry={onRetryMembers}
          />
        ) : (
          <div className="unavailable-view">
            <h2>无法访问这个工作空间</h2>
            <p>查看成员需要当前空间成员权限。</p>
            <Link className="button" to="/app">
              返回对话
            </Link>
          </div>
        )
      ) : (
        <ProfileSettings />
      )}
    </>
  );
}

export function UnavailableConversationView({
  hasMatch,
  onOpenSidebar,
  onBack,
}: {
  hasMatch: boolean;
  onOpenSidebar: () => void;
  onBack: () => void;
}) {
  return (
    <div className="unavailable-view">
      <IconButton
        label="打开频道列表"
        className="mobile-menu"
        onClick={onOpenSidebar}
      >
        <Menu size={ICON.lg} />
      </IconButton>
      <span className="large-symbol">
        <Hash size={ICON.xl} />
      </span>
      <h2>{hasMatch ? "这个对话暂时无法访问" : "选择一个对话"}</h2>
      <p>
        {hasMatch
          ? "你可能已离开工作空间，或尚未加入这个对话。"
          : "打开对话列表，选择频道或私信。"}
      </p>
      <Button onClick={onBack}>返回对话</Button>
    </div>
  );
}

export function RemoveMemberModal({
  removing,
  removingFromGroup,
  error,
  busy,
  onClose,
  onConfirm,
}: {
  removing: Member;
  removingFromGroup: boolean;
  error: string;
  busy: boolean;
  onClose: () => void;
  onConfirm: () => void;
}) {
  return (
    <Modal
      title={`移除 ${removing.displayName}？`}
      subtitle={
        removingFromGroup
          ? "该成员将无法继续访问这个小组对话。工作空间成员资格不受影响。"
          : "该成员将无法继续访问这个工作空间及其中的对话。可以通过新邀请重新加入。"
      }
      onClose={onClose}
    >
      <Notice>{error}</Notice>
      <div className="modal-actions">
        <Button className="secondary" onClick={onClose}>
          取消
        </Button>
        <Button className="danger" busy={busy} onClick={onConfirm}>
          确认移除
        </Button>
      </div>
    </Modal>
  );
}

export function Welcome({
  userName,
  onCreate,
  onMenu,
}: {
  userName: string;
  onCreate: () => void;
  onMenu: () => void;
}) {
  const navigate = useNavigate();
  const [inviteInput, setInviteInput] = useState("");
  const inviteCode = parseInviteCode(inviteInput);
  function submitInvite(event: FormEvent) {
    event.preventDefault();
    if (!inviteCode) return;
    navigate(`/join/${encodeURIComponent(inviteCode)}`);
  }
  return (
    <div className="welcome-view">
      <header>
        <IconButton label="打开侧栏" className="mobile-menu" onClick={onMenu}>
          <Menu size={ICON.lg} />
        </IconButton>
        <span>
          <Home size={ICON.sm} />
          你的起点
        </span>
      </header>
      <div className="welcome-content">
        <div className="welcome-illustration">
          <span className="welcome-bubble">
            <MessagesSquareIcon />
          </span>
        </div>
        <h1>你好，{userName}</h1>
        <p>
          创建一个团队，开始协作。
          <br />
          已有团队邀请？输入邀请链接或邀请码即可加入。
        </p>
        <Button onClick={onCreate}>
          <Plus size={ICON.md} />
          创建工作空间
        </Button>
        <form className="welcome-join-form" onSubmit={submitInvite}>
          <input
            value={inviteInput}
            onChange={(event) => setInviteInput(event.target.value)}
            placeholder="粘贴邀请链接或输入邀请码"
            aria-label="邀请链接或邀请码"
          />
          <Button
            type="submit"
            className="secondary"
            disabled={!inviteCode}
          >
            加入团队
          </Button>
        </form>
      </div>
    </div>
  );
}

function MessagesSquareIcon() {
  return (
    <svg
      width="70"
      height="64"
      viewBox="0 0 70 64"
      fill="none"
      aria-hidden="true"
    >
      <path
        d="M12 12h39a9 9 0 0 1 9 9v20a9 9 0 0 1-9 9H33L21 60V50h-9a9 9 0 0 1-9-9V21a9 9 0 0 1 9-9z"
        fill="#fff"
      />
      <path
        d="M26 3h33a8 8 0 0 1 8 8v19a8 8 0 0 1-8 8h-2v8l-10-8H26a8 8 0 0 1-8-8V11a8 8 0 0 1 8-8z"
        fill="#d8efae"
        stroke="#7164d9"
        strokeWidth="3"
      />
      <circle cx="31" cy="21" r="2.7" fill="#7164d9" />
      <circle cx="42" cy="21" r="2.7" fill="#7164d9" />
      <circle cx="53" cy="21" r="2.7" fill="#7164d9" />
    </svg>
  );
}
