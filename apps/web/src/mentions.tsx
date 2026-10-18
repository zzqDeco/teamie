import { Fragment, useId, useLayoutEffect, useRef, useState } from "react";
import type { KeyboardEvent, RefObject } from "react";
import type { Member, Mention } from "@teamie/api-client";
import { AgentAvatar, AgentBadge } from "./agent-ui";
import { Avatar } from "./ui";
import "./mentions.css";

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
          className={`message-mention ${m.userId === userId ? "mention-self" : ""}`}
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

export function MentionInput({
  value,
  mentions,
  onChange,
  members,
  enabled,
  inputRef,
  onKeyDown,
  onTyping,
  ...props
}: {
  value: string;
  mentions: Mention[];
  onChange: (body: string, mentions: Mention[]) => void;
  members: Member[];
  enabled: boolean;
  inputRef?: RefObject<HTMLTextAreaElement | null>;
  onKeyDown?: (event: KeyboardEvent<HTMLTextAreaElement>) => void;
  onTyping?: (body: string) => void;
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
  const query = /(?:^|\s)@([^\s@]{0,64})$/u.exec(value.slice(0, caret));
  const start = query ? caret - query[1].length - 1 : -1;
  const signature = `${value}:${caret}`;
  const candidates =
    query &&
    enabled &&
    !composing &&
    dismissed !== signature &&
    !mentions.some((m) => m.start <= start && m.end >= caret)
      ? members
          .filter((m) =>
            m.displayName
              .toLocaleLowerCase()
              .includes(query[1].toLocaleLowerCase()),
          )
          .slice(0, 12)
      : [];
  const selected =
    selection.signature === signature
      ? Math.min(selection.index, candidates.length - 1)
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
  function choose(member: Member) {
    const label = `@${member.displayName}`;
    const text = value.slice(0, start) + label + " " + value.slice(caret);
    if (text.length > 8000 || mentions.length >= 32) return;
    const next = moveMentions(value, text, mentions);
    next.push({ userId: member.userId, start, end: start + label.length });
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
          {candidates.map((member, index) => (
            <button
              type="button"
              role="option"
              id={`${id}-${index}`}
              aria-selected={selected === index}
              key={member.userId}
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => choose(member)}
            >
              {member.kind === "agent" ? (
                <AgentAvatar />
              ) : (
                <Avatar name={member.displayName} seed={member.userId} />
              )}
              <span>{member.displayName}</span>
              {member.kind === "agent" && <AgentBadge />}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
