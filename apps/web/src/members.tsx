import { useCallback, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import {
  ArrowRight,
  LoaderCircle,
  MessageCircle,
  Search,
  UserPlus,
  Users,
} from "lucide-react";
import { api } from "@teamie/api-client";
import type { Agent, Workspace } from "@teamie/api-client";
import { useAuth } from "./auth";
import { canManageAgent } from "./agent-management";
import { useChat } from "./chat";
import { errorText } from "./errors";
import { WorkspaceDialog } from "./dialogs";
import {
  AddParticipantToGroupDialog,
  ParticipantIdentity,
} from "./group-members";
import type { Participant } from "./participants";
import { chatStore } from "./store";
import {
  formatPresenceLabel,
  presenceClassName,
  usePresenceMap,
} from "./engagement-store";
import { BadgePill, Button, Notice } from "./ui";
import { ICON } from "./icons";

export function WorkspaceMembers({
  workspace,
  members,
  agents,
  loading,
  loadError,
  onRetry,
}: {
  workspace: Workspace;
  members: Participant[];
  agents: Agent[];
  loading: boolean;
  loadError: boolean;
  onRetry: () => void;
}) {
  const { user } = useAuth();
  const { data, refresh } = useChat();
  const presenceEnabled = !!data?.capabilities["im.presence.publish"];
  const presenceMap = usePresenceMap(presenceEnabled);
  const navigate = useNavigate();
  const owner = workspace.createdBy === user!.id;
  const [search, setSearch] = useState("");
  const [busyId, setBusyId] = useState("");
  const [error, setError] = useState("");
  const [inviting, setInviting] = useState(false);
  const [groupMemberId, setGroupMemberId] = useState("");
  const [newGroupMemberId, setNewGroupMemberId] = useState("");
  const closeInviting = useCallback(() => setInviting(false), []);
  const closeGroup = useCallback(() => setGroupMemberId(""), []);
  const closeNewGroup = useCallback(() => setNewGroupMemberId(""), []);
  const eligibleMember = (id: string) =>
    members.find(
      (member) => member.userId === id && member.selectable !== false,
    );
  const groupMember = eligibleMember(groupMemberId);
  const newGroupMember = eligibleMember(newGroupMemberId);
  const term = search.trim().toLocaleLowerCase();
  const filtered = members.filter((member) =>
    [
      member.displayName,
      member.ownerName || "",
      member.kind === "agent" ? "agent" : "",
    ]
      .join(" ")
      .toLocaleLowerCase()
      .includes(term),
  );

  async function direct(member: Participant) {
    if (member.selectable === false || !data?.capabilities["im.dm.create"])
      return;
    setBusyId(member.userId);
    setError("");
    const generation = chatStore.generation;
    try {
      const result = await api.createConversation(workspace.id, {
        type: "direct",
        memberIds: [member.userId],
      });
      if (generation !== chatStore.generation) return;
      chatStore.addConversation(result.conversation);
      await refresh();
      if (generation !== chatStore.generation) return;
      navigate(`/app/w/${workspace.id}/c/${result.conversation.id}`);
    } catch (e) {
      if (generation === chatStore.generation) setError(errorText(e));
    } finally {
      setBusyId("");
    }
  }

  return (
    <div className="members-view">
      <header className="members-heading">
        <div>
          <h1>
            成员 <span>{members.length}</span>
          </h1>
          <p>{workspace.name}</p>
        </div>
        {owner ? (
          <Button onClick={() => setInviting(true)}>
            <UserPlus size={ICON.sm} /> 邀请成员
          </Button>
        ) : (
          <span className="members-invite-hint">请联系空间创建者邀请</span>
        )}
      </header>
      <label className="member-search">
        <Search size={ICON.md} aria-hidden="true" />
        <span className="sr-only">搜索成员</span>
        <input
          type="search"
          value={search}
          onChange={(event) => setSearch(event.target.value)}
          placeholder="搜索名称或 Agent 创建者"
          maxLength={200}
        />
      </label>
      <Notice>{error}</Notice>
      {loadError && (
        <div className="member-load-error">
          <Notice>部分成员暂时无法加载，请重试。</Notice>
          <Button className="secondary" onClick={onRetry}>
            重新加载
          </Button>
        </div>
      )}
      {loading && !members.length ? (
        <div className="members-empty" role="status">
          <LoaderCircle className="spin" size={ICON.xl} />
          <p>正在加载成员…</p>
        </div>
      ) : filtered.length ? (
        <ul className="member-directory-list" aria-label="团队成员">
          {filtered.map((member) => {
            const agent = agents.find(
              (item) => item.principalId === member.userId,
            );
            const self = member.userId === user!.id;
            const humanPresence =
              presenceEnabled && member.kind === "human"
                ? presenceMap[member.userId] || "offline"
                : null;
            return (
              <li key={member.userId} className="member-directory-row">
                <div className="member-directory-identity">
                  <ParticipantIdentity
                    member={member}
                    badges={
                      <>
                        {self && (
                          <BadgePill className="member-self-label">
                            你
                          </BadgePill>
                        )}
                        {humanPresence && (
                          <BadgePill
                            className={`member-presence-badge ${presenceClassName(humanPresence)}`}
                            ariaLabel={`在线状态：${formatPresenceLabel(humanPresence)}`}
                          >
                            <i
                              className={`presence-dot ${presenceClassName(humanPresence)}`}
                              aria-hidden="true"
                            />
                            {formatPresenceLabel(humanPresence)}
                          </BadgePill>
                        )}
                        {member.kind === "agent" &&
                          member.selectable === false && (
                            <BadgePill className="member-unavailable">
                              {agent && !agent.enabled ? "已停用" : "暂不可添加"}
                            </BadgePill>
                          )}
                      </>
                    }
                  />
                </div>
                <div className="member-directory-actions">
                  {!self && data?.capabilities["im.dm.create"] && (
                    <Button
                      className="secondary"
                      disabled={member.selectable === false || !!busyId}
                      busy={busyId === member.userId}
                      aria-label={`与 ${member.displayName} 发起私聊`}
                      onClick={() => void direct(member)}
                    >
                      <MessageCircle size={ICON.sm} /> 私聊
                    </Button>
                  )}
                  {!self && data?.capabilities["im.group.create"] && (
                    <Button
                      className="secondary"
                      disabled={member.selectable === false || !!busyId}
                      aria-label={`将 ${member.displayName} 添加到小组`}
                      onClick={() => setGroupMemberId(member.userId)}
                    >
                      <Users size={ICON.sm} /> 添加到小组
                    </Button>
                  )}
                  {agent && canManageAgent(user, agent) && (
                    <Link
                      to={`/app/agents/${agent.id}`}
                      className="member-manage-link"
                      aria-label={`管理 ${member.displayName}`}
                    >
                      管理 <ArrowRight size={ICON.xs} />
                    </Link>
                  )}
                </div>
              </li>
            );
          })}
        </ul>
      ) : (
        <div className="members-empty" role="status">
          <Users size={ICON.xl} />
          <h2>{search ? "没有找到成员" : "暂无成员"}</h2>
          {search && <p>试试其他名称或 Agent 创建者。</p>}
        </div>
      )}
      {groupMember && data?.capabilities["im.group.create"] && (
        <AddParticipantToGroupDialog
          member={groupMember}
          workspace={workspace}
          onClose={closeGroup}
          onCreateGroup={() => {
            setNewGroupMemberId(groupMember.userId);
            setGroupMemberId("");
          }}
        />
      )}
      {newGroupMember && data?.capabilities["im.group.create"] && (
        <WorkspaceDialog
          kind="group"
          workspace={workspace}
          initialMemberIds={[newGroupMember.userId]}
          onClose={closeNewGroup}
        />
      )}
      {inviting && owner && (
        <WorkspaceDialog
          kind="invite"
          workspace={workspace}
          onClose={closeInviting}
        />
      )}
    </div>
  );
}
