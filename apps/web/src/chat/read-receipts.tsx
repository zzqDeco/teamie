import { useEffect, useRef, useState } from "react";
import { Check, CheckCheck } from "lucide-react";
import type { Conversation, Member } from "@teamie/api-client";
import { computeMessageReadSummary, useConversationReceipts } from "../engagement-store";
import { ICON } from "../icons";
import { handleRovingKeys } from "./roving-keys";

/**
 * Receipts on the caller's own messages. Humans and Agents are equal
 * readers: both appear as 「已读」 and in the same group name lists.
 */
export function OwnMessageReadReceipt({
  conversationId,
  conversationType,
  seq,
  userId,
  senderReadSeq,
  allMembers,
  isLatestOwn,
}: {
  conversationId: string;
  conversationType: Conversation["type"];
  seq: number;
  userId: string;
  senderReadSeq: number;
  allMembers: Member[];
  isLatestOwn: boolean;
}) {
  const receipts = useConversationReceipts(conversationId, true);
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const popoverRef = useRef<HTMLDivElement>(null);
  const summary = computeMessageReadSummary({
    receipts,
    seq,
    senderId: userId,
    senderReadSeq,
    conversationType,
    members: allMembers,
  });

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent) => {
      if (
        event.target instanceof Node &&
        !wrapRef.current?.contains(event.target)
      ) {
        setOpen(false);
      }
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.stopPropagation();
        setOpen(false);
        if (wrapRef.current?.contains(document.activeElement))
          triggerRef.current?.focus({ preventScroll: true });
      }
    };
    // Move focus into the list so long lists can be read and scrolled by keyboard.
    popoverRef.current
      ?.querySelector<HTMLElement>("li[tabindex]")
      ?.focus({ preventScroll: true });
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown, true);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown, true);
    };
  }, [open]);

  if (!summary || !summary.label) return null;

  const visibilityClass = isLatestOwn ? "persistent" : "on-hover";
  const statusClass = summary.readCount > 0 ? "is-read" : "is-unread";

  if (conversationType === "group") {
    return (
      <div
        className={`message-read-receipt-wrap ${visibilityClass}`}
        ref={wrapRef}
      >
        <button
          ref={triggerRef}
          type="button"
          className={`message-read-receipt is-interactive ${statusClass}`}
          aria-expanded={open}
          aria-label={`已读回执：${summary.label}`}
          onClick={() => setOpen((current) => !current)}
        >
          {summary.readCount > 0 ? (
            <CheckCheck size={ICON.xs} aria-hidden="true" />
          ) : (
            <Check size={ICON.xs} aria-hidden="true" />
          )}
          {summary.label}
        </button>
        {open && (
          <div
            ref={popoverRef}
            className="read-receipt-popover"
            role="dialog"
            aria-label="小组已读详情"
            onKeyDown={(event) => {
              handleRovingKeys(event, "li[tabindex]", { wrap: false });
            }}
          >
            <div className="read-receipt-section">
              <b>已读 ({summary.readMembers.length})</b>
              {summary.readMembers.length > 0 ? (
                <ul>
                  {summary.readMembers.map((member) => (
                    <li key={member.userId} tabIndex={-1}>{member.displayName}</li>
                  ))}
                </ul>
              ) : (
                <span>暂无成员已读</span>
              )}
            </div>
            <div className="read-receipt-section">
              <b>未读 ({summary.unreadMembers.length})</b>
              {summary.unreadMembers.length > 0 ? (
                <ul>
                  {summary.unreadMembers.map((member) => (
                    <li key={member.userId} tabIndex={-1}>{member.displayName}</li>
                  ))}
                </ul>
              ) : (
                <span>全部成员已读</span>
              )}
            </div>
          </div>
        )}
      </div>
    );
  }

  return (
    <span
      className={`message-read-receipt ${visibilityClass} ${statusClass}`}
      aria-label={`已读状态：${summary.label}`}
    >
      {summary.readCount > 0 ? (
        <CheckCheck size={ICON.xs} aria-hidden="true" />
      ) : (
        <Check size={ICON.xs} aria-hidden="true" />
      )}
      {summary.label}
    </span>
  );
}

