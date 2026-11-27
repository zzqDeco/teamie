import { useEffect, useState } from "react";
import { Forward, Image as ImageIcon, RefreshCw } from "lucide-react";
import type { Attachment, ConversationAttachmentItem } from "@teamie/api-client";
import { isPreviewableImage } from "../attachments";
import {
  subscribeAttachmentPreview,
  subscribeAttachmentThumbnail,
} from "../attachment-previews";
import { ICON } from "../icons";

export function isGalleryImageAttachment(att: Attachment): boolean {
  return Boolean(att.image) || isPreviewableImage(att.mediaType);
}

export function monthHeading(iso: string): string {
  const parsed = new Date(iso);
  if (Number.isNaN(parsed.getTime())) return "较早";
  return `${parsed.getFullYear()}年${parsed.getMonth() + 1}月`;
}

export function GalleryImageTile({
  conversationId,
  item,
  visible,
  onOpen,
  onReadyUrl,
}: {
  conversationId: string;
  item: ConversationAttachmentItem;
  visible: boolean;
  onOpen: (attachmentId: string) => void;
  onReadyUrl: (attachmentId: string, url: string | undefined) => void;
}) {
  const { attachment } = item;
  const useThumb = Boolean(attachment.image);
  const [state, setState] = useState<{
    key: string;
    url?: string;
    retryable?: boolean;
  }>({ key: "" });
  const [retryNonce, setRetryNonce] = useState(0);
  const subKey = `${conversationId}:${attachment.id}:${useThumb ? "t" : "c"}:${retryNonce}`;

  useEffect(() => {
    if (!visible) return;
    let active = true;
    const onPreview = (preview: { url?: string; retryable?: boolean }) => {
      if (!active) return;
      setState({ key: subKey, url: preview.url, retryable: preview.retryable });
      onReadyUrl(attachment.id, preview.url);
    };
    const unsubscribe = useThumb
      ? subscribeAttachmentThumbnail(
          conversationId,
          attachment.id,
          attachment.mediaType,
          onPreview,
        )
      : subscribeAttachmentPreview(conversationId, attachment.id, onPreview);
    return () => {
      active = false;
      unsubscribe();
      onReadyUrl(attachment.id, undefined);
    };
  }, [
    attachment.id,
    attachment.mediaType,
    conversationId,
    onReadyUrl,
    subKey,
    useThumb,
    visible,
  ]);

  const matched = state.key === subKey ? state : undefined;
  const url = matched?.url;
  const failedRetryable = !url && matched?.retryable === true;

  return (
    <div className="gallery-tile-cell" data-gallery-tile={attachment.id}>
      <button
        type="button"
        className="gallery-image-tile"
        aria-label={`预览图片 ${attachment.filename}`}
        onClick={() => onOpen(attachment.id)}
      >
        {url ? (
          <img
            src={url}
            alt={attachment.filename}
            className="gallery-tile-img"
            loading="lazy"
          />
        ) : (
          <span className="gallery-tile-placeholder" aria-hidden="true">
            {failedRetryable ? (
              <RefreshCw size={ICON.md} />
            ) : (
              <ImageIcon size={ICON.md} />
            )}
          </span>
        )}
        {item.isForwarded && (
          <span className="gallery-tile-badge" title="转发图片">
            <Forward size={ICON.xs} aria-hidden="true" />
          </span>
        )}
      </button>
      {failedRetryable && (
        <button
          type="button"
          className="gallery-tile-retry"
          aria-label={`重试加载 ${attachment.filename}`}
          onClick={() => setRetryNonce((n) => n + 1)}
        >
          重试
        </button>
      )}
    </div>
  );
}
