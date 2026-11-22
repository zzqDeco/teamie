import { cloneElement, isValidElement, useId, useRef } from "react";
import type { KeyboardEvent, ReactNode } from "react";
import { FieldError } from "./password-field";

const FIELD_CONTROL_TAGS = new Set(["input", "textarea", "select"]);

function mergeDescribedBy(existing: unknown, extraIds: readonly string[]): string {
  const tokens =
    typeof existing === "string" ? existing.split(/\s+/).filter(Boolean) : [];
  for (const id of extraIds) {
    if (id && !tokens.includes(id)) tokens.push(id);
  }
  return tokens.join(" ");
}

function attachDescribedBy(
  node: ReactNode,
  extraIds: readonly string[],
): ReactNode {
  if (!extraIds.length || node == null || typeof node === "boolean") return node;
  if (Array.isArray(node)) {
    let attached = false;
    let changed = false;
    const mapped = node.map((item) => {
      if (attached) return item;
      const next = attachDescribedBy(item, extraIds);
      if (next !== item) {
        attached = true;
        changed = true;
      }
      return next;
    });
    return changed ? mapped : node;
  }
  if (
    !isValidElement<{ "aria-describedby"?: string; children?: ReactNode }>(node)
  ) {
    return node;
  }
  if (typeof node.type === "string" && FIELD_CONTROL_TAGS.has(node.type)) {
    const merged = mergeDescribedBy(node.props["aria-describedby"], extraIds);
    return merged === node.props["aria-describedby"]
      ? node
      : cloneElement(node, { "aria-describedby": merged });
  }
  if (node.props.children !== undefined) {
    const nextChildren = attachDescribedBy(node.props.children, extraIds);
    if (nextChildren !== node.props.children) {
      return Array.isArray(nextChildren)
        ? cloneElement(node, undefined, ...nextChildren)
        : cloneElement(node, undefined, nextChildren);
    }
  }
  return node;
}

export function Field({
  label,
  optional,
  hint,
  hintId,
  error,
  errorId,
  htmlFor,
  className = "",
  children,
}: {
  label: ReactNode;
  optional?: ReactNode;
  hint?: ReactNode;
  hintId?: string;
  error?: string;
  errorId?: string;
  htmlFor?: string;
  className?: string;
  children: ReactNode;
}) {
  const autoHintId = useId();
  const autoErrorId = useId();
  const resolvedHintId = hintId || autoHintId;
  const resolvedErrorId = errorId || autoErrorId;
  const describedByIds = [
    ...(hint ? [resolvedHintId] : []),
    ...(error ? [resolvedErrorId] : []),
  ];
  const enhancedChildren = attachDescribedBy(children, describedByIds);
  return (
    <div className={`field ${className}`.trim()}>
      <label htmlFor={htmlFor}>
        <span>
          {label}
          {optional ? (
            <>
              {" "}
              <span className="optional">{optional}</span>
            </>
          ) : null}
        </span>
        {!htmlFor ? enhancedChildren : null}
      </label>
      {htmlFor ? enhancedChildren : null}
      {hint ? (
        <small id={resolvedHintId} className="field-help">
          {hint}
        </small>
      ) : null}
      <FieldError id={resolvedErrorId} message={error} />
    </div>
  );
}

export type TabItem<T extends string = string> = {
  value: T;
  label: ReactNode;
  badge?: ReactNode;
  disabled?: boolean;
  id?: string;
  controlsId?: string;
  itemClassName?: string;
};

export function Tabs<T extends string>({
  items,
  value,
  onChange,
  ariaLabel,
  variant = "segmented",
  mode = "tabs",
  className = "",
  tabClassName = "",
}: {
  items: readonly TabItem<T>[];
  value: T;
  onChange: (next: T) => void;
  ariaLabel: string;
  variant?: "segmented" | "underline";
  mode?: "tabs" | "group";
  className?: string;
  tabClassName?: string;
}) {
  const refs = useRef<Array<HTMLButtonElement | null>>([]);
  function moveFocus(currentIndex: number, key: string) {
    const enabledIndices = items
      .map((item, idx) => (item.disabled ? -1 : idx))
      .filter((idx) => idx >= 0);
    if (enabledIndices.length === 0) return;
    const pos = enabledIndices.indexOf(currentIndex);
    let nextPos = pos >= 0 ? pos : 0;
    if (key === "ArrowRight") {
      nextPos = (nextPos + 1) % enabledIndices.length;
    } else if (key === "ArrowLeft") {
      nextPos = (nextPos - 1 + enabledIndices.length) % enabledIndices.length;
    } else if (key === "Home") {
      nextPos = 0;
    } else if (key === "End") {
      nextPos = enabledIndices.length - 1;
    } else {
      return;
    }
    const targetIdx = enabledIndices[nextPos]!;
    const targetItem = items[targetIdx]!;
    onChange(targetItem.value);
    refs.current[targetIdx]?.focus();
  }
  function handleKeyDown(event: KeyboardEvent<HTMLButtonElement>, idx: number) {
    if (mode !== "tabs") return;
    if (
      event.key === "ArrowRight" ||
      event.key === "ArrowLeft" ||
      event.key === "Home" ||
      event.key === "End"
    ) {
      event.preventDefault();
      moveFocus(idx, event.key);
    }
  }
  const isTabs = mode === "tabs";
  return (
    <div
      className={`tabs-list ${variant} ${className}`.trim()}
      role={isTabs ? "tablist" : "group"}
      aria-label={ariaLabel}
    >
      {items.map((item, idx) => {
        const selected = item.value === value;
        return (
          <button
            key={item.value}
            ref={(el) => {
              refs.current[idx] = el;
            }}
            id={item.id}
            type="button"
            role={isTabs ? "tab" : undefined}
            aria-selected={isTabs ? selected : undefined}
            aria-pressed={!isTabs ? selected : undefined}
            aria-controls={item.controlsId}
            tabIndex={isTabs ? (selected ? 0 : -1) : undefined}
            disabled={item.disabled}
            className={`tab-trigger ${tabClassName} ${item.itemClassName || ""} ${selected ? "active" : ""}`.trim()}
            onClick={() => onChange(item.value)}
            onKeyDown={(event) => handleKeyDown(event, idx)}
          >
            {item.label}
            {item.badge}
          </button>
        );
      })}
    </div>
  );
}
