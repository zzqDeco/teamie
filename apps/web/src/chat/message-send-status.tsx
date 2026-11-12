import type { RefObject } from "react";
import { LoaderCircle, PencilLine, RefreshCw, Trash2 } from "lucide-react";
import { sendMessage } from "../chat";
import { chatStore } from "../store";
import type { LocalMessage } from "../store";
import { restoreComposerDraft } from "../composer-drafts";
import { ICON } from "../icons";
import { discardFailedMessage } from "./conversation-chrome";

/** Sending spinner or the failed-send actions (retry / edit and resend / delete). */
export function MessageSendStatus({
  message,
  conversationId,
  inputRef,
}: {
  message: LocalMessage;
  conversationId: string;
  inputRef: RefObject<HTMLTextAreaElement | null>;
}) {
  if (message.status === "sending") {
    return (
      <span className="send-status">
        <LoaderCircle size={ICON.xs} className="spin" /> 正在发送
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
            message.attachments?.length
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
