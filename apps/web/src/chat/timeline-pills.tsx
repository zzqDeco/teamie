import { ArrowDown } from "lucide-react";
import { ICON } from "../icons";

/** Floating 「回到最新」/「N 条新消息」 button shown while scrolled up in the latest timeline. */
export function JumpLatestButton({
  unreadCount,
  hasMention,
  onJump,
}: {
  unreadCount: number;
  hasMention: boolean;
  onJump: () => void;
}) {
  return (
    <button
      type="button"
      className={`jump-latest${unreadCount > 0 ? " has-unread" : ""}${hasMention ? " has-mention" : ""}`}
      onClick={onJump}
    >
      <ArrowDown size={ICON.xs} />
      {unreadCount > 0 ? `${hasMention ? "@ " : ""}${unreadCount} 条新消息` : "回到最新消息"}
    </button>
  );
}

/** In located/context mode: new messages arrived in the latest timeline. */
export function LocatedNewMessagesPill({
  count,
  onReturn,
}: {
  count: number;
  onReturn: () => void;
}) {
  return (
    <button type="button" className="located-new-messages" onClick={onReturn}>
      <ArrowDown size={ICON.xs} />
      {count} 条新消息 · 返回最新
    </button>
  );
}
