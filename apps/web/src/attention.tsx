import { useEffect, useRef, useState } from "react";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useLocation } from "react-router-dom";
import { Bell, CheckCheck, RefreshCw } from "lucide-react";
import { api, ApiError } from "@teamie/api-client";
import type { AttentionItem } from "@teamie/api-client";
import { useAuth } from "./auth";
import { useChat, useChatState } from "./chat";
import { SavedMessagesList } from "./chat/collaboration-ui";
import { errorText } from "./errors";
import {
  chatStore,
  isUserMentioned,
  reconcileMessages,
  savedItemConversationId,
  savedItemMessageId,
} from "./store";
import type { ChatSnapshot } from "./store";
import { fetchAttentionCount } from "./notification-projection";
import { AgentAvatar, AgentBadge } from "./agent-ui";
import { MentionText } from "./mentions";
import { Avatar, Button, date, IconButton, Notice, time } from "./ui";
import { ICON } from "./icons";
import { MobileNavButton } from "./shell/page-header";
import { UnreadBadge } from "./unread-badge";
import "./styles/attention.css";

export { SavedMessagesList };

function useAttentionScope() {
  const { user } = useAuth();
  const snapshot = useChatState();
  const conversations = Object.values(snapshot.conversations).sort((a, b) =>
    a.id.localeCompare(b.id),
  );
  return {
    userId: user!.id,
    generation: chatStore.generation,
    access: conversations.map((conversation) => conversation.id).join("|"),
    reads: conversations
      .map((conversation) => `${conversation.id}:${conversation.readSeq}`)
      .join("|"),
    epoch: chatStore.notificationEpoch,
    enabled: snapshot.userId === user!.id,
  };
}

const retry = (count: number, error: Error) =>
  count < 1 &&
  error.name !== "AbortError" &&
  !(
    error instanceof ApiError &&
    [400, 401, 403, 404, 409].includes(error.status)
  );

export function useAttentionCount() {
  const scope = useAttentionScope();
  const client = useQueryClient();
  const scopeKey = ["attention", scope.userId, scope.generation, scope.access, "count"] as const;
  // Read-cursor changes refresh the count in place instead of switching to a
  // new (empty) query, so the rail badge never blinks out while refetching.
  const lastReads = useRef(scope.reads);
  useEffect(() => {
    if (lastReads.current === scope.reads) return;
    lastReads.current = scope.reads;
    void client.invalidateQueries({
      queryKey: ["attention", scope.userId, scope.generation, scope.access, "count"],
    });
  }, [client, scope.reads, scope.userId, scope.generation, scope.access]);
  return useQuery({
    queryKey: [...scopeKey, scope.epoch],
    queryFn: ({ signal }) => fetchAttentionCount(signal),
    enabled: scope.enabled,
    staleTime: 5_000,
    refetchInterval: 30_000,
    // Only bridge refetches within the same account session.
    placeholderData: (previous, previousQuery) =>
      previousQuery?.queryKey[1] === scope.userId &&
      previousQuery.queryKey[2] === scope.generation
        ? previous
        : undefined,
    retry,
  });
}

// Reconcile only this user's already authorized messages. A reply target's
// retraction can remove a reply reason even when the reply itself is unchanged.
export function currentAttention(
  item: AttentionItem,
  snapshot: ChatSnapshot,
  userId: string,
): AttentionItem | null {
  const conversation = snapshot.conversations[item.message.conversationId];
  if (
    !conversation ||
    conversation.workspaceId !== item.conversation.workspaceId
  )
    return null;
  const cached = snapshot.messages[conversation.id] || [];
  const message = reconcileMessages(
    [item.message],
    cached.filter((m) => m.id === item.message.id),
  )[0];
  if (message.retractedAt || message.senderId === userId) return null;
  const reasons: AttentionItem["reasons"] = [];
  const hasHereMention = Boolean(message.mentions?.some((m) => m.kind === "here"));
  if (
    isUserMentioned(message.mentions, userId) ||
    (hasHereMention && item.reasons.includes("mention"))
  )
    reasons.push("mention");
  if (message.replyToId) {
    const original = cached.find((m) => m.id === message.replyToId);
    if (
      original
        ? !original.retractedAt && original.senderId === userId
        : item.reasons.includes("reply")
    )
      reasons.push("reply");
  }
  if (!reasons.length) return null;
  const enrichedMessage =
    hasHereMention && reasons.includes("mention") && message.mentions
      ? {
          ...message,
          mentions: message.mentions.map((m) =>
            m.kind === "here" && !m.recipientIds ? { ...m, recipientIds: [userId] } : m,
          ),
        }
      : message;
  return {
    ...item,
    message: enrichedMessage,
    reasons,
    isRead: item.isRead || conversation.readSeq >= message.seq,
  };
}

