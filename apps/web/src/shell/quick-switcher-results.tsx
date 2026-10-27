import type { RefObject } from "react";
import { Hash, MessageCircle, Users } from "lucide-react";
import type { Conversation, Message, WorkspaceSearchItem } from "@teamie/api-client";
import { AgentBadge } from "../agent-ui";
import { ICON } from "../icons";
import { Button, date, Modal, Notice, time } from "../ui";

export type MessageOption = {
  id: string;
  message: Message;
  conversation: WorkspaceSearchItem["conversation"];
  conversationLabel: string;
  path: string;
};

export type GroupedMessageResult = {
  conversationId: string;
  conversationType: Conversation["type"];
  conversationLabel: string;
  options: MessageOption[];
};

export type WorkspaceSearchState = {
  key: string;
  items: WorkspaceSearchItem[];
  cursor?: string;
  error: string;
  loadingMore: boolean;
};

export function Highlight({ text, query }: { text: string; query: string }) {
  if (!query) return text;
  const literal = query.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return text
    .split(new RegExp(`(${literal})`, "giu"))
    .map((part, i) => (i % 2 ? <mark key={i}>{part}</mark> : part));
}

export function QuickSwitcherMessageResults({
  searchPending,
  trimmedQuery,
  groupedMessageResults,
  listRef,
  activeItemId,
  setActiveId,
  onSelect,
  currentSearchPage,
  onLoadMore,
  onRetry,
}: {
  searchPending: boolean;
  trimmedQuery: string;
  groupedMessageResults: GroupedMessageResult[];
  listRef: RefObject<HTMLUListElement | null>;
  activeItemId?: string;
  setActiveId: (id: string) => void;
  onSelect: (path: string) => void;
  currentSearchPage: WorkspaceSearchState | null;
  onLoadMore: () => void;
  onRetry: () => void;
}) {
  return (
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
              onClick={onLoadMore}
            >
              加载更多
            </Button>
          ) : currentSearchPage?.error ? (
            <Button
              type="button"
              className="secondary quick-switcher-more"
              onClick={onRetry}
            >
              重试
            </Button>
          ) : null}
        </>
      )}
    </div>
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
