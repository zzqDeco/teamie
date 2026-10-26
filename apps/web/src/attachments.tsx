import { useEffect, useRef, useState } from "react";
import {
  Download,
  File as FileIcon,
  LoaderCircle,
  Paperclip,
  RefreshCw,
  X,
} from "lucide-react";
import { api } from "@teamie/api-client";
import type { Attachment, AttachmentLimits } from "@teamie/api-client";
import { errorText } from "./chat";
import { ICON } from "./icons";
import { Button, Modal } from "./ui";
import "./attachments.css";

const SAFE_IMAGE_MEDIA_TYPES = new Set([
  "image/png",
  "image/jpeg",
  "image/webp",
  "image/gif",
]);

export function isPreviewableImage(mediaType: string): boolean {
  return SAFE_IMAGE_MEDIA_TYPES.has(mediaType.trim().toLowerCase());
}

export type DraftFile = {
  id: string;
  uploadKey: string;
  file: File;
  state: "uploading" | "ready" | "failed";
  attachment?: Attachment;
  error?: string;
};
export function fileSize(bytes: number) {
  return bytes >= 1024 * 1024
    ? `${(bytes / (1024 * 1024)).toFixed(1)} MiB`
    : bytes >= 1024
      ? `${Math.ceil(bytes / 1024)} KiB`
      : `${bytes} B`;
}
export function validateFile(
  file: Pick<File, "name" | "size">,
  limit: number,
): string {
  if (!file.size) return "不能上传空文件";
  if (file.size > limit) return `文件超过 ${fileSize(limit)} 上限`;
  if (
    !file.name ||
    Array.from(file.name).length > 180 ||
    new TextEncoder().encode(file.name).length > 512 ||
    /[/\\]/.test(file.name) ||
    Array.from(file.name).some(
      (c) => c.codePointAt(0)! < 32 || c.codePointAt(0) === 127,
    ) ||
    /\p{Cf}/u.test(file.name) ||
    file.name.trim() !== file.name
  )
    return "文件名无效，请重命名后上传";
  return "";
}

export function useAttachmentDraft(
  conversationId: string,
  limits: AttachmentLimits,
) {
  const [files, setFiles] = useState<DraftFile[]>([]);
  const [error, setError] = useState("");
  const current = useRef<DraftFile[]>([]);
  const controllers = useRef(new Map<string, AbortController>());
  const active = useRef(true);
  const queue = useRef(Promise.resolve());
  const update = (value: DraftFile[]) => {
    current.current = value;
    if (active.current) setFiles(value);
  };
  useEffect(() => {
    active.current = true;
    const requests = controllers.current;
    return () => {
      active.current = false;
      requests.forEach((controller) => controller.abort());
      requests.clear();
      current.current.forEach((file) => {
        if (file.attachment)
          void api
            .deletePendingAttachment(conversationId, file.attachment.id)
            .catch(() => {});
      });
      current.current = [];
    };
  }, [conversationId]);
  async function upload(file: DraftFile) {
    const controller = new AbortController();
    controllers.current.set(file.id, controller);
    update(
      current.current.map((f) =>
        f.id === file.id ? { ...f, state: "uploading", error: undefined } : f,
      ),
    );
    try {
      const result = await api.uploadAttachment(
        conversationId,
        file.file,
        controller.signal,
        file.uploadKey,
      );
      if (!active.current || !current.current.some((f) => f.id === file.id)) {
        void api
          .deletePendingAttachment(conversationId, result.attachment.id)
          .catch(() => {});
        return;
      }
      update(
        current.current.map((f) =>
          f.id === file.id
            ? { ...f, state: "ready", attachment: result.attachment }
            : f,
        ),
      );
    } catch (e) {
      if (active.current && !controller.signal.aborted)
        update(
          current.current.map((f) =>
            f.id === file.id
              ? { ...f, state: "failed", error: errorText(e) }
              : f,
          ),
        );
    } finally {
      controllers.current.delete(file.id);
    }
  }
  function queueUpload(file: DraftFile) {
    queue.current = queue.current.then(async () => {
      if (active.current && current.current.some((f) => f.id === file.id))
        await upload(file);
    });
  }
  function add(selected: FileList | File[]) {
    setError("");
    for (const file of Array.from(selected)) {
      if (current.current.length >= limits.maxFilesPerMessage) {
        setError(`每条消息最多 ${limits.maxFilesPerMessage} 个文件`);
        break;
      }
      const failure = validateFile(file, limits.maxFileBytes);
      if (failure) {
        setError(`${file.name}：${failure}`);
        continue;
      }
      const draft: DraftFile = {
        id: crypto.randomUUID(),
        uploadKey: crypto.randomUUID(),
        file,
        state: "uploading",
      };
      update([...current.current, draft]);
      queueUpload(draft);
    }
  }
  function remove(id: string) {
    const file = current.current.find((f) => f.id === id);
    controllers.current.get(id)?.abort();
    update(current.current.filter((f) => f.id !== id));
    if (file?.attachment)
      void api
        .deletePendingAttachment(conversationId, file.attachment.id)
        .catch(() => {});
  }
  function consume() {
    const attachments = current.current.flatMap((f) =>
      f.attachment ? [f.attachment] : [],
    );
    update([]);
    setError("");
    return attachments;
  }
  return {
    files,
    error,
    add,
    remove,
    retry: queueUpload,
    consume,
    ready: files.length > 0 && files.every((f) => f.state === "ready"),
    blocked: files.some((f) => f.state !== "ready"),
  };
}

