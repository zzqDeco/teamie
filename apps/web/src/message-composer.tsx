import type { FormEvent, KeyboardEvent, RefObject } from "react";
import { CornerUpLeft, SendHorizontal, X } from "lucide-react";
import type {
  AttachmentLimits,
  Conversation,
  Member,
  Mention,
  Message,
} from "@teamie/api-client";
import { AttachmentDraft, AttachmentPicker } from "./attachments";
import type { useAttachmentDraft } from "./attachments";
import { MentionInput } from "./mentions";
import { IconButton } from "./ui";
import { ICON } from "./icons";

export function MessageComposer({
  conversation,
  name,
  body,
  mentions,
  replyToId,
  reply,
  replyError,
  attachments,
  attachmentLimits,
  uploadsEnabled,
  attachmentBlocked,
  mentionMembers,
  mentionsEnabled,
  otherTyping,
  inputRef,
  onChangeDraft,
  onClearReply,
  onTypingChange,
  onKeyDown,
  onSubmit,
}: {
  conversation: Conversation;
  name: string;
  body: string;
  mentions: Mention[];
  replyToId: string;
  reply?: Message;
  replyError?: string;
  attachments: ReturnType<typeof useAttachmentDraft>;
  attachmentLimits: AttachmentLimits;
  uploadsEnabled: boolean;
  attachmentBlocked: boolean;
  mentionMembers: Member[];
  mentionsEnabled: boolean;
  otherTyping: Array<{ userId: string; displayName: string }>;
  inputRef: RefObject<HTMLTextAreaElement | null>;
  onChangeDraft: (body: string, mentions: Mention[]) => void;
  onClearReply: () => void;
  onTypingChange: (value: string) => void;
  onKeyDown: (event: KeyboardEvent<HTMLTextAreaElement>) => void;
  onSubmit: (event?: FormEvent) => void;
}) {
  return (
    <div className="composer-area">
      <AttachmentDraft
        files={attachments.files}
        error={
          attachments.error ||
          (attachments.files.length && !uploadsEnabled
            ? "文件上传已关闭，请移除待发送文件"
            : "")
        }
        onRemove={attachments.remove}
        onRetry={attachments.retry}
      />
      {replyToId && (
        <div className="reply-preview">
          <CornerUpLeft size={ICON.sm} />
          <div>
            回复 <b>{reply?.senderName || "原消息"}</b>
            <span>
              {reply?.retractedAt
                ? "消息已撤回"
                : reply
                  ? reply.body || `${reply.attachments?.length || 0} 个文件`
                  : replyError || "正在加载原消息…"}
            </span>
          </div>
          <IconButton label="取消回复" onClick={onClearReply}>
            <X size={ICON.sm} />
          </IconButton>
        </div>
      )}
      <form
        className={`composer ${replyToId ? "with-reply" : ""}`}
        onSubmit={onSubmit}
      >
        <MentionInput
          inputRef={inputRef}
          value={body}
          mentions={mentions}
          onChange={onChangeDraft}
          members={mentionMembers}
          enabled={mentionsEnabled}
          onTyping={onTypingChange}
          onKeyDown={onKeyDown}
          placeholder={
            conversation.type === "channel"
              ? `发送消息到 # ${name}`
              : `发送消息给 ${name}`
          }
          aria-label="消息内容"
        />
        <div className="composer-tools">
          {uploadsEnabled && (
            <AttachmentPicker
              onSelect={attachments.add}
              disabled={
                attachments.files.length >= attachmentLimits.maxFilesPerMessage
              }
              limit={attachmentLimits.maxFileBytes}
            />
          )}
          {body.length > 7600 && (
            <span className="character-count">{body.length}/8000</span>
          )}
          <button
            className="send-button"
            type="submit"
            disabled={
              (!body.trim() && !attachments.ready) || attachmentBlocked
            }
            aria-label="发送消息"
            title="发送消息"
          >
            <SendHorizontal size={ICON.md} />
          </button>
        </div>
      </form>
      <div className="composer-footer">
        <span>
          {otherTyping.length
            ? `${otherTyping.map((x) => x.displayName).join("、")} 正在输入…`
            : ""}
        </span>
        <span>
          <kbd>Enter</kbd> 发送 · <kbd>Shift + Enter</kbd> 换行
        </span>
      </div>
    </div>
  );
}
