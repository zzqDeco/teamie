import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Check, LoaderCircle, Plus, UserMinus, Users } from "lucide-react";
import { api } from "@teamie/api-client";
import type { Conversation, Member, Workspace } from "@teamie/api-client";
import { useAuth } from "./auth";
import { errorText, useChat, useChatState } from "./chat";
import { chatStore } from "./store";
import { AgentAvatar, AgentBadge } from "./agent-ui";
import { Avatar, Button, Modal, Notice } from "./ui";
import type { Participant } from "./participants";
export function ParticipantIdentity({ member }: { member: Member }) {
  return (
    <>
      {member.kind === "agent" ? (
        <AgentAvatar />
      ) : (
        <Avatar name={member.displayName} seed={member.userId} />
      )}
      <span className="participant-identity">
        <b>
          <span className="participant-name" title={member.displayName}>
            {member.displayName}
          </span>
          {member.kind === "agent" && (
            <AgentBadge compact ownerName={member.ownerName} />
          )}
        </b>
        <small>
          {member.kind === "agent"
            ? member.ownerName
              ? `由 ${member.ownerName} 创建`
              : "Agent"
            : member.role === "owner"
              ? "空间所有者"
              : "团队成员"}
        </small>
      </span>
    </>
  );
}
export function GroupMembersDialog({
  conversation,
  members,
  onClose,
}: {
  conversation: Conversation;
  members: Participant[];
  onClose: () => void;
}) {
  const { user } = useAuth();
  const { data, refresh } = useChat();
  const client = useQueryClient();
  const [busyId, setBusyId] = useState(""),
    [error, setError] = useState("");
  const current = members.filter((member) =>
    conversation.memberIds.includes(member.userId),
  );
  const available = members.filter(
    (member) =>
      member.userId !== user!.id &&
      !conversation.memberIds.includes(member.userId) &&
      member.selectable !== false,
  );
  const addAllowed = !!data?.capabilities["im.group.create"];
  async function change(member: Member, remove = false) {
    if (!conversation.canManageMembers) return;
    setBusyId(member.userId);
    setError("");
    const generation = chatStore.generation;
    try {
      const result = remove
        ? await api.removeConversationMember(conversation.id, member.userId)
        : await api.addConversationMember(conversation.id, member.userId);
      if (generation !== chatStore.generation) return;
      chatStore.addConversation(result.conversation);
      await Promise.all([
        refresh(),
        client.invalidateQueries({ queryKey: ["members"] }),
      ]);
    } catch (e) {
      if (generation === chatStore.generation) setError(errorText(e));
    } finally {
      setBusyId("");
    }
  }
  return (
    <Modal
      title="小组成员"
      subtitle="移出后，成员将无法访问此对话。"
      onClose={onClose}
    >
      <Notice>{error}</Notice>
      <div className="group-member-section">
        <h3>
          当前成员 <span>{current.length}</span>
        </h3>
        {current.map((member) => (
          <div key={member.userId} className="group-member-row">
            <ParticipantIdentity member={member} />
            {member.userId === user!.id ? (
              <span className="member-self-label">你</span>
            ) : (
              conversation.canManageMembers && (
                <button
                  className="group-member-remove"
                  disabled={!!busyId}
                  onClick={() => void change(member, true)}
                  title={`从小组移除 ${member.displayName}`}
                  aria-label={`从小组移除 ${member.displayName}`}
                >
                  {busyId === member.userId ? (
                    <LoaderCircle className="spin" size={14} />
                  ) : (
                    <UserMinus size={14} />
                  )}
                </button>
              )
            )}
          </div>
        ))}
      </div>
      {conversation.canManageMembers && addAllowed && (
        <div className="group-member-section">
          <h3>添加伙伴</h3>
          {available.length ? (
            available.map((member) => (
              <div key={member.userId} className="group-member-row">
                <ParticipantIdentity member={member} />
                <Button
                  className="secondary small-button"
                  busy={busyId === member.userId}
                  disabled={!!busyId}
                  onClick={() => void change(member)}
                >
                  <Plus size={12} />
                  添加
                </Button>
              </div>
            ))
          ) : (
            <div className="mini-empty">暂无可添加成员。</div>
          )}
        </div>
      )}
      <div className="modal-actions">
        <Button className="secondary" onClick={onClose}>
          完成
        </Button>
      </div>
    </Modal>
  );
}
export function AddParticipantToGroupDialog({
  member,
  workspace,
  onClose,
  onCreateGroup,
}: {
  member: Participant;
  workspace: Workspace;
  onClose: () => void;
  onCreateGroup: () => void;
}) {
  const { user } = useAuth();
  const { data, refresh } = useChat();
  const snapshot = useChatState();
  const [selected, setSelected] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const groups = Object.values(snapshot.conversations).filter(
    (c) =>
      c.workspaceId === workspace.id &&
      c.type === "group" &&
      c.canManageMembers &&
      c.memberIds.includes(user!.id),
  );
  const selectedGroup = groups.find(
    (group) =>
      group.id === selected && !group.memberIds.includes(member.userId),
  );
  const addAllowed =
    !!data?.capabilities["im.group.create"] && member.selectable !== false;
  async function add() {
    if (!selectedGroup || !addAllowed) return;
    setBusy(true);
    setError("");
    const generation = chatStore.generation;
    try {
      const result = await api.addConversationMember(
        selectedGroup.id,
        member.userId,
      );
      if (generation !== chatStore.generation) return;
      chatStore.addConversation(result.conversation);
      await refresh();
      onClose();
    } catch (e) {
      if (generation === chatStore.generation) setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      title={`将 ${member.displayName} 添加到小组`}
      subtitle="选择一个你管理的小组。"
      onClose={onClose}
    >
      <div className="adding-agent-summary">
        <ParticipantIdentity member={member} />
      </div>
      <Notice>{error}</Notice>
      <div className="agent-group-options">
        {groups.length ? (
          groups.map((group) => {
            const already = group.memberIds.includes(member.userId);
            return (
              <button
                className={`agent-group-option ${selected === group.id ? "selected" : ""}`}
                key={group.id}
                disabled={already || busy || !addAllowed}
                onClick={() => setSelected(group.id)}
              >
                <span className="group-avatar">
                  <Users size={17} />
                </span>
                <span>
                  {group.name || "小组对话"}
                  <small>
                    {already
                      ? "已在小组中"
                      : `${group.memberIds.length} 位成员`}
                  </small>
                </span>
                <i>{selected === group.id && <Check size={12} />}</i>
              </button>
            );
          })
        ) : (
          <div className="mini-empty">暂无可管理的小组。</div>
        )}
      </div>
      <button
        className="create-agent-group-link"
        disabled={!addAllowed || busy}
        onClick={onCreateGroup}
      >
        <Plus size={14} />
        创建新小组
      </button>
      <div className="modal-actions">
        <Button className="secondary" onClick={onClose}>
          取消
        </Button>
        <Button
          busy={busy}
          disabled={!selectedGroup || !addAllowed}
          onClick={() => void add()}
        >
          添加到小组
        </Button>
      </div>
    </Modal>
  );
}
