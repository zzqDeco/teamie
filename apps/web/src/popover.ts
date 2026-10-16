import { useEffect, useId, useRef, useState } from "react";
import type { FocusEvent } from "react";
import { useLocation } from "react-router-dom";

export function usePopover<T extends HTMLElement = HTMLElement>(active: boolean) {
  const [open, setOpen] = useState(false);
  const panel = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const popover = useRef<T>(null);
  const id = useId();
  const location = useLocation();
  const expanded = active && open;

  useEffect(() => setOpen(false), [location.pathname]);
  useEffect(() => {
    if (!active) setOpen(false);
  }, [active]);
  useEffect(() => {
    if (!expanded) return;
    popover.current
      ?.querySelector<HTMLElement>("a[href], button:not(:disabled)")
      ?.focus();
    function dismiss(event: PointerEvent) {
      if (
        event.target instanceof Node &&
        !panel.current?.contains(event.target)
      ) {
        setOpen(false);
        trigger.current?.focus({ preventScroll: true });
      }
    }
    function escape(event: KeyboardEvent) {
      if (event.key !== "Escape") return;
      event.preventDefault();
      event.stopPropagation();
      setOpen(false);
      trigger.current?.focus({ preventScroll: true });
    }
    document.addEventListener("pointerdown", dismiss);
    document.addEventListener("keydown", escape, true);
    return () => {
      document.removeEventListener("pointerdown", dismiss);
      document.removeEventListener("keydown", escape, true);
    };
  }, [expanded]);

  return {
    expanded,
    setOpen,
    panel,
    trigger,
    popover,
    id,
    onBlur(event: FocusEvent<HTMLElement>) {
      if (!event.currentTarget.contains(event.relatedTarget)) setOpen(false);
    },
  };
}
