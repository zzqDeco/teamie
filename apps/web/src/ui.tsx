import {
  createContext,
  useCallback,
  useContext,
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
import { formatDateTime } from "./format-datetime";
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
export const AVATAR_TONES = [
  "avatar-tone-1",
  "avatar-tone-2",
  "avatar-tone-3",
  "avatar-tone-4",
  "avatar-tone-5",
  "avatar-tone-6",
] as const;
export type AvatarTone = (typeof AVATAR_TONES)[number];
/** Deterministic FNV-1a 32-bit hash mapped onto the 6-hue avatar token palette. */
export function avatarTone(seed: string): AvatarTone {
  let hash = 0x811c9dc5;
  for (let i = 0; i < seed.length; i++) {
    hash ^= seed.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193) >>> 0;
  }
  return AVATAR_TONES[hash % AVATAR_TONES.length]!;
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
  return (
    <span
      className={`avatar ${size} ${avatarTone(seed || name)} ${className}`.trim()}
      aria-hidden="true"
    >
      {Array.from(name.trim())[0]?.toUpperCase() || "T"}
    </span>
  );
}
export type ButtonVariant = "primary" | "secondary" | "danger" | "ghost";
export type ButtonSize = "sm" | "md" | "lg";
const buttonVariantClass: Record<ButtonVariant, string> = {
  primary: "",
  secondary: "secondary",
  danger: "danger",
  ghost: "ghost",
};
const buttonSizeClass: Record<ButtonSize, string> = {
  sm: "small-button",
  md: "",
  lg: "large-button",
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
    buttonSizeClass[size],
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
export type NoticeTone = "error" | "success" | "info" | "warning";
export function Notice({
  children,
  kind,
  tone,
  id,
  onDismiss,
}: {
  children: ReactNode;
  kind?: NoticeTone;
  tone?: NoticeTone;
  id?: string;
  onDismiss?: () => void;
}) {
  const effectiveKind = tone ?? kind ?? "error";
  return children ? (
    <div
      id={id}
      className={`notice ${effectiveKind} ${onDismiss ? "dismissible" : ""}`.trim()}
      role={effectiveKind === "error" ? "alert" : "status"}
    >
      <div className="notice-content">{children}</div>
      {onDismiss && (
        <button
          type="button"
          className="notice-dismiss"
          aria-label="关闭提示"
          onClick={onDismiss}
        >
          <X size={ICON.xs} aria-hidden="true" />
        </button>
      )}
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
  return <div className={`form-row ${className}`.trim()}>{children}</div>;
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
        role="switch"
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
    <div className="loading-screen" role="status">
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
    if (!candidate?.isConnected || candidate.closest("[inert]")) continue;
    candidate.focus({ preventScroll: true });
    if (document.activeElement === candidate) return true;
  }
  return false;
}
export function OverlayFocusBoundary({ edge }: { edge: "start" | "end" }) {
  return <span data-overlay-focus-boundary={edge} tabIndex={0}
    style={{ position: "fixed", width: 1, height: 1, opacity: 0, pointerEvents: "none" }} />;
}
export function useOverlayFocus(
  ref: RefObject<HTMLElement | null>,
  active: boolean,
  onClose: () => void,
  { preferFormField = false, fallbackFocus }: {
    preferFormField?: boolean;
    fallbackFocus?: () => HTMLElement | null | undefined;
  } = {},
) {
  const onCloseRef = useRef(onClose);
  const fallbackFocusRef = useRef(fallbackFocus);
  useEffect(() => {
    onCloseRef.current = onClose;
    fallbackFocusRef.current = fallbackFocus;
  }, [onClose, fallbackFocus]);
  useEffect(() => {
    if (!active) return;
    const previous = document.activeElement as HTMLElement | null;
    const node = ref.current;
    if (!node) return;
    const entry: OverlayEntry = { node, order: ++overlayOrder };
    overlayStack.push(entry);
    const focusables = () =>
      Array.from(node.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)).filter(
        (element) => element.getClientRects().length > 0 &&
          !element.hasAttribute("data-overlay-focus-boundary") &&
          !element.closest("[inert]"),
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
    // Keyboard events inside a cross-origin frame stay in its document. The
    // browser reaches these parent-document boundaries when tabbing out of it.
    function contain(event: FocusEvent) {
      if (!isTopOverlay(entry) || !(event.target instanceof HTMLElement)) return;
      if (!node?.contains(event.target)) return;
      const edge = event.target.dataset.overlayFocusBoundary;
      if (!edge) return;
      const elements = focusables();
      focusFirst([edge === "end" ? elements[0] : elements.at(-1), node]);
    }
    document.addEventListener("keydown", handle);
    document.addEventListener("focusin", contain);
    return () => {
      document.removeEventListener("keydown", handle);
      document.removeEventListener("focusin", contain);
      const index = overlayStack.indexOf(entry);
      if (index >= 0) overlayStack.splice(index, 1);
      if (!focusFirst([previous])) focusFirst([fallbackFocusRef.current?.()]);
    };
  }, [active, preferFormField, ref]);
}
const ModalCloseContext = createContext<(() => void) | null>(null);
/**
 * Returns the enclosing Modal's dismissal request so in-dialog 取消/完成
 * buttons take the same busy/dirty path as Escape, the backdrop and the X.
 */
export function useModalRequestClose() {
  const requestClose = useContext(ModalCloseContext);
  if (!requestClose)
    throw new Error("useModalRequestClose must be used inside a Modal");
  return requestClose;
}
/** A button that asks the enclosing Modal to close (dirty/busy aware). */
export function ModalCloseButton({
  children,
  variant = "secondary",
  ...props
}: Omit<Parameters<typeof Button>[0], "onClick" | "type">) {
  const requestClose = useModalRequestClose();
  return (
    <Button {...props} type="button" variant={variant} onClick={requestClose}>
      {children}
    </Button>
  );
}
/**
 * Every dismissal path (Escape, backdrop, close button, in-dialog
 * `ModalCloseButton`) goes through one request: `busy` or
 * `dismissible={false}` blocks it with a visual cue, and `dirty` asks for an
 * inline discard confirmation instead of closing.
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
        <ModalCloseContext.Provider value={requestClose}>
          {children}
        </ModalCloseContext.Provider>
      </div>
    </div>
  );
}
/**
 * Confirmation for consequential actions. Focus starts on Cancel, `busy`
 * blocks every dismissal path, and `impact` lists what will happen so the
 * consequence is read before the confirm button.
 */
export function ConfirmDialog({
  title,
  subtitle,
  impact = [],
  children,
  tone = "default",
  confirmLabel,
  cancelLabel = "取消",
  busy = false,
  confirmDisabled = false,
  error,
  onConfirm,
  onCancel,
}: {
  title: string;
  subtitle?: string;
  impact?: ReactNode[];
  children?: ReactNode;
  tone?: "default" | "danger";
  confirmLabel: string;
  cancelLabel?: string;
  busy?: boolean;
  confirmDisabled?: boolean;
  error?: ReactNode;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const impactId = useId();
  return (
    <Modal
      title={title}
      subtitle={subtitle}
      onClose={onCancel}
      busy={busy}
      className={`confirm-dialog ${tone === "danger" ? "danger" : ""}`.trim()}
    >
      {impact.length > 0 && (
        <ul id={impactId} className="confirm-impact" aria-label="影响">
          {impact.map((item, index) => (
            <li key={index}>{item}</li>
          ))}
        </ul>
      )}
      {children}
      {error ? <Notice>{error}</Notice> : null}
      <div className="modal-actions">
        <ModalCloseButton disabled={busy} data-autofocus>
          {cancelLabel}
        </ModalCloseButton>
        <Button
          type="button"
          variant={tone === "danger" ? "danger" : "primary"}
          busy={busy}
          disabled={confirmDisabled}
          aria-describedby={impact.length > 0 ? impactId : undefined}
          onClick={() => {
            if (!busy) onConfirm();
          }}
        >
          {confirmLabel}
        </Button>
      </div>
    </Modal>
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
/** Message time (10:35); see `formatDateTime` for every other kind. */
export function time(value: string) {
  return formatDateTime(value, "time");
}
/** Date separator label: 今天 / 昨天 / weekday date / cross-year date. */
export function date(value: string) {
  return formatDateTime(value, "day");
}
export { DataList, EmptyState } from "./management-ui";
export { Field, Tabs } from "./form-primitives";
export type { TabItem } from "./form-primitives";
