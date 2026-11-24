import { useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { useQueries } from "@tanstack/react-query";
import { LoaderCircle, Search, X } from "lucide-react";
import { api } from "@teamie/api-client";
import type {
  Conversation,
  GlobalSearchItem,
  GlobalSearchOptions,
  Member,
  Workspace,
} from "@teamie/api-client";
import { useAuth } from "../auth";
import { useChat, useChatState } from "../chat";
import { errorText } from "../errors";
import { ICON } from "../icons";
import { conversationName } from "../messages";
import { enrichParticipants } from "../participants";
import type { Participant } from "../participants";
import { MobileNavButton } from "../shell/page-header";
import { usePinyin } from "../shell/pinyin-match";
import { chatStore, compareTimestampsDesc, reconcileMessages } from "../store";
import { Button, IconButton, Notice } from "../ui";
import {
  matchesSearchTerms,
  parseSearchQuery,
} from "./search-highlight";
import { GlobalSearchFilters } from "./global-search-filters";
import type { DatePreset } from "./global-search-filters";
import { GlobalSearchResults } from "./global-search-results";
import type { VisibleGlobalSearchHit } from "./global-search-results";
import "../styles/global-search.css";

type SearchPageState = {
  key: string;
  items: GlobalSearchItem[];
  cursor?: string;
  error: string;
  loadingMore: boolean;
};

function parsePreset(raw: string | null, after: string, before: string): DatePreset {
  if (raw === "7d" || raw === "30d" || raw === "custom") return raw;
  if (after || before) return "custom";
  return "any";
}

function isoDateBoundary(rawDate: string, endOfDay: boolean): string | undefined {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(rawDate)) return undefined;
  const parsed = new Date(
    endOfDay ? `${rawDate}T23:59:59.999Z` : `${rawDate}T00:00:00.000Z`,
  );
  if (Number.isNaN(parsed.getTime())) return undefined;
  return parsed.toISOString();
}

function presetStartIso(preset: "7d" | "30d"): string {
  const days = preset === "7d" ? 7 : 30;
  return new Date(Date.now() - days * 24 * 60 * 60 * 1000).toISOString();
}

