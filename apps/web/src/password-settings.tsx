import { useEffect, useId, useRef, useState } from "react";
import type { FormEvent } from "react";
import { api, ApiError } from "@teamie/api-client";
import { useAuth } from "./auth";
import { errorText } from "./chat";
import { passwordPolicyError } from "./password-policy";
import { Button, Notice } from "./ui";

type PasswordField = "currentPassword" | "newPassword" | "confirmPassword";

export function PasswordSettings() {
  const { changePassword } = useAuth();
  const formRef = useRef<HTMLFormElement>(null);
  const pending = useRef(false);
  const errorId = useId();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [invalidField, setInvalidField] = useState<PasswordField>();

  useEffect(() => {
    if (!busy && invalidField) {
      const input = formRef.current?.elements.namedItem(invalidField);
      if (input instanceof HTMLInputElement) input.focus();
    }
  }, [busy, error, invalidField]);

  function fail(message: string, field?: PasswordField) {
    setError(message);
    setInvalidField(field);
    if (!busy && field) {
      const input = formRef.current?.elements.namedItem(field);
      if (input instanceof HTMLInputElement) input.focus();
    }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending.current) return;
    const form = event.currentTarget;
    const data = new FormData(form);
    const currentPassword = String(data.get("currentPassword") || "");
    const newPassword = String(data.get("newPassword") || "");
    const confirmPassword = String(data.get("confirmPassword") || "");
    if (!currentPassword) {
      fail("请填写当前密码。", "currentPassword");
      return;
    }
    const policyError = passwordPolicyError(newPassword);
    if (policyError) {
      fail(policyError, "newPassword");
      return;
    }
    if (newPassword === currentPassword) {
      fail("新密码不能与当前密码相同。", "newPassword");
      return;
    }
    if (newPassword !== confirmPassword) {
      fail("两次输入的新密码不一致。", "confirmPassword");
      return;
    }
    pending.current = true;
    setBusy(true);
    setError("");
    setInvalidField(undefined);
    const sessionToken = api.csrfToken;
    try {
      await changePassword(currentPassword, newPassword);
      form.reset();
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

  return (
    <form
      ref={formRef}
      className="settings-form password-settings"
      onSubmit={submit}
      aria-busy={busy}
    >
      <h3>修改密码</h3>
      <p className="field-help">修改后需要重新登录。</p>
      <Notice id={errorId}>{error}</Notice>
      <label>
        当前密码
        <input
          type="password"
          name="currentPassword"
          autoComplete="current-password"
          aria-invalid={invalidField === "currentPassword" || undefined}
          aria-describedby={
            invalidField === "currentPassword" ? errorId : undefined
          }
          required
          disabled={busy}
        />
      </label>
      <label>
        新密码
        <input
          type="password"
          name="newPassword"
          autoComplete="new-password"
          placeholder="至少 12 个字符"
          minLength={12}
          aria-invalid={invalidField === "newPassword" || undefined}
          aria-describedby={invalidField === "newPassword" ? errorId : undefined}
          required
          disabled={busy}
        />
      </label>
      <label>
        确认新密码
        <input
          type="password"
          name="confirmPassword"
          autoComplete="new-password"
          aria-invalid={invalidField === "confirmPassword" || undefined}
          aria-describedby={
            invalidField === "confirmPassword" ? errorId : undefined
          }
          required
          disabled={busy}
        />
      </label>
      <Button type="submit" busy={busy}>
        修改密码
      </Button>
    </form>
  );
}
