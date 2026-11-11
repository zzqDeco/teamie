import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { useQueries } from "@tanstack/react-query";
import { LoaderCircle, Search } from "lucide-react";
import { api } from "@teamie/api-client";
import type {
  AttentionUnreadDTO,
  Conversation,
  Member,
  Workspace,
} from "@teamie/api-client";
import { useChatState } from "./chat";
import { errorText } from "./errors";
import { ICON } from "./icons";
import { isComposingKey } from "./mentions";
import { conversationName } from "./messages";
import { enrichParticipants } from "./participants";
import { chatStore, compareTimestampsDesc, reconcileMessages } from "./store";
import { Modal, Notice } from "./ui";
import {
  KeyboardShortcutsModal,
  QuickSwitcherJumpResults,
  QuickSwitcherMessageResults,
} from "./shell/quick-switcher-results";
import {
  buildPageItems,
  groupEmptyState,
  rankItems,
  SHORTCUTS_ENTRY,
} from "./shell/quick-switcher-items";
import type { QuickGroup, QuickItem } from "./shell/quick-switcher-items";
import { usePinyin } from "./shell/pinyin-match";
import { conversationUnreadSummary } from "./unread-badge";
import type {
  GroupedMessageResult,
  MessageOption,
  WorkspaceSearchState,
} from "./shell/quick-switcher-results";
export { KeyboardShortcutsModal, SHORTCUTS_ENTRY };


