import { useEffect, useRef } from "react";
import type { MouseEvent, PointerEvent } from "react";

export const LONG_PRESS_MS = 500;
const MOVE_TOLERANCE_PX = 10;

/**
 * Touch/pen long-press detection. Mouse input is ignored (desktop keeps
 * hover/click affordances). After a long press fires, the follow-up click and
 * native context menu are swallowed so the press does not also activate.
 */
export function useLongPress<T extends Element>(
  onLongPress: (target: T, event: PointerEvent<T>) => void,
  options: { enabled?: boolean; ignore?: (target: Element) => boolean } = {},
) {
  const { enabled = true, ignore } = options;
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const origin = useRef<{ x: number; y: number } | null>(null);
  const fired = useRef(false);
  const firedAt = useRef(0);
  const callback = useRef(onLongPress);
  useEffect(() => {
    callback.current = onLongPress;
  });
  useEffect(() => () => clearTimeout(timer.current), []);

  const cancel = () => {
    clearTimeout(timer.current);
    timer.current = undefined;
    origin.current = null;
  };

  return {
    onPointerDown(event: PointerEvent<T>) {
      fired.current = false;
      if (!enabled || event.pointerType === "mouse" || !event.isPrimary) return;
      if (ignore && event.target instanceof Element && ignore(event.target)) return;
      cancel();
      origin.current = { x: event.clientX, y: event.clientY };
      const target = event.currentTarget;
      event.persist?.();
      timer.current = setTimeout(() => {
        timer.current = undefined;
        origin.current = null;
        fired.current = true;
        firedAt.current = Date.now();
        callback.current(target, event);
      }, LONG_PRESS_MS);
    },
    onPointerMove(event: PointerEvent<T>) {
      const start = origin.current;
      if (!start) return;
      if (
        Math.abs(event.clientX - start.x) > MOVE_TOLERANCE_PX ||
        Math.abs(event.clientY - start.y) > MOVE_TOLERANCE_PX
      )
        cancel();
    },
    onPointerUp: cancel,
    onPointerCancel: cancel,
    onPointerLeave: cancel,
    onContextMenu(event: MouseEvent<T>) {
      if (fired.current || timer.current) event.preventDefault();
    },
    onClickCapture(event: MouseEvent<T>) {
      if (!fired.current) return;
      fired.current = false;
      // Only the click synthesized by lifting the pressing finger is
      // swallowed — never later clicks, nor clicks inside portaled UI (the
      // sheet/popover the long-press opened) that bubble through React.
      if (
        Date.now() - firedAt.current > 1000 ||
        !(event.target instanceof Node) ||
        !event.currentTarget.contains(event.target)
      )
        return;
      event.preventDefault();
      event.stopPropagation();
    },
  };
}