export function GlobalSearchView({
  onMenu,
}: {
  onMenu: () => void;
}) {
  const { user } = useAuth();
  const chat = useChat();
  const snapshot = useChatState();
  const [searchParams, setSearchParams] = useSearchParams();
  const toPinyin = usePinyin();

  const [query, setQuery] = useState(() => searchParams.get("q") || "");
  const [workspaceId, setWorkspaceId] = useState(
    () => searchParams.get("workspaceId") || "",
  );
  const [conversationId, setConversationId] = useState(
    () => searchParams.get("conversationId") || "",
  );
  const [senderId, setSenderId] = useState(
    () => searchParams.get("senderId") || "",
  );
  const [senderFilterText, setSenderFilterText] = useState("");
  const [hasFiles, setHasFiles] = useState(
    () => searchParams.get("hasFiles") === "true",
  );
  const [datePreset, setDatePreset] = useState<DatePreset>(() =>
    parsePreset(
      searchParams.get("range"),
      searchParams.get("after") || "",
      searchParams.get("before") || "",
    ),
  );
  const [customAfter, setCustomAfter] = useState(
    () => searchParams.get("after") || "",
  );
  const [customBefore, setCustomBefore] = useState(
    () => searchParams.get("before") || "",
  );
  const [page, setPage] = useState<SearchPageState | null>(null);
  const [retryNonce, setRetryNonce] = useState(0);

  const inputRef = useRef<HTMLInputElement>(null);
  const requests = useRef(new Set<AbortController>());
  const sequence = useRef(0);
  const lastWrittenSearch = useRef(searchParams.toString());
  const generation = chatStore.generation;
  const userId = user!.id;

  // Adopt externally navigated /app/search?q=... changes (e.g. from QuickSwitcher).
  const currentSearchString = searchParams.toString();
  useEffect(() => {
    if (currentSearchString === lastWrittenSearch.current) return;
    lastWrittenSearch.current = currentSearchString;
    const params = new URLSearchParams(currentSearchString);
    setQuery(params.get("q") || "");
    setWorkspaceId(params.get("workspaceId") || "");
    setConversationId(params.get("conversationId") || "");
    setSenderId(params.get("senderId") || "");
    setHasFiles(params.get("hasFiles") === "true");
    const nextAfter = params.get("after") || "";
    const nextBefore = params.get("before") || "";
    setDatePreset(parsePreset(params.get("range"), nextAfter, nextBefore));
    setCustomAfter(nextAfter);
    setCustomBefore(nextBefore);
  }, [currentSearchString]);

  // Focus input on mount and when Cmd/Ctrl+Shift+F fires while already on /app/search.
  useEffect(() => {
    inputRef.current?.focus({ preventScroll: true });
    const onFocusShortcut = () => {
      inputRef.current?.focus({ preventScroll: true });
      inputRef.current?.select();
    };
    window.addEventListener("teamie:focus-global-search", onFocusShortcut);
    return () => {
      window.removeEventListener("teamie:focus-global-search", onFocusShortcut);
    };
  }, []);

  // Sync URL query params when filters change so browser history & reloads restore state.
  useEffect(() => {
    const next = new URLSearchParams();
    if (query.trim()) next.set("q", query.trim());
    if (workspaceId) next.set("workspaceId", workspaceId);
    if (conversationId) next.set("conversationId", conversationId);
    if (senderId) next.set("senderId", senderId);
    if (hasFiles) next.set("hasFiles", "true");
    if (datePreset !== "any") next.set("range", datePreset);
    if (datePreset === "custom" && customAfter) next.set("after", customAfter);
    if (datePreset === "custom" && customBefore)
      next.set("before", customBefore);
    const nextString = next.toString();
    if (nextString !== lastWrittenSearch.current) {
      lastWrittenSearch.current = nextString;
      setSearchParams(next, { replace: true });
    }
  }, [
    query,
    workspaceId,
    conversationId,
    senderId,
    hasFiles,
    datePreset,
    customAfter,
    customBefore,
    setSearchParams,
  ]);

  const workspaces: Workspace[] = useMemo(
    () => chat.data?.workspaces || [],
    [chat.data?.workspaces],
  );
  const workspaceMap = useMemo(
    () => new Map(workspaces.map((w) => [w.id, w])),
    [workspaces],
  );
  const discoveryAllowed = !!chat.data?.capabilities["agents.discover"];

  const memberQueries = useQueries({
    queries: workspaces.map((space) => ({
      queryKey: ["members", userId, space.id],
      queryFn: () => api.members(space.id),
      enabled: !workspaceId || space.id === workspaceId,
      staleTime: 60_000,
      retry: false,
    })),
  });
  const agentQueries = useQueries({
    queries: workspaces.map((space) => ({
      queryKey: ["workspaceAgents", userId, space.id],
      queryFn: () => api.workspaceAgents(space.id),
      enabled: discoveryAllowed && (!workspaceId || space.id === workspaceId),
      staleTime: 30_000,
      retry: false,
    })),
  });

  const membersByWorkspace = new Map<string, Participant[]>();
  const senderOptionsMap = new Map<string, Participant>();
  workspaces.forEach((space, index) => {
    const rawMembers: Member[] = memberQueries[index]?.data?.members || [];
    const agents = discoveryAllowed ? agentQueries[index]?.data?.agents || [] : [];
    const enriched = enrichParticipants(rawMembers, agents);
    membersByWorkspace.set(space.id, enriched);
    if (!workspaceId || space.id === workspaceId) {
      for (const participant of enriched) {
        if (!senderOptionsMap.has(participant.userId)) {
          senderOptionsMap.set(participant.userId, participant);
        }
      }
    }
  });

  const authorizedConversations = useMemo(() => {
    return Object.values(snapshot.conversations)
      .filter((c) => workspaceMap.has(c.workspaceId))
      .sort((a, b) => compareTimestampsDesc(a.updatedAt, b.updatedAt));
  }, [snapshot.conversations, workspaceMap]);

  const scopedConversations = useMemo(
    () =>
      workspaceId
        ? authorizedConversations.filter((c) => c.workspaceId === workspaceId)
        : authorizedConversations,
    [authorizedConversations, workspaceId],
  );

  const effectiveWorkspaceId =
    workspaceId && workspaceMap.has(workspaceId) ? workspaceId : "";
  const effectiveConversationId =
    conversationId &&
    scopedConversations.some((c) => c.id === conversationId)
      ? conversationId
      : "";

  const allSenders = [...senderOptionsMap.values()].sort((a, b) =>
    a.displayName.localeCompare(b.displayName, "zh-Hans-CN"),
  );
  const senderNeedle = senderFilterText.trim().toLowerCase();
  const senderCompact = senderNeedle.replace(/\s+/gu, "");
  const filteredSenders = senderNeedle
    ? allSenders.filter((member) => {
        if (member.displayName.toLowerCase().includes(senderNeedle)) return true;
        const forms = toPinyin?.(member.displayName);
        return Boolean(
          forms &&
            (forms.full.includes(senderCompact) ||
              forms.initials.includes(senderCompact)),
        );
      })
    : allSenders;

  const parsed = useMemo(() => parseSearchQuery(query), [query]);
  const invertedCustomDates =
    datePreset === "custom" &&
    Boolean(customAfter && customBefore && customAfter > customBefore);

  const computedAfter =
    datePreset === "7d" || datePreset === "30d"
      ? presetStartIso(datePreset)
      : datePreset === "custom"
        ? isoDateBoundary(customAfter, false)
        : undefined;
  const computedBefore =
    datePreset === "custom" ? isoDateBoundary(customBefore, true) : undefined;

  const hasCriteria =
    parsed.terms.length > 0 ||
    Boolean(senderId) ||
    hasFiles ||
    Boolean(computedAfter) ||
    Boolean(computedBefore);
  const hasValidationError =
    parsed.tooLong || parsed.tooManyTerms || invertedCustomDates;
  const canSearch = hasCriteria && !hasValidationError;

  const trimmedQuery = query.trim();
  const searchKey = JSON.stringify([
    userId,
    generation,
    trimmedQuery,
    effectiveWorkspaceId,
    effectiveConversationId,
    senderId,
    hasFiles,
    datePreset,
    datePreset === "custom" ? customAfter : "",
    datePreset === "custom" ? customBefore : "",
    retryNonce,
  ]);

  const currentPage = page?.key === searchKey ? page : null;
  const pending = canSearch && !currentPage;

  function buildRequestOptions(cursor?: string): GlobalSearchOptions {
    const options: GlobalSearchOptions = { limit: 20 };
    if (trimmedQuery) options.q = trimmedQuery;
    if (effectiveWorkspaceId) options.workspaceId = effectiveWorkspaceId;
    if (effectiveConversationId)
      options.conversationId = effectiveConversationId;
    if (senderId) options.senderId = senderId;
    if (hasFiles) options.hasFiles = true;
    if (datePreset === "7d" || datePreset === "30d") {
      options.after = presetStartIso(datePreset);
    } else if (datePreset === "custom") {
      const a = isoDateBoundary(customAfter, false);
      const b = isoDateBoundary(customBefore, true);
      if (a) options.after = a;
      if (b) options.before = b;
    }
    if (cursor) options.cursor = cursor;
    return options;
  }

  useEffect(() => {
    const operation = ++sequence.current;
    const activeRequests = requests.current;
    const controller = new AbortController();
    const isCurrent = () =>
      !controller.signal.aborted &&
      generation === chatStore.generation &&
      operation === sequence.current;

    if (!canSearch) {
      return () => {
        controller.abort();
      };
    }

    const requestOptions: GlobalSearchOptions = { limit: 20 };
    if (trimmedQuery) requestOptions.q = trimmedQuery;
    if (effectiveWorkspaceId) requestOptions.workspaceId = effectiveWorkspaceId;
    if (effectiveConversationId)
      requestOptions.conversationId = effectiveConversationId;
    if (senderId) requestOptions.senderId = senderId;
    if (hasFiles) requestOptions.hasFiles = true;
    if (datePreset === "7d" || datePreset === "30d") {
      requestOptions.after = presetStartIso(datePreset);
    } else if (datePreset === "custom") {
      const a = isoDateBoundary(customAfter, false);
      const b = isoDateBoundary(customBefore, true);
      if (a) requestOptions.after = a;
      if (b) requestOptions.before = b;
    }

    const timer = setTimeout(() => {
      activeRequests.add(controller);
      void api
        .searchGlobalMessages(requestOptions, controller.signal)
        .then((response) => {
          if (isCurrent()) {
            setPage({
              key: searchKey,
              items: response.items,
              cursor: response.cursor,
              error: "",
              loadingMore: false,
            });
          }
        })
        .catch((err) => {
          if (isCurrent()) {
            setPage({
              key: searchKey,
              items: [],
              error: errorText(err),
              loadingMore: false,
            });
          }
        })
        .finally(() => activeRequests.delete(controller));
    }, 250);

    return () => {
      clearTimeout(timer);
      controller.abort();
      activeRequests.forEach((req) => req.abort());
      activeRequests.clear();
    };
  }, [
    canSearch,
    customAfter,
    customBefore,
    datePreset,
    effectiveConversationId,
    effectiveWorkspaceId,
    generation,
    hasFiles,
    retryNonce,
    searchKey,
    senderId,
    trimmedQuery,
  ]);

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
      const response = await api.searchGlobalMessages(
        buildRequestOptions(currentPage.cursor),
        controller.signal,
      );
      if (isCurrent()) {
        setPage((old) => {
          if (!old || old.key !== searchKey) return old;
          const seen = new Set(old.items.map((it) => it.message.id));
          return {
            ...old,
            items: [
              ...old.items,
              ...response.items.filter((it) => !seen.has(it.message.id)),
            ],
            cursor: response.cursor,
            loadingMore: false,
          };
        });
      }
    } catch (err) {
      if (isCurrent()) {
        setPage((old) =>
          old?.key === searchKey
            ? { ...old, error: errorText(err), loadingMore: false }
            : old,
        );
      }
    } finally {
      requests.current.delete(controller);
    }
  }

  // Reconcile server hits against live chatStore edits, retractions, and revocations.
  const hasSnapshotConvs = Object.keys(snapshot.conversations).length > 0;
  const visibleItems: VisibleGlobalSearchHit[] = [];
  if (currentPage) {
    for (const rawItem of currentPage.items) {
      const liveConv = snapshot.conversations[rawItem.conversation.id];
      if (!liveConv && hasSnapshotConvs) continue;
      if (liveConv && liveConv.workspaceId !== rawItem.conversation.workspaceId) {
        continue;
      }
      const cached = (snapshot.messages[rawItem.conversation.id] || []).filter(
        (m) => m.id === rawItem.message.id,
      );
      const [reconciled] = reconcileMessages([rawItem.message], cached);
      if (!reconciled || reconciled.retractedAt) continue;
      const message =
        reconciled.forwarded || !rawItem.message.forwarded
          ? reconciled
          : { ...reconciled, forwarded: rawItem.message.forwarded };
      // Only re-check terms when local sync modified the body; trust server hits otherwise.
      if (
        message.body !== rawItem.message.body &&
        !matchesSearchTerms(message, parsed.terms, hasFiles)
      ) {
        continue;
      }
      const wsMembers = membersByWorkspace.get(rawItem.conversation.workspaceId) || [];
      const convForName: Conversation = liveConv || {
        id: rawItem.conversation.id,
        workspaceId: rawItem.conversation.workspaceId,
        type: rawItem.conversation.type,
        name: rawItem.conversation.name,
        memberIds: [],
        lastSeq: message.seq,
        readSeq: 0,
        updatedAt: message.createdAt,
      };
      visibleItems.push({
        item: { ...rawItem, message },
        conversationLabel: conversationName(convForName, userId, wsMembers),
        path: `/app/w/${encodeURIComponent(rawItem.conversation.workspaceId)}/c/${encodeURIComponent(rawItem.conversation.id)}?message=${encodeURIComponent(message.id)}`,
      });
    }
  }

  const hasAnyFilter = Boolean(
    workspaceId || conversationId || senderId || hasFiles || datePreset !== "any",
  );
  function resetFilters() {
    setWorkspaceId("");
    setConversationId("");
    setSenderId("");
    setSenderFilterText("");
    setHasFiles(false);
    setDatePreset("any");
    setCustomAfter("");
    setCustomBefore("");
  }

  return (
    <section className="global-search-view" aria-label="全局消息搜索">
      <header className="conversation-header global-search-header">
        <MobileNavButton onClick={onMenu} />
        <div className="global-search-title-group">
          <h1>
            <Search size={ICON.lg} aria-hidden="true" />
            全局搜索
          </h1>
          <p className="global-search-subtitle">
            支持多词空格 AND 检索与双引号短语，跨全部已授权工作空间查找消息与附件
          </p>
        </div>
      </header>

      <div className="global-search-controls">
        <form
          className="global-search-bar"
          role="search"
          onSubmit={(event) => {
            event.preventDefault();
            if (canSearch) setRetryNonce((n) => n + 1);
          }}
        >
          <label className="global-search-input-wrap">
            {pending ? (
              <LoaderCircle size={ICON.md} className="spin" aria-hidden="true" />
            ) : (
              <Search size={ICON.md} aria-hidden="true" />
            )}
            <input
              id="global-search-input"
              ref={inputRef}
              type="search"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder='输入关键词（空格分隔多词，如：发布 v0.4.11 或 "error code"）'
              aria-label="搜索全部工作空间消息"
            />
            {query && (
              <IconButton
                label="清空搜索词"
                onClick={() => {
                  setQuery("");
                  inputRef.current?.focus();
                }}
              >
                <X size={ICON.sm} />
              </IconButton>
            )}
          </label>
          <Button type="submit" disabled={!canSearch}>
            搜索
          </Button>
        </form>

        <GlobalSearchFilters
          workspaces={workspaces}
          workspaceMap={workspaceMap}
          scopedConversations={scopedConversations}
          membersByWorkspace={membersByWorkspace}
          filteredSenders={filteredSenders}
          userId={userId}
          effectiveWorkspaceId={effectiveWorkspaceId}
          effectiveConversationId={effectiveConversationId}
          senderId={senderId}
          senderFilterText={senderFilterText}
          hasFiles={hasFiles}
          datePreset={datePreset}
          customAfter={customAfter}
          customBefore={customBefore}
          hasAnyFilter={hasAnyFilter}
          onWorkspaceChange={(nextWs) => {
            setWorkspaceId(nextWs);
            setConversationId("");
          }}
          onConversationChange={setConversationId}
          onSenderFilterTextChange={setSenderFilterText}
          onSenderChange={setSenderId}
          onDatePresetChange={setDatePreset}
          onCustomAfterChange={setCustomAfter}
          onCustomBeforeChange={setCustomBefore}
          onToggleHasFiles={() => setHasFiles((value) => !value)}
          onReset={resetFilters}
        />

        {parsed.tooLong && (
          <Notice kind="error">搜索关键词最多 200 个字符</Notice>
        )}
        {parsed.tooManyTerms && (
          <Notice kind="error">单次搜索最多支持 8 个独立关键词</Notice>
        )}
        {invertedCustomDates && (
          <Notice kind="error">起始日期不能晚于结束日期</Notice>
        )}
      </div>

      <GlobalSearchResults
        hasCriteria={hasCriteria}
        pending={pending}
        error={currentPage?.error || ""}
        hasMore={Boolean(currentPage?.cursor)}
        loadingMore={Boolean(currentPage?.loadingMore)}
        items={visibleItems}
        terms={parsed.terms}
        onLoadMore={() => void loadMore()}
        onRetry={() => {
          setPage(null);
          setRetryNonce((n) => n + 1);
        }}
      />
    </section>
  );
}
