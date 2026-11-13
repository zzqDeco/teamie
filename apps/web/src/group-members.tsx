import { useState } from "react";
import type { ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Check, LoaderCircle, Plus, Trash2, UserMinus, Users } from "lucide-react";
import { api } from "@teamie/api-client";
import type { Conversation, Member, Workspace } from "@teamie/api-client";
import { useAuth } from "./auth";
import { useChat, useChatState } from "./chat";
import { errorText } from "./errors";
import { chatStore } from "./store";
import { AgentAvatar, AgentBadge } from "./agent-ui";
import { Avatar, Button, ConfirmDialog, Modal, ModalCloseButton, Notice } from "./ui";
import type { Participant } from "./participants";
import { ICON } from "./icons";
import { toast } from "./toast";
export function ParticipantIdentity({
  member,
  badges,
}: {
  member: Member;
  badges?: ReactNode;
}) {
  return (
    <>
      {member.kind === "agent" ? (
        <AgentAvatar seed={member.userId} />
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
          {badges}
        </b>
        <small>
          {member.kind === "agent"
            ? member.ownerName
              ? `由 ${member.ownerName} 创建`
              : "Agent"
            : member.role === "owner"
              ? "空间所有者"
              : "成员"}
        </small>
      </span>
    </>
  );
}
const DISSOLVE_GROUP_IMPACT = [
  "小组内的全部消息、上传文件、表情回应与置顶记录将被彻底清除",
  "所有成员在此小组中的收藏记录将被清除",
  "曾转发到其他频道或私信的快照与附件副本不受影响",
];

export function GroupExitModal({
  kind,
  conversation,
  userId,
  isWorkspaceOwner,
  onClose,
  onCompleted,
}: {
  kind: "leave" | "dissolve";
  conversation: Conversation;
  userId: string;
  isWorkspaceOwner: boolean;
  onClose: () => void;
  onCompleted: (kind: "leave" | "dissolve", conversationId: string) => Promise<void> | void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const isNonOwnerCreator = !isWorkspaceOwner && conversation.createdBy === userId;
  const groupTitle = conversation.name || "小组对话";

  async function confirm() {
    if (busy) return;
    setBusy(true);
    setError("");
    const generation = chatStore.generation;
    try {
      if (kind === "dissolve") {
        await api.deleteConversation(conversation.id);
      } else {
        await api.removeConversationMember(conversation.id, userId);
      }
      if (generation !== chatStore.generation) return;
      await onCompleted(kind, conversation.id);
    } catch (e) {
      if (generation === chatStore.generation) setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  if (kind === "dissolve") {
    return (
      <ConfirmDialog
        key={`dissolve-${conversation.id}`}
        tone="danger"
        title={`解散小组「${groupTitle}」？`}
        subtitle="解散后，该小组将被永久删除，所有成员均无法恢复。"
        impact={DISSOLVE_GROUP_IMPACT}
        confirmLabel="确认解散"
        busy={busy}
        error={error}
        onCancel={onClose}
        onConfirm={() => void confirm()}
      />
    );
  }

  return (
    <ConfirmDialog
      key={`leave-${conversation.id}`}
      tone="danger"
      title={`退出小组「${groupTitle}」？`}
      subtitle={
        isNonOwnerCreator
          ? "你是该小组创建者。退出后你将失去管理权限且无法继续查看消息；若需彻底删除请使用「解散小组」。"
          : "退出后，你将无法继续接收或查看此小组的消息。"
      }
      impact={
        isNonOwnerCreator
          ? [
              "其他成员仍可继续使用该小组，后续仅空间所有者可管理成员或解散小组",
              "你在此小组中的通知偏好与个人收藏将被清除",
            ]
          : [
              "小组其他成员仍可继续交流",
              "你在此小组中的通知偏好与个人收藏将被清除",
            ]
      }
      confirmLabel="确认退出"
      busy={busy}
      error={error}
      onCancel={onClose}
      onConfirm={() => void confirm()}
    />
  );
}

export function GroupMembersDialog({
  conversation,
  members,
  onClose,
  onDissolved,
}: {
  conversation: Conversation;
  members: Participant[];
  onClose: () => void;
  onDissolved?: (conversationId: string) => Promise<void> | void;
}) {
  const { user } = useAuth();
  const { data, refresh } = useChat();
  const client = useQueryClient();
  const [busyId, setBusyId] = useState(""),
    [error, setError] = useState(""),
    [confirmingRemoval, setConfirmingRemoval] = useState<Member | null>(null),
    [confirmingDissolve, setConfirmingDissolve] = useState(false),
    [dissolving, setDissolving] = useState(false);
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
  async function change(member: Member, remove = false): Promise<boolean> {
    if (!conversation.canManageMembers) return false;
    setBusyId(member.userId);
    setError("");
    const generation = chatStore.generation;
    try {
      const result = remove
        ? await api.removeConversationMember(conversation.id, member.userId)
        : await api.addConversationMember(conversation.id, member.userId);
      if (generation !== chatStore.generation) return false;
      chatStore.addConversation(result.conversation);
      if (!remove) toast.success(`已将 ${member.displayName} 加入小组`);
      await Promise.all([
        refresh(),
        client.invalidateQueries({ queryKey: ["members"] }),
      ]);
      return true;
    } catch (e) {
      if (generation === chatStore.generation) setError(errorText(e));
      return false;
    } finally {
      setBusyId("");
    }
  }
  async function confirmRemoval(member: Member) {
    if (await change(member, true)) setConfirmingRemoval(null);
  }
  async function confirmDissolve() {
    if (!conversation.canManageMembers || dissolving) return;
    setDissolving(true);
    setError("");
    const generation = chatStore.generation;
    try {
      await api.deleteConversation(conversation.id);
      if (generation !== chatStore.generation) return;
      setConfirmingDissolve(false);
      if (onDissolved) await onDissolved(conversation.id);
      else onClose();
    } catch (e) {
      if (generation === chatStore.generation) setError(errorText(e));
    } finally {
      setDissolving(false);
    }
  }
  if (confirmingDissolve)
    return (
      <ConfirmDialog
        key="confirm-dissolve"
        tone="danger"
        title={`解散小组「${conversation.name || "小组对话"}」？`}
        subtitle="解散后，该小组将被永久删除，所有成员均无法恢复。"
        impact={DISSOLVE_GROUP_IMPACT}
        confirmLabel="确认解散"
        busy={dissolving}
        error={error}
        onCancel={() => {
          setError("");
          setConfirmingDissolve(false);
        }}
        onConfirm={() => void confirmDissolve()}
      />
    );
  if (confirmingRemoval)
    return (
      <ConfirmDialog
        key="confirm-removal"
        tone="danger"
        title={`将 ${confirmingRemoval.displayName} 移出小组？`}
        subtitle="移出后，该成员将无法继续访问这个小组对话。工作空间成员资格不受影响。"
        confirmLabel="确认移出"
        busy={busyId === confirmingRemoval.userId}
        error={error}
        onCancel={() => {
          setError("");
          setConfirmingRemoval(null);
        }}
        onConfirm={() => void confirmRemoval(confirmingRemoval)}
      />
    );
  return (
    <Modal
      key="group-members"
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
                  onClick={() => {
                    setError("");
                    setConfirmingRemoval(member);
                  }}
                  title={`移出小组 ${member.displayName}`}
                  aria-label={`移出小组 ${member.displayName}`}
                >
                  {busyId === member.userId ? (
                    <LoaderCircle className="spin" size={ICON.xs} />
                  ) : (
                    <UserMinus size={ICON.xs} />
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
                  variant="secondary"
                  size="sm"
                  busy={busyId === member.userId}
                  disabled={!!busyId}
                  onClick={() => void change(member)}
                >
                  <Plus size={ICON.xs} />
                  添加
                </Button>
              </div>
            ))
          ) : (
            <div className="mini-empty">暂无可添加成员。</div>
          )}
        </div>
      )}
      {conversation.canManageMembers && (
        <div className="group-danger-zone">
          <div className="group-danger-copy">
            <b>解散小组</b>
            <small>永久删除该小组的全部消息、文件与置顶记录。</small>
          </div>
          <Button
            type="button"
            variant="danger"
            size="sm"
            disabled={!!busyId || dissolving}
            onClick={() => {
              setError("");
              setConfirmingDissolve(true);
            }}
          >
            <Trash2 size={ICON.xs} />
            解散小组
          </Button>
        </div>
      )}
      <div className="modal-actions">
        <ModalCloseButton>完成</ModalCloseButton>
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
      busy={busy}
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
                  <Users size={ICON.md} />
                </span>
                <span>
                  {group.name || "小组对话"}
                  <small>
                    {already
                      ? "已在小组中"
                      : `${group.memberIds.length} 位成员`}
                  </small>
                </span>
                <i>{selected === group.id && <Check size={ICON.xs} />}</i>
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
        <Plus size={ICON.xs} />
        创建新小组
      </button>
      <div className="modal-actions">
        <ModalCloseButton disabled={busy}>取消</ModalCloseButton>
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
