import { useState } from "react";
import type { KeyboardEvent, ReactNode } from "react";
import { Check, ChevronRight, Hash, LoaderCircle, Search, Users } from "lucide-react";
import type { Conversation, Member } from "@teamie/api-client";
import { AgentAvatar, AgentBadge } from "./agent-ui";
import { conversationName } from "./composer-drafts";
import { ICON } from "./icons";
import { FieldError } from "./password-field";
import type { Participant } from "./participants";
import { usePinyin } from "./shell/pinyin-match";
import type { PinyinConverter } from "./shell/pinyin-match";
import { compareTimestampsDesc } from "./store";
import { Avatar, Button, Notice } from "./ui";

const WORD_SPLIT = /[\s\-_./#·・,，、()（）]+/u;

function isPickerComposingKey(
  event: Pick<KeyboardEvent<HTMLInputElement>, "keyCode"> & {
    nativeEvent?: { isComposing?: boolean; keyCode?: number };
  },
) {
  return Boolean(
    event.nativeEvent?.isComposing ||
      event.keyCode === 229 ||
      event.nativeEvent?.keyCode === 229,
  );
}

export function isDisabledAgent(member: Member & { agentEnabled?: boolean }) {
  return member.kind === "agent" && member.agentEnabled === false;
}

export function agentDisabledState(
  member: Member & { agentEnabled?: boolean },
): boolean | undefined {
  if (member.kind !== "agent" || typeof member.agentEnabled !== "boolean") {
    return undefined;
  }
  return !member.agentEnabled;
}

/**
 * Multi-tier text + pinyin match rank (lower is better), or `null` when
 * unmatched:
 *   0: exact prefix
 *   1: word prefix
 *   2: pinyin full or initials prefix
 *   3: substring
 *   4: pinyin full or initials substring
 *   5: extraText substring
 *   6: extraText pinyin full or initials substring
 */
export function matchPinyinText(
  label: string,
  query: string,
  toPinyin: PinyinConverter | null,
  extraText?: string,
): number | null {
  const q = query.trim().toLocaleLowerCase();
  if (!q) return 0;
  const lower = label.toLocaleLowerCase();
  if (lower.startsWith(q)) return 0;
  if (lower.split(WORD_SPLIT).some((part) => part && part.startsWith(q))) return 1;
  const compact = q.replace(/\s+/gu, "");
  const forms = toPinyin?.(label);
  if (
    forms?.full &&
    (forms.full.startsWith(compact) || forms.initials.startsWith(compact))
  ) {
    return 2;
  }
  if (lower.includes(q)) return 3;
  if (
    forms?.full &&
    (forms.full.includes(compact) || forms.initials.includes(compact))
  ) {
    return 4;
  }
  if (extraText) {
    const extraLower = extraText.toLocaleLowerCase();
    if (extraLower.includes(q)) return 5;
    const extraForms = toPinyin?.(extraText);
    if (
      extraForms?.full &&
      (extraForms.full.includes(compact) || extraForms.initials.includes(compact))
    ) {
      return 6;
    }
  }
  return null;
}

/**
 * Filters and ranks participants by display name, pinyin, and owner/kind metadata.
 * Disabled Agents always sort after active members; ties keep original order.
 */
export function rankParticipantsByQuery<T extends Member & { agentEnabled?: boolean }>(
  members: T[],
  query: string,
  toPinyin: PinyinConverter | null,
): T[] {
  return members
    .map((member, index) => {
      const extra = [member.ownerName || "", member.kind === "agent" ? "agent" : ""]
        .filter(Boolean)
        .join(" ");
      const rank = matchPinyinText(member.displayName, query, toPinyin, extra);
      return { member, index, rank };
    })
    .filter(
      (item): item is { member: T; index: number; rank: number } =>
        item.rank !== null,
    )
    .sort(
      (a, b) =>
        Number(isDisabledAgent(a.member)) - Number(isDisabledAgent(b.member)) ||
        a.rank - b.rank ||
        a.index - b.index,
    )
    .map((item) => item.member);
}

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
        <AgentAvatar seed={member.userId} disabled={agentDisabledState(member)} />
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

type MemberPickerBaseProps = {
  members: Participant[];
  label: string;
  searchPlaceholder?: string;
  searchLabel?: string;
  emptyText?: string;
  noMatchText?: string;
  loading?: boolean;
  loadError?: boolean;
  onRetry?: () => void;
  errorId?: string;
  fieldError?: string;
  busy?: boolean;
  busyMemberId?: string;
};

export type MemberPickerProps =
  | (MemberPickerBaseProps & {
      mode: "single-instant";
      onInstantSelect: (member: Participant) => void;
    })
  | (MemberPickerBaseProps & {
      mode: "multiple";
      selectedIds: string[];
      onChangeSelected: (nextIds: string[]) => void;
      maxSelected?: number;
    });

export function MemberPicker(props: MemberPickerProps) {
  const {
    members,
    label,
    searchPlaceholder = "搜索成员（支持拼音）",
    searchLabel = "搜索成员",
    emptyText = "暂无可选成员。",
    noMatchText = "没有找到匹配的成员。",
    loading = false,
    loadError = false,
    onRetry,
    errorId,
    fieldError,
    busy = false,
    busyMemberId = "",
  } = props;
  const [query, setQuery] = useState("");
  const [composing, setComposing] = useState(false);
  const toPinyin = usePinyin();
  const filtered = rankParticipantsByQuery(members, query, toPinyin);
  const selectedIds = props.mode === "multiple" ? props.selectedIds : [];
  const eligibleSelectedIds =
    props.mode === "multiple"
      ? selectedIds.filter((id) => members.some((member) => member.userId === id))
      : [];
  const hiddenSelectedCount =
    props.mode === "multiple"
      ? eligibleSelectedIds.filter(
          (id) => !filtered.some((member) => member.userId === id),
        ).length
      : 0;
  const maxSelected = props.mode === "multiple" ? props.maxSelected : undefined;

  function toggleMember(member: Participant) {
    if (busy) return;
    if (props.mode === "single-instant") {
      props.onInstantSelect(member);
      return;
    }
    const already = props.selectedIds.includes(member.userId);
    if (!already && maxSelected !== undefined && eligibleSelectedIds.length >= maxSelected) {
      return;
    }
    props.onChangeSelected(
      already
        ? props.selectedIds.filter((id) => id !== member.userId)
        : [...props.selectedIds, member.userId],
    );
  }

  return (
    <div
      className="member-select"
      aria-describedby={fieldError && errorId ? errorId : undefined}
    >
      <div className="picker-header">
        <span className="label-text">
          {props.mode === "multiple"
            ? `${label} · 已选 ${eligibleSelectedIds.length} 位`
            : label}
        </span>
        {hiddenSelectedCount > 0 && (
          <span className="picker-selection-hint">
            另有 {hiddenSelectedCount} 位已选
          </span>
        )}
      </div>
      {members.length > 0 && (
        <div className="input-icon picker-search">
          <Search size={ICON.sm} aria-hidden="true" />
          <input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onInput={(e) => e.stopPropagation()}
            onCompositionStart={() => setComposing(true)}
            onCompositionEnd={() => setComposing(false)}
            onBlur={() => setComposing(false)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                if (composing || isPickerComposingKey(e)) return;
                e.preventDefault();
                if (
                  props.mode === "single-instant" &&
                  filtered.length === 1 &&
                  !busy
                ) {
                  props.onInstantSelect(filtered[0]);
                }
              }
            }}
            placeholder={searchPlaceholder}
            aria-label={searchLabel}
            maxLength={200}
            disabled={busy}
          />
        </div>
      )}
      {loading && <p className="muted">正在加载成员…</p>}
      {loadError && (
        <div role="alert">
          <Notice kind="error">成员加载失败。</Notice>
          {onRetry && (
            <Button type="button" variant="secondary" size="sm" onClick={onRetry}>
              重新加载
            </Button>
          )}
        </div>
      )}
      {filtered.length > 0 && (
        <div className="member-select-list">
          {filtered.map((member) => {
            const isSelected =
              props.mode === "multiple" && selectedIds.includes(member.userId);
            const atCap =
              props.mode === "multiple" &&
              !isSelected &&
              maxSelected !== undefined &&
              eligibleSelectedIds.length >= maxSelected;
            const isMemberBusy = busy && busyMemberId === member.userId;
            return (
              <button
                key={member.userId}
                type="button"
                disabled={busy || atCap}
                aria-pressed={props.mode === "multiple" ? isSelected : undefined}
                aria-busy={isMemberBusy || undefined}
                className={`member-option${isSelected ? " selected" : ""}`}
                onClick={() => toggleMember(member)}
              >
                <ParticipantIdentity member={member} />
                {props.mode === "multiple" ? (
                  <span className="selection-check">
                    {isSelected && <Check size={ICON.xs} />}
                  </span>
                ) : (
                  <span className="member-option-action" aria-hidden="true">
                    {isMemberBusy ? (
                      <LoaderCircle className="spin" size={ICON.xs} />
                    ) : (
                      <ChevronRight size={ICON.xs} />
                    )}
                  </span>
                )}
              </button>
            );
          })}
        </div>
      )}
      {!loading && !loadError && members.length === 0 && (
        <div className="mini-empty">{emptyText}</div>
      )}
      {!loading && !loadError && members.length > 0 && filtered.length === 0 && (
        <div className="mini-empty">{noMatchText}</div>
      )}
      {errorId && <FieldError id={errorId} message={fieldError} />}
    </div>
  );
}

