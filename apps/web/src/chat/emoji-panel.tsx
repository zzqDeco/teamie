import { useEffect, useState } from "react";
import { LoaderCircle } from "lucide-react";
import type { Message } from "@teamie/api-client";
import { ICON } from "../icons";
import { loadEmojiCatalog, loadedEmojiCatalog, validRecentReactions } from "./emoji-recent";

const RECENT_TAB = "recent";

/**
 * Full emoji panel (categories, search, recent). The catalog chunk is
 * fetched on first open; every offered emoji comes from the pinned Unicode
 * Emoji 17.0 allowlist, so selections always satisfy the reaction contract.
 */
export function EmojiPanel({
  userId,
  reactions,
  onSelect,
}: {
  userId: string;
  reactions: Message["reactions"];
  onSelect: (emoji: string) => void;
}) {
  const [catalog, setCatalog] = useState(loadedEmojiCatalog);
  const [failed, setFailed] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const [query, setQuery] = useState("");
  const recent = catalog ? validRecentReactions(userId, catalog) : [];
  const [tab, setTab] = useState<string>(() => (recent.length ? RECENT_TAB : "smileys"));

  useEffect(() => {
    if (catalog) return;
    let active = true;
    loadEmojiCatalog().then(
      (module) => {
        if (!active) return;
        setCatalog(module);
        if (validRecentReactions(userId, module).length) setTab(RECENT_TAB);
      },
      () => {
        if (active) setFailed(true);
      },
    );
    return () => {
      active = false;
    };
  }, [catalog, attempt, userId]);

  if (!catalog) {
    return (
      <div className="emoji-panel">
        {failed ? (
          <p className="emoji-panel-status" role="alert">
            表情加载失败
            <button
              type="button"
              className="emoji-panel-retry"
              onClick={() => {
                setFailed(false);
                setAttempt((value) => value + 1);
              }}
            >
              重试
            </button>
          </p>
        ) : (
          <p className="emoji-panel-status" role="status">
            <LoaderCircle size={ICON.xs} className="spin" /> 正在加载表情…
          </p>
        )}
      </div>
    );
  }

  const term = query.trim();
  const group = catalog.EMOJI_GROUPS.find((item) => item.id === tab);
  const emojis = term
    ? catalog.searchEmojis(term)
    : tab === RECENT_TAB
      ? recent
      : (group?.entries.map(([emoji]) => emoji) ?? []);
  const gridLabel = term ? "搜索结果" : tab === RECENT_TAB ? "最近使用" : group?.label || "表情";

  return (
    <div className="emoji-panel">
      <input
        type="search"
        className="emoji-panel-search"
        aria-label="搜索表情"
        placeholder="搜索表情（英文名称或分类）"
        value={query}
        onChange={(event) => setQuery(event.target.value)}
      />
      {!term && (
        <div className="emoji-panel-tabs" role="group" aria-label="表情分类">
          <button
            type="button"
            className="emoji-panel-tab"
            aria-pressed={tab === RECENT_TAB}
            aria-label="最近使用"
            title="最近使用"
            onClick={() => setTab(RECENT_TAB)}
          >
            🕘
          </button>
          {catalog.EMOJI_GROUPS.map((item) => (
            <button
              key={item.id}
              type="button"
              className="emoji-panel-tab"
              aria-pressed={tab === item.id}
              aria-label={item.label}
              title={item.label}
              onClick={() => setTab(item.id)}
            >
              {item.entries[0]?.[0]}
            </button>
          ))}
        </div>
      )}
      <div className="emoji-panel-grid" role="group" aria-label={gridLabel}>
        {emojis.length === 0 && (
          <p className="emoji-panel-status">
            {term ? "没有找到表情" : tab === RECENT_TAB ? "还没有最近使用的表情" : ""}
          </p>
        )}
        {emojis.map((emoji) => {
          const active = !!reactions?.find((item) => item.emoji === emoji)?.userIds.includes(userId);
          return (
            <button
              key={emoji}
              type="button"
              className={`reaction-picker-emoji ${active ? "active" : ""}`}
              aria-label={`表情回应 ${emoji}`}
              onClick={() => onSelect(emoji)}
            >
              {emoji}
            </button>
          );
        })}
      </div>
    </div>
  );
}
