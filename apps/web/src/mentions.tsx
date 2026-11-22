import { Fragment, useId, useLayoutEffect, useRef, useState } from "react";
import type { ClipboardEvent, KeyboardEvent, RefObject } from "react";
import type { Member, Mention } from "@teamie/api-client";
import { AgentAvatar, AgentBadge } from "./agent-ui";
import { plainPreview } from "./chat/plain-preview";
import { agentDisabledState, isDisabledAgent, matchPinyinText } from "./member-picker";
import { isUserMentioned } from "./notification-model";
import { usePinyin } from "./shell/pinyin-match";
import type { PinyinConverter } from "./shell/pinyin-match";
import { Avatar } from "./ui";
import "./styles/mentions.css";

// Offsets use the browser's UTF-16 selection units. Any edit intersecting a
// selected name drops its identity instead of guessing who the text names.
export function moveMentions(
  before: string,
  after: string,
  mentions: Mention[],
): Mention[] {
  let start = 0;
  while (
    start < before.length &&
    start < after.length &&
    before[start] === after[start]
  )
    start++;
  let oldEnd = before.length,
    newEnd = after.length;
  while (
    oldEnd > start &&
    newEnd > start &&
    before[oldEnd - 1] === after[newEnd - 1]
  ) {
    oldEnd--;
    newEnd--;
  }
  const delta = newEnd - oldEnd;
  return mentions.flatMap((m) => {
    if (m.end <= start) return [m];
    if (m.start >= oldEnd)
      return [{ ...m, start: m.start + delta, end: m.end + delta }];
    return [];
  });
}

export function trimMentionDraft(body: string, mentions: Mention[]) {
  const offset = body.length - body.trimStart().length;
  const text = body.trim();
  return {
    body: text,
    mentions: mentions
      .filter((m) => m.start >= offset && m.end <= offset + text.length)
      .map((m) => ({ ...m, start: m.start - offset, end: m.end - offset })),
  };
}

export function buildMentionSpans(body: string, mentions: Mention[]): Mention[] {
  return trimMentionDraft(body, mentions).mentions;
}

// Safari dispatches the candidate-confirming keydown after compositionend with
// isComposing=false but keyCode 229, so both signals mark an IME keystroke.
export function isComposingKey(event: KeyboardEvent<HTMLElement>) {
  return event.nativeEvent.isComposing || event.nativeEvent.keyCode === 229;
}

export function MentionText({
  body,
  mentions = [],
  userId,
}: {
  body: string;
  mentions?: Mention[];
  userId: string;
}) {
  const parts = [];
  let end = 0;
  for (const m of mentions) {
    if (
      !Number.isInteger(m.start) ||
      !Number.isInteger(m.end) ||
      m.start < end ||
      m.end <= m.start ||
      m.end > body.length ||
      body[m.start] !== "@"
    )
      continue;
    parts.push(
      <Fragment key={`${m.start}-${m.end}`}>
        {body.slice(end, m.start)}
        <span
          className={`message-mention mention-pill${isUserMentioned([m], userId) ? " mention-self" : ""}`}
          data-user-id={m.userId}
          data-mention-kind={m.kind}
        >
          {body.slice(m.start, m.end)}
        </span>
      </Fragment>,
    );
    end = m.end;
  }
  return (
    <>
      {parts}
      {body.slice(end)}
    </>
  );
}

function previewSlice(
  slice: string,
  hasPrecedingMention: boolean,
  hasFollowingMention: boolean,
): string {
  if (!slice) return "";
  const cleaned = plainPreview(slice);
  if (!cleaned) {
    return hasPrecedingMention && hasFollowingMention && /\s/.test(slice) ? " " : "";
  }
  const leading = hasPrecedingMention && /^\s/.test(slice) ? " " : "";
  const trailing = hasFollowingMention && /\s$/.test(slice) ? " " : "";
  return `${leading}${cleaned}${trailing}`;
}

/**
 * Strips Markdown from non-mention text slices while preserving structured
 * mention pills from the original UTF-16 `start`/`end` offsets.
 */
export function PlainMentionText({
  body,
  mentions = [],
  userId,
}: {
  body: string;
  mentions?: Mention[];
  userId: string;
}) {
  const parts = [];
  let end = 0;
  let hadMention = false;
  for (const m of mentions) {
    if (
      !Number.isInteger(m.start) ||
      !Number.isInteger(m.end) ||
      m.start < end ||
      m.end <= m.start ||
      m.end > body.length ||
      body[m.start] !== "@"
    )
      continue;
    parts.push(
      <Fragment key={`${m.start}-${m.end}`}>
        {previewSlice(body.slice(end, m.start), hadMention, true)}
        <span
          className={`message-mention mention-pill${isUserMentioned([m], userId) ? " mention-self" : ""}`}
          data-user-id={m.userId}
          data-mention-kind={m.kind}
        >
          {body.slice(m.start, m.end)}
        </span>
      </Fragment>,
    );
    end = m.end;
    hadMention = true;
  }
  return (
    <>
      {parts}
      {previewSlice(body.slice(end), hadMention, false)}
    </>
  );
}