function readRecentConversationIds(userId: string): string[] {
  if (!userId || typeof localStorage === "undefined") return [];
  try {
    const raw = localStorage.getItem(`teamie.nav.${userId}`);
    if (!raw) return [];
    const parsed = JSON.parse(raw) as { recentConversationIds?: unknown };
    if (!Array.isArray(parsed?.recentConversationIds)) return [];
    return parsed.recentConversationIds.filter(
      (id): id is string => typeof id === "string" && !!id,
    );
  } catch {
    return [];
  }
}

/**
 * Filters same-workspace speakable conversations and orders recent non-current
 * targets first (recent visit order, then updatedAt descending), placing the
 * current conversation last while still keeping it selectable.
 */
export function sortForwardConversations(
  conversations: Conversation[],
  currentConversation: Conversation,
  userId: string,
  recentConversationIds?: string[],
): Conversation[] {
  const eligible = conversations.filter(
    (c) =>
      c.workspaceId === currentConversation.workspaceId &&
      (c.type === "channel" || c.memberIds.includes(userId)),
  );
  const base = eligible.length > 0 ? eligible : [currentConversation];
  const recent = recentConversationIds ?? readRecentConversationIds(userId);
  const recentIndex = new Map(recent.map((id, idx) => [id, idx]));
  const rankRecent = (c: Conversation) =>
    recentIndex.get(c.id) ?? Number.MAX_SAFE_INTEGER;
  return [...base].sort((a, b) => {
    const aCurrent = a.id === currentConversation.id ? 1 : 0;
    const bCurrent = b.id === currentConversation.id ? 1 : 0;
    if (aCurrent !== bCurrent) return aCurrent - bCurrent;
    const rDiff = rankRecent(a) - rankRecent(b);
    if (rDiff !== 0) return rDiff;
    return compareTimestampsDesc(a.updatedAt, b.updatedAt);
  });
}

