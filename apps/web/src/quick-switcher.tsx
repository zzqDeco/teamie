import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { useQueries } from "@tanstack/react-query";
import {
  Bell,
  Bot,
  Hash,
  MessageCircle,
  Search,
  Settings,
  Users,
} from "lucide-react";
import { api } from "@teamie/api-client";
import type { Conversation, Member, Workspace } from "@teamie/api-client";
import { isComposingKey } from "./mentions";
import { conversationName } from "./messages";
import { enrichParticipants } from "./participants";
import { Modal } from "./ui";

type QuickItem = {
  id: string;
  label: string;
  subtitle: string;
  path: string;
  kind: "channel" | "direct" | "group" | "workspace" | "page";
  unread: number;
};

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
  const [query, setQuery] = useState("");
  const [activeId, setActiveId] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLUListElement>(null);

  useEffect(() => {
    inputRef.current?.focus({ preventScroll: true });
  }, []);

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

  const term = query.trim().toLocaleLowerCase();
  const items = term
    ? allItems.filter((item) =>
        `${item.label} ${item.subtitle}`.toLocaleLowerCase().includes(term),
      )
    : allItems;
  const foundIndex = activeId
    ? items.findIndex((item) => item.id === activeId)
    : -1;
  const boundedIndex = foundIndex >= 0 ? foundIndex : 0;
  const activeItemId = items[boundedIndex]?.id;

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
      if (items.length)
        setActiveId((currentId) => {
          const currentIndex = currentId
            ? items.findIndex((item) => item.id === currentId)
            : -1;
          const base = currentIndex >= 0 ? currentIndex : 0;
          return items[(base + 1) % items.length]?.id || "";
        });
      return;
    }
    if (event.key === "ArrowUp") {
      event.preventDefault();
      if (items.length)
        setActiveId((currentId) => {
          const currentIndex = currentId
            ? items.findIndex((item) => item.id === currentId)
            : -1;
          const base = currentIndex >= 0 ? currentIndex : 0;
          return items[(base - 1 + items.length) % items.length]?.id || "";
        });
      return;
    }
    if (event.key === "Enter" && items[boundedIndex]) {
      event.preventDefault();
      onSelect(items[boundedIndex].path);
    }
  }

  return (
    <Modal title="快速跳转" subtitle="搜索对话、工作空间或常用页面" onClose={onClose}>
      <div className="quick-switcher">
        <label className="quick-switcher-input">
          <Search size={17} aria-hidden="true" />
          <input
            ref={inputRef}
            type="search"
            role="combobox"
            aria-expanded={items.length > 0}
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
            placeholder="输入频道、私信或工作空间名称…"
            aria-label="快速跳转搜索"
          />
        </label>
        {items.length ? (
          <ul
            id="quick-switcher-listbox"
            ref={listRef}
            className="quick-switcher-list"
            role="listbox"
            aria-label="跳转结果"
          >
            {items.map((item, index) => {
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
                        <Hash size={16} />
                      ) : item.kind === "group" || item.kind === "workspace" ? (
                        <Users size={16} />
                      ) : item.kind === "direct" ? (
                        <MessageCircle size={16} />
                      ) : item.id === "page:attention" ? (
                        <Bell size={16} />
                      ) : item.id === "page:agents" ? (
                        <Bot size={16} />
                      ) : (
                        <Settings size={16} />
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
        )}
      </div>
    </Modal>
  );
}

const shortcuts = [
  { keys: ["⌘ / Ctrl", "K"], description: "打开快速跳转" },
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
