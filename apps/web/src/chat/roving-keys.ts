import type { KeyboardEvent } from "react";

/**
 * Arrow-key navigation for menus/lists: ArrowUp/ArrowDown (and optionally
 * ArrowLeft/ArrowRight) move focus between `selector` matches inside the
 * container, Home/End jump to the ends. Returns true when it handled the key.
 */
export function handleRovingKeys(
  event: KeyboardEvent<HTMLElement>,
  selector: string,
  { horizontal = false, wrap = true }: { horizontal?: boolean; wrap?: boolean } = {},
): boolean {
  const items = Array.from(event.currentTarget.querySelectorAll<HTMLElement>(selector));
  if (!items.length) return false;
  const index = items.indexOf(document.activeElement as HTMLElement);
  const last = items.length - 1;
  const step = (delta: number) =>
    index < 0
      ? delta > 0 ? 0 : last
      : wrap
        ? (index + delta + items.length) % items.length
        : Math.min(last, Math.max(0, index + delta));
  let next: number;
  switch (event.key) {
    case "ArrowDown": next = step(1); break;
    case "ArrowUp": next = step(-1); break;
    case "ArrowRight": if (!horizontal) return false; next = step(1); break;
    case "ArrowLeft": if (!horizontal) return false; next = step(-1); break;
    case "Home": next = 0; break;
    case "End": next = last; break;
    default: return false;
  }
  event.preventDefault();
  items[next]?.focus({ preventScroll: false });
  return true;
}
