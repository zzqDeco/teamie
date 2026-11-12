import type { ReactNode } from "react";

/**
 * Shared management empty state primitive (for genuinely empty collections or search zero-matches).
 * Keep load/network error states (`isError` + retry) strictly separate from `<EmptyState>`.
 */
export function EmptyState({
  icon,
  title,
  description,
  action,
  role,
  headingLevel = "h3",
  className,
}: {
  icon?: ReactNode;
  title: string;
  description?: ReactNode;
  action?: ReactNode;
  role?: "status";
  headingLevel?: "h2" | "h3";
  className?: string;
}) {
  const Heading = headingLevel;
  return (
    <div
      className={`empty-state${className ? ` ${className}` : ""}`}
      role={role}
    >
      {icon && (
        <span className="empty-state-icon" aria-hidden="true">
          {icon}
        </span>
      )}
      <Heading className="empty-state-title">{title}</Heading>
      {description && <p className="empty-state-description">{description}</p>}
      {action && <div className="empty-state-action">{action}</div>}
    </div>
  );
}

/**
 * Shared semantic list container for management directories and catalogs.
 */
export function DataList({
  label,
  as = "ul",
  className,
  children,
}: {
  label?: string;
  as?: "ul" | "div";
  className?: string;
  children: ReactNode;
}) {
  const Tag = as;
  return (
    <Tag
      role={as === "div" ? "list" : undefined}
      aria-label={label}
      className={`data-list${className ? ` ${className}` : ""}`}
    >
      {children}
    </Tag>
  );
}
