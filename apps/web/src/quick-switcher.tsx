import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { useQueries } from "@tanstack/react-query";
import {
  Bell,
  Bot,
  Hash,
  LoaderCircle,
  MessageCircle,
  Search,
  Settings,
  Users,
} from "lucide-react";
import { api } from "@teamie/api-client";
import type {
  Conversation,
  Member,
  Message,
  Workspace,
  WorkspaceSearchItem,
} from "@teamie/api-client";
import { AgentBadge } from "./agent-ui";
import { errorText, useChatState } from "./chat";
import { ICON } from "./icons";
import { isComposingKey } from "./mentions";
import { conversationName } from "./messages";
import { enrichParticipants } from "./participants";
import { chatStore, reconcileMessages } from "./store";
import { Button, date, Modal, Notice, time } from "./ui";

type QuickItem = {
  id: string;
  label: string;
  subtitle: string;
  path: string;
  kind: "channel" | "direct" | "group" | "workspace" | "page";
  unread: number;
};

type WorkspaceSearchState = {
  key: string;
  items: WorkspaceSearchItem[];
  cursor?: string;
  error: string;
  loadingMore: boolean;
};

function Highlight({ text, query }: { text: string; query: string }) {
  if (!query) return text;
  const literal = query.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return text
    .split(new RegExp(`(${literal})`, "giu"))
    .map((part, i) => (i % 2 ? <mark key={i}>{part}</mark> : part));
}

