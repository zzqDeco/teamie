import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { RefObject } from "react";
import { createPortal } from "react-dom";
import { Plus } from "lucide-react";
import type { Message } from "@teamie/api-client";
import { COMMON_REACTION_EMOJIS } from "../engagement-store";
import { ICON } from "../icons";
import { EmojiPanel } from "./emoji-panel";
import {
  loadEmojiCatalog,
  loadedEmojiCatalog,
  reactionAriaLabel,
  validRecentReactions,
} from "./emoji-recent";

export function ReactionPicker({
  anchorRef,
  reactions,
  userId,
  onSelect,
  onClose,
}: {
  anchorRef: RefObject<HTMLDivElement | null>;
  reactions: Message["reactions"];
  userId: string;
  onSelect: (emoji: string) => void;
  onClose: () => void;
}) {
  const menuRef = useRef<HTMLDivElement>(null);
  const [expanded, setExpanded] = useState(false);
  const [catalog, setCatalog] = useState(loadedEmojiCatalog);
  useEffect(() => {
    if (catalog) return;
    let active = true;
    // Recents are validated against the pinned allowlist before display.
    loadEmojiCatalog().then(
      (module) => {
        if (active) setCatalog(module);
      },
      () => {},
    );
    return () => {
      active = false;
    };
  }, [catalog]);
  const quick = [
    ...COMMON_REACTION_EMOJIS,
    ...validRecentReactions(userId, catalog)
      .filter((emoji) => !(COMMON_REACTION_EMOJIS as readonly string[]).includes(emoji))
      .slice(0, 8),
  ];
  const select = (emoji: string) => {
    anchorRef.current?.querySelector("button")?.focus({ preventScroll: true });
    onSelect(emoji);
  };

  useLayoutEffect(() => {
    const anchor = anchorRef.current;
    const menu = menuRef.current;
    if (!anchor || !menu) return;
    const scroller = anchor.closest(".message-scroll");
    const viewport = window.visualViewport;
    const restoreFocus = () => anchor.querySelector("button")?.focus({ preventScroll: true });
    const updatePosition = () => {
      const target = anchor.getBoundingClientRect();
      const clip = scroller?.getBoundingClientRect();
      const viewLeft = viewport?.offsetLeft ?? 0;
      const viewTop = viewport?.offsetTop ?? 0;
      const left = Math.max(viewLeft, clip?.left ?? viewLeft) + 8;
      const right = Math.min(viewLeft + (viewport?.width ?? window.innerWidth), clip?.right ?? Infinity) - 8;
      const top = Math.max(viewTop, clip?.top ?? viewTop) + 8;
      const bottom = Math.min(viewTop + (viewport?.height ?? window.innerHeight), clip?.bottom ?? Infinity) - 8;

      // A scrolled-away message must not leave its actions over another row.
      if (clip && clip.height > 0 && (target.bottom <= clip.top || target.top >= clip.bottom)) {
        onClose();
        return;
      }
      menu.style.maxWidth = `${Math.max(0, right - left)}px`;
      const size = menu.getBoundingClientRect();
      const below = target.bottom + 6;
      const above = target.top - size.height - 6;
      const y = below + size.height <= bottom ? below : above;
      menu.style.left = `${Math.max(left, Math.min(target.right - size.width, right - size.width))}px`;
      menu.style.top = `${Math.max(top, Math.min(y, bottom - size.height))}px`;
    };
    const onPointerDown = (event: PointerEvent) => {
      if (event.target instanceof Node && !anchor.contains(event.target) && !menu.contains(event.target)) {
        onClose();
      }
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        restoreFocus();
        onClose();
      }
    };

    updatePosition();
    menu.querySelector("button")?.focus({ preventScroll: true });
    const observer = new ResizeObserver(updatePosition);
    observer.observe(anchor);
    observer.observe(menu);
    if (scroller) observer.observe(scroller);
    window.addEventListener("resize", updatePosition);
    window.addEventListener("scroll", updatePosition, true);
    viewport?.addEventListener("resize", updatePosition);
    viewport?.addEventListener("scroll", updatePosition);
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown, true);
    return () => {
      observer.disconnect();
      window.removeEventListener("resize", updatePosition);
      window.removeEventListener("scroll", updatePosition, true);
      viewport?.removeEventListener("resize", updatePosition);
      viewport?.removeEventListener("scroll", updatePosition);
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown, true);
    };
  }, [anchorRef, onClose]);

  return createPortal(
    // eslint-disable-next-line jsx-a11y/no-noninteractive-element-interactions -- keyboard scoping for dialog popover
    <div
      ref={menuRef}
      className={`reaction-picker-popover ${expanded ? "expanded" : ""}`}
      role="dialog"
      aria-label="选择表情回应"
      tabIndex={-1}
      onKeyDown={(event) => {
        // Portal events still bubble through the message's React tree.
        event.stopPropagation();
        const target = event.target as HTMLElement;
        if (target.tagName === "INPUT") {
          if (event.key === "ArrowDown") {
            event.preventDefault();
            const targetBtn =
              event.currentTarget.querySelector<HTMLButtonElement>(
                '.emoji-panel-grid button[tabindex="0"]',
              ) ??
              event.currentTarget.querySelector<HTMLButtonElement>(
                ".emoji-panel-grid button",
              );
            targetBtn?.focus({ preventScroll: true });
            targetBtn?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
          }
          return;
        }
        if (target.closest(".emoji-panel")) {
          return;
        }
        const buttons = Array.from(
          event.currentTarget.querySelectorAll<HTMLButtonElement>(
            ".reaction-picker-quick button",
          ),
        );
        const index = buttons.indexOf(document.activeElement as HTMLButtonElement);
        let next: number;
        switch (event.key) {
          case "ArrowRight":
          case "ArrowDown":
            if (expanded && event.key === "ArrowDown") return;
            next = (index + 1) % buttons.length;
            break;
          case "ArrowLeft":
          case "ArrowUp":
            if (expanded && event.key === "ArrowUp") return;
            next = (index - 1 + buttons.length) % buttons.length;
            break;
          case "Home":
            next = 0;
            break;
          case "End":
            next = buttons.length - 1;
            break;
          case "Tab":
            if (expanded) return;
            anchorRef.current?.querySelector("button")?.focus({ preventScroll: true });
            onClose();
            return;
          default:
            return;
        }
        event.preventDefault();
        buttons[next]?.focus({ preventScroll: true });
      }}
    >
      <div className="reaction-picker-quick" role="group" aria-label="快捷表情">
        {quick.map((emoji) => {
          const active = !!reactions?.find((item) => item.emoji === emoji)?.userIds.includes(userId);
          const label = catalog?.emojiLabel(emoji);
          return (
            <button
              key={emoji}
              type="button"
              className={`reaction-picker-emoji ${active ? "active" : ""}`}
              aria-pressed={active}
              aria-label={reactionAriaLabel(emoji, label)}
              title={label || undefined}
              onClick={() => select(emoji)}
            >
              {emoji}
            </button>
          );
        })}
        {!expanded && (
          <button
            type="button"
            className="reaction-picker-emoji reaction-picker-more"
            aria-label="更多表情"
            title="更多表情"
            onClick={() => {
              setExpanded(true);
              requestAnimationFrame(() => {
                const search = menuRef.current?.querySelector<HTMLInputElement>(
                  ".emoji-panel-search",
                );
                if (search) search.focus({ preventScroll: true });
                else menuRef.current?.focus({ preventScroll: true });
              });
            }}
          >
            <Plus size={ICON.sm} />
          </button>
        )}
      </div>
      {expanded && (
        <EmojiPanel
          userId={userId}
          reactions={reactions}
          onSelect={select}
          autoFocusSearch
        />
      )}
    </div>,
    document.body,
  );
}
