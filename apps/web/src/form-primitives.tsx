import { useId, useRef } from "react";
import type { KeyboardEvent, ReactNode } from "react";
import { FieldError } from "./password-field";

export function Field({
  label,
  optional,
  hint,
  error,
  errorId,
  htmlFor,
  className = "",
  children,
}: {
  label: ReactNode;
  optional?: ReactNode;
  hint?: ReactNode;
  error?: string;
  errorId?: string;
  htmlFor?: string;
  className?: string;
  children: ReactNode;
}) {
  const autoErrorId = useId();
  const resolvedErrorId = errorId || autoErrorId;
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
        {!htmlFor ? children : null}
      </label>
      {htmlFor ? children : null}
      {hint ? <small className="field-help">{hint}</small> : null}
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
};

export function Tabs<T extends string>({
  items,
  value,
  onChange,
  ariaLabel,
  className = "",
  tabClassName = "",
}: {
  items: readonly TabItem<T>[];
  value: T;
  onChange: (next: T) => void;
  ariaLabel: string;
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
  return (
    <div
      className={`tabs-list ${className}`.trim()}
      role="tablist"
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
            role="tab"
            aria-selected={selected}
            aria-controls={item.controlsId}
            tabIndex={selected ? 0 : -1}
            disabled={item.disabled}
            className={`tab-trigger ${tabClassName} ${selected ? "active" : ""}`.trim()}
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
