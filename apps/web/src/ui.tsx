import { useEffect, useId, useRef } from "react";
import type { ButtonHTMLAttributes, ReactNode } from "react";
import { LoaderCircle, X } from "lucide-react";
import { Link } from "react-router-dom";
export function Logo({ compact = false }: { compact?: boolean }) {
  return (
    <span className="brand">
      <span className="brand-icon" aria-hidden="true">
        <svg viewBox="0 0 40 40">
          <path d="M9 10h23v6h-9v17h-6V16H9z" fill="currentColor" />
          <circle cx="31" cy="30" r="4" fill="#d8efae" />
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
export function Button({
  children,
  busy = false,
  className = "",
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { busy?: boolean }) {
  return (
    <button
      {...props}
      disabled={props.disabled || busy}
      className={`button ${className}`}
    >
      {busy && <LoaderCircle className="spin" size={16} />} {children}
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
}: {
  children: ReactNode;
  kind?: "error" | "success" | "info";
}) {
  return children ? (
    <div
      className={`notice ${kind}`}
      role={kind === "error" ? "alert" : "status"}
    >
      {children}
    </div>
  ) : null;
}
export function Loading({ label = "正在加载你的空间…" }: { label?: string }) {
  return (
    <div className="loading-screen">
      <Logo />
      <LoaderCircle className="spin" size={26} />
      <p>{label}</p>
    </div>
  );
}
export function Modal({
  title,
  subtitle,
  children,
  onClose,
}: {
  title: string;
  subtitle?: string;
  children: ReactNode;
  onClose: () => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const titleId = useId();
  useEffect(() => {
    const previous = document.activeElement as HTMLElement;
    const node = ref.current;
    const focusables = () =>
      Array.from(
        node?.querySelectorAll<HTMLElement>(
          'button:not(:disabled), input:not(:disabled), textarea:not(:disabled), select:not(:disabled), a[href], [tabindex="0"]',
        ) || [],
      );
    (
      node?.querySelector<HTMLElement>("input, textarea") || focusables()[0]
    )?.focus();
    const handle = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
      if (event.key === "Tab") {
        const elements = focusables();
        const first = elements[0],
          last = elements[elements.length - 1];
        if (event.shiftKey && document.activeElement === first) {
          event.preventDefault();
          last?.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault();
          first?.focus();
        }
      }
    };
    document.addEventListener("keydown", handle);
    return () => {
      document.removeEventListener("keydown", handle);
      previous?.focus();
    };
  }, [onClose]);
  return (
    <div
      className="modal-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div
        ref={ref}
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
      >
        <IconButton label="关闭弹窗" className="modal-close" onClick={onClose}>
          <X size={19} />
        </IconButton>
        <div className="modal-heading">
          <h2 id={titleId}>{title}</h2>
          {subtitle && <p>{subtitle}</p>}
        </div>
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
        <a href="/#why">为什么选择 Teamie</a>
        <Link to="/login">登录</Link>
        <Link className="button small-button" to="/login?mode=register">
          加入 Teamie <span aria-hidden="true">↗</span>
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
