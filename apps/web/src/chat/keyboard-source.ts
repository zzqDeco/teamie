import type { KeyboardEvent } from "react";

/** On-screen keyboards shrink the visual viewport well below this ratio. */
const OSK_VIEWPORT_RATIO = 0.85;
const HARDWARE_ONLY_KEYS = new Set([
  "Tab", "Escape", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight",
  "Meta", "Control", "Alt", "CapsLock", "Home", "End", "PageUp", "PageDown",
]);
const HARDWARE_EVIDENCE_MS = 60_000;
let hardwareSeenAt = 0;
let tracking = false;

function trackHardwareKeys() {
  if (tracking || typeof document === "undefined") return;
  tracking = true;
  document.addEventListener(
    "keydown",
    (event) => {
      if (HARDWARE_ONLY_KEYS.has(event.key)) hardwareSeenAt = Date.now();
    },
    true,
  );
}

type KeyLike = Pick<
  KeyboardEvent<HTMLElement>,
  "key" | "shiftKey" | "metaKey" | "ctrlKey" | "altKey" | "keyCode"
>;

/**
 * Whether the last keydown came from a hardware keyboard. With a visual
 * viewport the decisive signal is that no on-screen keyboard is covering the
 * page; otherwise we fall back to recent hardware-only keys (Tab, arrows…).
 */
export function isHardwareKeydown(event: KeyLike): boolean {
  trackHardwareKeys();
  if (event.keyCode === 229 || event.key === "Unidentified" || event.key === "Process") return false;
  const viewport = typeof window !== "undefined" ? window.visualViewport : null;
  if (viewport && window.innerHeight > 0) {
    return viewport.height * (viewport.scale || 1) >= window.innerHeight * OSK_VIEWPORT_RATIO;
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

/** Test hook: forget hardware-key evidence. */
export function resetKeyboardSourceForTests() {
  hardwareSeenAt = 0;
}
