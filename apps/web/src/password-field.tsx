import { useEffect, useId, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { Check, Eye, EyeOff, TriangleAlert, X } from "lucide-react";
import { passwordRules } from "./password-policy";
import { ICON } from "./icons";

/** Inline field error, referenced by the field's aria-describedby. */
export function FieldError({ id, message }: { id: string; message?: string }) {
  if (!message) return null;
  return (
    <small id={id} className="field-error">
      <TriangleAlert size={ICON.xs} aria-hidden="true" />
      {message}
    </small>
  );
}

/** Builds aria props for a field that may show an inline error. */
export function fieldErrorProps(errorId: string, message?: string) {
  return message
    ? { "aria-invalid": true as const, "aria-describedby": errorId }
    : {};
}

export function PasswordField({
  name,
  label,
  autoComplete,
  placeholder,
  error,
  readOnly = false,
  required = true,
  showRules = false,
  confirmOf,
  mismatchMessage = "两次输入的密码不一致。",
}: {
  name: string;
  label: string;
  autoComplete: "new-password" | "current-password";
  placeholder?: string;
  /** Validation message owned by the form (submit / revalidation). */
  error?: string;
  readOnly?: boolean;
  required?: boolean;
  /** Show the live password rule checklist (new passwords only). */
  showRules?: boolean;
  /** Name of the password field this confirmation must match (checked on blur). */
  confirmOf?: string;
  mismatchMessage?: string;
}) {
  const id = useId();
  const errorId = useId();
  const rulesId = useId();
  const capsId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const [value, setValue] = useState("");
  const [visible, setVisible] = useState(false);
  const [capsLock, setCapsLock] = useState(false);
  const [mismatch, setMismatch] = useState(false);
  const rules = passwordRules(value);
  const message = error || (mismatch ? mismatchMessage : "");

  useEffect(() => {
    const form = inputRef.current?.form;
    if (!form) return;
    const reset = () => {
      setValue("");
      setVisible(false);
      setMismatch(false);
    };
    form.addEventListener("reset", reset);
    return () => form.removeEventListener("reset", reset);
  }, []);

  function otherValue(input: HTMLInputElement) {
    if (!confirmOf) return "";
    const other = input.form?.elements.namedItem(confirmOf);
    return other instanceof HTMLInputElement ? other.value : "";
  }
  function updateCapsLock(event: KeyboardEvent<HTMLInputElement>) {
    if (typeof event.getModifierState === "function")
      setCapsLock(event.getModifierState("CapsLock"));
  }

  const describedBy = [
    message ? errorId : "",
    capsLock ? capsId : "",
    showRules ? rulesId : "",
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <div className="password-field">
      <label htmlFor={id}>{label}</label>
      <div className="password-input">
        <input
          ref={inputRef}
          id={id}
          type={visible ? "text" : "password"}
          name={name}
          autoComplete={autoComplete}
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          placeholder={placeholder}
          required={required}
          readOnly={readOnly}
          aria-disabled={readOnly || undefined}
          aria-invalid={message ? true : undefined}
          aria-describedby={describedBy || undefined}
          onInput={(event) => {
            const next = event.currentTarget.value;
            setValue(next);
            if (mismatch && (!next || next === otherValue(event.currentTarget)))
              setMismatch(false);
          }}
          onKeyDown={updateCapsLock}
          onKeyUp={updateCapsLock}
          onBlur={(event) => {
            setCapsLock(false);
            const current = event.currentTarget.value;
            if (confirmOf)
              setMismatch(!!current && current !== otherValue(event.currentTarget));
          }}
        />
        <button
          type="button"
          className="password-toggle"
          aria-label="显示密码"
          aria-pressed={visible}
          aria-controls={id}
          title={visible ? "隐藏密码" : "显示密码"}
          onClick={() => setVisible((current) => !current)}
        >
          {visible ? (
            <EyeOff size={ICON.sm} aria-hidden="true" />
          ) : (
            <Eye size={ICON.sm} aria-hidden="true" />
          )}
        </button>
      </div>
      {showRules && (
        <ul className="password-rules" id={rulesId} aria-live="polite">
          <li className={rules.minLength ? "met" : ""}>
            {rules.minLength ? (
              <Check size={ICON.xs} aria-hidden="true" />
            ) : (
              <X size={ICON.xs} aria-hidden="true" />
            )}
            <span className="sr-only">
              {rules.minLength ? "已满足：" : "未满足："}
            </span>
            至少 12 个字符
          </li>
          <li className={value && rules.maxBytes ? "met" : ""}>
            {rules.maxBytes ? (
              <Check size={ICON.xs} aria-hidden="true" />
            ) : (
              <X size={ICON.xs} aria-hidden="true" />
            )}
            <span className="sr-only">
              {rules.maxBytes ? "已满足：" : "未满足："}
            </span>
            不超过 72 字节（约 24 个汉字）
          </li>
        </ul>
      )}
      {capsLock && (
        <small id={capsId} className="field-warning">
          <TriangleAlert size={ICON.xs} aria-hidden="true" />
          已开启大写锁定
        </small>
      )}
      <FieldError id={errorId} message={message} />
    </div>
  );
}
