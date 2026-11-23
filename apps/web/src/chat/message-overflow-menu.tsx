import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import {
  Copy,
  Link2,
  MailWarning,
  MoreHorizontal,
  Pencil,
  Pin,
  Trash2,
} from "lucide-react";
import { ICON } from "../icons";
import { IconButton } from "../ui";

export function MessageOverflowMenu({
  open,
  onOpenChange,
  tabIndex,
  canPin,
  isPinned,
  canMarkUnread,
  hasBody,
  canEdit,
  canRetract,
  onTogglePin,
  onMarkUnread,
  onCopyText,
  onCopyLink,
  onEdit,
  onRetract,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  tabIndex: number;
  canPin: boolean;
  isPinned: boolean;
  canMarkUnread: boolean;
  hasBody: boolean;
  canEdit: boolean;
  canRetract: boolean;
  onTogglePin: () => void;
  onMarkUnread: () => void;
  onCopyText: () => void;
  onCopyLink: () => void;
  onEdit: () => void;
  onRetract: () => void;
}) {
  const wrapRef = useRef<HTMLDivElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const [flipUp, setFlipUp] = useState(false);

  useLayoutEffect(() => {
    if (!open) {
      setFlipUp(false);
      return;
    }
    const menu = menuRef.current;
    const scroller = wrapRef.current?.closest(".message-scroll");
    if (!menu) return;
    const menuRect = menu.getBoundingClientRect();
    const limitBottom =
      scroller?.getBoundingClientRect().bottom ?? window.innerHeight;
    setFlipUp(menuRect.height > 0 && menuRect.bottom > limitBottom - 8);
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent) => {
      if (
        event.target instanceof Node &&
        !wrapRef.current?.contains(event.target)
      ) {
        onOpenChange(false);
      }
    };
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        onOpenChange(false);
        wrapRef.current
          ?.querySelector<HTMLButtonElement>("button")
          ?.focus({ preventScroll: true });
      }
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown, true);
    const raf = requestAnimationFrame(() => {
      const menu = menuRef.current;
      if (menu && !menu.contains(document.activeElement)) {
        menu
          .querySelector<HTMLElement>('[role="menuitem"]')
          ?.focus({ preventScroll: true });
      }
    });
    return () => {
      cancelAnimationFrame(raf);
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown, true);
    };
  }, [open, onOpenChange]);

  function handleMenuKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    event.stopPropagation();
    const items = Array.from(
      menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? [],
    );
    if (!items.length) return;
    const index = items.indexOf(document.activeElement as HTMLElement);
    let next = -1;
    if (event.key === "ArrowDown") next = (index + 1) % items.length;
    else if (event.key === "ArrowUp")
      next = (index - 1 + items.length) % items.length;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = items.length - 1;
    else if (event.key === "Tab") {
      onOpenChange(false);
      return;
    }
    if (next >= 0) {
      event.preventDefault();
      items[next]?.focus({ preventScroll: true });
    }
  }

  function run(action: () => void) {
    onOpenChange(false);
    action();
  }

  return (
    <div className="message-overflow-wrap" ref={wrapRef}>
      <IconButton
        label="更多操作"
        tabIndex={tabIndex}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => onOpenChange(!open)}
      >
        <MoreHorizontal size={ICON.sm} />
      </IconButton>
      {open && (
        // eslint-disable-next-line jsx-a11y/interactive-supports-focus -- focus moves into menuitem on open
        <div
          ref={menuRef}
          className={`menu message-overflow-menu${flipUp ? " flip-up" : ""}`}
          role="menu"
          aria-label="更多消息操作"
          onKeyDown={handleMenuKeyDown}
        >
          {(canPin || isPinned) && (
            <button
              type="button"
              role="menuitem"
              className="menu-item"
              onClick={() => run(onTogglePin)}
            >
              <Pin size={ICON.xs} aria-hidden="true" />
              {isPinned ? "取消置顶" : "置顶"}
            </button>
          )}
          {canMarkUnread && (
            <button
              type="button"
              role="menuitem"
              className="menu-item"
              onClick={() => run(onMarkUnread)}
            >
              <MailWarning size={ICON.xs} aria-hidden="true" />
              标为未读
            </button>
          )}
          {hasBody && (
            <button
              type="button"
              role="menuitem"
              className="menu-item"
              onClick={() => run(onCopyText)}
            >
              <Copy size={ICON.xs} aria-hidden="true" />
              复制文本
            </button>
          )}
          <button
            type="button"
            role="menuitem"
            className="menu-item"
            onClick={() => run(onCopyLink)}
          >
            <Link2 size={ICON.xs} aria-hidden="true" />
            复制消息链接
          </button>
          {canEdit && (
            <button
              type="button"
              role="menuitem"
              className="menu-item"
              onClick={() => run(onEdit)}
            >
              <Pencil size={ICON.xs} aria-hidden="true" />
              编辑消息
            </button>
          )}
          {canRetract && (
            <>
              <div className="menu-separator" role="separator" />
              <button
                type="button"
                role="menuitem"
                className="menu-item danger"
                onClick={() => run(onRetract)}
              >
                <Trash2 size={ICON.xs} aria-hidden="true" />
                撤回消息
              </button>
            </>
          )}
        </div>
      )}
    </div>
  );
}
