// @vitest-environment jsdom
import { act, useEffect } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import {
  MemoryRouter,
  Navigate,
  Route,
  Routes,
  useLocation,
} from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@teamie/api-client";
import type { Session, User } from "@teamie/api-client";
import { AuthProvider, useAuth } from "./auth";
import { ProfileSettings } from "./dialogs";
import { passwordPolicyError } from "./password-policy";
import { Login } from "./public";
import { chatStore } from "./store";

const user: User = {
  id: "password-test-user",
  kind: "human",
  platformRole: "user",
  displayName: "测试伙伴",
  email: "password-test@example.test",
  createdAt: "2026-10-17T00:00:00Z",
};
const session: Session = { user, csrfToken: "example-current-session" };
const nextSession: Session = {
  user: { ...user, id: "next-user", displayName: "另一位伙伴" },
  csrfToken: "example-next-account-session",
};
const currentPassword = "example-old-password";
const newPassword = "example-new-password";
const inviteCode = "example-single-use-invite";

let container: HTMLDivElement;
let root: Root;
let client: QueryClient;
let activeAuth: ReturnType<typeof useAuth>;

function Settings() {
  const { user, loading } = useAuth();
  if (loading) return null;
  return user ? <ProfileSettings /> : <Navigate to="/login" replace />;
}
function Probe() {
  const auth = useAuth();
  useEffect(() => {
    activeAuth = auth;
  }, [auth]);
  const { user, loading } = auth;
  const location = useLocation();
  return (
    <>
      <output data-location>{location.pathname + location.search}</output>
      <output data-account>{loading ? "loading" : user?.id || "signed-out"}</output>
      <output data-auth-error>{auth.error}</output>
    </>
  );
}
async function settle(action?: () => void) {
  await act(async () => {
    action?.();
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}
async function mount(path: string, authenticated = false) {
  if (authenticated) vi.mocked(api.session).mockResolvedValue(session);
  await settle(() =>
    root.render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={[path]}>
          <AuthProvider>
            <Routes>
              <Route path="/login" element={<Login />} />
              <Route path="/app/settings" element={<Settings />} />
              <Route path="/app" element={<div>工作空间</div>} />
            </Routes>
            <Probe />
          </AuthProvider>
        </MemoryRouter>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
function find<T extends HTMLElement>(selector: string): T {
  const node = container.querySelector<T>(selector);
  if (!node) throw new Error(`Missing control: ${selector}`);
  return node;
}
function fill(name: string, value: string) {
  find<HTMLInputElement>(`input[name="${name}"]`).value = value;
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}
async function editProfileName(value: string) {
  const input = find<HTMLInputElement>(".settings-form input");
  await settle(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!
      .set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
function fillRegistration(password = newPassword, confirmation = password) {
  fill("displayName", user.displayName);
  fill("email", user.email);
  fill("password", password);
  fill("confirmPassword", confirmation);
}
function fillPasswordSettings(password = newPassword, confirmation = password) {
  fill("currentPassword", currentPassword);
  fill("newPassword", password);
  fill("confirmPassword", confirmation);
}
async function submit(selector = "form") {
  await settle(() =>
    find<HTMLFormElement>(selector).dispatchEvent(
      new Event("submit", { bubbles: true, cancelable: true }),
    ),
  );
}

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  api.csrfToken = "";
  vi.spyOn(api, "session").mockRejectedValue(
    new ApiError(401, "unauthorized", "unauthorized"),
  );
  vi.spyOn(api, "registration").mockResolvedValue({
    mode: "invite",
    bootstrapRequired: false,
  });
  vi.spyOn(api, "register").mockResolvedValue(session);
  vi.spyOn(api, "login").mockResolvedValue({
    user,
    csrfToken: "example-new-session",
  });
  vi.spyOn(api, "changePassword").mockResolvedValue(undefined);
  vi.spyOn(api, "logout").mockResolvedValue(undefined);
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
});
afterEach(async () => {
  await settle(() => root.unmount());
  client.clear();
  chatStore.clear();
  api.csrfToken = "";
  container.remove();
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("shared password policy", () => {
  it.each([
    ["a".repeat(11), false],
    ["a".repeat(12), true],
    ["a".repeat(72), true],
    ["a".repeat(73), false],
    ["🔑".repeat(11), false],
    ["🔑".repeat(12), true],
    ["🔑".repeat(18), true],
    ["🔑".repeat(19), false],
    ["密".repeat(24), true],
    ["密".repeat(25), false],
  ])("checks Unicode code points and UTF-8 bytes: %s", (password, valid) => {
    expect(passwordPolicyError(password as string) === "").toBe(valid);
  });
});

describe("registration confirmation", () => {
  it("keeps a mismatched or missing confirmation local and preserves the invitation", async () => {
    await mount(`/login?mode=register&inviteCode=${inviteCode}`);
    fillRegistration(newPassword, "example-other-password");
    const confirm = find<HTMLInputElement>('input[name="confirmPassword"]');
    expect(confirm.required).toBe(true);
    expect(confirm.autocomplete).toBe("new-password");
    await submit();
    expect(api.register).not.toHaveBeenCalled();
    expect(api.login).not.toHaveBeenCalled();
    expect(container.textContent).toContain("两次输入的密码不一致。");
    expect(document.activeElement).toBe(confirm);
    expect(confirm.getAttribute("aria-invalid")).toBe("true");
    expect(document.getElementById(confirm.getAttribute("aria-describedby")!)?.textContent)
      .toContain("两次输入的密码不一致。");
    expect(find<HTMLInputElement>('input[name="inviteCode"]').value).toBe(inviteCode);
    fill("confirmPassword", "");
    await submit();
    expect(api.register).not.toHaveBeenCalled();
    expect(find<HTMLInputElement>('input[name="inviteCode"]').value).toBe(inviteCode);
  });

  it("registers with only the contract fields after a matching confirmation", async () => {
    await mount(`/login?mode=register&inviteCode=${inviteCode}`);
    fillRegistration();
    const passwordInputs = Array.from(container.querySelectorAll<HTMLInputElement>('input[type="password"]'));
    await submit();
    expect(api.register).toHaveBeenCalledExactlyOnceWith({
      email: user.email,
      displayName: user.displayName,
      password: newPassword,
      inviteCode,
    });
    expect(vi.mocked(api.register).mock.calls[0][0]).not.toHaveProperty("confirmPassword");
    expect(find("[data-location]").textContent).toBe("/app");
    expect(find("[data-account]").textContent).toBe(user.id);
    expect(passwordInputs.every((input) => input.value === "")).toBe(true);
  });

  it.each(["🔑".repeat(11), "密".repeat(25)])(
    "blocks invalid Unicode lengths before registration: %s",
    async (password) => {
      await mount(`/login?mode=register&inviteCode=${inviteCode}`);
      fillRegistration(password);
      await submit();
      expect(api.register).not.toHaveBeenCalled();
      expect(container.textContent).toContain(passwordPolicyError(password));
      expect(document.activeElement).toBe(find('input[name="password"]'));
    },
  );

  it("accepts twelve non-BMP characters and does not count UTF-16 code units", async () => {
    await mount(`/login?mode=register&inviteCode=${inviteCode}`);
    const password = "🔑".repeat(12);
    fillRegistration(password);
    await submit();
    expect(api.register).toHaveBeenCalledWith(expect.objectContaining({ password }));
  });

  it("clears registration validation when switching to login and requires no confirmation", async () => {
    await mount(`/login?mode=register&inviteCode=${inviteCode}`);
    fillRegistration(newPassword, "example-other-password");
    await submit();
    await settle(() => find<HTMLButtonElement>(".auth-tabs button:first-child").click());
    expect(container.querySelector('[name="confirmPassword"]')).toBeNull();
    expect(container.querySelector('[role="alert"]')).toBeNull();
    expect(find<HTMLInputElement>('input[name="password"]').autocomplete).toBe("current-password");
    expect(find<HTMLInputElement>('input[name="password"]').hasAttribute("minlength")).toBe(false);
    fill("email", user.email);
    fill("password", currentPassword);
    await submit();
    expect(api.login).toHaveBeenCalledExactlyOnceWith(user.email, currentPassword);
    expect(api.register).not.toHaveBeenCalled();
    expect(find("[data-location]").textContent).toBe("/app");
  });
});

describe("account password settings", () => {
  it.each([
    ["short", "short", "密码至少需要 12 个字符。", "newPassword"],
    ["🔑".repeat(11), "🔑".repeat(11), "密码至少需要 12 个字符。", "newPassword"],
    ["密".repeat(25), "密".repeat(25), "密码过长，请控制在 72 字节以内。", "newPassword"],
    [currentPassword, currentPassword, "新密码不能与当前密码相同。", "newPassword"],
    [newPassword, "example-other-password", "两次输入的新密码不一致。", "confirmPassword"],
  ])("blocks invalid password changes locally: %s", async (password, confirm, message, field) => {
    await mount("/app/settings", true);
    fillPasswordSettings(password, confirm);
    await submit(".password-settings");
    expect(api.changePassword).not.toHaveBeenCalled();
    expect(container.textContent).toContain(message);
    expect(document.activeElement).toBe(find(`input[name="${field}"]`));
    expect(find("[data-account]").textContent).toBe(user.id);
  });

  it("keeps the active account and cached content on a wrong current password, then allows a retry", async () => {
    vi.mocked(api.changePassword).mockRejectedValueOnce(
      new ApiError(400, "invalid_current_password", "wrong current password"),
    );
    await mount("/app/settings", true);
    client.setQueryData(["protected-content", user.id], { marker: "retained" });
    fillPasswordSettings();
    await submit(".password-settings");
    expect(container.textContent).toContain("当前密码不正确，请重新输入。");
    expect(find("[data-account]").textContent).toBe(user.id);
    expect(find("[data-location]").textContent).toBe("/app/settings");
    expect(api.csrfToken).toBe(session.csrfToken);
    expect(client.getQueryData(["protected-content", user.id])).toEqual({ marker: "retained" });
    expect(chatStore.getSnapshot().userId).toBe(user.id);
    expect(find<HTMLInputElement>('input[name="newPassword"]').value).toBe(newPassword);
    expect(document.activeElement).toBe(find('input[name="currentPassword"]'));
    expect(find<HTMLButtonElement>('.password-settings button[type="submit"]').disabled).toBe(false);
    fill("currentPassword", "example-corrected-password");
    await submit(".password-settings");
    expect(api.changePassword).toHaveBeenLastCalledWith("example-corrected-password", newPassword);
    expect(find("[data-location]").textContent).toBe("/login?passwordChanged=1");
    expect(api.logout).not.toHaveBeenCalled();
  });

  it("prevents duplicate submissions while the password request is pending", async () => {
    let finish!: () => void;
    vi.mocked(api.changePassword).mockImplementation(
      () => new Promise<void>((resolve) => { finish = resolve; }),
    );
    await mount("/app/settings", true);
    fillPasswordSettings();
    const form = find<HTMLFormElement>(".password-settings");
    await settle(() => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    });
    expect(api.changePassword).toHaveBeenCalledExactlyOnceWith(currentPassword, newPassword);
    expect(find<HTMLButtonElement>('.password-settings button[type="submit"]').disabled).toBe(true);
    // Inputs stay focusable while pending (disabling would drop focus to body).
    expect(Array.from(form.querySelectorAll("input"), (input) => input.disabled)).toEqual([false, false, false]);
    expect(Array.from(form.querySelectorAll("input"), (input) => input.readOnly)).toEqual([true, true, true]);
    expect(Array.from(form.querySelectorAll("input"), (input) => input.getAttribute("aria-disabled"))).toEqual(["true", "true", "true"]);
    await settle(() => finish());
    expect(find("[data-location]").textContent).toBe("/login?passwordChanged=1");
  });

  it("clears session and account caches after success, shows confirmation and allows login with the new password", async () => {
    await mount("/app/settings", true);
    client.setQueryData(["protected-content", user.id], { marker: "private-content" });
    chatStore.apply({ events: [], cursor: "19", hasMore: false });
    fillPasswordSettings();
    const passwordInputs = Array.from(container.querySelectorAll<HTMLInputElement>('.password-settings input'));
    expect(passwordInputs.map((input) => input.autocomplete)).toEqual([
      "current-password", "new-password", "new-password",
    ]);
    await submit(".password-settings");
    expect(api.changePassword).toHaveBeenCalledExactlyOnceWith(currentPassword, newPassword);
    expect(api.logout).not.toHaveBeenCalled();
    expect(api.csrfToken).toBe("");
    expect(chatStore.getSnapshot()).toMatchObject({ userId: "", cursor: "0", messages: {}, conversations: {} });
    expect(localStorage.getItem(`teamie.sync.v1.${user.id}`)).toBeNull();
    expect(client.getQueryData(["protected-content", user.id])).toBeUndefined();
    expect(client.getMutationCache().getAll()).toHaveLength(0);
    expect(find("[data-account]").textContent).toBe("signed-out");
    expect(find("[data-location]").textContent).toBe("/login?passwordChanged=1");
    expect(container.textContent).toContain("密码已修改，请重新登录。");
    expect(passwordInputs.every((input) => input.value === "")).toBe(true);
    const cached = JSON.stringify(client.getQueriesData({}));
    expect(cached).not.toContain(newPassword);
    expect(cached).not.toContain(currentPassword);
    fill("email", user.email);
    fill("password", newPassword);
    await submit();
    expect(api.login).toHaveBeenCalledExactlyOnceWith(user.email, newPassword);
    expect(find("[data-account]").textContent).toBe(user.id);
    expect(api.csrfToken).toBe("example-new-session");
    expect(find("[data-location]").textContent).toBe("/app");
  });

  it.each([
    [new ApiError(429, "rate_limited", "too frequent"), "操作过于频繁，请稍后再试。"],
    [new Error("network unavailable"), "修改密码失败，请检查网络后重试。"],
  ])("shows retryable localized feedback without logging out: %s", async (failure, message) => {
    vi.mocked(api.changePassword).mockRejectedValueOnce(failure);
    await mount("/app/settings", true);
    fillPasswordSettings();
    await submit(".password-settings");
    expect(container.textContent).toContain(message);
    expect(find("[data-account]").textContent).toBe(user.id);
    expect(find<HTMLButtonElement>('.password-settings button[type="submit"]').disabled).toBe(false);
    expect(api.logout).not.toHaveBeenCalled();
  });
});

describe("account changes with pending requests", () => {
  it("continues to save profile changes for the current session", async () => {
    const updated = { ...user, displayName: "新的显示名称" };
    vi.spyOn(api, "profile").mockResolvedValue({ user: updated });
    await mount("/app/settings", true);
    await editProfileName(updated.displayName);
    await submit("form.settings-form:not(.password-settings)");
    expect(activeAuth.user).toEqual(updated);
    expect(api.csrfToken).toBe(session.csrfToken);
    expect(container.textContent).toContain("资料已保存。");
  });

  it("does not let a late profile save restore the account revoked by a password change", async () => {
    const profile = deferred<{ user: User }>();
    vi.spyOn(api, "profile").mockReturnValue(profile.promise);
    await mount("/app/settings", true);
    await editProfileName("新的显示名称");
    await submit("form.settings-form:not(.password-settings)");
    expect(api.profile).toHaveBeenCalledWith("新的显示名称");
    const invalidate = vi.spyOn(client, "invalidateQueries");
    fillPasswordSettings();
    await submit(".password-settings");
    expect(find("[data-account]").textContent).toBe("signed-out");
    expect(find("[data-location]").textContent).toBe("/login?passwordChanged=1");
    await settle(() => profile.resolve({ user: { ...user, displayName: "新的显示名称" } }));
    expect(find("[data-account]").textContent).toBe("signed-out");
    expect(find("[data-location]").textContent).toBe("/login?passwordChanged=1");
    expect(invalidate).not.toHaveBeenCalled();
    expect(api.csrfToken).toBe("");
  });

  it("ignores a profile save from the previous session even when the same account logs in again", async () => {
    const profile = deferred<{ user: User }>();
    vi.spyOn(api, "profile").mockReturnValue(profile.promise);
    await mount("/app/settings", true);
    await editProfileName("旧会话的修改");
    await submit("form.settings-form:not(.password-settings)");
    const invalidate = vi.spyOn(client, "invalidateQueries");
    await settle(() => activeAuth.setSession({ user, csrfToken: "example-relogin-session" }));
    await settle(() => profile.resolve({ user: { ...user, displayName: "旧会话的修改" } }));
    expect(activeAuth.user?.displayName).toBe(user.displayName);
    expect(invalidate).not.toHaveBeenCalled();
    expect(container.textContent).not.toContain("资料已保存。");
  });

  it("does not allow profile updates to sign in a logged-out account or replace another account", async () => {
    await mount("/app", true);
    await settle(() => activeAuth.updateUser(nextSession.user));
    expect(activeAuth.user?.id).toBe(user.id);
    await settle(() => { void activeAuth.logout(); });
    await settle(() => activeAuth.updateUser(user));
    expect(find("[data-account]").textContent).toBe("signed-out");
    await settle(() => activeAuth.setSession(nextSession));
    await settle(() => activeAuth.updateUser(user));
    expect(find("[data-account]").textContent).toBe(nextSession.user.id);
    expect(api.csrfToken).toBe(nextSession.csrfToken);
  });

  it.each([
    nextSession,
    { user, csrfToken: "example-same-account-relogin-session" },
  ])("preserves a newer login when an old password response arrives late: $csrfToken", async (replacement) => {
    const password = deferred<void>();
    vi.mocked(api.changePassword).mockReturnValue(password.promise);
    await mount("/app/settings", true);
    fillPasswordSettings();
    await submit(".password-settings");
    expect(api.changePassword).toHaveBeenCalledOnce();
    await settle(() => { void activeAuth.logout(); });
    await settle(() => activeAuth.setSession(replacement));
    client.setQueryData(["protected-content", replacement.user.id], "new account content");
    chatStore.apply({ events: [], cursor: "23", hasMore: false });
    const location = find("[data-location]").textContent;
    await settle(() => password.resolve());
    expect(find("[data-account]").textContent).toBe(replacement.user.id);
    expect(api.csrfToken).toBe(replacement.csrfToken);
    expect(client.getQueryData(["protected-content", replacement.user.id])).toBe("new account content");
    expect(chatStore.getSnapshot()).toMatchObject({ userId: replacement.user.id, cursor: "23" });
    expect(find("[data-location]").textContent).toBe(location);
    expect(container.textContent).not.toContain("密码已修改，请重新登录。");
  });

  it("does not let an old logout clear or redirect a newer login", async () => {
    const logout = deferred<void>();
    vi.mocked(api.logout).mockReturnValue(logout.promise);
    await mount("/app/settings", true);
    await settle(() => find<HTMLButtonElement>(".session-setting button").click());
    expect(api.logout).toHaveBeenCalledOnce();
    await settle(() => activeAuth.setSession(nextSession));
    client.setQueryData(["protected-content", nextSession.user.id], "new account content");
    await settle(() => logout.resolve());
    expect(find("[data-account]").textContent).toBe(nextSession.user.id);
    expect(api.csrfToken).toBe(nextSession.csrfToken);
    expect(find("[data-location]").textContent).toBe("/app/settings");
    expect(client.getQueryData(["protected-content", nextSession.user.id])).toBe("new account content");
  });

  it("does not show a failed old password request on a newer account's form", async () => {
    const password = deferred<void>();
    vi.mocked(api.changePassword).mockReturnValue(password.promise);
    await mount("/app/settings", true);
    fillPasswordSettings();
    await submit(".password-settings");
    await settle(() => activeAuth.setSession(nextSession));
    await settle(() => password.reject(new ApiError(401, "unauthorized", "expired")));
    expect(find("[data-account]").textContent).toBe(nextSession.user.id);
    expect(api.csrfToken).toBe(nextSession.csrfToken);
    expect(container.querySelector('[role="alert"]')).toBeNull();
    expect(find<HTMLButtonElement>('.password-settings button[type="submit"]').disabled).toBe(false);
  });

  it("does not let a late session lookup replace a newer login", async () => {
    const lookup = deferred<Session>();
    vi.mocked(api.session).mockReturnValue(lookup.promise);
    vi.mocked(api.login).mockResolvedValue(nextSession);
    await mount("/login");
    fill("email", nextSession.user.email);
    fill("password", currentPassword);
    await submit();
    expect(find("[data-account]").textContent).toBe(nextSession.user.id);
    await settle(() => lookup.resolve(session));
    expect(find("[data-account]").textContent).toBe(nextSession.user.id);
    expect(api.csrfToken).toBe(nextSession.csrfToken);
    expect(find("[data-auth-error]").textContent).toBe("");
  });

  it("ignores a failed old session lookup after a newer login", async () => {
    const lookup = deferred<Session>();
    vi.mocked(api.session).mockReturnValue(lookup.promise);
    await mount("/login");
    await settle(() => activeAuth.setSession(nextSession));
    await settle(() => lookup.reject(new Error("old connection failure")));
    expect(find("[data-account]").textContent).toBe(nextSession.user.id);
    expect(find("[data-auth-error]").textContent).toBe("");
    expect(activeAuth.loading).toBe(false);
  });

  it("keeps a new login after the old session lookup receives an actual 401", async () => {
    const response = deferred<Response>();
    vi.mocked(api.session).mockRestore();
    vi.stubGlobal("fetch", vi.fn().mockReturnValue(response.promise));
    await mount("/login");
    await settle(() => activeAuth.setSession(nextSession));
    await settle(() => response.resolve(new Response(
      JSON.stringify({ error: { code: "unauthorized", message: "expired" } }),
      { status: 401 },
    )));
    expect(find("[data-account]").textContent).toBe(nextSession.user.id);
    expect(api.csrfToken).toBe(nextSession.csrfToken);
    expect(activeAuth.loading).toBe(false);
    expect(find("[data-auth-error]").textContent).toBe("");
  });

  it("finishes initial loading when the current session receives an actual 401", async () => {
    vi.mocked(api.session).mockRestore();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(
      JSON.stringify({ error: { code: "unauthorized", message: "expired" } }),
      { status: 401 },
    )));
    await mount("/login");
    expect(find("[data-account]").textContent).toBe("signed-out");
    expect(activeAuth.loading).toBe(false);
    expect(api.csrfToken).toBe("");
    expect(container.querySelector('input[name="password"]')).toBeTruthy();
  });

  it.each([false, true])(
    "reconciles the cookie written by a completed auth request after its form unmounted (register=%s)",
    async (register) => {
      const authentication = deferred<Session>();
      if (register) vi.mocked(api.register).mockReturnValue(authentication.promise);
      else vi.mocked(api.login).mockReturnValue(authentication.promise);
      await mount(register ? `/login?mode=register&inviteCode=${inviteCode}` : "/login");
      if (register) fillRegistration();
      else {
        fill("email", user.email);
        fill("password", currentPassword);
      }
      await submit();
      await settle(() => activeAuth.setSession(nextSession));
      expect(container.querySelector(".auth-form")).toBeNull();
      await settle(() => { void activeAuth.logout(); });
      expect(api.csrfToken).toBe("");
      // A browser applies this old auth response's Set-Cookie before the
      // promise settles. The next session lookup must reflect that cookie.
      vi.mocked(api.session).mockResolvedValue(session);
      client.setQueryData(["account-data", nextSession.user.id], "private data");
      await settle(() => authentication.resolve(session));
      await settle();
      expect(find("[data-account]").textContent).toBe(session.user.id);
      expect(api.csrfToken).toBe(session.csrfToken);
      expect(client.getQueryData(["account-data", nextSession.user.id])).toBeUndefined();
      expect(api.session).toHaveBeenCalledTimes(2);
    },
  );
});