export function QuickSwitcher({
  workspaces,
  conversations,
  currentWorkspaceId,
  discoveryAllowed,
  members,
  userId,
  recentConversationIds,
  isPlatformAdmin = false,
  attentionProjection,
  onSelect,
  onClose,
}: {
  workspaces: Workspace[];
  conversations: Conversation[];
  currentWorkspaceId?: string;
  discoveryAllowed?: boolean;
  members: Member[];
  userId: string;
  recentConversationIds: string[];
  /** Human platform administrators also get administrator page entries. */
  isPlatformAdmin?: boolean;
  attentionProjection?: ReadonlyArray<AttentionUnreadDTO>;
  onSelect: (path: string) => void;
  onClose: () => void;
}) {
  const [mode, setMode] = useState<"jump" | "messages">("jump");
  const [query, setQuery] = useState("");
  const [activeId, setActiveId] = useState("");
  const [searchResult, setSearchResult] =
    useState<WorkspaceSearchState | null>(null);
  const [retryNonce, setRetryNonce] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLUListElement>(null);
  const requests = useRef(new Set<AbortController>());
  const sequence = useRef(0);
  const snapshot = useChatState();
  const generation = chatStore.generation;
  const toPinyin = usePinyin();

  useEffect(() => {
    inputRef.current?.focus({ preventScroll: true });
  }, [mode]);

  const workspacesNeedingMembers = new Set(
    conversations
      .filter((c) => c.type !== "channel" && !c.name.trim())
      .map((c) => c.workspaceId),
  );
  const memberQueries = useQueries({
    queries: workspaces.map((space) => ({
      queryKey: ["members", userId, space.id],
      queryFn: () => api.members(space.id),
      enabled: workspacesNeedingMembers.has(space.id),
      staleTime: 60_000,
      retry: false,
    })),
  });
  const agentQueries = useQueries({
    queries: workspaces.map((space) => ({
      queryKey: ["workspaceAgents", userId, space.id],
      queryFn: () => api.workspaceAgents(space.id),
      enabled: Boolean(
        discoveryAllowed && workspacesNeedingMembers.has(space.id),
      ),
      staleTime: 30_000,
      retry: false,
    })),
  });
  const membersByWorkspace = new Map<string, Member[]>();
  const agentLoadingByWorkspace = new Map<string, boolean>();
  workspaces.forEach((space, index) => {
    const memberQuery = memberQueries[index];
    const agentQuery = agentQueries[index];
    agentLoadingByWorkspace.set(
      space.id,
      Boolean(
        discoveryAllowed &&
          workspacesNeedingMembers.has(space.id) &&
          agentQuery?.isPending,
      ),
    );
    const loaded = memberQuery?.data?.members;
    const rawMembers =
      loaded ||
      (space.id === currentWorkspaceId
        ? members
        : !workspacesNeedingMembers.has(space.id) || !memberQuery?.isPending
          ? []
          : undefined);
    if (rawMembers) {
      const loadedAgents = discoveryAllowed
        ? agentQuery?.data?.agents || []
        : [];
      const enriched = enrichParticipants(rawMembers, loadedAgents);
      const merged =
        space.id === currentWorkspaceId && members.length
          ? [
              ...enriched,
              ...members.filter(
                (m) => !enriched.some((rm) => rm.userId === m.userId),
              ),
            ]
          : enriched;
      membersByWorkspace.set(space.id, merged);
    }
  });

  const workspaceMap = new Map(workspaces.map((w) => [w.id, w]));
  const targetWorkspace =
    (currentWorkspaceId ? workspaceMap.get(currentWorkspaceId) : undefined) ||
    workspaces[0];

  const recentOrder = new Map(
    recentConversationIds.map((id, index) => [id, index]),
  );

  const sortedConversations = [...conversations]
    .filter((c) => {
      if (!workspaceMap.has(c.workspaceId)) return false;
      if (c.type !== "channel" && !c.name.trim()) {
        const wsMembers = membersByWorkspace.get(c.workspaceId);
        if (!wsMembers) return false;
        const otherIds = c.memberIds.filter((id) => id !== userId);
        const resolved =
          otherIds.length === 0 ||
          otherIds.some((id) => wsMembers.some((m) => m.userId === id));
        if (!resolved && agentLoadingByWorkspace.get(c.workspaceId)) {
          return false;
        }
      }
      return true;
    })
    .sort((a, b) => {
      const rankA = recentOrder.get(a.id) ?? Number.MAX_SAFE_INTEGER;
      const rankB = recentOrder.get(b.id) ?? Number.MAX_SAFE_INTEGER;
      if (rankA !== rankB) return rankA - rankB;
      return compareTimestampsDesc(a.updatedAt, b.updatedAt);
    });

  const conversationItems: QuickItem[] = sortedConversations.map((c) => ({
    id: `conv:${c.id}`,
    label: conversationName(c, userId, membersByWorkspace.get(c.workspaceId) || []),
    subtitle: workspaceMap.get(c.workspaceId)?.name || "工作空间",
    path: `/app/w/${c.workspaceId}/c/${c.id}`,
    kind: c.type,
    conversation: c,
  }));
  const workspaceItems: QuickItem[] = workspaces.map((space) => ({
    id: `ws:${space.id}`,
    label: space.name,
    subtitle: "工作空间",
    path: `/app/w/${space.id}`,
    kind: "workspace",
  }));
  const pageItems = buildPageItems({ workspace: targetWorkspace, isPlatformAdmin });

  const trimmedQuery = query.trim();
  const termLower = trimmedQuery.toLocaleLowerCase();
  const validQuery = Array.from(trimmedQuery).length <= 200;
  const searchEnabled =
    mode === "messages" &&
    Boolean(targetWorkspace && validQuery && trimmedQuery);
  const searchKey = JSON.stringify([
    userId,
    generation,
    targetWorkspace?.id || "",
    mode,
    trimmedQuery,
  ]);
  const currentSearchPage =
    searchResult?.key === searchKey ? searchResult : null;
  const searchPending = searchEnabled && !currentSearchPage;

  useEffect(() => {
    const operation = ++sequence.current;
    const activeRequests = requests.current;
    const controller = new AbortController();
    const isCurrent = () =>
      !controller.signal.aborted &&
      generation === chatStore.generation &&
      operation === sequence.current;
    const timer =
      searchEnabled && targetWorkspace
        ? setTimeout(() => {
            activeRequests.add(controller);
            void api
              .searchWorkspaceMessages(
                targetWorkspace.id,
                { q: trimmedQuery, limit: 20 },
                controller.signal,
              )
              .then((response) => {
                if (isCurrent()) {
                  setSearchResult({
                    key: searchKey,
                    items: response.items,
                    cursor: response.cursor,
                    error: "",
                    loadingMore: false,
                  });
                }
              })
              .catch((error) => {
                if (isCurrent()) {
                  setSearchResult({
                    key: searchKey,
                    items: [],
                    error: errorText(error),
                    loadingMore: false,
                  });
                }
              })
              .finally(() => activeRequests.delete(controller));
          }, 250)
        : undefined;
    return () => {
      clearTimeout(timer);
      controller.abort();
      activeRequests.forEach((req) => req.abort());
      activeRequests.clear();
    };
  }, [
    generation,
    mode,
    retryNonce,
    searchEnabled,
    searchKey,
    targetWorkspace,
    trimmedQuery,
  ]);

  async function loadMoreMessages() {
    if (
      !targetWorkspace ||
      !currentSearchPage?.cursor ||
      currentSearchPage.loadingMore
    ) {
      return;
    }
    const operation = sequence.current;
    const controller = new AbortController();
    const isCurrent = () =>
      !controller.signal.aborted &&
      generation === chatStore.generation &&
      operation === sequence.current;
    requests.current.add(controller);
    setSearchResult({ ...currentSearchPage, error: "", loadingMore: true });
    try {
      const response = await api.searchWorkspaceMessages(
        targetWorkspace.id,
        { q: trimmedQuery, cursor: currentSearchPage.cursor, limit: 20 },
        controller.signal,
      );
      if (isCurrent()) {
        setSearchResult((old) => {
          if (!old || old.key !== searchKey) return old;
          const seen = new Set(old.items.map((it) => it.message.id));
          const merged = [
            ...old.items,
            ...response.items.filter((it) => !seen.has(it.message.id)),
          ];
          return {
            ...old,
            items: merged,
            cursor: response.cursor,
            loadingMore: false,
          };
        });
      }
    } catch (error) {
      if (isCurrent()) {
        setSearchResult((old) =>
          old?.key === searchKey
            ? { ...old, error: errorText(error), loadingMore: false }
            : old,
        );
      }
    } finally {
      requests.current.delete(controller);
    }
  }

  const authorizedConvMap = new Map<string, Conversation>();
  for (const c of conversations) {
    if (workspaceMap.has(c.workspaceId)) {
      authorizedConvMap.set(c.id, c);
    }
  }
  for (const c of Object.values(snapshot.conversations)) {
    if (workspaceMap.has(c.workspaceId)) {
      authorizedConvMap.set(c.id, c);
    }
  }

  const queryLower = trimmedQuery.toLowerCase();
  const reconciledMessageOptions: MessageOption[] = [];
  if (mode === "messages" && currentSearchPage && targetWorkspace) {
    const wsMembers = membersByWorkspace.get(targetWorkspace.id) || [];
    for (const rawItem of currentSearchPage.items) {
      if (rawItem.conversation.workspaceId !== targetWorkspace.id) continue;
      const liveConv = authorizedConvMap.get(rawItem.conversation.id);
      if (!liveConv && Object.keys(snapshot.conversations).length > 0) continue;
      const cached = (snapshot.messages[rawItem.conversation.id] || []).filter(
        (m) => m.id === rawItem.message.id,
      );
      const [reconciled] = reconcileMessages([rawItem.message], cached);
      if (!reconciled || reconciled.retractedAt) {
        continue;
      }
      const message =
        reconciled.forwarded || !rawItem.message.forwarded
          ? reconciled
          : { ...reconciled, forwarded: rawItem.message.forwarded };
      const bodyChanged = message.body !== rawItem.message.body;
      const matchesQuery =
        message.body.toLowerCase().includes(queryLower) ||
        (message.forwarded?.body || "").toLowerCase().includes(queryLower);
      if (bodyChanged && !matchesQuery) {
        continue;
      }
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
      const conversationLabel = conversationName(
        convForName,
        userId,
        wsMembers,
      );
      reconciledMessageOptions.push({
        id: `msg:${message.id}`,
        message,
        conversation: rawItem.conversation,
        conversationLabel,
        path: `/app/w/${encodeURIComponent(rawItem.conversation.workspaceId)}/c/${encodeURIComponent(rawItem.conversation.id)}?message=${encodeURIComponent(message.id)}`,
      });
    }
  }

  const groupedMessageResults: GroupedMessageResult[] = [];
  const groupByConv = new Map<
    string,
    (typeof groupedMessageResults)[number]
  >();
  for (const opt of reconciledMessageOptions) {
    let group = groupByConv.get(opt.conversation.id);
    if (!group) {
      group = {
        conversationId: opt.conversation.id,
        conversationType: opt.conversation.type,
        conversationLabel: opt.conversationLabel,
        options: [],
      };
      groupByConv.set(opt.conversation.id, group);
      groupedMessageResults.push(group);
    }
    group.options.push(opt);
  }

  const jumpGroups: QuickGroup[] = termLower
    ? [{
        id: "results",
        label: "",
        items: rankItems([...conversationItems, ...workspaceItems, ...pageItems], trimmedQuery, toPinyin),
      }].filter((group) => group.items.length > 0)
    : groupEmptyState({
        conversations: conversationItems,
        recentConversationIds,
        workspaces: workspaceItems,
        pages: pageItems,
      });
  const jumpItems = jumpGroups.flatMap((group) => group.items);

  const selectableOptions: Array<{ id: string; path: string }> =
    mode === "jump"
      ? jumpItems.map((it) => ({ id: it.id, path: it.path }))
      : groupedMessageResults.flatMap((g) =>
          g.options.map((opt) => ({ id: opt.id, path: opt.path })),
        );

  const foundIndex = activeId
    ? selectableOptions.findIndex((item) => item.id === activeId)
    : -1;
  const boundedIndex = foundIndex >= 0 ? foundIndex : 0;
  const activeItemId = selectableOptions[boundedIndex]?.id;

  useEffect(() => {
    const activeOption = listRef.current?.querySelector<HTMLElement>(
      '[role="option"][aria-selected="true"]',
    );
    activeOption?.scrollIntoView?.({ block: "nearest" });
  }, [boundedIndex, activeItemId]);

  function handleKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (isComposingKey(event)) return;
    if (event.key === "ArrowDown") {
      event.preventDefault();
      if (selectableOptions.length)
        setActiveId((currentId) => {
          const currentIndex = currentId
            ? selectableOptions.findIndex((item) => item.id === currentId)
            : -1;
          const base = currentIndex >= 0 ? currentIndex : 0;
          return (
            selectableOptions[(base + 1) % selectableOptions.length]?.id || ""
          );
        });
      return;
    }
    if (event.key === "ArrowUp") {
      event.preventDefault();
      if (selectableOptions.length)
        setActiveId((currentId) => {
          const currentIndex = currentId
            ? selectableOptions.findIndex((item) => item.id === currentId)
            : -1;
          const base = currentIndex >= 0 ? currentIndex : 0;
          return (
            selectableOptions[
              (base - 1 + selectableOptions.length) % selectableOptions.length
            ]?.id || ""
          );
        });
      return;
    }
    if (event.key === "Enter" && selectableOptions[boundedIndex]) {
      event.preventDefault();
      onSelect(selectableOptions[boundedIndex].path);
    }
  }

  return (
    <Modal
      title="快速跳转"
      subtitle={
        mode === "messages" && targetWorkspace
          ? `在 ${targetWorkspace.name} 中搜索消息`
          : "搜索对话、工作空间或常用页面"
      }
      onClose={onClose}
    >
      <div className="quick-switcher">
        <div
          className="search-modes quick-switcher-modes"
          role="group"
          aria-label="快速跳转模式"
        >
          <button
            type="button"
            aria-pressed={mode === "jump"}
            onClick={() => {
              setMode("jump");
              setActiveId("");
            }}
          >
            跳转
          </button>
          <button
            type="button"
            aria-pressed={mode === "messages"}
            disabled={!targetWorkspace}
            onClick={() => {
              setMode("messages");
              setActiveId("");
            }}
          >
            搜索消息
          </button>
        </div>
        <label className="quick-switcher-input">
          {searchPending ? (
            <LoaderCircle size={ICON.md} className="spin" aria-hidden="true" />
          ) : (
            <Search size={ICON.md} aria-hidden="true" />
          )}
          <input
            ref={inputRef}
            type="search"
            role="combobox"
            aria-expanded={selectableOptions.length > 0}
            aria-autocomplete="list"
            aria-controls="quick-switcher-listbox"
            aria-activedescendant={
              activeItemId ? `quick-switcher-opt-${activeItemId}` : undefined
            }
            data-autofocus
            value={query}
            onChange={(event) => {
              setQuery(event.target.value);
              setActiveId("");
            }}
            onKeyDown={handleKeyDown}
            placeholder={
              mode === "messages"
                ? targetWorkspace
                  ? `在 ${targetWorkspace.name} 中搜索消息关键词…`
                  : "当前没有可搜索的工作空间"
                : "输入名称、拼音或首字母…"
            }
            aria-label={mode === "messages" ? "搜索工作空间消息" : "快速跳转搜索"}
          />
        </label>
        {mode === "messages" && !validQuery && (
          <Notice>关键词最多 200 个字符</Notice>
        )}
        {mode === "jump" ? (
          jumpItems.length ? (
            <QuickSwitcherJumpResults
              groups={jumpGroups}
              listRef={listRef}
              activeItemId={activeItemId}
              setActiveId={setActiveId}
              onSelect={onSelect}
              summaryOf={(conversation) =>
                conversationUnreadSummary(conversation, snapshot, userId, attentionProjection)
              }
            />
          ) : (
            <div className="quick-switcher-empty" role="status">
              没有匹配的对话或页面
            </div>
          )
        ) : (
          <QuickSwitcherMessageResults
            searchPending={searchPending}
            trimmedQuery={trimmedQuery}
            groupedMessageResults={groupedMessageResults}
            listRef={listRef}
            activeItemId={activeItemId}
            setActiveId={setActiveId}
            onSelect={onSelect}
            currentSearchPage={currentSearchPage}
            onLoadMore={() => void loadMoreMessages()}
            onRetry={() => {
              setSearchResult(null);
              setRetryNonce((n) => n + 1);
            }}
          />
        )}
      </div>
    </Modal>
  );
}