export function AttachmentPicker({
  onSelect,
  disabled,
  limit,
}: {
  onSelect: (files: FileList) => void;
  disabled: boolean;
  limit: number;
}) {
  const input = useRef<HTMLInputElement>(null);
  return (
    <>
      <input
        className="attachment-input"
        hidden
        ref={input}
        type="file"
        multiple
        aria-label="选择聊天文件"
        onChange={(event) => {
          if (event.target.files) onSelect(event.target.files);
          event.target.value = "";
        }}
      />
      <button
        type="button"
        className="attachment-add"
        disabled={disabled}
        onClick={() => input.current?.click()}
        aria-label="添加文件"
        title={`添加文件，每个最多 ${fileSize(limit)}`}
      >
        <Paperclip size={ICON.md} />
      </button>
    </>
  );
}

export function AttachmentDraft({
  files,
  error,
  onRemove,
  onRetry,
}: {
  files: DraftFile[];
  error: string;
  onRemove: (id: string) => void;
  onRetry: (file: DraftFile) => void;
}) {
  return (
    <>
      {files.length > 0 && (
        <ul className="attachment-drafts" aria-label="待发送文件">
          {files.map((file) => (
            <li key={file.id} className={`attachment-draft ${file.state}`}>
              <FileIcon size={ICON.md} />
              <span className="attachment-details">
                <span className="attachment-name">{file.file.name}</span>
                <span
                  className="attachment-meta"
                  role={file.state === "failed" ? "alert" : "status"}
                >
                  {file.state === "uploading"
                    ? "正在上传…"
                    : file.state === "ready"
                      ? `${fileSize(file.file.size)} · 已就绪`
                      : file.error || "上传失败"}
                </span>
              </span>
              {file.state === "uploading" && (
                <LoaderCircle size={ICON.sm} className="spin" />
              )}
              {file.state === "failed" && (
                <button
                  type="button"
                  aria-label={`重试上传 ${file.file.name}`}
                  onClick={() => onRetry(file)}
                >
                  <RefreshCw size={ICON.sm} />
                </button>
              )}
              <button
                type="button"
                aria-label={`移除文件 ${file.file.name}`}
                onClick={() => onRemove(file.id)}
              >
                <X size={ICON.sm} />
              </button>
            </li>
          ))}
        </ul>
      )}
      {error && (
        <p role="alert" className="attachment-error">
          {error}
        </p>
      )}
    </>
  );
}

