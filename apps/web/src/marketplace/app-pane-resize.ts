import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type {
  CSSProperties,
  KeyboardEvent as ReactKeyboardEvent,
  MouseEvent as ReactMouseEvent,
  PointerEvent as ReactPointerEvent,
} from "react";
import { chatStore } from "../store";

export const DEFAULT_APP_PANE_WIDTH = 400;
export const MIN_APP_PANE_WIDTH = 280;
export const MAX_APP_PANE_WIDTH_CEILING = 960;
const RESERVED_SHELL_WIDTH = 800;
const KEYBOARD_RESIZE_STEP = 24;
const KEYBOARD_RESIZE_FAST_STEP = 48;
const DRAGGING_BODY_CLASS = "mp-pane-resizing";

const appPaneStorageKey = (userId: string) => `teamie.appPaneWidth.${userId}`;

export function computeMaxAppPaneWidth(
  viewportWidth = typeof window !== "undefined" ? window.innerWidth : 1440,
): number {
  const safeViewport =
    Number.isFinite(viewportWidth) && viewportWidth > 0 ? viewportWidth : 1440;
  return Math.max(
    MIN_APP_PANE_WIDTH,
    Math.min(
      MAX_APP_PANE_WIDTH_CEILING,
      Math.floor(safeViewport - RESERVED_SHELL_WIDTH),
    ),
  );
}

export function clampAppPaneWidth(
  width: number,
  maxWidth = MAX_APP_PANE_WIDTH_CEILING,
): number {
  const safeMax = Math.max(
    MIN_APP_PANE_WIDTH,
    Number.isFinite(maxWidth)
      ? Math.floor(maxWidth)
      : MAX_APP_PANE_WIDTH_CEILING,
  );
  return Math.min(safeMax, Math.max(MIN_APP_PANE_WIDTH, Math.round(width)));
}

export function readPersistedAppPaneWidth(
  userId?: string,
  maxWidth = MAX_APP_PANE_WIDTH_CEILING,
): number {
  if (!userId) {
    return clampAppPaneWidth(DEFAULT_APP_PANE_WIDTH, maxWidth);
  }
  try {
    const raw = localStorage.getItem(appPaneStorageKey(userId));
    if (!raw) {
      return clampAppPaneWidth(DEFAULT_APP_PANE_WIDTH, maxWidth);
    }
    const trimmed = raw.trim();
    if (!trimmed) {
      return clampAppPaneWidth(DEFAULT_APP_PANE_WIDTH, maxWidth);
    }
    const parsed = Number(trimmed);
    if (!Number.isFinite(parsed)) {
      return clampAppPaneWidth(DEFAULT_APP_PANE_WIDTH, maxWidth);
    }
    return clampAppPaneWidth(parsed, maxWidth);
  } catch {
    return clampAppPaneWidth(DEFAULT_APP_PANE_WIDTH, maxWidth);
  }
}

export function writePersistedAppPaneWidth(userId: string, width: number) {
  if (!userId || !Number.isFinite(width)) return;
  try {
    localStorage.setItem(
      appPaneStorageKey(userId),
      String(clampAppPaneWidth(width, MAX_APP_PANE_WIDTH_CEILING)),
    );
  } catch {
    /* Ignore storage access errors. */
  }
}

export function clearPersistedAppPaneWidth(userId?: string) {
  if (!userId) return;
  try {
    localStorage.removeItem(appPaneStorageKey(userId));
  } catch {
    /* Ignore storage access errors. */
  }
}

let trackedAppPaneUserId = chatStore.getSnapshot().userId;
chatStore.subscribe(() => {
  const snap = chatStore.getSnapshot();
  if (snap.userId !== trackedAppPaneUserId) {
    if (trackedAppPaneUserId) {
      clearPersistedAppPaneWidth(trackedAppPaneUserId);
    }
    trackedAppPaneUserId = snap.userId;
  }
});

type ActiveDrag = {
  pointerId: number | null;
  startX: number;
  startWidth: number;
  lastWidth: number;
  handle: HTMLDivElement | null;
};

