import { useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { Member, Message } from "@teamie/api-client";
import { useLongPress } from "./long-press";

type Reaction = NonNullable<Message["reactions"]>[number];
const TOOLTIP_NAMES = 10;

export function reactorNames(reaction: Reaction, userId: string, members: Member[]) {
  return reaction.userIds.map((uid) =>
    uid === userId
      ? "你"
      : members.find((member) => member.userId === uid)?.displayName || "未知成员",
  );
}

function summary(names: string[]) {
  return names.length > TOOLTIP_NAMES
    ? `${names.slice(0, TOOLTIP_NAMES).join("、")}…等 ${names.length} 人`
    : names.join("、");
}

function ReactorSheet({
  reaction,
  names,
  onClose,
}: {
  reaction: Reaction;
  names: string[];
  onClose: () => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    ref.current?.focus({ preventScroll: true });
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        onClose();
      }
    };
    const onPointer = (event: PointerEvent) => {
      if (!(event.target instanceof Node) || !ref.current?.contains(event.target)) onClose();
    };
    document.addEventListener("keydown", onKey, true);
    document.addEventListener("pointerdown", onPointer, true);
    return () => {
      document.removeEventListener("keydown", onKey, true);
      document.removeEventListener("pointerdown", onPointer, true);
      previous?.focus?.({ preventScroll: true });
    };
  }, [onClose]);
  return createPortal(
    <div
      ref={ref}
      className="reactor-sheet"
      role="dialog"
      aria-label={`回应 ${reaction.emoji} 的成员`}
      tabIndex={-1}
    >
      <b>
        {reaction.emoji} · {reaction.count} 人回应
      </b>
      <ul>
        {names.map((name, index) => (
          <li key={`${reaction.userIds[index]}`}>{name}</li>
        ))}
      </ul>
    </div>,
    document.body,
  );
}

function ReactionChip({
  reaction,
  userId,
  members,
  canReact,
  busy,
  onToggle,
}: {
  reaction: Reaction;
  userId: string;
  members: Member[];
  canReact: boolean;
  busy: boolean;
  onToggle: (emoji: string) => void;
}) {
  const active = reaction.userIds.includes(userId);
  const names = reactorNames(reaction, userId, members);
  return (
    <button
      type="button"
      data-reaction-emoji={reaction.emoji}
      className={`reaction-chip ${active ? "active" : ""}`}
      // Contract: own chips stay removable when creation is disabled.
      disabled={(!canReact && !active) || busy}
      aria-pressed={active}
      title={`${summary(names)} 回应了 ${reaction.emoji}`}
      aria-label={`${reaction.emoji} ${reaction.count} 人回应：${summary(names)}`}
      onClick={() => onToggle(reaction.emoji)}
    >
      <span className="reaction-chip-emoji">{reaction.emoji}</span>
      <span className="reaction-chip-count">{reaction.count}</span>
    </button>
  );
}

/** Persisted reaction chips; visible regardless of `im.reactions.create`. */
export function ReactionChips({
  reactions,
  userId,
  members,
  canReact,
  busy,
  onToggle,
}: {
  reactions: Reaction[];
  userId: string;
  members: Member[];
  canReact: boolean;
  busy: boolean;
  onToggle: (emoji: string) => void;
}) {
  const [sheetEmoji, setSheetEmoji] = useState("");
  const closeSheet = useCallback(() => setSheetEmoji(""), []);
  const sheetReaction = reactions.find((reaction) => reaction.emoji === sheetEmoji);
  // Long-press on a chip shows who reacted (touch has no hover title) without
  // toggling. Handled on the group so disabled chips still reveal names.
  const press = useLongPress<HTMLDivElement>((_, event) => {
    const chip = (event.target as Element | null)?.closest?.("[data-reaction-emoji]");
    const emoji = chip?.getAttribute("data-reaction-emoji");
    if (emoji) setSheetEmoji(emoji);
  });
  return (
    <div className="message-reactions" role="group" aria-label="表情回应" {...press}>
      {reactions.map((reaction) => (
        <ReactionChip
          key={reaction.emoji}
          reaction={reaction}
          userId={userId}
          members={members}
          canReact={canReact}
          busy={busy}
          onToggle={onToggle}
        />
      ))}
      {sheetReaction && (
        <ReactorSheet
          reaction={sheetReaction}
          names={reactorNames(sheetReaction, userId, members)}
          onClose={closeSheet}
        />
      )}
    </div>
  );
}
