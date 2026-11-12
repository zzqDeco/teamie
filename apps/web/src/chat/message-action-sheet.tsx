import type { ReactNode } from "react";
import { createPortal } from "react-dom";
import {
  Bookmark,
  Copy,
  CornerUpLeft,
  CornerUpRight,
  Link2,
  MailWarning,
  Pencil,
  Pin,
  Trash2,
} from "lucide-react";
import type { Message } from "@teamie/api-client";
import { COMMON_REACTION_EMOJIS } from "../engagement-store";
import { ICON } from "../icons";
import { Modal } from "../ui";
import {
  markMessageAsUnread,
  toggleMessagePin,
  toggleMessageSave,
} from "./collaboration-ui";
import { messagePreviewText } from "./plain-preview";

export type MessageSheetPermissions = {
  canReact: boolean;
  canForward: boolean;
  canPin: boolean;
  canSave: boolean;
  canMarkUnread: boolean;
  canEdit: boolean;
  canRetract: boolean;
  isPinned: boolean;
  isSaved: boolean;
  isOwn: boolean;
};

/**
 * Touch action sheet opened by long-pressing a message. Offers the same
 * actions as the hover toolbar with text labels and ≥44px targets.
 */
export function MessageActionSheet({
  conversationId,
  message,
  permissions: p,
  onClose,
  onReact,
  onReply,
  onForward,
  onCopyText,
  onCopyLink,
  onEdit,
  onRetract,
}: {
  conversationId: string;
  message: Message;
  permissions: MessageSheetPermissions;
  onClose: () => void;
  onReact: (emoji: string) => void;
  onReply: () => void;
  onForward: () => void;
  onCopyText: () => void;
  onCopyLink: () => void;
  onEdit: () => void;
  onRetract: () => void;
}) {
  const action = (
    label: string,
    icon: ReactNode,
    run: () => void,
    danger = false,
  ) => (
    <li key={label}>
      <button
        type="button"
        className={`message-sheet-action ${danger ? "danger" : ""}`}
        onClick={() => {
          onClose();
          run();
        }}
      >
        {icon}
        {label}
      </button>
    </li>
  );
  const preview = messagePreviewText(message, "消息");
  // Portaled so the sheet is not nested inside the message article's DOM.
  return createPortal(
    <Modal
      title="消息操作"
      subtitle={`${message.senderName}：${preview.length > 60 ? `${preview.slice(0, 60)}…` : preview}`}
      onClose={onClose}
      className="message-action-sheet"
    >
      {p.canReact && (
        <div className="message-sheet-reactions" role="group" aria-label="添加表情回应">
          {COMMON_REACTION_EMOJIS.map((emoji) => (
            <button
              key={emoji}
              type="button"
              className="reaction-picker-emoji"
              aria-label={`表情回应 ${emoji}`}
              onClick={() => {
                onClose();
                onReact(emoji);
              }}
            >
              {emoji}
            </button>
          ))}
        </div>
      )}
      <ul className="message-sheet-actions">
        {action("回复", <CornerUpLeft size={ICON.md} />, onReply)}
        {p.canForward && action("转发", <CornerUpRight size={ICON.md} />, onForward)}
        {(p.canPin || p.isPinned) &&
          action(p.isPinned ? "取消置顶" : "置顶", <Pin size={ICON.md} />, () =>
            void toggleMessagePin(conversationId, message, p.isPinned),
          )}
        {(p.canSave || p.isSaved) &&
          action(p.isSaved ? "取消收藏" : "收藏", <Bookmark size={ICON.md} />, () =>
            void toggleMessageSave(conversationId, message, p.isSaved),
          )}
        {p.canMarkUnread &&
          action("标为未读", <MailWarning size={ICON.md} />, () =>
            void markMessageAsUnread(conversationId, message),
          )}
        {!!message.body && action("复制文本", <Copy size={ICON.md} />, onCopyText)}
        {action("复制消息链接", <Link2 size={ICON.md} />, onCopyLink)}
        {p.isOwn && p.canEdit && action("编辑消息", <Pencil size={ICON.md} />, onEdit)}
        {p.isOwn && p.canRetract && action("撤回消息", <Trash2 size={ICON.md} />, onRetract, true)}
      </ul>
    </Modal>,
    document.body,
  );
}
