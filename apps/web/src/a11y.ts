type InertRecord = { count: number; hadInert: boolean };

const inertRefs = new WeakMap<HTMLElement, InertRecord>();

const IGNORED_TAGS = new Set(["SCRIPT", "STYLE", "LINK", "TEMPLATE"]);
const EXEMPT_LIVE_SELECTOR = ".toaster, .toast-region, .connection-live-region";

/** Returns "auto" when the user requests reduced motion, otherwise "smooth". */
export function preferredScrollBehavior(): ScrollBehavior {
  return window.matchMedia?.("(prefers-reduced-motion: reduce)")?.matches
    ? "auto"
    : "smooth";
}

function addInertRef(element: HTMLElement, touched: HTMLElement[]) {
  const existing = inertRefs.get(element);
  if (existing) {
    existing.count += 1;
  } else {
    const hadInert = Boolean(element.inert) || element.hasAttribute("inert");
    inertRefs.set(element, { count: 1, hadInert });
    if (!hadInert) {
      element.inert = true;
      element.setAttribute("inert", "");
    }
  }
  touched.push(element);
}

function markInertExcludingExempt(element: HTMLElement, touched: HTMLElement[]) {
  if (IGNORED_TAGS.has(element.tagName)) return;
  if (element.matches(EXEMPT_LIVE_SELECTOR)) return;
  if (element.querySelector(EXEMPT_LIVE_SELECTOR)) {
    for (const child of Array.from(element.children)) {
      if (child instanceof HTMLElement) {
        markInertExcludingExempt(child, touched);
      }
    }
    return;
  }
  addInertRef(element, touched);
}

/**
 * Applies ref-counted `inert` to every sibling along the ancestor chain of
 * `container` up to `document.body`, isolating modal dialogs from background
 * DOM and screen readers without portaling (preserving inline layout/tests
 * and supporting nested/portaled modals while keeping app-level live regions
 * such as `.toaster` and `.connection-live-region` active).
 */
export function applyInertOutside(container: HTMLElement | null): () => void {
  if (!container || typeof document === "undefined") return () => {};
  const touched: HTMLElement[] = [];
  let current: HTMLElement | null = container;
  while (current && current !== document.body && current.parentElement) {
    const parent: HTMLElement = current.parentElement;
    for (const child of Array.from(parent.children)) {
      if (!(child instanceof HTMLElement) || child === current) continue;
      markInertExcludingExempt(child, touched);
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