/**
 * @ candidate order: prefix, word-start, pinyin prefix, substring, pinyin substring;
 * ties keep member order. Disabled Agents stay mentionable but always rank
 * after everyone else (they will not respond). The current user is excluded.
 */
export function rankMentionMembers<T extends Member & { agentEnabled?: boolean }>(
  members: T[],
  query: string,
  currentUserId = "",
  limit = 12,
  toPinyin?: PinyinConverter | null,
): T[] {
  return members
    .map((member, index) => ({
      member,
      index,
      rank: matchPinyinText(member.displayName, query, toPinyin ?? null),
    }))
    .filter(
      (item): item is { member: T; index: number; rank: number } =>
        item.rank !== null &&
        (!currentUserId || item.member.userId !== currentUserId),
    )
    .sort(
      (a, b) =>
        Number(isDisabledAgent(a.member)) - Number(isDisabledAgent(b.member)) ||
        a.rank - b.rank ||
        a.index - b.index,
    )
    .slice(0, limit)
    .map((item) => item.member);
}

const BROADCAST_OPTIONS: Array<{
  kind: "all" | "here";
  token: string;
  subtitle: string;
  labels: string[];
  pinyinPrefixes: string[];
}> = [
  {
    kind: "all",
    token: "所有人",
    subtitle: "全体成员",
    labels: ["所有人", "全体成员", "all"],
    pinyinPrefixes: ["suoyouren", "syr", "quantichengyuan", "qtcy"],
  },
  {
    kind: "here",
    token: "在线成员",
    subtitle: "当前在线",
    labels: ["在线成员", "当前在线", "here"],
    pinyinPrefixes: [
      "zaixianchengyuan",
      "zxcy",
      "zaixian",
      "zx",
      "dangqianzaixian",
      "dqzx",
    ],
  },
];

function matchesBroadcastOption(
  option: (typeof BROADCAST_OPTIONS)[number],
  query: string,
  toPinyin: PinyinConverter | null,
) {
  if (!query) return true;
  if (option.labels.some((label) => label.includes(query))) return true;
  if (option.pinyinPrefixes.some((alias) => alias.startsWith(query))) return true;
  return option.labels.some((label) => {
    const rank = matchPinyinText(label, query, toPinyin);
    return rank !== null && rank <= 2;
  });
}

type MentionCandidate =
  | { type: "member"; member: Member & { agentEnabled?: boolean } }
  | { type: "broadcast"; kind: "all" | "here"; token: string; subtitle: string };