export function MessageAttachments({
  conversationId,
  attachments,
}: {
  conversationId: string;
  attachments: Attachment[];
}) {
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [previewUrls, setPreviewUrls] = useState<Record<string, string>>({});
  const [previewFile, setPreviewFile] = useState<Attachment | null>(null);
  const imageIdsKey = attachments
    .filter((attachment) => isPreviewableImage(attachment.mediaType))
    .map((attachment) => attachment.id)
    .join("\u0000");

  useEffect(() => {
    setPreviewFile(null);
    if (!imageIdsKey) {
      setPreviewUrls({});
      return;
    }
    let active = true;
    const createdUrls: string[] = [];
    const ids = imageIdsKey.split("\u0000");
    setPreviewUrls({});
    for (const id of ids) {
      void api
        .attachmentContent(conversationId, id)
        .then((blob) => {
          if (!active) return;
          const url = URL.createObjectURL(blob);
          createdUrls.push(url);
          setPreviewUrls((prev) => ({ ...prev, [id]: url }));
        })
        .catch(() => {
          /* Fall back to standard file card if thumbnail loading fails. */
        });
    }
    return () => {
      active = false;
      for (const url of createdUrls) {
        URL.revokeObjectURL(url);
      }
    };
  }, [conversationId, imageIdsKey]);

  async function download(attachment: Attachment) {
    setBusy(attachment.id);
    setError("");
    try {
      const blob = await api.attachmentContent(conversationId, attachment.id);
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = attachment.filename;
      link.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy("");
    }
  }
  const activePreviewUrl = previewFile ? previewUrls[previewFile.id] : undefined;
  return (
    <>
      <ul className="message-attachments" aria-label="消息附件">
        {attachments.map((attachment) => {
          const previewUrl = previewUrls[attachment.id];
          return (
            <li
              key={attachment.id}
              className={previewUrl ? "attachment-image-item" : undefined}
            >
              {previewUrl && (
                <button
                  type="button"
                  className="attachment-thumbnail-button"
                  onClick={() => setPreviewFile(attachment)}
                  aria-label={`预览图片 ${attachment.filename}`}
                >
                  <img
                    className="attachment-thumbnail"
                    src={previewUrl}
                    alt={attachment.filename}
                    loading="lazy"
                  />
                </button>
              )}
              <button
                type="button"
                disabled={!!busy}
                onClick={() => void download(attachment)}
                aria-label={`下载文件 ${attachment.filename}`}
              >
                <FileIcon size={ICON.lg} />
                <span className="attachment-details">
                  <span className="attachment-name">{attachment.filename}</span>
                  <span className="attachment-meta">
                    {fileSize(attachment.sizeBytes)}
                  </span>
                </span>
                {busy === attachment.id ? (
                  <LoaderCircle className="spin" size={ICON.sm} />
                ) : (
                  <Download size={ICON.sm} />
                )}
              </button>
            </li>
          );
        })}
      </ul>
      {previewFile && activePreviewUrl && (
        <Modal
          title={previewFile.filename}
          subtitle={fileSize(previewFile.sizeBytes)}
          onClose={() => setPreviewFile(null)}
          className="attachment-lightbox-modal"
        >
          <div className="attachment-lightbox-body">
            <img
              className="attachment-lightbox-image"
              src={activePreviewUrl}
              alt={previewFile.filename}
            />
          </div>
          <div className="modal-actions">
            <Button
              type="button"
              variant="secondary"
              onClick={() => setPreviewFile(null)}
            >
              关闭
            </Button>
            <Button
              type="button"
              busy={busy === previewFile.id}
              onClick={() => void download(previewFile)}
            >
              下载原图
            </Button>
          </div>
        </Modal>
      )}
      {error && (
        <p role="alert" className="attachment-error">
          {error}
        </p>
      )}
    </>
  );
}
