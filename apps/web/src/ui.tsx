import {
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import type { ButtonHTMLAttributes, ReactNode, RefObject } from "react";
import { LoaderCircle, X } from "lucide-react";
import { Link } from "react-router-dom";
import { ICON } from "./icons";
export function Logo({ compact = false }: { compact?: boolean }) {
  return (
    <span className="brand">
      <span className="brand-icon" aria-hidden="true">
        <svg viewBox="0 0 40 40">
          <path d="M9 10h23v6h-9v17h-6V16H9z" fill="currentColor" />
          <circle cx="31" cy="30" r="4" fill="var(--logo-accent, #d8efae)" />
        </svg>
      </span>
      {!compact && (
        <span>
          teamie<span className="brand-period">.</span>
        </span>
      )}
    </span>
  );
}
export function Avatar({
  name,
  size = "",
  seed = "",
  className = "",
}: {
  name: string;
  size?: "small" | "large" | "";
  seed?: string;
  className?: string;
}) {
  const colors = ["lilac", "mint", "peach", "blue", "rose"];
  const n = Array.from(seed || name).reduce(
    (sum, c) => sum + c.charCodeAt(0),
    0,
  );
  return (
    <span
      className={`avatar ${size} ${colors[n % colors.length]} ${className}`}
      aria-hidden="true"
    >
      {Array.from(name.trim())[0]?.toUpperCase() || "T"}
    </span>
  );
}
export type ButtonVariant = "primary" | "secondary" | "danger" | "ghost";
export type ButtonSize = "sm" | "md";
const buttonVariantClass: Record<ButtonVariant, string> = {
  primary: "",
  secondary: "secondary",
  danger: "danger",
  ghost: "ghost",
};
export function Button({
  children,
  busy = false,
  variant = "primary",
  size = "md",
  className = "",
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  busy?: boolean;
  variant?: ButtonVariant;
  size?: ButtonSize;
}) {
  const classes = [
    "button",
    buttonVariantClass[variant],
    size === "sm" ? "small-button" : "",
    className,
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <button
      {...props}
      disabled={props.disabled || busy}
      aria-busy={busy || undefined}
      className={classes}
    >
      {busy && (
        <>
          <LoaderCircle className="spin" size={ICON.sm} aria-hidden="true" />
          <span className="sr-only">处理中…</span>
        </>
      )}{" "}
      {children}
    </button>
  );
}
export function IconButton({
  children,
  label,
  className = "",
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { label: string }) {
  return (
    <button
      type="button"
      {...props}
      className={`icon-button ${className}`}
      title={label}
      aria-label={label}
    >
      {children}
    </button>
  );
}
export function Notice({
  children,
  kind = "error",
  id,
}: {
  children: ReactNode;
  kind?: "error" | "success" | "info";
  id?: string;
}) {
  return children ? (
    <div
      id={id}
      className={`notice ${kind}`}
      role={kind === "error" ? "alert" : "status"}
    >
      {children}
    </div>
  ) : null;
}
export function BadgePill({
  children,
  className = "",
  title,
  ariaLabel,
}: {
  children: ReactNode;
  className?: string;
  title?: string;
  ariaLabel?: string;
}) {
  return (
    <span
      className={`badge-pill ${className}`.trim()}
      title={title}
      aria-label={ariaLabel}
    >
      {children}
    </span>
  );
}
export function FormRow({
  children,
  className = "",
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={`gateway-form-row ${className}`.trim()}>{children}</div>
  );
}
export function SwitchField({
  checked,
  onChange,
  disabled = false,
  name,
  title,
  description,
  reason,
  reasonId,
  className = "",
}: {
  checked: boolean;
  onChange: (checked: boolean) => void;
  disabled?: boolean;
  name?: string;
  title: ReactNode;
  description?: ReactNode;
  reason?: ReactNode;
  reasonId?: string;
  className?: string;
}) {
  return (
    <label className={`agent-enabled-switch ${className}`.trim()}>
      <input
        type="checkbox"
        name={name}
        checked={checked}
        disabled={disabled}
        aria-describedby={reason && reasonId ? reasonId : undefined}
        onChange={(e) => onChange(e.target.checked)}
      />
      <span>
        {description || reason ? <b>{title}</b> : title}
        {description && <small>{description}</small>}
        {reason && (
          <small id={reasonId} className="agent-disabled-reason">
            {reason}
          </small>
        )}
      </span>
      <i aria-hidden="true" />
    </label>
  );
}
export function Loading({ label = "正在加载你的空间…" }: { label?: string }) {
  return (
    <div className="loading-screen">
      <Logo />
      <LoaderCircle className="spin" size={ICON.xl} />
      <p>{label}</p>
    </div>
  );
}
const FOCUSABLE_SELECTOR = [
  "button:not(:disabled)",
  'input:not(:disabled):not([type="hidden"])',
  "textarea:not(:disabled)",
  "select:not(:disabled)",
  "a[href]",
  "summary",
  '[tabindex]:not([tabindex="-1"])',
].join(", ");
const FORM_FIELD_SELECTOR =
  'input:not(:disabled):not([type="hidden"]), textarea:not(:disabled), select:not(:disabled)';
type OverlayEntry = { node: HTMLElement; order: number };
const overlayStack: OverlayEntry[] = [];
let overlayOrder = 0;
/**
 * An overlay is top-most unless another open overlay is nested inside it or
 * was opened later outside it. Only the top-most overlay handles Escape/Tab.
 */
function isTopOverlay(entry: OverlayEntry) {
  return overlayStack.every(
    (other) =>
      other === entry ||
      !(
        entry.node.contains(other.node) ||
        (other.order > entry.order && !other.node.contains(entry.node))
      ),
  );
}
function focusFirst(candidates: Iterable<HTMLElement | null | undefined>) {
  for (const candidate of candidates) {
    if (!candidate) continue;
    candidate.focus({ preventScroll: true });
    if (document.activeElement === candidate) return true;
  }
  return false;
}
export function useOverlayFocus(
  ref: RefObject<HTMLElement | null>,
  active: boolean,
  onClose: () => void,
  { preferFormField = false }: { preferFormField?: boolean } = {},
) {
  const onCloseRef = useRef(onClose);
  useEffect(() => {
    onCloseRef.current = onClose;
  }, [onClose]);
  useEffect(() => {
    if (!active) return;
    const previous = document.activeElement as HTMLElement | null;
    const node = ref.current;
    if (!node) return;
    const entry: OverlayEntry = { node, order: ++overlayOrder };
    overlayStack.push(entry);
    const focusables = () =>
      Array.from(node.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)).filter(
        (element) => element.getClientRects().length > 0,
      );
    const autoFocusTarget = node.querySelector<HTMLElement>(
      "[data-autofocus]:not(:disabled)",
    );
    const fields = preferFormField
      ? Array.from(node.querySelectorAll<HTMLElement>(FORM_FIELD_SELECTOR))
      : [];
    if (!focusFirst([autoFocusTarget, ...fields, focusables()[0]]))
      node.focus({ preventScroll: true });
    function handle(event: KeyboardEvent) {
      if (!isTopOverlay(entry)) return;
      if (event.key === "Escape") {
        // Nested popovers/menus claim Escape by preventing default first.
        if (event.defaultPrevented) return;
        event.preventDefault();
        onCloseRef.current();
        return;
      }
      if (event.key !== "Tab") return;
      const elements = focusables();
      const first = elements[0],
        last = elements.at(-1);
      if (!first) {
        event.preventDefault();
        node?.focus();
      } else if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last?.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    }
    document.addEventListener("keydown", handle);
    return () => {
      document.removeEventListener("keydown", handle);
      const index = overlayStack.indexOf(entry);
      if (index >= 0) overlayStack.splice(index, 1);
      if (previous?.isConnected) previous.focus({ preventScroll: true });
    };
  }, [active, preferFormField, ref]);
}
/**
 * Every dismissal path (Escape, backdrop, close button) goes through one
 * request: `busy` or `dismissible={false}` blocks it with a visual cue, and
 * `dirty` asks for an inline discard confirmation instead of closing.
 */
export function Modal({
  title,
  subtitle,
  children,
  onClose,
  className = "",
  dismissible = true,
  busy = false,
  dirty = false,
  discardMessage = "放弃未保存的更改？",
}: {
  title: string;
  subtitle?: string;
  children: ReactNode;
  onClose: () => void;
  className?: string;
  dismissible?: boolean;
  busy?: boolean;
  dirty?: boolean;
  discardMessage?: string;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const subtitleId = useId();
  const discardId = useId();
  const [confirmingDiscard, setConfirmingDiscard] = useState(false);
  const [blockedCue, setBlockedCue] = useState(false);
  const returnFocus = useRef<HTMLElement | null>(null);
  const state = useRef({ dismissible, busy, dirty, confirmingDiscard });
  useLayoutEffect(() => {
    state.current = { dismissible, busy, dirty, confirmingDiscard };
  });
  const blocked = busy || !dismissible;
  const keepEditing = useCallback(() => {
    setConfirmingDiscard(false);
    const target = returnFocus.current;
    returnFocus.current = null;
    const node = ref.current;
    if (target?.isConnected && node?.contains(target))
      target.focus({ preventScroll: true });
    else node?.focus({ preventScroll: true });
  }, []);
  const requestClose = useCallback(() => {
    const current = state.current;
    if (current.confirmingDiscard) {
      keepEditing();
      return;
    }
    if (current.busy || !current.dismissible) {
      setBlockedCue(true);
      return;
    }
    if (current.dirty) {
      returnFocus.current = document.activeElement as HTMLElement | null;
      setConfirmingDiscard(true);
      return;
    }
    onClose();
  }, [keepEditing, onClose]);
  useEffect(() => {
    if (!blockedCue) return;
    const timer = window.setTimeout(() => setBlockedCue(false), 600);
    return () => window.clearTimeout(timer);
  }, [blockedCue]);
  useEffect(() => {
    if (!confirmingDiscard) return;
    ref.current
      ?.querySelector<HTMLElement>("[data-discard-keep]")
      ?.focus();
  }, [confirmingDiscard]);
  useOverlayFocus(ref, true, requestClose, { preferFormField: true });
  return (
    <div
      className="modal-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) requestClose();
      }}
    >
      <div
        ref={ref}
        className={`modal ${className} ${blockedCue ? "dismiss-blocked" : ""}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={subtitle ? subtitleId : undefined}
        aria-busy={busy || undefined}
        tabIndex={-1}
      >
        <IconButton
          label="关闭弹窗"
          className="modal-close"
          aria-disabled={blocked || undefined}
          onClick={requestClose}
        >
          <X size={ICON.lg} aria-hidden="true" />
        </IconButton>
        <div className="modal-heading">
          <h2 id={titleId}>{title}</h2>
          {subtitle && <p id={subtitleId}>{subtitle}</p>}
        </div>
        {confirmingDiscard && (
          <div
            className="modal-discard"
            role="group"
            aria-labelledby={discardId}
          >
            <p id={discardId}>{discardMessage}</p>
            <div>
              <Button
                type="button"
                variant="secondary"
                size="sm"
                data-discard-keep
                onClick={keepEditing}
              >
                保留编辑
              </Button>
              <Button
                type="button"
                variant="danger"
                size="sm"
                onClick={() => {
                  if (state.current.busy) return;
                  setConfirmingDiscard(false);
                  onClose();
                }}
              >
                放弃
              </Button>
            </div>
          </div>
        )}
        {children}
      </div>
    </div>
  );
}
export function PublicHeader() {
  return (
    <header className="public-header">
      <Link to="/" aria-label="Teamie 首页">
        <Logo />
      </Link>
      <nav>
        <Link to="/login">登录</Link>
        <Link className="button small-button" to="/login?mode=register">
          受邀注册
        </Link>
      </nav>
    </header>
  );
}
export function time(value: string) {
  return new Date(value).toLocaleTimeString("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
  });
}
export function date(value: string) {
  const d = new Date(value);
  const today = new Date();
  return d.toDateString() === today.toDateString()
    ? "今天"
    : d.toLocaleDateString("zh-CN", {
        month: "long",
        day: "numeric",
        weekday: "long",
      });
}
