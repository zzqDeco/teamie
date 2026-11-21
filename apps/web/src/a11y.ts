type InertRecord = { count: number; hadInert: boolean };

const inertRefs = new WeakMap<HTMLElement, InertRecord>();

const IGNORED_TAGS = new Set(["SCRIPT", "STYLE", "LINK", "TEMPLATE"]);

/** Returns "auto" when the user requests reduced motion, otherwise "smooth". */
export function preferredScrollBehavior(): ScrollBehavior {
  return window.matchMedia?.("(prefers-reduced-motion: reduce)")?.matches
    ? "auto"
    : "smooth";
}

/**
 * Applies ref-counted `inert` to every sibling along the ancestor chain of
 * `container` up to `document.body`, isolating modal dialogs from background
 * DOM and screen readers without portaling (preserving inline layout/tests
 * and supporting nested modals cleanly).
 */
export function applyInertOutside(container: HTMLElement | null): () => void {
  if (!container || typeof document === "undefined") return () => {};
  const touched: HTMLElement[] = [];
  let current: HTMLElement | null = container;
  while (current && current !== document.body && current.parentElement) {
    const parent: HTMLElement = current.parentElement;
    for (const child of Array.from(parent.children)) {
      if (!(child instanceof HTMLElement) || child === current) continue;
      if (IGNORED_TAGS.has(child.tagName)) continue;
      if (child.classList.contains("toast-region")) continue;
      const existing = inertRefs.get(child);
      if (existing) {
        existing.count += 1;
      } else {
        const hadInert = Boolean(child.inert) || child.hasAttribute("inert");
        inertRefs.set(child, { count: 1, hadInert });
        if (!hadInert) {
          child.inert = true;
          child.setAttribute("inert", "");
        }
      }
      touched.push(child);
    }
    current = parent;
  }

  let released = false;
  return () => {
    if (released) return;
    released = true;
    for (const child of touched) {
      const record = inertRefs.get(child);
      if (!record) continue;
      record.count -= 1;
      if (record.count <= 0) {
        inertRefs.delete(child);
        if (!record.hadInert) {
          child.inert = false;
          child.removeAttribute("inert");
        }
      }
    }
  };
}
