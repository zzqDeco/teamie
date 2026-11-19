import { useEffect, useRef } from "react";
import type { MouseEvent, PointerEvent } from "react";

export const LONG_PRESS_MS = 500;
const MOVE_TOLERANCE_PX = 10;
const GHOST_CLICK_WINDOW_MS = 450;
const AWAITING_LIFT_TIMEOUT_MS = 30_000;

type GhostGuardState = {
  phase: "awaiting-lift" | "post-lift";
  liftedAt: number;
  cleanup: () => void;
};

let activeGhostGuard: GhostGuardState | null = null;

/**
 * Arms a window-capture guard once a touch/pen long-press fires so the
 * compatibility mouse/click burst synthesized when the finger lifts cannot
 * dismiss the newly opened modal backdrop (mousedown) or activate an item
 * inside a portaled action sheet (click).
 *
 * A fresh pointerdown/touchstart immediately disarms the post-lift window so a
 * deliberate tap on a sheet action works without delay.
 */
function armGhostClickGuard(pointerId: number | undefined) {
  activeGhostGuard?.cleanup();

  let fallbackTimer: ReturnType<typeof setTimeout> | undefined;

  const disarm = () => {
    clearTimeout(fallbackTimer);
    fallbackTimer = undefined;
    window.removeEventListener("pointerup", onLift, true);
    window.removeEventListener("pointercancel", onLift, true);
    window.removeEventListener("touchend", onLift, true);
    window.removeEventListener("touchcancel", onLift, true);
    window.removeEventListener("pointerdown", onNewGesture, true);
    window.removeEventListener("touchstart", onNewGesture, true);
    window.removeEventListener("contextmenu", onContextMenu, true);
    window.removeEventListener("mousedown", onGhostMouse, true);
    window.removeEventListener("mouseup", onGhostMouse, true);
    window.removeEventListener("click", onGhostClick, true);
    if (activeGhostGuard === state) activeGhostGuard = null;
  };

  const state: GhostGuardState = {
    phase: "awaiting-lift",
    liftedAt: 0,
    cleanup: disarm,
  };

  const swallow = (event: Event) => {
    event.preventDefault();
    event.stopPropagation();
    event.stopImmediatePropagation();
  };

  const onLift = (event: Event) => {
    if (
      typeof pointerId === "number" &&
      "pointerId" in event &&
      typeof event.pointerId === "number" &&
      event.pointerId !== pointerId
    ) {
      return;
    }
    if (
      "touches" in event &&
      event.touches &&
      (event.touches as TouchList).length > 0
    ) {
      return;
    }
    if (state.phase === "awaiting-lift") {
      state.phase = "post-lift";
    }
    state.liftedAt = Date.now();
    clearTimeout(fallbackTimer);
    fallbackTimer = setTimeout(disarm, GHOST_CLICK_WINDOW_MS);
  };

  const onNewGesture = () => {
    if (state.phase === "post-lift") disarm();
  };

  const onContextMenu = (event: Event) => {
    if (
      state.phase === "awaiting-lift" ||
      Date.now() - state.liftedAt <= GHOST_CLICK_WINDOW_MS
    ) {
      swallow(event);
    } else {
      disarm();
    }
  };

  const onGhostMouse = (event: Event) => {
    if (state.phase !== "post-lift") return;
    if (Date.now() - state.liftedAt > GHOST_CLICK_WINDOW_MS) {
      disarm();
      return;
    }
    swallow(event);
  };

  const onGhostClick = (event: Event) => {
    if (state.phase !== "post-lift") return;
    if (Date.now() - state.liftedAt > GHOST_CLICK_WINDOW_MS) {
      disarm();
      return;
    }
    swallow(event);
    // One touch lift synthesizes at most one click; disarm immediately so a
    // subsequent click (or synthetic click in tests) is never swallowed.
    disarm();
  };

  window.addEventListener("pointerup", onLift, true);
  window.addEventListener("pointercancel", onLift, true);
  window.addEventListener("touchend", onLift, true);
  window.addEventListener("touchcancel", onLift, true);
  window.addEventListener("pointerdown", onNewGesture, true);
  window.addEventListener("touchstart", onNewGesture, true);
  window.addEventListener("contextmenu", onContextMenu, true);
  window.addEventListener("mousedown", onGhostMouse, true);
  window.addEventListener("mouseup", onGhostMouse, true);
  window.addEventListener("click", onGhostClick, true);

  fallbackTimer = setTimeout(disarm, AWAITING_LIFT_TIMEOUT_MS);
  activeGhostGuard = state;
  return state;
}

/**
 * Touch/pen long-press detection. Mouse input is ignored (desktop keeps
 * hover/click affordances). After a long press fires, the follow-up lift
 * mouse/click burst and native context menu are swallowed globally (including
 * portaled sheets) so lifting the finger never activates a newly opened item.
 */
export function useLongPress<T extends Element>(
  onLongPress: (target: T, event: PointerEvent<T>) => void,
  options: { enabled?: boolean; ignore?: (target: Element) => boolean } = {},
) {
  const { enabled = true, ignore } = options;
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const origin = useRef<{ x: number; y: number } | null>(null);
  const fired = useRef(false);
  const ownGuard = useRef<GhostGuardState | null>(null);
  const callback = useRef(onLongPress);
  useEffect(() => {
    callback.current = onLongPress;
  });
  useEffect(
    () => () => {
      clearTimeout(timer.current);
      if (ownGuard.current && activeGhostGuard === ownGuard.current) {
        ownGuard.current.cleanup();
      }
      ownGuard.current = null;
    },
    [],
  );

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
      const pointerId = event.pointerId;
      event.persist?.();
      timer.current = setTimeout(() => {
        timer.current = undefined;
        origin.current = null;
        fired.current = true;
        ownGuard.current = armGhostClickGuard(pointerId);
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
  };
}
