import { useId, useLayoutEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { ChevronDown, ChevronUp } from "lucide-react";
import { ICON } from "../icons";

/** Bodies taller than this are clamped to MESSAGE_COLLAPSED_HEIGHT. */
export const MESSAGE_COLLAPSE_THRESHOLD = 720;
export const MESSAGE_COLLAPSED_HEIGHT = 600;

// One window listener for every row (no per-row ResizeObserver): width
// changes are the only thing that re-flows an already rendered body.
const resizeListeners = new Set<() => void>();
let resizeFrame = 0;
function onWindowResize() {
  if (resizeFrame) return;
  resizeFrame = requestAnimationFrame(() => {
    resizeFrame = 0;
    for (const listener of resizeListeners) listener();
  });
}
function subscribeWindowResize(listener: () => void) {
  if (!resizeListeners.size) window.addEventListener("resize", onWindowResize);
  resizeListeners.add(listener);
  return () => {
    resizeListeners.delete(listener);
    if (!resizeListeners.size) window.removeEventListener("resize", onWindowResize);
  };
}

/**
 * Message text container that collapses very tall bodies behind 「展开全文」.
 * The rule is purely visual and applies to every sender (human or Agent).
 */
export function CollapsibleMessageBody({
  contentKey,
  children,
}: {
  /** Changes when the rendered content changes (body/edit), to re-measure. */
  contentKey: string;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [overflowing, setOverflowing] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const id = useId();
  useLayoutEffect(() => {
    const measure = () => {
      const element = ref.current;
      if (element) setOverflowing(element.scrollHeight > MESSAGE_COLLAPSE_THRESHOLD);
    };
    measure();
    return subscribeWindowResize(measure);
  }, [contentKey]);
  const collapsed = overflowing && !expanded;
  return (
    <>
      <div
        ref={ref}
        id={id}
        className={`message-text message-body ${collapsed ? "collapsed" : ""}`}
      >
        {children}
      </div>
      {overflowing && (
        <button
          type="button"
          className="message-expand-toggle"
          aria-expanded={expanded}
          aria-controls={id}
          onClick={() => {
            setExpanded((value) => !value);
            if (expanded) {
              const article = ref.current?.closest("article");
              const top = article?.getBoundingClientRect().top ?? 0;
              const scroller = article?.closest(".message-scroll");
              if (article && scroller && top < scroller.getBoundingClientRect().top)
                article.scrollIntoView?.({ block: "start" });
            }
          }}
        >
          {expanded ? <ChevronUp size={ICON.xs} /> : <ChevronDown size={ICON.xs} />}
          {expanded ? "收起" : "展开全文"}
        </button>
      )}
    </>
  );
}