export function useConversationAppPaneResize({
  userId,
  overlayMembers,
}: {
  userId: string;
  overlayMembers: boolean;
}) {
  const [viewportWidth, setViewportWidth] = useState(() =>
    typeof window !== "undefined" ? window.innerWidth : 1440,
  );
  const [widthState, setWidthState] = useState(() => ({
    userId,
    width: readPersistedAppPaneWidth(userId),
  }));
  const [isDragging, setIsDragging] = useState(false);

  const userIdRef = useRef(userId);
  const prevUserIdRef = useRef(userId);
  const dragRef = useRef<ActiveDrag | null>(null);
  const dragListenersCleanupRef = useRef<(() => void) | null>(null);

  useEffect(() => {
    userIdRef.current = userId;
    if (prevUserIdRef.current && prevUserIdRef.current !== userId) {
      clearPersistedAppPaneWidth(prevUserIdRef.current);
    }
    prevUserIdRef.current = userId;
    setWidthState((prev) =>
      prev.userId === userId
        ? prev
        : { userId, width: readPersistedAppPaneWidth(userId) },
    );
  }, [userId]);

  useEffect(() => {
    const onWindowResize = () => setViewportWidth(window.innerWidth);
    onWindowResize();
    window.addEventListener("resize", onWindowResize);
    return () => window.removeEventListener("resize", onWindowResize);
  }, []);

  const maxWidth = useMemo(
    () => computeMaxAppPaneWidth(viewportWidth),
    [viewportWidth],
  );
  const preferredWidth =
    widthState.userId === userId
      ? widthState.width
      : readPersistedAppPaneWidth(userId);
  const effectiveWidth = clampAppPaneWidth(preferredWidth, maxWidth);

  const commitWidth = useCallback((nextWidth: number) => {
    const activeUserId = userIdRef.current;
    const normalized = clampAppPaneWidth(nextWidth, MAX_APP_PANE_WIDTH_CEILING);
    setWidthState({ userId: activeUserId, width: normalized });
    writePersistedAppPaneWidth(activeUserId, normalized);
  }, []);

  const applyDragMove = useCallback(
    (clientX: number, pointerId?: number) => {
      const drag = dragRef.current;
      if (!drag || !Number.isFinite(clientX)) return;
      if (
        drag.pointerId !== null &&
        typeof pointerId === "number" &&
        pointerId !== drag.pointerId
      ) {
        return;
      }
      const currentMax = computeMaxAppPaneWidth(window.innerWidth);
      const nextWidth = clampAppPaneWidth(
        drag.startWidth + (drag.startX - clientX),
        currentMax,
      );
      if (nextWidth === drag.lastWidth) return;
      drag.lastWidth = nextWidth;
      commitWidth(nextWidth);
    },
    [commitWidth],
  );

  const stopDrag = useCallback((pointerId?: number, checkPointer = false) => {
    const drag = dragRef.current;
    if (!drag) {
      dragListenersCleanupRef.current?.();
      dragListenersCleanupRef.current = null;
      document.body.classList.remove(DRAGGING_BODY_CLASS);
      return;
    }
    if (
      checkPointer &&
      drag.pointerId !== null &&
      typeof pointerId === "number" &&
      pointerId !== drag.pointerId
    ) {
      return;
    }
    dragRef.current = null;
    dragListenersCleanupRef.current?.();
    dragListenersCleanupRef.current = null;
    if (
      drag.pointerId !== null &&
      drag.handle &&
      typeof drag.handle.releasePointerCapture === "function"
    ) {
      try {
        drag.handle.releasePointerCapture(drag.pointerId);
      } catch {
        /* Ignore synthetic or already-released pointer capture. */
      }
    }
    document.body.classList.remove(DRAGGING_BODY_CLASS);
    setIsDragging(false);
    writePersistedAppPaneWidth(userIdRef.current, drag.lastWidth);
  }, []);

  useEffect(() => {
    if (overlayMembers && dragRef.current) {
      stopDrag();
    }
  }, [overlayMembers, stopDrag]);

  useEffect(
    () => () => {
      dragListenersCleanupRef.current?.();
      dragListenersCleanupRef.current = null;
      dragRef.current = null;
      document.body.classList.remove(DRAGGING_BODY_CLASS);
    },
    [],
  );

  const onPointerDown = useCallback(
    (event: ReactPointerEvent<HTMLDivElement>) => {
      if (overlayMembers || event.button !== 0) return;
      event.preventDefault();
      const handle = event.currentTarget;
      const pointerId =
        typeof event.pointerId === "number" ? event.pointerId : null;
      if (pointerId !== null && typeof handle.setPointerCapture === "function") {
        try {
          handle.setPointerCapture(pointerId);
        } catch {
          /* Ignore synthetic events without an active browser pointer. */
        }
      }
      dragListenersCleanupRef.current?.();
      dragRef.current = {
        pointerId,
        startX: event.clientX,
        startWidth: effectiveWidth,
        lastWidth: effectiveWidth,
        handle,
      };
      document.body.classList.add(DRAGGING_BODY_CLASS);
      const onWindowPointerMove = (moveEvent: PointerEvent) => {
        applyDragMove(moveEvent.clientX, moveEvent.pointerId);
      };
      const onWindowPointerEnd = (endEvent: PointerEvent) => {
        stopDrag(endEvent.pointerId, true);
      };
      const onWindowBlur = () => {
        stopDrag();
      };
      window.addEventListener("pointermove", onWindowPointerMove);
      window.addEventListener("pointerup", onWindowPointerEnd);
      window.addEventListener("pointercancel", onWindowPointerEnd);
      window.addEventListener("blur", onWindowBlur);
      dragListenersCleanupRef.current = () => {
        window.removeEventListener("pointermove", onWindowPointerMove);
        window.removeEventListener("pointerup", onWindowPointerEnd);
        window.removeEventListener("pointercancel", onWindowPointerEnd);
        window.removeEventListener("blur", onWindowBlur);
      };
      setIsDragging(true);
    },
    [applyDragMove, effectiveWidth, overlayMembers, stopDrag],
  );

  const onPointerMove = useCallback(
    (event: ReactPointerEvent<HTMLDivElement>) => {
      applyDragMove(event.clientX, event.pointerId);
    },
    [applyDragMove],
  );

  const onPointerUp = useCallback(
    (event: ReactPointerEvent<HTMLDivElement>) => {
      stopDrag(event.pointerId, true);
    },
    [stopDrag],
  );

  const onPointerCancel = useCallback(
    (event: ReactPointerEvent<HTMLDivElement>) => {
      stopDrag(event.pointerId, true);
    },
    [stopDrag],
  );

  const onLostPointerCapture = useCallback(
    (event: ReactPointerEvent<HTMLDivElement>) => {
      stopDrag(event.pointerId, true);
    },
    [stopDrag],
  );

  const onDoubleClick = useCallback(
    (event: ReactMouseEvent<HTMLDivElement>) => {
      if (overlayMembers) return;
      event.preventDefault();
      commitWidth(DEFAULT_APP_PANE_WIDTH);
    },
    [commitWidth, overlayMembers],
  );

  const onKeyDown = useCallback(
    (event: ReactKeyboardEvent<HTMLDivElement>) => {
      if (overlayMembers || event.altKey || event.ctrlKey || event.metaKey) {
        return;
      }
      const step = event.shiftKey
        ? KEYBOARD_RESIZE_FAST_STEP
        : KEYBOARD_RESIZE_STEP;
      let nextWidth: number | null = null;
      if (event.key === "ArrowLeft") {
        nextWidth = clampAppPaneWidth(effectiveWidth + step, maxWidth);
      } else if (event.key === "ArrowRight") {
        nextWidth = clampAppPaneWidth(effectiveWidth - step, maxWidth);
      } else if (event.key === "Home") {
        nextWidth = MIN_APP_PANE_WIDTH;
      } else if (event.key === "End") {
        nextWidth = maxWidth;
      }
      if (nextWidth !== null) {
        event.preventDefault();
        commitWidth(nextWidth);
      }
    },
    [commitWidth, effectiveWidth, maxWidth, overlayMembers],
  );

  const desktopWidthStyle = !overlayMembers
    ? ({ "--mp-pane-width": `${effectiveWidth}px` } as CSSProperties)
    : undefined;

  return {
    effectiveWidth,
    maxWidth,
    isDragging,
    desktopWidthStyle,
    handleProps: {
      onPointerDown,
      onPointerMove,
      onPointerUp,
      onPointerCancel,
      onLostPointerCapture,
      onDoubleClick,
      onKeyDown,
    },
  };
}
