import { useEffect, useRef, useState } from "react";
import type { TouchEvent } from "react";
import {
  ChevronLeft,
  ChevronRight,
  CornerUpLeft,
  LoaderCircle,
  RefreshCw,
  ZoomIn,
  ZoomOut,
} from "lucide-react";
import type { Attachment } from "@teamie/api-client";
import { subscribeAttachmentPreview } from "../attachment-previews";
import { ICON } from "../icons";
import { Button, date, IconButton, Modal, time } from "../ui";

export type LightboxImageItem = {
  attachment: Attachment;
  conversationId: string;
  thumbnailUrl?: string;
  messageId?: string;
  senderName?: string;
  createdAt?: string;
  isForwarded?: boolean;
};

function formatFileSize(bytes: number): string {
  return bytes >= 1024 * 1024
    ? `${(bytes / (1024 * 1024)).toFixed(1)} MiB`
    : bytes >= 1024
      ? `${Math.ceil(bytes / 1024)} KiB`
      : `${bytes} B`;
}

/**
 * Shared image lightbox used by timeline attachments, forwarded cards, and the
 * conversation media gallery. Shows the cached thumbnail immediately with zero
 * flicker while upgrading in the background to full-resolution `/content`,
 * supporting prev/next carousel navigation, keyboard arrows, mobile swipe,
 * fit/100% zoom, download, and locate-in-conversation.
 */