export function QuickSwitcher({
  workspaces,
  conversations,
  currentWorkspaceId,
  discoveryAllowed = false,
  members,
  userId,
  recentConversationIds,
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

  useEffect(() => {
    inputRef.current?.focus({ preventScroll: true });
  }, [mode]);

  const memberQueries = useQueries({
    queries: workspaces.map((space) => ({
      queryKey: ["members", userId, space.id],
      queryFn: () => api.members(space.id),
      staleTime: 60_000,
    })),
  });
  const agentQueries = useQueries({
    queries: workspaces.map((space) => ({
      queryKey: ["workspaceAgents", userId, space.id],
      queryFn: () => api.workspaceAgents(space.id),
      enabled: discoveryAllowed,
      staleTime: 30_000,
    })),
  });
  const membersByWorkspace = new Map<string, Member[]>();
  workspaces.forEach((space, index) => {
    const loaded = memberQueries[index]?.data?.members;
    const rawMembers =
      loaded || (space.id === currentWorkspaceId ? members : undefined);
    const rawAgents = discoveryAllowed
      ? agentQueries[index]?.data?.agents || []
      : [];
    if (rawMembers) {
      const merged =
        space.id === currentWorkspaceId && members.length
          ? [
              ...rawMembers,
              ...members.filter(
                (m) => !rawMembers.some((rm) => rm.userId === m.userId),
              ),
            ]
          : rawMembers;
      membersByWorkspace.set(space.id, enrichParticipants(merged, rawAgents));
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
        const otherIds = c.memberIds.filter((id) => id !== userId);
        if (
          !wsMembers ||
          (otherIds.length > 0 &&
            !otherIds.some((id) => wsMembers.some((m) => m.userId === id)))
        ) {
          return false;
        }
      }
      return true;
    })
    .sort((a, b) => {
      const rankA = recentOrder.get(a.id) ?? Number.MAX_SAFE_INTEGER;
      const rankB = recentOrder.get(b.id) ?? Number.MAX_SAFE_INTEGER;
      if (rankA !== rankB) return rankA - rankB;
      return b.updatedAt.localeCompare(a.updatedAt);
    });

  const allItems: QuickItem[] = [
    ...sortedConversations.map((c) => {
      const space = workspaceMap.get(c.workspaceId);
      const label = conversationName(
        c,
        userId,
        membersByWorkspace.get(c.workspaceId) || [],
      );
      return {
        id: `conv:${c.id}`,
        label,
        subtitle: space?.name || "工作空间",
        path: `/app/w/${c.workspaceId}/c/${c.id}`,
        kind: c.type,
        unread: Math.max(0, c.lastSeq - c.readSeq),
      };
    }),
    ...workspaces.map((space) => ({
      id: `ws:${space.id}`,
      label: space.name,
      subtitle: "工作空间",
      path: `/app/w/${space.id}`,
      kind: "workspace" as const,
      unread: 0,
    })),
    {
      id: "page:attention",
      label: "提及与回复",
      subtitle: "收件箱",
      path: "/app/attention",
      kind: "page",
      unread: 0,
    },
    {
      id: "page:agents",
      label: "个人 Agent",
      subtitle: "管理你的 Agent",
      path: "/app/agents",
      kind: "page",
      unread: 0,
    },
    {
      id: "page:settings",
      label: "账号设置",
      subtitle: "个人资料与密码",
      path: "/app/settings",
      kind: "page",
      unread: 0,
    },
  ];

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

  type MessageOption = {
    id: string;
    message: Message;
    conversation: WorkspaceSearchItem["conversation"];
    conversationLabel: string;
    path: string;
  };

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
      const [message] = reconcileMessages([rawItem.message], cached);
      const bodyChanged = message?.body !== rawItem.message.body;
      if (
        !message ||
        message.retractedAt ||
        (bodyChanged && !message.body.toLowerCase().includes(queryLower))
      ) {
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

  const groupedMessageResults: Array<{
    conversationId: string;
    conversationType: Conversation["type"];
    conversationLabel: string;
    options: MessageOption[];
  }> = [];
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

  const jumpItems = termLower
    ? allItems.filter((item) =>
        `${item.label} ${item.subtitle}`.toLocaleLowerCase().includes(termLower),
      )
    : allItems;

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
            autoFocus
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
                : "输入频道、私信或工作空间名称…"
            }
            aria-label={mode === "messages" ? "搜索工作空间消息" : "快速跳转搜索"}
          />
        </label>
        {mode === "messages" && !validQuery && (
          <Notice>关键词最多 200 个字符</Notice>
        )}
        {mode === "jump" ? (
          jumpItems.length ? (
            <ul
              id="quick-switcher-listbox"
              ref={listRef}
              className="quick-switcher-list"
              role="listbox"
              aria-label="跳转结果"
            >
              {jumpItems.map((item, index) => {
                const selected = index === boundedIndex;
                return (
                  <li
                    key={item.id}
                    id={`quick-switcher-opt-${item.id}`}
                    role="option"
                    aria-selected={selected}
                  >
                    <button
                      type="button"
                      className={`quick-switcher-option ${selected ? "selected" : ""}`}
                      onMouseEnter={() => setActiveId(item.id)}
                      onClick={() => onSelect(item.path)}
                    >
                      <span className="quick-switcher-icon" aria-hidden="true">
                        {item.kind === "channel" ? (
                          <Hash size={ICON.sm} />
                        ) : item.kind === "group" ||
                          item.kind === "workspace" ? (
                          <Users size={ICON.sm} />
                        ) : item.kind === "direct" ? (
                          <MessageCircle size={ICON.sm} />
                        ) : item.id === "page:attention" ? (
                          <Bell size={ICON.sm} />
                        ) : item.id === "page:agents" ? (
                          <Bot size={ICON.sm} />
                        ) : (
                          <Settings size={ICON.sm} />
                        )}
                      </span>
                      <span className="quick-switcher-text">
                        <b>{item.label}</b>
                        <small>{item.subtitle}</small>
                      </span>
                      {item.unread > 0 && (
                        <span className="unread-badge">
                          {item.unread > 99 ? "99+" : item.unread}
                        </span>
                      )}
                    </button>
                  </li>
                );
              })}
            </ul>
          ) : (
            <div className="quick-switcher-empty" role="status">
              没有匹配的对话或页面
            </div>
          )
        ) : (
          <div className="quick-switcher-search-body" aria-busy={searchPending}>
            {!trimmedQuery ? (
              <div className="quick-switcher-empty" role="status">
                输入关键词搜索当前工作空间的消息
              </div>
            ) : searchPending ? (
              <div className="quick-switcher-empty" role="status">
                正在搜索消息…
              </div>
            ) : (
              <>
                {groupedMessageResults.length > 0 ? (
                  <ul
                    id="quick-switcher-listbox"
                    ref={listRef}
                    className="quick-switcher-list quick-switcher-message-groups"
                    role="listbox"
                    aria-label="工作空间消息搜索结果"
                  >
                    {groupedMessageResults.map((group) => (
                      <li
                        key={group.conversationId}
                        className="quick-switcher-group"
                        role="presentation"
                      >
                        <div className="quick-switcher-group-header">
                          {group.conversationType === "channel" ? (
                            <Hash size={ICON.xs} aria-hidden="true" />
                          ) : group.conversationType === "group" ? (
                            <Users size={ICON.xs} aria-hidden="true" />
                          ) : (
                            <MessageCircle size={ICON.xs} aria-hidden="true" />
                          )}
                          <span>{group.conversationLabel}</span>
                        </div>
                        <ul
                          className="quick-switcher-group-items"
                          role="group"
                          aria-label={group.conversationLabel}
                        >
                          {group.options.map((opt) => {
                            const selected = opt.id === activeItemId;
                            return (
                              <li
                                key={opt.id}
                                id={`quick-switcher-opt-${opt.id}`}
                                role="option"
                                aria-selected={selected}
                              >
                                <button
                                  type="button"
                                  className={`quick-switcher-option quick-switcher-message-option ${selected ? "selected" : ""}`}
                                  onMouseEnter={() => setActiveId(opt.id)}
                                  onClick={() => onSelect(opt.path)}
                                >
                                  <div className="quick-switcher-message-meta">
                                    <b>{opt.message.senderName}</b>
                                    {opt.message.senderKind === "agent" && (
                                      <AgentBadge />
                                    )}
                                    <time dateTime={opt.message.createdAt}>
                                      {date(opt.message.createdAt)}{" "}
                                      {time(opt.message.createdAt)}
                                    </time>
                                  </div>
                                  <p className="quick-switcher-message-snippet">
                                    <Highlight
                                      text={opt.message.body}
                                      query={trimmedQuery}
                                    />
                                  </p>
                                </button>
                              </li>
                            );
                          })}
                        </ul>
                      </li>
                    ))}
                  </ul>
                ) : !currentSearchPage?.error ? (
                  <div className="quick-switcher-empty" role="status">
                    没有找到匹配的消息
                  </div>
                ) : null}
                {currentSearchPage?.error && (
                  <Notice>{currentSearchPage.error}</Notice>
                )}
                {currentSearchPage?.cursor ? (
                  <Button
                    type="button"
                    className="secondary quick-switcher-more"
                    busy={currentSearchPage.loadingMore}
                    onClick={() => void loadMoreMessages()}
                  >
                    加载更多
                  </Button>
                ) : currentSearchPage?.error ? (
                  <Button
                    type="button"
                    className="secondary quick-switcher-more"
                    onClick={() => {
                      setSearchResult(null);
                      setRetryNonce((n) => n + 1);
                    }}
                  >
                    重试
                  </Button>
                ) : null}
              </>
            )}
          </div>
        )}
      </div>
    </Modal>
  );
}

const shortcuts = [
  { keys: ["⌘ / Ctrl", "K"], description: "打开快速跳转与工作空间搜索" },
  { keys: ["Alt", "↑ / ↓"], description: "切换到上一个或下一个对话" },
  { keys: ["Alt", "Shift", "↑ / ↓"], description: "跳转到上一个或下一个未读对话" },
  { keys: ["↑"], description: "在空输入框中编辑上一条自己发送的消息" },
  { keys: ["Enter"], description: "发送消息，或在聚焦消息上打开操作菜单" },
  { keys: ["Shift", "Enter"], description: "消息换行" },
  { keys: ["Esc"], description: "关闭弹窗或返回消息输入框" },
];

export function KeyboardShortcutsModal({ onClose }: { onClose: () => void }) {
  return (
    <Modal title="键盘快捷键" subtitle="使用键盘更快地切换对话和处理消息" onClose={onClose}>
      <dl className="shortcuts-list">
        {shortcuts.map((item) => (
          <div key={item.description} className="shortcuts-row">
            <dt>{item.description}</dt>
            <dd>
              {item.keys.map((key) => (
                <kbd key={key}>{key}</kbd>
              ))}
            </dd>
          </div>
        ))}
      </dl>
    </Modal>
  );
}
