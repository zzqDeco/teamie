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
import {
  beginStagedMessageBind,
  bindStagedAttachmentsToMessage,
  endStagedMessageBind,
  markStagedMessageQueued,
  unbindStagedAttachmentsToDraft,
} from "./pwa/offline-attachments";
import { readAttachmentDraft } from "./attachment-drafts";
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
import { toast } from "./toast";
import { prefersEnterToSend } from "./chat/keyboard-source";
import { messagePreviewText } from "./chat/plain-preview";

/** Contract: message text max 8000 characters. */
export const MESSAGE_MAX_CHARS = 8000;
const COUNTER_FROM = 7600;

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
  allowBroadcastMentions,
  showBroadcastOnEmptyQuery = true,
  allowHereMention,
  inputRef,
  editing,
  located,
  latestLoaded,
  latest,
  canEdit,
  offline = false,
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
  allowBroadcastMentions?: boolean;
  showBroadcastOnEmptyQuery?: boolean;
  allowHereMention?: boolean;
  inputRef: RefObject<HTMLTextAreaElement | null>;
  editing: Message | null;
  located: boolean;
  latestLoaded: boolean;
  latest: number;
  canEdit: boolean;
  offline?: boolean;
  messages: LocalMessage[];
  sendTyping: (conversationId: string, active: boolean) => void;
  onClearReply: () => void;
  onStartEdit: (message: Message) => void;
  onSent: () => void;
}) {
  const lastTyping = useRef(0);
  const submittingStaged = useRef(false);
  const [bindingStaged, setBindingStaged] = useState(false);
  const draftEditSeqRef = useRef(0);
  // Text lives here so keystrokes re-render only the composer, not the
  // conversation timeline. The parent remounts this component per
  // conversation and draft epoch, so the initial read is always current.
  // Restoring a failed message replaces the stored text from outside; the
  // revision change reloads it here without remounting the textarea.
  const textRevision = useSyncExternalStore(
    subscribeDrafts,
    useCallback(() => composerTextRevision(conversation.id), [conversation.id]),
  );
  const latestTextRevisionRef = useRef(textRevision);
  useEffect(() => {
    latestTextRevisionRef.current = textRevision;
  }, [textRevision]);
  const [draft, setDraft] = useState(() => {
    const stored = readComposerDraft(conversation.id);
    return { body: stored.body, mentions: stored.mentions, revision: textRevision };
  });
  if (draft.revision !== textRevision) {
    const stored = readComposerDraft(conversation.id);
    setDraft({ body: stored.body, mentions: stored.mentions, revision: textRevision });
  }
  const { body, mentions } = draft;
  const trimmedLength = trimMentionDraft(body, mentions).body.length;
  const overLimit = trimmedLength > MESSAGE_MAX_CHARS;

  function changeDraft(value: string, nextMentions: Mention[]) {
    draftEditSeqRef.current += 1;
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
    if (submittingStaged.current) return;
    const trimmed = trimMentionDraft(body, mentions);
    const text = trimmed.body;
    if (
      (!text && !attachments.ready) ||
      text.length > MESSAGE_MAX_CHARS ||
      attachmentBlocked
    )
      return;
    const hasStaged = attachments.files.some((f) => f.state === "staged");
    const clientMessageId = crypto.randomUUID();
    const generation = chatStore.generation;
    const submittedEditSeq = draftEditSeqRef.current;
    const submittedTextRevision = textRevision;
    const submittedReplyToId = replyToId;

    const finalizeLocalSend = (
      uploadedFiles: ReturnType<typeof attachments.consume>,
      stagedSummaries?: LocalMessage["stagedAttachments"],
    ) => {
      const message: LocalMessage = {
        id: `local:${clientMessageId}`,
        clientMessageId,
        conversationId: conversation.id,
        body: text,
        ...(trimmed.mentions.length ? { mentions: trimmed.mentions } : {}),
        ...(uploadedFiles.length ? { attachments: uploadedFiles } : {}),
        ...(stagedSummaries?.length ? { stagedAttachments: stagedSummaries } : {}),
        senderId: user.id,
        senderName: user.displayName,
        senderKind: user.kind,
        seq: Number.MAX_SAFE_INTEGER,
        createdAt: new Date().toISOString(),
        ...(submittedReplyToId ? { replyToId: submittedReplyToId } : {}),
      };
      void sendMessage(message, generation, { offline });
      const editedDuringBind =
        draftEditSeqRef.current !== submittedEditSeq ||
        latestTextRevisionRef.current !== submittedTextRevision;
      if (!editedDuringBind) {
        writeComposerDraft(conversation.id, {
          body: "",
          mentions: [],
          replyToId: "",
        });
        setDraft({
          body: "",
          mentions: [],
          revision: latestTextRevisionRef.current,
        });
        lastTyping.current = 0;
        sendTyping(conversation.id, false);
        onSent();
      } else if (submittedReplyToId) {
        flushComposerDrafts();
        const currentStored = readComposerDraft(conversation.id);
        if (currentStored.replyToId === submittedReplyToId) {
          writeComposerDraft(conversation.id, {
            ...currentStored,
            replyToId: "",
          });
          onSent();
        }
      }
      inputRef.current?.focus();
    };

    if (!hasStaged) {
      const files = attachments.consume();
      finalizeLocalSend(files);
      return;
    }

    const stagedDrafts = attachments.files.filter((f) => f.state === "staged");
    submittingStaged.current = true;
    setBindingStaged(true);
    beginStagedMessageBind(user.id, clientMessageId);
    void bindStagedAttachmentsToMessage(
      user.id,
      conversation.id,
      stagedDrafts.map((f) => f.id),
      clientMessageId,
    )
      .then((bound) => {
        if (chatStore.generation !== generation) return;
        const boundIds = new Set(bound.map((r) => r.id));
        if (stagedDrafts.some((f) => !boundIds.has(f.id))) {
          if (bound.length) {
            void unbindStagedAttachmentsToDraft(
              user.id,
              conversation.id,
              clientMessageId,
            ).catch(() => {});
          }
          toast.error("部分离线附件暂存已失效，请移除后重新选择。");
          return;
        }
        const currentById = new Map(
          readAttachmentDraft(conversation.id).map((f) => [f.id, f]),
        );
        if (stagedDrafts.some((f) => currentById.get(f.id)?.state === "uploading")) {
          void unbindStagedAttachmentsToDraft(
            user.id,
            conversation.id,
            clientMessageId,
          ).catch(() => {});
          return;
        }
        const consumed = attachments.consumeForSend(boundIds);
        const summaries = consumed.staged.map((f) => ({
          id: f.id,
          uploadKey: f.uploadKey,
          filename: f.file.name,
          mediaType: f.file.type || "application/octet-stream",
          sizeBytes: f.file.size,
        }));
        if (!text && !consumed.uploaded.length && !summaries.length) {
          void unbindStagedAttachmentsToDraft(
            user.id,
            conversation.id,
            clientMessageId,
          ).catch(() => {});
          return;
        }
        finalizeLocalSend(consumed.uploaded, summaries);
        chatStore.flush();
        markStagedMessageQueued(user.id, clientMessageId);
      })
      .catch(() => {
        if (chatStore.generation === generation) {
          toast.error("离线暂存附件绑定失败，请稍后重试。");
        }
      })
      .finally(() => {
        endStagedMessageBind(user.id, clientMessageId);
        submittingStaged.current = false;
        setBindingStaged(false);
      });
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
    // Enter sends with a hardware keyboard (incl. iPad keyboards); the
    // on-screen keyboard inserts a newline; Cmd/Ctrl+Enter always sends.
    if (event.key === "Enter" && prefersEnterToSend(event)) {
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
                  ? messagePreviewText(reply, "原消息")
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
          allowBroadcastMentions={
            allowBroadcastMentions ??
            (conversation.type !== "direct" &&
              (conversation as { kind?: string }).kind !== "dm")
          }
          showBroadcastOnEmptyQuery={showBroadcastOnEmptyQuery}
          allowHereMention={allowHereMention}
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
          currentUserId={user.id}
          onKeyDown={keyDown}
          onPaste={(event) => {
            const files = event.clipboardData?.files;
            if (!files || files.length === 0) return;
            event.preventDefault();
            if (uploadsEnabled) attachments.add(Array.from(files));
            else toast.error("文件上传已关闭，无法粘贴文件");
          }}
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
          {trimmedLength > COUNTER_FROM && (
            <span
              id={`composer-count-${conversation.id}`}
              className={`character-count ${overLimit ? "over-limit" : ""}`}
              role={overLimit ? "alert" : undefined}
            >
              {trimmedLength}/{MESSAGE_MAX_CHARS}
              {overLimit && <span className="sr-only">，超出 {MESSAGE_MAX_CHARS} 字上限</span>}
            </span>
          )}
          <button
            className="send-button"
            type="submit"
            disabled={
              bindingStaged ||
              (!body.trim() && !attachments.ready) ||
              attachmentBlocked ||
              overLimit
            }
            aria-describedby={overLimit ? `composer-count-${conversation.id}` : undefined}
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
    <span role="status" aria-live="polite" aria-atomic="true">
      {otherTyping.length
        ? `${otherTyping.map((x) => x.displayName).join("、")} 正在输入…`
        : ""}
    </span>
  );
}
