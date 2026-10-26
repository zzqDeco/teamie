import { useCallback, useEffect, useRef, useSyncExternalStore } from "react";
import type { ReactNode } from "react";
import { CircleAlert, CircleCheck, Info, X } from "lucide-react";
import { ICON } from "./icons";

export type ToastKind = "success" | "error" | "info";
export type ToastAction = { label: string; onClick: () => void };
export type ToastOptions = {
  kind?: ToastKind;
  /** Milliseconds before auto-dismissal. Defaults depend on kind. */
  duration?: number;
  action?: ToastAction;
};
export type ToastItem = {
  id: number;
  kind: ToastKind;
  message: ReactNode;
  duration: number;
  action?: ToastAction;
};

/** Visible toast limit; the oldest toast is dropped when a new one arrives. */
export const TOAST_LIMIT = 3;
export const TOAST_DURATION: Record<ToastKind, number> = {
  success: 4000,
  info: 4000,
  error: 8000,
};
const ACTION_MIN_DURATION = 6000;

let items: ToastItem[] = [];
let nextId = 1;
const listeners = new Set<() => void>();
function emit() {
  for (const listener of listeners) listener();
}
function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}
function snapshot() {
  return items;
}
function show(message: ReactNode, options: ToastOptions = {}) {
  const kind = options.kind || "info";
  const duration =
    options.duration ??
    Math.max(
      TOAST_DURATION[kind],
      options.action ? ACTION_MIN_DURATION : 0,
    );
  const id = nextId++;
  items = [...items, { id, kind, message, duration, action: options.action }]
    .slice(-TOAST_LIMIT);
  emit();
  return id;
}
function dismiss(id: number) {
  if (!items.some((item) => item.id === id)) return;
  items = items.filter((item) => item.id !== id);
  emit();
}
function clear() {
  if (!items.length) return;
  items = [];
  emit();
}

type ToastApi = {
  (message: ReactNode, options?: ToastOptions): number;
  success: (message: ReactNode, options?: Omit<ToastOptions, "kind">) => number;
  error: (message: ReactNode, options?: Omit<ToastOptions, "kind">) => number;
  info: (message: ReactNode, options?: Omit<ToastOptions, "kind">) => number;
  dismiss: (id: number) => void;
  clear: () => void;
};

/**
 * Global, transient feedback. Safe to call outside React and before the
 * Toaster mounts; persisted content must never rely on a toast alone.
 */
export const toast: ToastApi = Object.assign(
  (message: ReactNode, options?: ToastOptions) => show(message, options),
  {
    success: (message: ReactNode, options?: Omit<ToastOptions, "kind">) =>
      show(message, { ...options, kind: "success" }),
    error: (message: ReactNode, options?: Omit<ToastOptions, "kind">) =>
      show(message, { ...options, kind: "error" }),
    info: (message: ReactNode, options?: Omit<ToastOptions, "kind">) =>
      show(message, { ...options, kind: "info" }),
    dismiss,
    clear,
  },
);

export function useToast() {
  return toast;
}

const icons: Record<ToastKind, typeof Info> = {
  success: CircleCheck,
  error: CircleAlert,
  info: Info,
};

function ToastCard({ item }: { item: ToastItem }) {
  const remaining = useRef(item.duration);
  const startedAt = useRef(0);
  const timer = useRef<number | undefined>(undefined);
  const hovered = useRef(false);
  const focused = useRef(false);
  const pause = useCallback(() => {
    if (timer.current === undefined) return;
    window.clearTimeout(timer.current);
    timer.current = undefined;
    remaining.current = Math.max(
      0,
      remaining.current - (Date.now() - startedAt.current),
    );
  }, []);
  const resume = useCallback(() => {
    if (timer.current !== undefined || hovered.current || focused.current)
      return;
    startedAt.current = Date.now();
    timer.current = window.setTimeout(
      () => dismiss(item.id),
      remaining.current,
    );
  }, [item.id]);
  useEffect(() => {
    resume();
    return () => {
      if (timer.current !== undefined) window.clearTimeout(timer.current);
      timer.current = undefined;
    };
  }, [resume]);
  const Icon = icons[item.kind];
  return (
    <div
      className={`toast ${item.kind}`}
      data-toast-id={item.id}
      onMouseEnter={() => {
        hovered.current = true;
        pause();
      }}
      onMouseLeave={() => {
        hovered.current = false;
        resume();
      }}
      onFocus={() => {
        focused.current = true;
        pause();
      }}
      onBlur={(event) => {
        if (event.currentTarget.contains(event.relatedTarget as Node | null))
          return;
        focused.current = false;
        resume();
      }}
    >
      <Icon className="toast-icon" size={ICON.md} aria-hidden="true" />
      <div className="toast-message">{item.message}</div>
      {item.action && (
        <button
          type="button"
          className="toast-action"
          onClick={() => {
            item.action!.onClick();
            dismiss(item.id);
          }}
        >
          {item.action.label}
        </button>
      )}
      <button
        type="button"
        className="toast-close"
        aria-label="关闭通知"
        title="关闭通知"
        onClick={() => dismiss(item.id)}
      >
        <X size={ICON.sm} aria-hidden="true" />
      </button>
    </div>
  );
}

/**
 * Mount once near the root. The live regions are always present so that
 * assistive technology observes insertions: errors use role=alert, other
 * kinds a polite status region.
 */
export function Toaster() {
  const current = useSyncExternalStore(subscribe, snapshot, snapshot);
  const polite = current.filter((item) => item.kind !== "error");
  const assertive = current.filter((item) => item.kind === "error");
  return (
    <section className="toaster" aria-label="通知">
      <div className="toast-region" role="alert">
        {assertive.map((item) => (
          <ToastCard key={item.id} item={item} />
        ))}
      </div>
      <div className="toast-region" role="status" aria-live="polite">
        {polite.map((item) => (
          <ToastCard key={item.id} item={item} />
        ))}
      </div>
    </section>
  );
}
