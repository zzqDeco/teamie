import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import {
  useInfiniteQuery,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { LoaderCircle, Megaphone } from "lucide-react";
import { api, ApiError } from "@teamie/api-client";
import type { Announcement, AnnouncementsPage, User } from "@teamie/api-client";
import { errorText } from "./errors";
import { MessageBody } from "./markdown";
import { BadgePill, Button, Modal, Notice } from "./ui";
import { ICON } from "./icons";
import "./styles/announcements.css";

export function announcementsQueryKey(userId: string) {
  return ["announcements", userId] as const;
}

export function announcementsHistoryQueryKey(userId: string) {
  return ["announcements", "history", userId] as const;
}

export function charCount(value: string) {
  return Array.from(value).length;
}

export function formatAnnouncementTime(iso: string) {
  const ts = Date.parse(iso);
  if (Number.isNaN(ts)) return iso;
  return new Date(ts).toLocaleString("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
}

export function usePlatformAnnouncements(userId?: string) {
  const client = useQueryClient();
  const [ackedByUser, setAckedByUser] = useState<{
    userId?: string;
    seq: number;
  }>({ seq: 0 });
  const ackedSeq = ackedByUser.userId === userId ? ackedByUser.seq : 0;

  const query = useQuery({
    queryKey: announcementsQueryKey(userId || ""),
    queryFn: () => api.announcements({ limit: 20 }),
    enabled: !!userId && typeof api.announcements === "function",
    staleTime: 30_000,
    retry: (count, error) =>
      !(error instanceof ApiError && [401, 403, 404].includes(error.status)) &&
      count < 1,
  });

  const data = query.data;
  const latest: Announcement | undefined = data?.items[0];
  const effectiveReadSeq = Math.max(data?.readSeq ?? 0, ackedSeq);
  const latestSeq = data?.latestSeq ?? 0;
  const hasUnread = !!latest && latestSeq > effectiveReadSeq;
  const unreadCount =
    !data?.items.length || latestSeq <= effectiveReadSeq
      ? 0
      : data.items.filter((item) => item.seq > effectiveReadSeq).length;

  const applyLocalReadSeq = useCallback(
    (seq: number) => {
      if (!userId || seq <= 0) return;
      setAckedByUser((prev) => ({
        userId,
        seq: prev.userId === userId ? Math.max(prev.seq, seq) : seq,
      }));
      client.setQueryData<AnnouncementsPage>(
        announcementsQueryKey(userId),
        (prev) =>
          prev
            ? { ...prev, readSeq: Math.max(prev.readSeq, seq) }
            : prev,
      );
    },
    [client, userId],
  );

  const markRead = useCallback(
    async (seq: number) => {
      if (!userId || seq <= 0 || typeof api.markAnnouncementsRead !== "function")
        return;
      const res = await api.markAnnouncementsRead(seq);
      applyLocalReadSeq(Math.max(seq, res.readSeq));
    },
    [applyLocalReadSeq, userId],
  );

  return {
    query,
    latest,
    items: data?.items ?? [],
    readSeq: effectiveReadSeq,
    latestSeq,
    hasUnread,
    unreadCount,
    applyLocalReadSeq,
    markRead,
  };
}

export function AnnouncementModals({
  user,
  latest,
  latestSeq,
  readSeq,
  hasUnread,
  unreadCount,
  suppressPopup,
  historyOpen,
  onOpenHistory,
  onCloseHistory,
  onMarkRead,
  onDismissOptimistic,
}: {
  user: User;
  latest?: Announcement;
  latestSeq: number;
  readSeq: number;
  hasUnread: boolean;
  unreadCount: number;
  suppressPopup: boolean;
  historyOpen: boolean;
  onOpenHistory: () => void;
  onCloseHistory: () => void;
  onMarkRead: (seq: number) => Promise<void>;
  onDismissOptimistic: (seq: number) => void;
}) {
  const [ackBusy, setAckBusy] = useState(false);
  const [ackError, setAckError] = useState("");

  useEffect(() => {
    setAckError("");
  }, [latest?.id, historyOpen]);

  const showPopup = !historyOpen && !suppressPopup && hasUnread && !!latest;

  async function handleAcknowledge(openHistoryAfter = false) {
    if (!latest || ackBusy) return;
    setAckBusy(true);
    setAckError("");
    try {
      await onMarkRead(latest.seq);
      if (openHistoryAfter) onOpenHistory();
    } catch (err) {
      setAckError(errorText(err));
    } finally {
      setAckBusy(false);
    }
  }

  function handleDismissPopup() {
    if (!latest || ackBusy) return;
    const seq = latest.seq;
    onDismissOptimistic(seq);
    void onMarkRead(seq).catch(() => {
      /* Best-effort read acknowledgement when dismissed via Escape/backdrop. */
    });
  }

  if (historyOpen) {
    return (
      <AnnouncementHistoryModal
        user={user}
        latestSeq={latestSeq}
        readSeq={readSeq}
        onMarkRead={onMarkRead}
        onDismissOptimistic={onDismissOptimistic}
        onClose={onCloseHistory}
      />
    );
  }

  if (!showPopup || !latest) return null;

  return (
    <Modal
      title={latest.title}
      subtitle={`平台公告 · ${latest.authorName} 发布于 ${formatAnnouncementTime(latest.createdAt)}`}
      onClose={handleDismissPopup}
      busy={ackBusy}
      className="announcement-popup-modal"
    >
      {unreadCount > 1 && (
        <div className="announcement-unread-banner">
          <span>共有 {unreadCount} 条未读公告，当前为你展示最新一条。</span>
        </div>
      )}
      <Notice>{ackError}</Notice>
      <div className="announcement-body-scroll">
        <div className="announcement-markdown">
          <MessageBody body={latest.body} userId={user.id} />
        </div>
      </div>
      <div className="announcement-modal-actions">
        <Button
          type="button"
          variant="secondary"
          disabled={ackBusy}
          onClick={() => void handleAcknowledge(true)}
        >
          查看历史公告
        </Button>
        <div className="announcement-modal-actions-right">
          <Button
            type="button"
            busy={ackBusy}
            onClick={() => void handleAcknowledge(false)}
          >
            我知道了
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function AnnouncementHistoryModal({
  user,
  latestSeq,
  readSeq,
  onMarkRead,
  onDismissOptimistic,
  onClose,
}: {
  user: User;
  latestSeq: number;
  readSeq: number;
  onMarkRead: (seq: number) => Promise<void>;
  onDismissOptimistic: (seq: number) => void;
  onClose: () => void;
}) {
  const historyQuery = useInfiniteQuery({
    queryKey: announcementsHistoryQueryKey(user.id),
    initialPageParam: 0,
    queryFn: ({ pageParam }) =>
      api.announcements({
        limit: 20,
        ...(pageParam > 0 ? { beforeSeq: pageParam } : {}),
      }),
    getNextPageParam: (lastPage) => {
      if (!lastPage.hasMore || lastPage.items.length === 0) return undefined;
      return lastPage.items[lastPage.items.length - 1].seq;
    },
    staleTime: 0,
  });

  const maxLoadedSeq =
    historyQuery.data?.pages[0]?.latestSeq ?? latestSeq;

  useEffect(() => {
    if (maxLoadedSeq > readSeq) {
      onDismissOptimistic(maxLoadedSeq);
      void onMarkRead(maxLoadedSeq).catch(() => {});
    }
  }, [maxLoadedSeq, onDismissOptimistic, onMarkRead, readSeq]);

  const allItems = useMemo(() => {
    const seen = new Set<string>();
    const out: Announcement[] = [];
    for (const page of historyQuery.data?.pages ?? []) {
      for (const item of page.items) {
        if (!seen.has(item.id)) {
          seen.add(item.id);
          out.push(item);
        }
      }
    }
    return out;
  }, [historyQuery.data?.pages]);

  return (
    <Modal
      title="平台公告"
      subtitle="查看平台版本更新与系统通知记录。"
      onClose={onClose}
      className="announcement-history-modal"
    >
      {historyQuery.isPending ? (
        <div className="announcement-history-empty" role="status">
          <LoaderCircle className="spin" size={ICON.lg} />
          <p>正在加载历史公告…</p>
        </div>
      ) : historyQuery.isError ? (
        <div className="announcement-history-empty">
          <Notice>{errorText(historyQuery.error)}</Notice>
          <Button
            type="button"
            variant="secondary"
            onClick={() => void historyQuery.refetch()}
          >
            重新加载
          </Button>
        </div>
      ) : allItems.length === 0 ? (
        <div className="announcement-history-empty">
          <Megaphone size={ICON.xl} aria-hidden="true" />
          <p>暂无已发布的平台公告。</p>
        </div>
      ) : (
        <div className="announcement-history-list">
          {allItems.map((item, idx) => (
            <article key={item.id} className="announcement-card">
              <div className="announcement-card-header">
                <h4 className="announcement-card-title">
                  <span>{item.title}</span>
                  {idx === 0 && <BadgePill>最新</BadgePill>}
                </h4>
              </div>
              <div className="announcement-card-meta">
                <span>{item.authorName}</span>
                <span aria-hidden="true">·</span>
                <time dateTime={item.createdAt}>
                  {formatAnnouncementTime(item.createdAt)}
                </time>
              </div>
              <div className="announcement-markdown">
                <MessageBody body={item.body} userId={user.id} />
              </div>
            </article>
          ))}
          {historyQuery.hasNextPage && (
            <div className="announcement-load-more">
              <Button
                type="button"
                variant="secondary"
                size="sm"
                busy={historyQuery.isFetchingNextPage}
                onClick={() => void historyQuery.fetchNextPage()}
              >
                加载更早公告
              </Button>
            </div>
          )}
        </div>
      )}
      <div className="announcement-modal-actions">
        {user.platformRole === "admin" ? (
          <Link
            to="/app/admin/announcements"
            className="button secondary small-button"
            onClick={onClose}
          >
            管理与发布公告
          </Link>
        ) : (
          <span />
        )}
        <div className="announcement-modal-actions-right">
          <Button type="button" variant="secondary" onClick={onClose}>
            关闭
          </Button>
        </div>
      </div>
    </Modal>
  );
}
