import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { RefObject } from "react";
import {
  CornerUpLeft,
  Download,
  File as FileIcon,
  Images,
  LoaderCircle,
  Search,
  X,
} from "lucide-react";
import { api } from "@teamie/api-client";
import type {
  Attachment,
  Conversation,
  ConversationAttachmentItem,
} from "@teamie/api-client";
import { AgentBadge } from "../agent-ui";
import { fileSize } from "../attachments";
import { useChatState } from "../chat";
import { errorText } from "../errors";
import { ICON } from "../icons";
import { chatStore } from "../store";
import { Button, date, IconButton, Notice, Tabs, time } from "../ui";
import { AttachmentLightbox } from "./attachment-lightbox";
import type { LightboxImageItem } from "./attachment-lightbox";
import {
  GalleryImageTile,
  isGalleryImageAttachment,
  monthHeading,
} from "./gallery-image-tile";
import "../styles/gallery.css";

type GalleryTab = "image" | "file";

type GalleryPageState = {
  key: string;
  items: ConversationAttachmentItem[];
  watermarkSeq: number;
  cursor?: string;
  error: string;
  loadingMore: boolean;
};

export function ConversationGalleryPane({
  paneRef,
  overlayMembers,
  mobile,
  sidebar,
  conversation,
  onClose,
  onLocateMessage,
}: {
  paneRef: RefObject<HTMLElement | null>;
  overlayMembers: boolean;
  mobile: boolean;
  sidebar: boolean;
  conversation: Conversation;
  onClose: () => void;
  onLocateMessage: (messageId: string) => void;
}) {
  const snapshot = useChatState();
  const [tab, setTab] = useState<GalleryTab>("image");
  const [fileQuery, setFileQuery] = useState("");
  const [page, setPage] = useState<GalleryPageState | null>(null);
  const [retryNonce, setRetryNonce] = useState(0);
  const [downloadingId, setDownloadingId] = useState("");
  const [downloadError, setDownloadError] = useState("");
  const [lightboxId, setLightboxId] = useState<string | null>(null);
  const [thumbUrls, setThumbUrls] = useState<Record<string, string>>({});
  const [visibleTileIds, setVisibleTileIds] = useState<Set<string>>(
    () => new Set(),
  );

  const scrollRef = useRef<HTMLDivElement>(null);
  const requests = useRef(new Set<AbortController>());
  const sequence = useRef(0);
  const generation = chatStore.generation;
  const conversationId = conversation.id;

  const trimmedQuery = tab === "file" ? fileQuery.trim() : "";
  const validQuery = Array.from(trimmedQuery).length <= 200;
  const queryKey = JSON.stringify([
    generation,
    conversationId,
    tab,
    trimmedQuery,
    retryNonce,
  ]);
  const currentPage = page?.key === queryKey ? page : null;
  const pending = validQuery && !currentPage;

  useEffect(() => {
    const operation = ++sequence.current;
    const activeRequests = requests.current;
    const controller = new AbortController();
    const isCurrent = () =>
      !controller.signal.aborted &&
      generation === chatStore.generation &&
      operation === sequence.current;

    if (!validQuery) {
      return () => {
        controller.abort();
      };
    }

    const delayMs = tab === "file" && trimmedQuery ? 250 : 0;
    const timer = setTimeout(() => {
      activeRequests.add(controller);
      const startLastSeq =
        chatStore.getSnapshot().conversations[conversationId]?.lastSeq ?? 0;
      void api
        .listConversationAttachments(
          conversationId,
          {
            kind: tab,
            ...(trimmedQuery ? { q: trimmedQuery } : {}),
            limit: 40,
          },
          controller.signal,
        )
        .then((response) => {
          if (isCurrent()) {
            const maxItemSeq = response.items.reduce(
              (max, it) => Math.max(max, it.messageSeq),
              0,
            );
            setPage({
              key: queryKey,
              items: response.items,
              watermarkSeq: Math.max(startLastSeq, maxItemSeq),
              cursor: response.cursor,
              error: "",
              loadingMore: false,
            });
          }
        })
        .catch((err) => {
          if (isCurrent()) {
            setPage({
              key: queryKey,
              items: [],
              watermarkSeq: 0,
              error: errorText(err),
              loadingMore: false,
            });
          }
        })
        .finally(() => activeRequests.delete(controller));
    }, delayMs);

    return () => {
      clearTimeout(timer);
      controller.abort();
      activeRequests.forEach((req) => req.abort());
      activeRequests.clear();
    };
  }, [conversationId, generation, queryKey, tab, trimmedQuery, validQuery]);

  async function loadMore() {
    if (!currentPage?.cursor || currentPage.loadingMore) return;
    const operation = sequence.current;
    const controller = new AbortController();
    const isCurrent = () =>
      !controller.signal.aborted &&
      generation === chatStore.generation &&
      operation === sequence.current;
    requests.current.add(controller);
    setPage({ ...currentPage, error: "", loadingMore: true });
    try {
      const response = await api.listConversationAttachments(
        conversationId,
        {
          kind: tab,
          ...(trimmedQuery ? { q: trimmedQuery } : {}),
          cursor: currentPage.cursor,
          limit: 40,
        },
        controller.signal,
      );
      if (isCurrent()) {
        setPage((old) => {
          if (!old || old.key !== queryKey) return old;
          const seen = new Set(old.items.map((it) => it.attachment.id));
          return {
            ...old,
            items: [
              ...old.items,
              ...response.items.filter((it) => !seen.has(it.attachment.id)),
            ],
            cursor: response.cursor,
            loadingMore: false,
          };
        });
      }
    } catch (err) {
      if (isCurrent()) {
        setPage((old) =>
          old?.key === queryKey
            ? { ...old, error: errorText(err), loadingMore: false }
            : old,
        );
      }
    } finally {
      requests.current.delete(controller);
    }
  }

  // Reconcile against live chatStore messages: drop retracted items immediately,
  // and prepend newly arrived live attachments when unfiltered.
  const storeMessages = snapshot.messages[conversationId];
  const visibleItems = useMemo(() => {
    if (!currentPage) return [];
    const liveMsgs = storeMessages || [];
    const retractedIds = new Set<string>();
    for (const msg of liveMsgs) {
      if (msg.retractedAt) retractedIds.add(msg.id);
    }
    const baseItems = currentPage.items.filter(
      (item) => !retractedIds.has(item.messageId),
    );
    const seenAttIds = new Set(baseItems.map((it) => it.attachment.id));
    const watermarkSeq = currentPage.watermarkSeq;
    const livePrepends: ConversationAttachmentItem[] = [];
    const queryLower = trimmedQuery.toLowerCase();
    for (const msg of liveMsgs) {
      if (msg.retractedAt || msg.status || msg.seq <= watermarkSeq) continue;
      const pushMatching = (list: Attachment[] | undefined, isForwarded: boolean) => {
        if (!list?.length) return;
        for (const att of list) {
          if (seenAttIds.has(att.id)) continue;
          const isImg = isGalleryImageAttachment(att);
          if (tab === "image" ? !isImg : isImg) continue;
          if (queryLower && !att.filename.toLowerCase().includes(queryLower)) {
            continue;
          }
          seenAttIds.add(att.id);
          livePrepends.push({
            attachment: att,
            messageId: msg.id,
            messageSeq: msg.seq,
            senderId: msg.senderId,
            senderName: msg.senderName,
            senderKind: msg.senderKind,
            createdAt: msg.createdAt,
            isForwarded,
          });
        }
      };
      pushMatching(msg.attachments, false);
      pushMatching(msg.forwarded?.attachments, true);
    }
    livePrepends.sort(
      (a, b) =>
        b.messageSeq - a.messageSeq ||
        b.attachment.id.localeCompare(a.attachment.id),
    );
    return [...livePrepends, ...baseItems];
  }, [currentPage, storeMessages, tab, trimmedQuery]);

  // Viewport-driven IntersectionObserver for image tiles so off-screen tiles
  // do not saturate the thumbnail queue.
  const imageIdsSignature =
    tab === "image"
      ? visibleItems.map((it) => it.attachment.id).join("|")
      : "";

  useEffect(() => {
    if (tab !== "image" || !imageIdsSignature) return;
    const container = scrollRef.current;
    if (!container) return;
    const nodes = Array.from(
      container.querySelectorAll<HTMLElement>("[data-gallery-tile]"),
    );
    if (typeof IntersectionObserver === "undefined") {
      setVisibleTileIds(new Set(imageIdsSignature.split("|")));
      return;
    }
    const observer = new IntersectionObserver(
      (entries) => {
        setVisibleTileIds((prev) => {
          const next = new Set(prev);
          let changed = false;
          for (const entry of entries) {
            const id = (entry.target as HTMLElement).dataset.galleryTile;
            if (!id) continue;
            if (entry.isIntersecting) {
              if (!next.has(id)) {
                next.add(id);
                changed = true;
              }
            } else if (next.delete(id)) {
              changed = true;
            }
          }
          return changed ? next : prev;
        });
      },
      { root: container, rootMargin: "200px 0px" },
    );
    for (const node of nodes) observer.observe(node);
    return () => observer.disconnect();
  }, [imageIdsSignature, tab]);

  const handleReadyUrl = useCallback(
    (attachmentId: string, url: string | undefined) => {
      setThumbUrls((prev) => {
        if (url && prev[attachmentId] === url) return prev;
        if (!url && !(attachmentId in prev)) return prev;
        const next = { ...prev };
        if (url) next[attachmentId] = url;
        else delete next[attachmentId];
        return next;
      });
    },
    [],
  );

  async function download(attachment: Attachment) {
    setDownloadingId(attachment.id);
    setDownloadError("");
    const startGen = chatStore.generation;
    try {
      const blob = await api.attachmentContent(conversationId, attachment.id);
      if (startGen !== chatStore.generation) return;
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = attachment.filename;
      link.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (err) {
      if (startGen === chatStore.generation) setDownloadError(errorText(err));
    } finally {
      setDownloadingId((cur) => (cur === attachment.id ? "" : cur));
    }
  }

  const imageGroups = useMemo(() => {
    if (tab !== "image") return [];
    const groups: Array<{ label: string; items: ConversationAttachmentItem[] }> =
      [];
    for (const item of visibleItems) {
      const label = monthHeading(item.createdAt);
      const last = groups[groups.length - 1];
      if (last && last.label === label) last.items.push(item);
      else groups.push({ label, items: [item] });
    }
    return groups;
  }, [tab, visibleItems]);

  const lightboxItems: LightboxImageItem[] =
    tab === "image"
      ? visibleItems.map((item) => ({
          attachment: item.attachment,
          conversationId,
          thumbnailUrl: thumbUrls[item.attachment.id],
          messageId: item.messageId,
          senderName: item.senderName,
          createdAt: item.createdAt,
          isForwarded: item.isForwarded,
        }))
      : [];
  const lightboxIndex = lightboxId
    ? lightboxItems.findIndex((it) => it.attachment.id === lightboxId)
    : -1;

  const hasMore = Boolean(currentPage?.cursor);

  return (
    <>
      {overlayMembers && !sidebar && (
        <button
          type="button"
          className="side-pane-shade"
          aria-label="关闭媒体与文件面板"
          onClick={onClose}
        />
      )}
      <aside
        className="side-pane gallery-pane"
        ref={paneRef}
        role={overlayMembers ? "dialog" : undefined}
        aria-modal={overlayMembers ? true : undefined}
        aria-label="会话媒体与文件"
        tabIndex={-1}
        inert={mobile && sidebar}
      >
        <header>
          <span className="gallery-header-title">
            <Images size={ICON.md} aria-hidden="true" />
            媒体与文件
          </span>
          <IconButton label="关闭媒体与文件" onClick={onClose}>
            <X size={ICON.md} />
          </IconButton>
        </header>

        <div className="gallery-toolbar">
          <Tabs
            mode="group"
            className="gallery-tabs"
            ariaLabel="画廊分类"
            value={tab}
            onChange={(next) => {
              setTab(next);
              setLightboxId(null);
              setDownloadError("");
            }}
            items={[
              { value: "image", label: "图片" },
              { value: "file", label: "文件" },
            ]}
          />
          {tab === "file" && (
            <label className="gallery-file-search">
              <Search size={ICON.sm} aria-hidden="true" />
              <input
                type="search"
                value={fileQuery}
                onChange={(event) => setFileQuery(event.target.value)}
                placeholder="按文件名筛选…"
                aria-label="按文件名筛选会话文件"
              />
              {fileQuery && (
                <IconButton
                  label="清空文件名筛选"
                  onClick={() => setFileQuery("")}
                >
                  <X size={ICON.xs} />
                </IconButton>
              )}
            </label>
          )}
          {!validQuery && (
            <Notice kind="error">文件名关键词最多 200 个字符</Notice>
          )}
          {downloadError && <Notice kind="error">{downloadError}</Notice>}
        </div>

        <div
          className="gallery-scroll"
          ref={scrollRef}
          aria-busy={pending || undefined}
        >
          {pending ? (
            <p className="gallery-empty" role="status">
              <LoaderCircle size={ICON.sm} className="spin" aria-hidden="true" />
              正在加载{tab === "image" ? "图片" : "文件"}…
            </p>
          ) : currentPage?.error && !visibleItems.length ? (
            <div className="gallery-error">
              <Notice kind="error">{currentPage.error}</Notice>
              <Button
                type="button"
                variant="secondary"
                onClick={() => {
                  setPage(null);
                  setRetryNonce((n) => n + 1);
                }}
              >
                重试
              </Button>
            </div>
          ) : visibleItems.length === 0 ? (
            <>
              <p className="gallery-empty" role="status">
                {hasMore
                  ? "当前页附件已被撤回，可继续加载更早内容"
                  : tab === "image"
                    ? "当前对话暂无图片"
                    : trimmedQuery
                      ? "没有匹配的文件"
                      : "当前对话暂无文件"}
              </p>
              {hasMore && (
                <Button
                  type="button"
                  variant="secondary"
                  className="gallery-more"
                  busy={Boolean(currentPage?.loadingMore)}
                  onClick={() => void loadMore()}
                >
                  加载更多
                </Button>
              )}
            </>
          ) : tab === "image" ? (
            <>
              <div className="gallery-month-list">
                {imageGroups.map((group) => (
                  <section key={group.label} className="gallery-month-group">
                    <h4 className="gallery-month-heading">{group.label}</h4>
                    <div className="gallery-image-grid">
                      {group.items.map((item) => (
                        <GalleryImageTile
                          key={item.attachment.id}
                          conversationId={conversationId}
                          item={item}
                          visible={visibleTileIds.has(item.attachment.id)}
                          onOpen={(id) => setLightboxId(id)}
                          onReadyUrl={handleReadyUrl}
                        />
                      ))}
                    </div>
                  </section>
                ))}
              </div>
              {currentPage?.error && (
                <Notice kind="error">{currentPage.error}</Notice>
              )}
              {hasMore && (
                <Button
                  type="button"
                  variant="secondary"
                  className="gallery-more"
                  busy={Boolean(currentPage?.loadingMore)}
                  onClick={() => void loadMore()}
                >
                  加载更多
                </Button>
              )}
            </>
          ) : (
            <>
              <ul className="gallery-file-list" aria-label="会话文件列表">
                {visibleItems.map((item) => {
                  const { attachment } = item;
                  return (
                    <li key={attachment.id} className="gallery-file-row">
                      <span className="gallery-file-icon" aria-hidden="true">
                        <FileIcon size={ICON.md} />
                      </span>
                      <div className="gallery-file-info">
                        <b title={attachment.filename}>{attachment.filename}</b>
                        <span className="gallery-file-meta">
                          <span>{fileSize(attachment.sizeBytes)}</span>
                          <span>·</span>
                          <span>{item.senderName}</span>
                          {item.senderKind === "agent" && <AgentBadge compact />}
                          {item.isForwarded && (
                            <span className="gallery-forwarded-tag">转发</span>
                          )}
                          <span>·</span>
                          <time dateTime={item.createdAt}>
                            {date(item.createdAt)} {time(item.createdAt)}
                          </time>
                        </span>
                      </div>
                      <div className="gallery-file-actions">
                        <IconButton
                          label={`下载 ${attachment.filename}`}
                          disabled={Boolean(downloadingId)}
                          onClick={() => void download(attachment)}
                        >
                          {downloadingId === attachment.id ? (
                            <LoaderCircle size={ICON.sm} className="spin" />
                          ) : (
                            <Download size={ICON.sm} />
                          )}
                        </IconButton>
                        <IconButton
                          label={`定位到包含 ${attachment.filename} 的消息`}
                          onClick={() => {
                            if (overlayMembers) onClose();
                            onLocateMessage(item.messageId);
                          }}
                        >
                          <CornerUpLeft size={ICON.sm} />
                        </IconButton>
                      </div>
                    </li>
                  );
                })}
              </ul>
              {currentPage?.error && (
                <Notice kind="error">{currentPage.error}</Notice>
              )}
              {hasMore && (
                <Button
                  type="button"
                  variant="secondary"
                  className="gallery-more"
                  busy={Boolean(currentPage?.loadingMore)}
                  onClick={() => void loadMore()}
                >
                  加载更多
                </Button>
              )}
            </>
          )}
        </div>
      </aside>

      {lightboxIndex >= 0 && (
        <AttachmentLightbox
          items={lightboxItems}
          index={lightboxIndex}
          onIndexChange={(nextIndex) => {
            const next = lightboxItems[nextIndex];
            if (next) setLightboxId(next.attachment.id);
          }}
          onClose={() => setLightboxId(null)}
          onDownload={(att) => void download(att)}
          downloadingId={downloadingId}
          onLocateMessage={(msgId) => {
            if (overlayMembers) onClose();
            onLocateMessage(msgId);
          }}
        />
      )}
    </>
  );
}
