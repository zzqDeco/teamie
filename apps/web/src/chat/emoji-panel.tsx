import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { LoaderCircle } from "lucide-react";
import type { Message } from "@teamie/api-client";
import { ICON } from "../icons";
import {
  loadEmojiCatalog,
  loadedEmojiCatalog,
  reactionAriaLabel,
  validRecentReactions,
} from "./emoji-recent";

const RECENT_TAB = "recent";
const DEFAULT_GRID_COLUMNS = 8;

function measureGridColumns(
  grid: HTMLElement | null,
  buttons: HTMLButtonElement[],
): number {
  if (buttons.length < 2) return 1;
  const firstOffsetTop = buttons[0]!.offsetTop;
  let byOffset = 0;
  for (const btn of buttons) {
    if (btn.offsetTop !== firstOffsetTop) break;
    byOffset += 1;
  }
  if (byOffset > 0 && byOffset < buttons.length) return byOffset;

  const firstRectTop = buttons[0]!.getBoundingClientRect().top;
  let byRect = 0;
  for (const btn of buttons) {
    if (Math.abs(btn.getBoundingClientRect().top - firstRectTop) > 1) break;
    byRect += 1;
  }
  if (byRect > 0 && byRect < buttons.length) return byRect;

  if (grid && typeof getComputedStyle === "function") {
    const tracks = getComputedStyle(grid)
      .gridTemplateColumns.trim()
      .split(/\s+/)
      .filter((token) => /^\d+(\.\d+)?px$/.test(token));
    if (tracks.length > 1) return tracks.length;
  }
  return DEFAULT_GRID_COLUMNS;
}

/**
 * Full emoji panel (categories, search, recent). The catalog chunk is
 * fetched on first open; every offered emoji comes from the pinned Unicode
 * Emoji 17.0 allowlist, so selections always satisfy the reaction contract.
 */
