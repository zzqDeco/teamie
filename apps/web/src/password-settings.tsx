import { useEffect, useId, useRef, useState } from "react";
import type { FormEvent } from "react";
import { api, ApiError } from "@teamie/api-client";
import { useAuth } from "./auth";
import { errorText } from "./errors";
import { passwordPolicyError } from "./password-policy";
import { PasswordField } from "./password-field";
import { toast } from "./toast";
import { Button, Notice } from "./ui";

type Field = "currentPassword" | "newPassword" | "confirmPassword";
const FIELDS: Field[] = [
  "currentPassword",
  "newPassword",
  "confirmPassword",
];

function fieldError(field: Field, data: FormData): string {
  const currentPassword = String(data.get("currentPassword") || "");
  const newPassword = String(data.get("newPassword") || "");
  const confirmPassword = String(data.get("confirmPassword") || "");
  if (field === "currentPassword")
    return currentPassword ? "" : "请填写当前密码。";
  if (field === "newPassword") {
    const policyError = passwordPolicyError(newPassword);
    if (policyError) return policyError;
    return newPassword === currentPassword ? "新密码不能与当前密码相同。" : "";
  }
  if (!confirmPassword) return "请再次输入新密码。";
  return newPassword !== confirmPassword ? "两次输入的新密码不一致。" : "";
}

export function PasswordSettings() {
  const { changePassword } = useAuth();
  const formRef = useRef<HTMLFormElement>(null);
  const pending = useRef(false);
  const errorId = useId();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [invalidField, setInvalidField] = useState<Field>();

  useEffect(() => {
    if (!busy && invalidField) {
      const input = formRef.current?.elements.namedItem(invalidField);
      if (input instanceof HTMLInputElement) input.focus();
    }
  }, [busy, error, invalidField]);

  function fail(message: string, field?: Field) {
    setError(message);
    setInvalidField(field);
    if (!busy && field) {
      const input = formRef.current?.elements.namedItem(field);
      if (input instanceof HTMLInputElement) input.focus();
    }
  }

  function revalidate(event: FormEvent<HTMLFormElement>) {
    // Validate on submit first, then live only for the field already in error.
    const target = event.target as HTMLInputElement;
    if (!invalidField || target.name !== invalidField) return;
    const message = fieldError(invalidField, new FormData(event.currentTarget));
    if (!message) {
      setError("");
      setInvalidField(undefined);
    } else if (message !== error) setError(message);
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending.current) return;
    const form = event.currentTarget;
    const data = new FormData(form);
    const currentPassword = String(data.get("currentPassword") || "");
    const newPassword = String(data.get("newPassword") || "");
    for (const field of FIELDS) {
      const message = fieldError(field, data);
      if (message) {
        fail(message, field);
        return;
      }
    }
    pending.current = true;
    setBusy(true);
    setError("");
    setInvalidField(undefined);
    const sessionToken = api.csrfToken;
    try {
      await changePassword(currentPassword, newPassword);
      form.reset();
      // A successful change clears this session; a stale response does not.
      if (!api.csrfToken) toast.success("密码已修改，请使用新密码重新登录。");
    } catch (error) {
      if (sessionToken !== api.csrfToken) return;
      fail(
        error instanceof ApiError
          ? errorText(error)
          : "修改密码失败，请检查网络后重试。",
        error instanceof ApiError && error.code === "invalid_current_password"
          ? "currentPassword"
          : error instanceof ApiError &&
              ["invalid_password", "password_unchanged"].includes(error.code)
            ? "newPassword"
            : undefined,
      );
    } finally {
      pending.current = false;
      setBusy(false);
    }
  }

  const inlineError = (field: Field) =>
    invalidField === field ? error : undefined;

  return (
    <form
      ref={formRef}
      className="settings-form password-settings"
      onSubmit={submit}
      onInput={revalidate}
      aria-busy={busy}
      noValidate
    >
      <h3>修改密码</h3>
      <p className="field-help">修改后需要重新登录。</p>
      <Notice id={errorId}>{error}</Notice>
      <PasswordField
        name="currentPassword"
        label="当前密码"
        autoComplete="current-password"
        error={inlineError("currentPassword")}
        readOnly={busy}
      />
      <PasswordField
        name="newPassword"
        label="新密码"
        autoComplete="new-password"
        placeholder="至少 12 个字符"
        error={inlineError("newPassword")}
        readOnly={busy}
        showRules
      />
      <PasswordField
        name="confirmPassword"
        label="确认新密码"
        autoComplete="new-password"
        error={inlineError("confirmPassword")}
        readOnly={busy}
        confirmOf="newPassword"
        mismatchMessage="两次输入的新密码不一致。"
      />
      <Button type="submit" busy={busy}>
        修改密码
      </Button>
    </form>
  );
}
