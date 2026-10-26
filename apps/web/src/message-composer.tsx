import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import type { FormEvent, KeyboardEvent, RefObject } from "react";
import { CornerUpLeft, SendHorizontal, X } from "lucide-react";
import type {
  AttachmentLimits,
  Conversation,
  Member,
  Mention,
  Message,
  User,
} from "@teamie/api-client";
import { sendMessage } from "./chat";
import { chatStore } from "./store";
import type { LocalMessage } from "./store";
import { AttachmentDraft, AttachmentPicker } from "./attachments";
import type { useAttachmentDraft } from "./attachments";
import { isComposingKey, MentionInput, trimMentionDraft } from "./mentions";
import {
  composerTextRevision,
  extendHistoryBoundary,
  flushComposerDrafts,
  readComposerDraft,
  subscribeDrafts,
  writeComposerDraft,
  writeComposerText,
} from "./composer-drafts";
import { useTypingUsers } from "./typing-store";
import { IconButton } from "./ui";
import { ICON } from "./icons";

export function MessageComposer({
  conversation,
  user,
  name,
  draftEpoch,
  replyToId,
  reply,
  replyError,
  attachments,
  attachmentLimits,
  uploadsEnabled,
  attachmentBlocked,
  mentionMembers,
  mentionsEnabled,
  inputRef,
  editing,
  located,
  latestLoaded,
  latest,
  canEdit,
  messages,
  sendTyping,
  onClearReply,
  onStartEdit,
  onSent,
}: {
  conversation: Conversation;
  user: User;
  name: string;
  /** Draft generation from `composerDraftEpoch`; the parent remounts on change. */
  draftEpoch: string;
  replyToId: string;
  reply?: Message;
  replyError?: string;
  attachments: ReturnType<typeof useAttachmentDraft>;
  attachmentLimits: AttachmentLimits;
  uploadsEnabled: boolean;
  attachmentBlocked: boolean;
  mentionMembers: Member[];
  mentionsEnabled: boolean;
  inputRef: RefObject<HTMLTextAreaElement | null>;
  editing: Message | null;
  located: boolean;
  latestLoaded: boolean;
  latest: number;
  canEdit: boolean;
  messages: LocalMessage[];
  sendTyping: (conversationId: string, active: boolean) => void;
  onClearReply: () => void;
  onStartEdit: (message: Message) => void;
  onSent: () => void;
}) {
  const lastTyping = useRef(0);
  // Text lives here so keystrokes re-render only the composer, not the
  // conversation timeline. The parent remounts this component per
  // conversation and draft epoch, so the initial read is always current.
  // Restoring a failed message replaces the stored text from outside; the
  // revision change reloads it here without remounting the textarea.
  const textRevision = useSyncExternalStore(
    subscribeDrafts,
    useCallback(() => composerTextRevision(conversation.id), [conversation.id]),
  );
  const [draft, setDraft] = useState(() => {
    const stored = readComposerDraft(conversation.id);
    return { body: stored.body, mentions: stored.mentions, revision: textRevision };
  });
  if (draft.revision !== textRevision) {
    const stored = readComposerDraft(conversation.id);
    setDraft({ body: stored.body, mentions: stored.mentions, revision: textRevision });
  }
  const { body, mentions } = draft;

  function changeDraft(value: string, nextMentions: Mention[]) {
    setDraft({ body: value, mentions: nextMentions, revision: textRevision });
    writeComposerText(conversation.id, draftEpoch, value, nextMentions);
  }

  useEffect(() => () => flushComposerDrafts(), []);

  useEffect(
    () => () => {
      lastTyping.current = 0;
      sendTyping(conversation.id, false);
    },
    [conversation.id, sendTyping],
  );

  useEffect(() => {
    const input = inputRef.current;
    if (!input) return;
    input.style.height = "auto";
    input.style.height = `${input.scrollHeight}px`;
  }, [body, inputRef]);

  function submit(event?: FormEvent) {
    event?.preventDefault();
    const trimmed = trimMentionDraft(body, mentions);
    const text = trimmed.body;
    if (
      (!text && !attachments.ready) ||
      text.length > 8000 ||
      attachmentBlocked
    )
      return;
    const files = attachments.consume();
    const clientMessageId = crypto.randomUUID();
    const message: Message = {
      id: `local:${clientMessageId}`,
      clientMessageId,
      conversationId: conversation.id,
      body: text,
      ...(trimmed.mentions.length ? { mentions: trimmed.mentions } : {}),
      ...(files.length ? { attachments: files } : {}),
      senderId: user.id,
      senderName: user.displayName,
      senderKind: user.kind,
      seq: Number.MAX_SAFE_INTEGER,
      createdAt: new Date().toISOString(),
      ...(replyToId ? { replyToId } : {}),
    };
    void sendMessage(message, chatStore.generation);
    writeComposerDraft(conversation.id, {
      body: "",
      mentions: [],
      replyToId: "",
    });
    setDraft({ body: "", mentions: [], revision: textRevision });
    lastTyping.current = 0;
    sendTyping(conversation.id, false);
    onSent();
    inputRef.current?.focus();
  }

  function keyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (isComposingKey(event)) return;
    if (
      event.key === "ArrowUp" &&
      !event.shiftKey &&
      !event.altKey &&
      !event.metaKey &&
      !event.ctrlKey &&
      !body &&
      !editing &&
      !replyToId &&
      attachments.files.length === 0 &&
      !located &&
      latestLoaded &&
      latest >= conversation.lastSeq &&
      canEdit
    ) {
      const contiguousFrom = latest
        ? extendHistoryBoundary(latest, messages)
        : Infinity;
      const own = [...messages]
        .reverse()
        .find(
          (m) =>
            m.senderId === user.id &&
            !m.status &&
            m.seq >= contiguousFrom &&
            !m.retractedAt &&
            (m.body || m.attachments?.length),
        );
      if (own) {
        event.preventDefault();
        onStartEdit(own);
      }
      return;
    }
    if (
      !window.matchMedia("(pointer: coarse)").matches &&
      event.key === "Enter" &&
      !event.shiftKey
    ) {
      event.preventDefault();
      submit();
    }
  }

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
        onSubmit={submit}
        onBlur={flushComposerDrafts}
      >
        <MentionInput
          inputRef={inputRef}
          value={body}
          mentions={mentions}
          onChange={changeDraft}
          members={mentionMembers}
          enabled={mentionsEnabled}
          onTyping={(value) => {
            if (!value) {
              if (lastTyping.current > 0) {
                lastTyping.current = 0;
                sendTyping(conversation.id, false);
              }
              return;
            }
            if (Date.now() - lastTyping.current > 1500) {
              sendTyping(conversation.id, true);
              lastTyping.current = Date.now();
            }
          }}
          onKeyDown={keyDown}
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
        <TypingIndicator conversationId={conversation.id} />
        <span>
          <kbd>Enter</kbd> 发送 · <kbd>Shift + Enter</kbd> 换行
        </span>
      </div>
    </div>
  );
}

/** Subscribes to typing hints itself so they never re-render the timeline. */
function TypingIndicator({ conversationId }: { conversationId: string }) {
  const otherTyping = useTypingUsers(conversationId);
  return (
    <span>
      {otherTyping.length
        ? `${otherTyping.map((x) => x.displayName).join("、")} 正在输入…`
        : ""}
    </span>
  );
}