export function MentionInput({
  value,
  mentions,
  onChange,
  members,
  enabled,
  allowBroadcastMentions = false,
  showBroadcastOnEmptyQuery = true,
  allowHereMention = true,
  inputRef,
  onKeyDown,
  onTyping,
  currentUserId = "",
  ...props
}: {
  value: string;
  mentions: Mention[];
  onChange: (body: string, mentions: Mention[]) => void;
  /** `agentEnabled` comes from Agent discovery (see `Participant`). */
  members: Array<Member & { agentEnabled?: boolean }>;
  enabled: boolean;
  allowBroadcastMentions?: boolean;
  showBroadcastOnEmptyQuery?: boolean;
  allowHereMention?: boolean;
  inputRef?: RefObject<HTMLTextAreaElement | null>;
  onKeyDown?: (event: KeyboardEvent<HTMLTextAreaElement>) => void;
  onPaste?: (event: ClipboardEvent<HTMLTextAreaElement>) => void;
  onTyping?: (body: string) => void;
  /** Excluded from @ candidates (you cannot mention yourself). */
  currentUserId?: string;
  placeholder?: string;
  autoFocus?: boolean;
  required?: boolean;
  "aria-label": string;
}) {
  const ownRef = useRef<HTMLTextAreaElement>(null);
  const ref = inputRef || ownRef;
  const id = useId();
  const listRef = useRef<HTMLDivElement>(null);
  const [caret, setCaret] = useState(0);
  const [composing, setComposing] = useState(false);
  const [dismissed, setDismissed] = useState("");
  const [selection, setSelection] = useState({ signature: "", index: 0 });
  const pendingCaret = useRef<number | null>(null);
  const toPinyin = usePinyin(enabled);
  const query = /(?:^|\s)@([^\s@]{0,64})$/u.exec(value.slice(0, caret));
  const start = query ? caret - query[1].length - 1 : -1;
  const signature = `${value}:${caret}`;
  const activeQuery =
    query &&
    enabled &&
    !composing &&
    dismissed !== signature &&
    !mentions.some((m) => m.start <= start && m.end >= caret)
      ? query[1].toLocaleLowerCase()
      : null;
  const hasBroadcastInDraft = mentions.some(
    (m) => m.kind === "all" || m.kind === "here",
  );
  const broadcastCandidates: MentionCandidate[] =
    activeQuery !== null &&
    allowBroadcastMentions &&
    !hasBroadcastInDraft &&
    (activeQuery.length > 0 || showBroadcastOnEmptyQuery)
      ? BROADCAST_OPTIONS.filter(
          (option) =>
            (option.kind !== "here" || allowHereMention) &&
            matchesBroadcastOption(option, activeQuery, toPinyin),
        ).map((option) => ({
          type: "broadcast" as const,
          kind: option.kind,
          token: option.token,
          subtitle: option.subtitle,
        }))
      : [];
  const memberCandidates: MentionCandidate[] =
    activeQuery !== null
      ? rankMentionMembers(members, activeQuery, currentUserId, 12, toPinyin).map((member) => ({
          type: "member" as const,
          member,
        }))
      : [];
  const candidates: MentionCandidate[] = [
    ...memberCandidates,
    ...broadcastCandidates,
  ];
  const selected =
    selection.signature === signature
      ? Math.min(selection.index, Math.max(0, candidates.length - 1))
      : 0;
  useLayoutEffect(() => {
    listRef.current?.children.item(selected)?.scrollIntoView({ block: "nearest" });
  }, [signature, selected, candidates.length]);
  useLayoutEffect(() => {
    if (pendingCaret.current !== null && ref.current) {
      ref.current.focus();
      ref.current.setSelectionRange(pendingCaret.current, pendingCaret.current);
      pendingCaret.current = null;
    }
  }, [ref, value]);
  function choose(candidate: MentionCandidate) {
    const label =
      candidate.type === "member"
        ? `@${candidate.member.displayName}`
        : `@${candidate.token}`;
    const text = value.slice(0, start) + label + " " + value.slice(caret);
    if (text.length > 8000 || mentions.length >= 32) return;
    const next = moveMentions(value, text, mentions);
    if (candidate.type === "member") {
      next.push({
        userId: candidate.member.userId,
        start,
        end: start + label.length,
      });
    } else {
      next.push({
        kind: candidate.kind,
        start,
        end: start + label.length,
      });
    }
    next.sort((a, b) => a.start - b.start);
    const position = start + label.length + 1;
    pendingCaret.current = position;
    setCaret(position);
    onChange(text, next);
    onTyping?.(text);
  }
  function keyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (composing || isComposingKey(event)) return;
    if (candidates.length) {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        setSelection({
          signature,
          index:
            (selected +
              (event.key === "ArrowDown" ? 1 : -1) +
              candidates.length) %
            candidates.length,
        });
        return;
      }
      if (event.key === "Enter" || event.key === "Tab") {
        event.preventDefault();
        choose(candidates[selected]);
        return;
      }
      if (event.key === "Escape") {
        event.preventDefault();
        setDismissed(signature);
        return;
      }
    }
    onKeyDown?.(event);
  }
  return (
    <div className="mention-input">
      <textarea
        {...props}
        ref={ref}
        value={value}
        maxLength={8000}
        rows={1}
        role="combobox"
        aria-autocomplete="list"
        aria-expanded={candidates.length > 0}
        aria-controls={candidates.length ? id : undefined}
        aria-activedescendant={
          candidates.length ? `${id}-${selected}` : undefined
        }
        onChange={(e) => {
          setCaret(e.target.selectionStart);
          onChange(
            e.target.value,
            moveMentions(value, e.target.value, mentions),
          );
          onTyping?.(e.target.value);
        }}
        onSelect={(e) => setCaret(e.currentTarget.selectionStart)}
        onBlur={() => {
          setComposing(false);
          setDismissed(signature);
        }}
        onCompositionStart={() => setComposing(true)}
        onCompositionEnd={(e) => {
          setComposing(false);
          setCaret(e.currentTarget.selectionStart);
        }}
        onKeyDown={keyDown}
      />
      {candidates.length > 0 && (
        <div
          className="mention-options"
          ref={listRef}
          id={id}
          role="listbox"
          aria-label="提及成员"
        >
          {candidates.map((candidate, index) =>
            candidate.type === "member" ? (
              <button
                type="button"
                role="option"
                id={`${id}-${index}`}
                aria-selected={selected === index}
                key={candidate.member.userId}
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => choose(candidate)}
              >
                {candidate.member.kind === "agent" ? (
                  <AgentAvatar
                    seed={candidate.member.userId}
                    disabled={agentDisabledState(candidate.member)}
                  />
                ) : (
                  <Avatar
                    name={candidate.member.displayName}
                    seed={candidate.member.userId}
                  />
                )}
                <span>{candidate.member.displayName}</span>
                {candidate.member.kind === "agent" && <AgentBadge />}
                {isDisabledAgent(candidate.member) && (
                  <span className="agent-availability disabled mention-agent-disabled">
                    已停用，不会回应
                  </span>
                )}
              </button>
            ) : (
              <button
                type="button"
                role="option"
                id={`${id}-${index}`}
                aria-selected={selected === index}
                key={`broadcast-${candidate.kind}`}
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => choose(candidate)}
              >
                <span className="mention-broadcast-icon">@</span>
                <span>{candidate.token}</span>
                <span className="mention-broadcast-badge">
                  {candidate.subtitle}
                </span>
              </button>
            ),
          )}
        </div>
      )}
    </div>
  );
}