export function AttachmentLightbox({
  items,
  index,
  onIndexChange,
  onClose,
  onDownload,
  downloadingId = "",
  onLocateMessage,
}: {
  items: LightboxImageItem[];
  index: number;
  onIndexChange: (nextIndex: number) => void;
  onClose: () => void;
  onDownload: (attachment: Attachment) => void;
  downloadingId?: string;
  onLocateMessage?: (messageId: string) => void;
}) {
  const safeIndex =
    items.length > 0 ? Math.min(Math.max(index, 0), items.length - 1) : -1;
  const current = safeIndex >= 0 ? items[safeIndex] : undefined;
  const currentId = current?.attachment.id || "";
  const currentConvId = current?.conversationId || "";

  const [fullState, setFullState] = useState<{
    key: string;
    url?: string;
    retryable?: boolean;
  }>({ key: "" });
  const [retryNonce, setRetryNonce] = useState(0);
  const [zoomKey, setZoomKey] = useState("");
  const touchOrigin = useRef<{ x: number; y: number } | null>(null);

  const activeKey = `${currentConvId}:${currentId}:${retryNonce}`;
  const zoomed = Boolean(activeKey && zoomKey === `${currentConvId}:${currentId}`);

  useEffect(() => {
    if (!currentConvId || !currentId) return;
    let active = true;
    const unsubscribe = subscribeAttachmentPreview(
      currentConvId,
      currentId,
      (preview) => {
        if (!active) return;
        setFullState({
          key: activeKey,
          url: preview.url,
          retryable: preview.retryable,
        });
      },
    );
    return () => {
      active = false;
      unsubscribe();
    };
  }, [activeKey, currentConvId, currentId]);

  const canPrev = safeIndex > 0;
  const canNext = safeIndex >= 0 && safeIndex < items.length - 1;

  useEffect(() => {
    if (items.length <= 1) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey) return;
      if (event.key === "ArrowLeft" && safeIndex > 0) {
        event.preventDefault();
        onIndexChange(safeIndex - 1);
      } else if (event.key === "ArrowRight" && safeIndex < items.length - 1) {
        event.preventDefault();
        onIndexChange(safeIndex + 1);
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [items.length, onIndexChange, safeIndex]);

  if (!current) return null;

  const matchedFull = fullState.key === activeKey ? fullState : undefined;
  const displayUrl = matchedFull?.url || current.thumbnailUrl;
  const upgrading = !matchedFull?.url && matchedFull?.retryable === undefined;
  const fullFailedRetryable = !matchedFull?.url && matchedFull?.retryable === true;
  const fullDenied = !matchedFull?.url && matchedFull?.retryable === false;

  const subtitleParts = [formatFileSize(current.attachment.sizeBytes)];
  if (current.attachment.image) {
    subtitleParts.push(
      `${current.attachment.image.width} × ${current.attachment.image.height}`,
    );
  }
  if (current.senderName) subtitleParts.push(current.senderName);
  if (current.createdAt) {
    subtitleParts.push(`${date(current.createdAt)} ${time(current.createdAt)}`);
  }
  if (items.length > 1) {
    subtitleParts.push(`${safeIndex + 1} / ${items.length}`);
  }

  function toggleZoom() {
    const idKey = `${currentConvId}:${currentId}`;
    setZoomKey((prev) => (prev === idKey ? "" : idKey));
  }

  function handleTouchStart(event: TouchEvent<HTMLDivElement>) {
    if (event.touches.length !== 1 || zoomed) return;
    touchOrigin.current = {
      x: event.touches[0].clientX,
      y: event.touches[0].clientY,
    };
  }

  function handleTouchEnd(event: TouchEvent<HTMLDivElement>) {
    const start = touchOrigin.current;
    touchOrigin.current = null;
    if (!start || zoomed || !event.changedTouches.length) return;
    const dx = event.changedTouches[0].clientX - start.x;
    const dy = Math.abs(event.changedTouches[0].clientY - start.y);
    if (Math.abs(dx) < 48 || dy > 44) return;
    if (dx > 0 && canPrev) onIndexChange(safeIndex - 1);
    else if (dx < 0 && canNext) onIndexChange(safeIndex + 1);
  }

  return (
    <Modal
      title={current.attachment.filename}
      subtitle={subtitleParts.join(" · ")}
      onClose={onClose}
      className="attachment-lightbox-modal"
    >
      <div
        className={`attachment-lightbox-body ${zoomed ? "is-zoomed" : ""}`}
        onTouchStart={handleTouchStart}
        onTouchEnd={handleTouchEnd}
      >
        {items.length > 1 && (
          <IconButton
            className="attachment-lightbox-nav prev"
            label="上一张图片"
            disabled={!canPrev}
            onClick={() => canPrev && onIndexChange(safeIndex - 1)}
          >
            <ChevronLeft size={ICON.lg} />
          </IconButton>
        )}

        {displayUrl ? (
          <img
            className={`attachment-lightbox-image ${zoomed ? "is-zoomed" : ""}`}
            src={displayUrl}
            alt={current.attachment.filename}
            onDoubleClick={toggleZoom}
          />
        ) : (
          <div className="attachment-lightbox-placeholder" role="status">
            {upgrading ? (
              <>
                <LoaderCircle size={ICON.lg} className="spin" aria-hidden="true" />
                <span>正在加载原图…</span>
              </>
            ) : fullDenied ? (
              <span>图片暂时无法访问</span>
            ) : (
              <span>原图加载失败</span>
            )}
          </div>
        )}

        {items.length > 1 && (
          <IconButton
            className="attachment-lightbox-nav next"
            label="下一张图片"
            disabled={!canNext}
            onClick={() => canNext && onIndexChange(safeIndex + 1)}
          >
            <ChevronRight size={ICON.lg} />
          </IconButton>
        )}
      </div>

      <div className="attachment-lightbox-status-row">
        {displayUrl && upgrading && (
          <span className="attachment-lightbox-hint" role="status">
            <LoaderCircle size={ICON.xs} className="spin" aria-hidden="true" />
            正在加载原图…
          </span>
        )}
        {fullFailedRetryable && (
          <button
            type="button"
            className="attachment-lightbox-retry"
            onClick={() => setRetryNonce((n) => n + 1)}
          >
            <RefreshCw size={ICON.xs} aria-hidden="true" />
            重试加载原图
          </button>
        )}
        {displayUrl && (
          <button
            type="button"
            className="attachment-lightbox-zoom-btn"
            aria-pressed={zoomed}
            onClick={toggleZoom}
          >
            {zoomed ? (
              <>
                <ZoomOut size={ICON.xs} aria-hidden="true" />
                适应窗口
              </>
            ) : (
              <>
                <ZoomIn size={ICON.xs} aria-hidden="true" />
                原始尺寸
              </>
            )}
          </button>
        )}
      </div>

      <div className="modal-actions">
        {current.messageId && onLocateMessage && (
          <Button
            type="button"
            variant="secondary"
            onClick={() => {
              onClose();
              onLocateMessage(current.messageId!);
            }}
          >
            <CornerUpLeft size={ICON.sm} aria-hidden="true" />
            定位到原消息
          </Button>
        )}
        <Button type="button" variant="secondary" onClick={onClose}>
          关闭
        </Button>
        <Button
          type="button"
          busy={downloadingId === current.attachment.id}
          disabled={
            Boolean(downloadingId) && downloadingId !== current.attachment.id
          }
          onClick={() => onDownload(current.attachment)}
        >
          下载原图
        </Button>
      </div>
    </Modal>
  );
}