export function ConversationTargetPicker({
  conversations,
  currentConversation,
  members,
  userId,
  selectedId,
  onSelect,
  recentConversationIds,
  busy = false,
}: {
  conversations: Conversation[];
  currentConversation: Conversation;
  members: Member[];
  userId: string;
  selectedId: string;
  onSelect: (conversationId: string) => void;
  recentConversationIds?: string[];
  busy?: boolean;
}) {
  const [query, setQuery] = useState("");
  const [composing, setComposing] = useState(false);
  const toPinyin = usePinyin();
  const ordered = sortForwardConversations(
    conversations,
    currentConversation,
    userId,
    recentConversationIds,
  );
  const filtered = ordered
    .map((conv, index) => {
      const label = conversationName(conv, userId, members);
      const kindExtra =
        conv.type === "channel"
          ? "频道 channel"
          : conv.type === "group"
            ? "小组 group"
            : "私信 direct";
      const rank = matchPinyinText(label, query, toPinyin, kindExtra);
      return { conv, index, rank };
    })
    .filter(
      (item): item is { conv: Conversation; index: number; rank: number } =>
        item.rank !== null,
    )
    .sort((a, b) => a.rank - b.rank || a.index - b.index)
    .map((item) => item.conv);

  const selectedConv = ordered.find((c) => c.id === selectedId);
  const selectedHidden =
    Boolean(selectedConv) && !filtered.some((c) => c.id === selectedId);

  return (
    <div className="forward-field forward-target-picker">
      <div className="picker-header">
        <span className="label-text">目标对话</span>
        {selectedHidden && selectedConv && (
          <span className="picker-selection-hint">
            已选：
            {selectedConv.type === "channel"
              ? `# ${conversationName(selectedConv, userId, members)}`
              : conversationName(selectedConv, userId, members)}
          </span>
        )}
      </div>
      <div className="input-icon picker-search">
        <Search size={ICON.sm} aria-hidden="true" />
        <input
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onInput={(e) => e.stopPropagation()}
          onCompositionStart={() => setComposing(true)}
          onCompositionEnd={() => setComposing(false)}
          onBlur={() => setComposing(false)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              if (composing || isPickerComposingKey(e)) return;
              e.preventDefault();
              if (filtered.length === 1 && !busy) {
                onSelect(filtered[0].id);
              }
            }
          }}
          placeholder="搜索频道、小组或私信（支持拼音）"
          aria-label="搜索目标对话"
          maxLength={200}
          disabled={busy}
        />
      </div>
      <div className="forward-target-list" role="listbox" aria-label="目标对话">
        {filtered.map((conv) => {
          const isSelected = conv.id === selectedId;
          const name = conversationName(conv, userId, members);
          const isCurrent = conv.id === currentConversation.id;
          const peerMember =
            conv.type === "direct"
              ? members.find(
                  (m) => m.userId !== userId && conv.memberIds.includes(m.userId),
                )
              : undefined;
          return (
            <button
              key={conv.id}
              type="button"
              role="option"
              aria-selected={isSelected}
              data-conversation-id={conv.id}
              disabled={busy}
              className={`forward-target-option${isSelected ? " selected" : ""}`}
              onClick={() => onSelect(conv.id)}
            >
              <span className="forward-target-icon" aria-hidden="true">
                {conv.type === "channel" ? (
                  <Hash size={ICON.sm} />
                ) : conv.type === "group" ? (
                  <Users size={ICON.sm} />
                ) : peerMember?.kind === "agent" ? (
                  <AgentAvatar
                    seed={peerMember.userId}
                    disabled={agentDisabledState(peerMember)}
                  />
                ) : (
                  <Avatar name={name} seed={peerMember?.userId || conv.id} />
                )}
              </span>
              <span className="forward-target-meta">
                <b>
                  <span className="forward-target-name">
                    {conv.type === "channel" ? `# ${name}` : name}
                  </span>
                  {peerMember?.kind === "agent" && <AgentBadge compact />}
                </b>
                <small>
                  {conv.type === "channel"
                    ? "频道"
                    : conv.type === "group"
                      ? `小组 · ${conv.memberIds.length} 位成员`
                      : "私信"}
                  {isCurrent ? " · 当前对话" : ""}
                </small>
              </span>
              <span className="selection-check">
                {isSelected && <Check size={ICON.xs} />}
              </span>
            </button>
          );
        })}
        {filtered.length === 0 && (
          <div className="mini-empty">没有找到匹配的对话。</div>
        )}
      </div>
    </div>
  );
}