export function EmojiPanel({
  userId,
  reactions,
  onSelect,
  autoFocusSearch = false,
}: {
  userId: string;
  reactions: Message["reactions"];
  onSelect: (emoji: string) => void;
  autoFocusSearch?: boolean;
}) {
  const [catalog, setCatalog] = useState(loadedEmojiCatalog);
  const [failed, setFailed] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const [query, setQuery] = useState("");
  const recent = catalog ? validRecentReactions(userId, catalog) : [];
  const [tab, setTab] = useState<string>(() => (recent.length ? RECENT_TAB : "smileys"));
  const [activeIndex, setActiveIndex] = useState(0);
  const searchRef = useRef<HTMLInputElement>(null);
  const retryRef = useRef<HTMLButtonElement>(null);
  const gridRef = useRef<HTMLDivElement>(null);
  const tabsRef = useRef<HTMLDivElement>(null);
  const didAutoFocusRef = useRef(false);

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

  useEffect(() => {
    if (!autoFocusSearch || !catalog || didAutoFocusRef.current) return;
    const input = searchRef.current;
    if (!input) return;
    didAutoFocusRef.current = true;
    const popover = input.closest(".reaction-picker-popover");
    const active = document.activeElement;
    if (
      !active ||
      active === document.body ||
      (popover && popover.contains(active))
    ) {
      input.focus({ preventScroll: true });
    }
  }, [autoFocusSearch, catalog]);

  useEffect(() => {
    if (!autoFocusSearch || !failed || catalog) return;
    const button = retryRef.current;
    if (!button) return;
    const popover = button.closest(".reaction-picker-popover");
    const active = document.activeElement;
    if (
      !active ||
      active === document.body ||
      (popover && popover.contains(active))
    ) {
      button.focus({ preventScroll: true });
    }
  }, [autoFocusSearch, failed, catalog]);

  if (!catalog) {
    return (
      <div className="emoji-panel">
        {failed ? (
          <p className="emoji-panel-status" role="alert">
            表情加载失败
            <button
              ref={retryRef}
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
  const clampedIndex = emojis.length > 0 ? Math.min(activeIndex, emojis.length - 1) : 0;

  function handleTabKeyDown(event: KeyboardEvent<HTMLButtonElement>) {
    const buttons = Array.from(
      tabsRef.current?.querySelectorAll<HTMLButtonElement>("button") ?? [],
    );
    if (!buttons.length) return;
    const index = buttons.indexOf(event.currentTarget);
    if (index < 0) return;
    let next: number;
    switch (event.key) {
      case "ArrowRight":
        next = (index + 1) % buttons.length;
        break;
      case "ArrowLeft":
        next = (index - 1 + buttons.length) % buttons.length;
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = buttons.length - 1;
        break;
      default:
        return;
    }
    event.preventDefault();
    event.stopPropagation();
    buttons[next]?.focus({ preventScroll: true });
  }

  function handleGridKeyDown(
    event: KeyboardEvent<HTMLButtonElement>,
    index: number,
  ) {
    const buttons = Array.from(
      gridRef.current?.querySelectorAll<HTMLButtonElement>("button") ?? [],
    );
    if (!buttons.length) return;
    const cols = measureGridColumns(gridRef.current, buttons);
    let next: number;
    switch (event.key) {
      case "ArrowRight":
        next = Math.min(buttons.length - 1, index + 1);
        break;
      case "ArrowLeft":
        next = Math.max(0, index - 1);
        break;
      case "ArrowDown":
        if (index + cols < buttons.length) {
          next = index + cols;
        } else if (
          Math.floor(index / cols) < Math.floor((buttons.length - 1) / cols)
        ) {
          next = buttons.length - 1;
        } else {
          next = index;
        }
        break;
      case "ArrowUp":
        next = index - cols >= 0 ? index - cols : index;
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = buttons.length - 1;
        break;
      default:
        return;
    }
    event.preventDefault();
    event.stopPropagation();
    setActiveIndex(next);
    buttons[next]?.focus({ preventScroll: true });
  }

  return (
    <div className="emoji-panel">
      <input
        ref={searchRef}
        type="search"
        className="emoji-panel-search"
        aria-label="搜索表情"
        placeholder="搜索表情（中文、拼音或英文）"
        value={query}
        onChange={(event) => {
          setQuery(event.target.value);
          setActiveIndex(0);
        }}
      />
      {!term && (
        <div
          ref={tabsRef}
          className="emoji-panel-tabs"
          role="group"
          aria-label="表情分类"
        >
          <button
            type="button"
            className="emoji-panel-tab"
            aria-pressed={tab === RECENT_TAB}
            aria-label="最近使用"
            title="最近使用"
            onClick={() => {
              setTab(RECENT_TAB);
              setActiveIndex(0);
            }}
            onKeyDown={handleTabKeyDown}
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
              onClick={() => {
                setTab(item.id);
                setActiveIndex(0);
              }}
              onKeyDown={handleTabKeyDown}
            >
              {item.entries[0]?.[0]}
            </button>
          ))}
        </div>
      )}
      <div
        ref={gridRef}
        className="emoji-panel-grid"
        role="group"
        aria-label={gridLabel}
      >
        {emojis.length === 0 && (
          <p className="emoji-panel-status">
            {term ? "没有找到表情" : tab === RECENT_TAB ? "还没有最近使用的表情" : ""}
          </p>
        )}
        {emojis.map((emoji, index) => {
          const active = !!reactions?.find((item) => item.emoji === emoji)?.userIds.includes(userId);
          const label = catalog.emojiLabel(emoji);
          return (
            <button
              key={emoji}
              type="button"
              className={`reaction-picker-emoji ${active ? "active" : ""}`}
              tabIndex={index === clampedIndex ? 0 : -1}
              aria-pressed={active}
              aria-label={reactionAriaLabel(emoji, label)}
              title={label || undefined}
              onFocus={() => setActiveIndex(index)}
              onKeyDown={(event) => handleGridKeyDown(event, index)}
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