type BatchReadFailure = {
  total: number;
  remaining: Array<[string, number]>;
};

const MARK_READ_CONCURRENCY = 4;

export function AttentionInbox({ onMenu }: { onMenu: () => void }) {
  const scope = useAttentionScope();
  const { data: chatData } = useChat();
  const snapshot = useChatState();
  const location = useLocation();
  const client = useQueryClient();
  const [tab, setTab] = useState<"all" | "unread" | "saved">(() =>
    new URLSearchParams(location.search).get("tab") === "saved"
      ? "saved"
      : "all",
  );
  const unreadOnly = tab === "unread";
  const isSavedTab = tab === "saved";
  const canSave = chatData?.capabilities["im.messages.save"] !== false;
  const [savedLoading, setSavedLoading] = useState(false);
  const [savedError, setSavedError] = useState("");
  const [savedCursor, setSavedCursor] = useState<string | undefined>(undefined);
  const [loadingMoreSaved, setLoadingMoreSaved] = useState(false);
  const [markingRead, setMarkingRead] = useState(false);
  const [markReadError, setMarkReadError] = useState("");
  const [batchFailure, setBatchFailure] = useState<BatchReadFailure | null>(
    null,
  );
  const markReadAbort = useRef<AbortController | null>(null);
  const savedBaselineSeq = useRef(0);

  useEffect(() => {
    if (new URLSearchParams(location.search).get("tab") === "saved") {
      setTab("saved");
    }
  }, [location.search]);

  useEffect(() => {
    if (!isSavedTab || !scope.enabled || typeof api.listSavedMessages !== "function") return;
    let active = true;
    const controller = new AbortController();
    const token = chatStore.generation;
    const expectedAccess = scope.access;
    const startDeltaSeq = chatStore.getSnapshot().savedDeltaSeq ?? 0;
    savedBaselineSeq.current = startDeltaSeq;
    setSavedLoading(true);
    setSavedError("");
    setSavedCursor(undefined);
    void Promise.resolve(api.listSavedMessages({ limit: 50 }, controller.signal))
      .then((page) => {
        const currentAccess = Object.values(chatStore.getSnapshot().conversations)
          .sort((a, b) => a.id.localeCompare(b.id))
          .map((c) => c.id)
          .join("|");
        if (
          !active ||
          token !== chatStore.generation ||
          currentAccess !== expectedAccess
        )
          return;
        if (page && Array.isArray(page.items)) {
          chatStore.setSavedMessages(page.items, startDeltaSeq);
          setSavedCursor(page.cursor || undefined);
        }
      })
      .catch((e) => {
        if (!active || controller.signal.aborted || token !== chatStore.generation) return;
        setSavedError(errorText(e));
      })
      .finally(() => {
        if (active && token === chatStore.generation) setSavedLoading(false);
      });
    return () => {
      active = false;
      controller.abort();
    };
  }, [isSavedTab, scope.enabled, scope.userId, scope.generation, scope.access]);

  async function loadMoreSaved() {
    if (
      !savedCursor ||
      loadingMoreSaved ||
      !scope.enabled ||
      typeof api.listSavedMessages !== "function"
    )
      return;
    const token = chatStore.generation;
    const expectedAccess = scope.access;
    setLoadingMoreSaved(true);
    setSavedError("");
    try {
      const page = await api.listSavedMessages({
        cursor: savedCursor,
        limit: 50,
      });
      const currentAccess = Object.values(chatStore.getSnapshot().conversations)
        .sort((a, b) => a.id.localeCompare(b.id))
        .map((c) => c.id)
        .join("|");
      if (token !== chatStore.generation || currentAccess !== expectedAccess)
        return;
      if (page && Array.isArray(page.items)) {
        const existing = chatStore.getSnapshot().savedMessages || [];
        const seenIds = new Set(
          existing.map((item) => savedItemMessageId(item)),
        );
        const merged = [
          ...existing,
          ...page.items.filter((item) => {
            const id = savedItemMessageId(item);
            if (!id || seenIds.has(id)) return false;
            seenIds.add(id);
            return true;
          }),
        ];
        chatStore.setSavedMessages(merged, savedBaselineSeq.current);
        setSavedCursor(page.cursor || undefined);
      }
    } catch (e) {
      if (token === chatStore.generation) {
        setSavedError(errorText(e));
      }
    } finally {
      if (token === chatStore.generation) {
        setLoadingMoreSaved(false);
      }
    }
  }

  useEffect(() => {
    markReadAbort.current?.abort();
    markReadAbort.current = null;
    setMarkingRead(false);
    setMarkReadError("");
    setBatchFailure(null);
  }, [scope.userId, scope.generation]);

  useEffect(() => {
    return () => {
      markReadAbort.current?.abort();
    };
  }, []);

  const query = useInfiniteQuery({
    queryKey: [
      "attention",
      scope.userId,
      scope.generation,
      scope.access,
      "items",
      unreadOnly,
    ],
    queryFn: ({ pageParam, signal }) =>
      api.attention({ cursor: pageParam, unreadOnly, limit: 50 }, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.cursor || undefined,
    enabled: scope.enabled,
    staleTime: 5_000,
    refetchInterval: 30_000,
    retry,
  });
  const seen = new Set<string>();
  const items = (query.data?.pages.flatMap((page) => page.items) || []).flatMap(
    (item) => {
      const current = currentAttention(item, snapshot, scope.userId);
      if (
        !current ||
        (unreadOnly && current.isRead) ||
        seen.has(current.message.id)
      )
        return [];
      seen.add(current.message.id);
      return [current];
    },
  );

  const rawPages = query.data?.pages ?? [];
  let locallyResolvedUnread = 0;
  const seenRaw = new Set<string>();
  for (const page of rawPages) {
    for (const raw of page.items) {
      if (seenRaw.has(raw.message.id)) continue;
      seenRaw.add(raw.message.id);
      if (!raw.isRead) {
        const cur = currentAttention(raw, snapshot, scope.userId);
        if (!cur || cur.isRead) locallyResolvedUnread += 1;
      }
    }
  }
  const serverUnread = Math.max(
    0,
    (rawPages[0]?.unreadCount ?? 0) - locallyResolvedUnread,
  );
  const activeRemaining =
    batchFailure?.remaining.filter(([convId, seq]) => {
      const conv = snapshot.conversations[convId];
      return Boolean(conv && conv.readSeq < seq);
    }) ?? [];
  const completedCount = batchFailure
    ? Math.max(0, batchFailure.total - activeRemaining.length)
    : 0;
  const hasUnread =
    items.some((item) => !item.isRead) ||
    serverUnread > 0 ||
    activeRemaining.length > 0;

  async function markAllRead(retryRemainingOnly: boolean) {
    if (markingRead || !scope.enabled) return;
    const generation = chatStore.generation;
    const userId = scope.userId;
    markReadAbort.current?.abort();
    const controller = new AbortController();
    markReadAbort.current = controller;
    setMarkingRead(true);
    setMarkReadError("");

    try {
      // Retrying keeps the original batch's scope, but its failed targets may
      // have stopped being attention since the previous attempt.
      const retryBounds =
        retryRemainingOnly && batchFailure
          ? new Map(activeRemaining)
          : null;
      const maxSeqByConv = new Map<string, number>();
      const recordUnread = (rawItem: AttentionItem) => {
        const current = currentAttention(
          rawItem,
          chatStore.getSnapshot(),
          userId,
        );
        if (!current || current.isRead) return;
        if (retryBounds) {
          const bound = retryBounds.get(current.conversation.id);
          if (bound === undefined || current.message.seq > bound) return;
        }
        const prev = maxSeqByConv.get(current.conversation.id) ?? 0;
        if (current.message.seq > prev) {
          maxSeqByConv.set(current.conversation.id, current.message.seq);
        }
      };

      let cursor: string | undefined = undefined;
      const seenCursors = new Set<string>();
      for (;;) {
        const page = await api.attention(
          { cursor, unreadOnly: true, limit: 100 },
          controller.signal,
        );
        if (controller.signal.aborted || chatStore.generation !== generation)
          return;
        for (const rawItem of page.items) {
          recordUnread(rawItem);
        }
        if (!page.cursor || seenCursors.has(page.cursor)) break;
        seenCursors.add(page.cursor);
        cursor = page.cursor;
      }

      const currentConvs = chatStore.getSnapshot().conversations;
      const targets = Array.from(maxSeqByConv.entries()).filter(
        ([convId, seq]) =>
          Boolean(currentConvs[convId]) && currentConvs[convId].readSeq < seq,
      );
      const totalTargets = retryBounds ? batchFailure!.total : targets.length;

      if (targets.length === 0) {
        setBatchFailure(null);
        await client.invalidateQueries({ queryKey: ["attention", userId] });
        return;
      }

      const failed: Array<[string, number]> = [];
      let nextIndex = 0;
      const workerCount = Math.min(MARK_READ_CONCURRENCY, targets.length);
      const workers = Array.from({ length: workerCount }, async () => {
        while (nextIndex < targets.length) {
          const idx = nextIndex++;
          const [convId, seq] = targets[idx];
          if (controller.signal.aborted || chatStore.generation !== generation)
            return;
          const conv = chatStore.getSnapshot().conversations[convId];
          if (!conv || conv.readSeq >= seq) continue;
          try {
            const res = await api.read(convId, seq);
            if (
              controller.signal.aborted ||
              chatStore.generation !== generation
            )
              return;
            chatStore.markRead(
              convId,
              res.readSeq,
              res.manualUnreadSeq ?? null,
            );
          } catch {
            if (
              controller.signal.aborted ||
              chatStore.generation !== generation
            )
              return;
            failed.push([convId, seq]);
          }
        }
      });

      await Promise.all(workers);
      if (controller.signal.aborted || chatStore.generation !== generation)
        return;

      if (failed.length > 0) {
        setBatchFailure({
          total: totalTargets,
          remaining: failed,
        });
      } else {
        setBatchFailure(null);
      }
      await client.invalidateQueries({ queryKey: ["attention", userId] });
    } catch (error) {
      if (!controller.signal.aborted && chatStore.generation === generation) {
        setMarkReadError(errorText(error));
      }
    } finally {
      if (markReadAbort.current === controller) {
        markReadAbort.current = null;
        setMarkingRead(false);
      }
    }
  }

  return (
    <section className="attention-view">
      <header className="conversation-header">
        <MobileNavButton onClick={onMenu} />
        <span className="conversation-symbol" aria-hidden="true">
          <Bell size={ICON.lg} aria-hidden="true" />
        </span>
        <div>
          <h1 tabIndex={-1}>提及与回复</h1>
        </div>
        <div className="conversation-header-actions">
          <Button
            variant="secondary"
            size="sm"
            className="attention-mark-all-read"
            busy={markingRead}
            disabled={markingRead || query.isPending || !hasUnread}
            title="将各会话标为已读至最新提及或回复位置，之后的普通消息仍保持未读"
            onClick={() => void markAllRead(activeRemaining.length > 0)}
          >
            <CheckCheck size={ICON.sm} aria-hidden="true" />
            {activeRemaining.length > 0
              ? `已处理 ${completedCount} / ${batchFailure!.total}，重试剩余`
              : "全部标为已读"}
          </Button>
          <IconButton
            label="刷新提醒"
            disabled={query.isFetching}
            onClick={() => void query.refetch()}
          >
            <RefreshCw size={ICON.md} className={query.isFetching ? "spin" : ""} />
          </IconButton>
        </div>
      </header>
      <div className="attention-filters" role="group" aria-label="提醒筛选">
        <button
          aria-pressed={tab === "all"}
          onClick={() => setTab("all")}
        >
          全部
        </button>
        <button
          aria-pressed={tab === "saved"}
          onClick={() => setTab("saved")}
        >
          收藏
        </button>
        <button
          aria-pressed={tab === "unread"}
          onClick={() => setTab("unread")}
        >
          未读
          <UnreadBadge count={serverUnread} />
        </button>
      </div>
      {isSavedTab ? (
        <SavedMessagesList
          items={(snapshot.savedMessages || []).filter((item) =>
            Boolean(snapshot.conversations[savedItemConversationId(item)]),
          )}
          loading={savedLoading}
          error={savedError}
          userId={scope.userId}
          canSave={canSave}
          hasMore={Boolean(savedCursor)}
          loadingMore={loadingMoreSaved}
          onLoadMore={() => void loadMoreSaved()}
        />
      ) : (
      <div className="attention-scroll" aria-busy={query.isPending}>
        {query.isPending && (
          <p className="attention-empty" role="status">
            正在加载…
          </p>
        )}
        {query.error && <Notice>{errorText(query.error)}</Notice>}
        {markReadError && <Notice>{markReadError}</Notice>}
        {activeRemaining.length > 0 && !markReadError && (
          <Notice>
            {`已处理 ${completedCount} / ${batchFailure!.total}，重试剩余会话即可完成标记已读。`}
          </Notice>
        )}
        {!query.isPending && !query.error && !items.length && (
          <p className="attention-empty">
            {unreadOnly ? "没有未读提醒" : "暂无提及或回复"}
          </p>
        )}
        <ol className="attention-list" aria-label="提及与回复">
          {items.map(
            ({ message, conversation, workspaceName, reasons, isRead }) => (
              <li key={message.id}>
                <Link
                  className={`attention-item ${isRead ? "read" : "unread"}`}
                  to={`/app/w/${encodeURIComponent(conversation.workspaceId)}/c/${encodeURIComponent(conversation.id)}?message=${encodeURIComponent(message.id)}`}
                >
                  {message.senderKind === "agent" ? (
                    <AgentAvatar seed={message.senderId} />
                  ) : (
                    <Avatar name={message.senderName} seed={message.senderId} />
                  )}
                  <div className="attention-item-content">
                    <div className="attention-item-meta">
                      <b>{message.senderName}</b>
                      {message.senderKind === "agent" && <AgentBadge />}
                      <span>
                        {reasons.length === 2
                          ? "提及并回复了你"
                          : reasons[0] === "mention"
                            ? "提及了你"
                            : "回复了你"}
                      </span>
                      {!isRead && (
                        <>
                          <i aria-hidden="true" />
                          <span className="sr-only">（未读）</span>
                        </>
                      )}
                      <time dateTime={message.createdAt}>
                        {date(message.createdAt)} {time(message.createdAt)}
                      </time>
                    </div>
                    <p className="attention-item-body">
                      {message.body ? (
                        <MentionText
                          body={message.body}
                          mentions={message.mentions}
                          userId={scope.userId}
                        />
                      ) : (
                        `${message.attachments?.length || 0} 个文件`
                      )}
                    </p>
                    <span className="attention-source">
                      {workspaceName} /{" "}
                      {conversation.name ||
                        (conversation.type === "direct"
                          ? message.senderName
                          : "小组对话")}
                    </span>
                  </div>
                </Link>
              </li>
            ),
          )}
        </ol>
        {query.hasNextPage && (
          <Button
            variant="secondary"
            className="attention-more"
            busy={query.isFetchingNextPage}
            disabled={query.isFetching}
            onClick={() => void query.fetchNextPage()}
          >
            加载更多
          </Button>
        )}
        {query.error && (
          <Button
            variant="secondary"
            className="attention-more"
            onClick={() => void query.refetch()}
          >
            重试
          </Button>
        )}
      </div>
      )}
    </section>
  );
}
