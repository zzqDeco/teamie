import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { RefObject } from "react";
import { createPortal } from "react-dom";
import { Plus } from "lucide-react";
import type { Message } from "@teamie/api-client";
import { COMMON_REACTION_EMOJIS } from "../engagement-store";
import { ICON } from "../icons";
import { EmojiPanel } from "./emoji-panel";
import { loadEmojiCatalog, loadedEmojiCatalog, validRecentReactions } from "./emoji-recent";

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
    <div
      ref={menuRef}
      className={`reaction-picker-popover ${expanded ? "expanded" : ""}`}
      role={expanded ? "dialog" : "menu"}
      aria-label="选择表情回应"
      onKeyDown={(event) => {
        // Portal events still bubble through the message's React tree.
        event.stopPropagation();
        const buttons = Array.from(event.currentTarget.querySelectorAll("button"));
        if ((event.target as HTMLElement).tagName === "INPUT") {
          if (event.key === "ArrowDown") {
            event.preventDefault();
            event.currentTarget
              .querySelector<HTMLButtonElement>(".emoji-panel-grid button")
              ?.focus({ preventScroll: true });
          }
          return;
        }
        const index = buttons.indexOf(document.activeElement as HTMLButtonElement);
        let next: number;
        switch (event.key) {
          case "ArrowRight": case "ArrowDown": next = (index + 1) % buttons.length; break;
          case "ArrowLeft": case "ArrowUp": next = (index - 1 + buttons.length) % buttons.length; break;
          case "Home": next = 0; break;
          case "End": next = buttons.length - 1; break;
          case "Tab":
            if (expanded) return;
            anchorRef.current?.querySelector("button")?.focus({ preventScroll: true });
            onClose();
            return;
          default: return;
        }
        event.preventDefault();
        buttons[next]?.focus({ preventScroll: true });
      }}
    >
      <div className="reaction-picker-quick" role="none">
        {quick.map((emoji) => {
          const active = !!reactions?.find((item) => item.emoji === emoji)?.userIds.includes(userId);
          return (
            <button
              key={emoji}
              type="button"
              role={expanded ? undefined : "menuitem"}
              className={`reaction-picker-emoji ${active ? "active" : ""}`}
              aria-label={`表情回应 ${emoji}`}
              onClick={() => select(emoji)}
            >
              {emoji}
            </button>
          );
        })}
        {!expanded && (
          <button
            type="button"
            role="menuitem"
            className="reaction-picker-emoji reaction-picker-more"
            aria-label="更多表情"
            title="更多表情"
            onClick={() => {
              setExpanded(true);
              requestAnimationFrame(() =>
                menuRef.current
                  ?.querySelector<HTMLInputElement>(".emoji-panel-search, .emoji-panel button")
                  ?.focus({ preventScroll: true }),
              );
            }}
          >
            <Plus size={ICON.sm} />
          </button>
        )}
      </div>
      {expanded && <EmojiPanel userId={userId} reactions={reactions} onSelect={select} />}
    </div>,
    document.body,
  );
}
