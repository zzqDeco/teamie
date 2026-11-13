import type { KeyboardEvent } from "react";

/** On-screen keyboards shrink the visual or layout viewport well below this ratio. */
const OSK_VIEWPORT_RATIO = 0.85;
const HARDWARE_ONLY_KEYS = new Set([
  "Tab", "Escape", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight",
  "Meta", "Control", "Alt", "CapsLock", "Home", "End", "PageUp", "PageDown",
]);
const HARDWARE_EVIDENCE_MS = 60_000;
type HeightBaseline = { inner: number; outer: number };

let hardwareSeenAt = 0;
let trackedWindow: Window | null = null;
// Peak observed layout height per layout width so Android Chrome's
// `interactive-widget=resizes-content` (which shrinks both innerHeight and
// visualViewport.height while outerHeight stays full-window) still exposes
// soft-keyboard opening, while real outer-window resizes rebase cleanly.
const peakHeightByWidth = new Map<number, HeightBaseline>();

function recordViewportHeight(): number {
  if (typeof window === "undefined" || !(window.innerHeight > 0)) return 0;
  const widthKey = Math.round(window.innerWidth || 0);
  const inner = window.innerHeight;
  const outer = window.outerHeight > 0 ? window.outerHeight : 0;
  const prev = peakHeightByWidth.get(widthKey);
  if (!prev) {
    peakHeightByWidth.set(widthKey, { inner, outer });
    return inner;
  }
  let rebasedInner = prev.inner;
  if (prev.outer > 0 && outer > 0 && outer < prev.outer - 2) {
    rebasedInner = inner;
  }
  const nextInner = Math.max(rebasedInner, inner);
  const nextOuter = outer > 0 ? outer : prev.outer;
  if (nextInner !== prev.inner || nextOuter !== prev.outer) {
    peakHeightByWidth.set(widthKey, { inner: nextInner, outer: nextOuter });
  }
  return nextInner;
}

function rebaseViewportOnHardwareKey() {
  if (typeof window === "undefined" || !(window.innerHeight > 0)) return;
  const viewport = window.visualViewport;
  if (
    viewport &&
    viewport.height * (viewport.scale || 1) < window.innerHeight * OSK_VIEWPORT_RATIO
  ) {
    return;
  }
  const widthKey = Math.round(window.innerWidth || 0);
  const outer = window.outerHeight > 0 ? window.outerHeight : 0;
  peakHeightByWidth.set(widthKey, { inner: window.innerHeight, outer });
}

function trackHardwareKeys() {
  if (typeof window === "undefined" || typeof document === "undefined") return;
  if (trackedWindow === window) return;
  trackedWindow = window;
  recordViewportHeight();
  document.addEventListener(
    "keydown",
    (event) => {
      if (HARDWARE_ONLY_KEYS.has(event.key)) {
        hardwareSeenAt = Date.now();
        rebaseViewportOnHardwareKey();
      }
    },
    true,
  );
  window.addEventListener("resize", recordViewportHeight, { passive: true });
  document.addEventListener("focusin", recordViewportHeight, true);
}

trackHardwareKeys();

type KeyLike = Pick<
  KeyboardEvent<HTMLElement>,
  "key" | "shiftKey" | "metaKey" | "ctrlKey" | "altKey" | "keyCode"
>;

function hasOpenVirtualKeyboard(): boolean {
  if (typeof navigator !== "undefined" && "virtualKeyboard" in navigator) {
    const vk = (navigator as Navigator & { virtualKeyboard?: { boundingRect?: { height?: number } } }).virtualKeyboard;
    if ((vk?.boundingRect?.height ?? 0) > 0) return true;
  }
  if (typeof window === "undefined" || !(window.innerHeight > 0)) return false;
  const peakHeight = recordViewportHeight();
  if (peakHeight > 0 && window.innerHeight < peakHeight * OSK_VIEWPORT_RATIO) {
    return true;
  }
  const viewport = window.visualViewport;
  if (viewport) {
    const referenceHeight = Math.max(peakHeight, window.innerHeight);
    if (viewport.height * (viewport.scale || 1) < referenceHeight * OSK_VIEWPORT_RATIO) {
      return true;
    }
  }
  return false;
}

/**
 * Whether the last keydown came from a hardware keyboard. With a visual
 * viewport the decisive signal is that no on-screen keyboard is covering or
 * shrinking the page; otherwise we fall back to recent hardware-only keys.
 */
export function isHardwareKeydown(event: KeyLike): boolean {
  trackHardwareKeys();
  if (event.keyCode === 229 || event.key === "Unidentified" || event.key === "Process") return false;
  if (hasOpenVirtualKeyboard()) return false;
  const viewport = typeof window !== "undefined" ? window.visualViewport : null;
  if (viewport && window.innerHeight > 0) {
    return true;
  }
  return Date.now() - hardwareSeenAt < HARDWARE_EVIDENCE_MS;
}

/**
 * Enter behaviour for message inputs: Cmd/Ctrl+Enter always sends, Shift+Enter
 * is a newline; plain Enter sends on fine pointers and on touch devices only
 * when a hardware keyboard is in use (an iPad keyboard sends, the on-screen
 * keyboard inserts a newline).
 */
export function prefersEnterToSend(event: KeyLike): boolean {
  if (event.key !== "Enter") return false;
  if (event.metaKey || event.ctrlKey) return true;
  if (event.shiftKey || event.altKey) return false;
  const coarse =
    typeof window !== "undefined" && !!window.matchMedia?.("(pointer: coarse)").matches;
  return !coarse || isHardwareKeydown(event);
}

/** Test hook: forget hardware-key and viewport-peak evidence. */
export function resetKeyboardSourceForTests() {
  hardwareSeenAt = 0;
  peakHeightByWidth.clear();
}
