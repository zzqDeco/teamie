import { useRef, useState } from "react";
import type { RefObject } from "react";
import { Clock, LoaderCircle, PencilLine, RefreshCw, Trash2 } from "lucide-react";
import { api } from "@teamie/api-client";
import { restoreMessageAttachmentsToDraft } from "../attachment-drafts";
import { retryTransientFailures, sendMessage } from "../chat";
import {
  releaseQueuedMessageForEdit,
  reserveQueuedMessageForEdit,
} from "../pwa/offline-attachment-send";
import { chatStore } from "../store";
import type { LocalMessage } from "../store";
import { restoreComposerDraft } from "../composer-drafts";
import { ICON } from "../icons";
import { toast } from "../toast";
import { discardFailedMessage } from "./conversation-chrome";

/** Outbound message status: queued (offline), sending spinner, or failed actions. */
export function MessageSendStatus({
  message,
  conversationId,
  inputRef,
}: {
  message: LocalMessage;
  conversationId: string;
  inputRef: RefObject<HTMLTextAreaElement | null>;
}) {
  const restoringRef = useRef(false);
  const [restoring, setRestoring] = useState(false);
  const hasStaged = Boolean(message.stagedAttachments?.length);
  if (message.status === "queued") {
    const canEdit = Boolean(message.body || hasStaged || message.attachments?.length);
    return (
      <span className="send-status queued-status">
        <Clock size={ICON.xs} aria-hidden="true" />
        {hasStaged ? "等待网络恢复后上传附件并发送" : "等待网络恢复后发送"}
        {canEdit && (
          <button
            type="button"
            disabled={restoring}
            onClick={() => {
              if (restoringRef.current) return;
              const generation = chatStore.generation;
              const current = (
                chatStore.getSnapshot().messages[conversationId] || []
              ).find((m) => m.clientMessageId === message.clientMessageId);
              if (!current || current.status !== "queued") return;
              restoringRef.current = true;
              setRestoring(true);
              reserveQueuedMessageForEdit(message.clientMessageId, generation);
              const offline =
                api.sessionUnverified ||
                (typeof navigator !== "undefined" && navigator.onLine === false);
              void (async () => {
                let restored = false;
                try {
                  if (hasStaged || current.attachments?.length) {
                    await restoreMessageAttachmentsToDraft(
                      conversationId,
                      current,
                      offline,
                    );
                  }
                  if (chatStore.generation !== generation) return;
                  const latest = (
                    chatStore.getSnapshot().messages[conversationId] || []
                  ).find((m) => m.clientMessageId === message.clientMessageId);
                  if (!latest || latest.status !== "queued") return;
                  restoreComposerDraft(conversationId, latest);
                  chatStore.discardLocal(conversationId, message.clientMessageId);
                  restored = true;
                  inputRef.current?.focus();
                } catch {
                  if (chatStore.generation === generation) {
                    toast.error("恢复附件失败，请稍后重试。");
                  }
                } finally {
                  releaseQueuedMessageForEdit(message.clientMessageId);
                  restoringRef.current = false;
                  setRestoring(false);
                  if (
                    !restored &&
                    chatStore.generation === generation &&
                    !api.sessionUnverified &&
                    (typeof navigator === "undefined" || navigator.onLine !== false)
                  ) {
                    void retryTransientFailures(generation);
                  }
                }
              })();
            }}
          >
            <PencilLine size={ICON.xs} aria-hidden="true" />
            编辑
          </button>
        )}
        <button
          type="button"
          disabled={restoring}
          onClick={() => discardFailedMessage(conversationId, message)}
        >
          <Trash2 size={ICON.xs} aria-hidden="true" />
          取消发送
        </button>
      </span>
    );
  }
  if (message.status === "sending") {
    return (
      <span className="send-status">
        <LoaderCircle size={ICON.xs} className="spin" />{" "}
        {hasStaged ? "正在上传附件并发送…" : "正在发送"}
      </span>
    );
  }
  if (message.status !== "failed") return null;
  return (
    <span className="send-status failed-status">
      {message.error || "发送失败"}
      {!message.permanent && (
        <button
          type="button"
          onClick={() => void sendMessage(message, chatStore.generation)}
        >
          <RefreshCw size={ICON.xs} />
          重试
        </button>
      )}
      {message.permanent && !!message.body && (
        <button
          type="button"
          title={
            message.attachments?.length || hasStaged
              ? "恢复文字到输入框，附件需要重新添加"
              : undefined
          }
          onClick={() => {
            restoreComposerDraft(conversationId, message);
            discardFailedMessage(conversationId, message);
            inputRef.current?.focus();
          }}
        >
          <PencilLine size={ICON.xs} />
          编辑并重发
        </button>
      )}
      <button
        type="button"
        onClick={() => discardFailedMessage(conversationId, message)}
      >
        <Trash2 size={ICON.xs} />
        删除
      </button>
    </span>
  );
}
