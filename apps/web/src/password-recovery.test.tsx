// @vitest-environment jsdom
import { act, StrictMode, useEffect } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter, Route, Routes, useNavigate } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@teamie/api-client";
import type { CreatedPasswordResetLink, PasswordResetInspection, Session, User } from "@teamie/api-client";
import { AuthProvider, useAuth } from "./auth";
import { Login } from "./public";
import { ForgotPassword, ResetPassword } from "./password-recovery";
import { PlatformUsers } from "./platform-users";
import { chatStore } from "./store";

const user: User = { id: "target-user", kind: "human", platformRole: "user", displayName: "测试伙伴", email: "target@example.test", createdAt: "2026-10-17T00:00:00Z" };
const admin: User = { ...user, id: "admin-a", displayName: "管理员甲", platformRole: "admin" };
const other: User = { ...user, id: "other-user", displayName: "另一位伙伴" };
const session = (account: User, csrfToken = `example-session-${account.id}`): Session => ({ user: account, csrfToken });
const token = "example-fragment-reset-capability";
const password = "example-new-password";
const inspection = (): PasswordResetInspection => ({ account: { id: user.id, displayName: user.displayName, emailMasked: "t***@example.test" }, expiresAt: new Date(Date.now() + 3_600_000).toISOString() });
const created = (value = token): CreatedPasswordResetLink => ({ reset: { id: "example-reset-id", createdAt: new Date().toISOString(), expiresAt: new Date(Date.now() + 3_600_000).toISOString() }, url: `/reset-password#token=${value}` });
let container: HTMLDivElement;
let root: Root;
let client: QueryClient;
let activeAuth: ReturnType<typeof useAuth>;
let navigate: ReturnType<typeof useNavigate>;
function Probe() {
  const auth = useAuth();
  const go = useNavigate();
  useEffect(() => { activeAuth = auth; navigate = go; }, [auth, go]);
  return <output data-account>{auth.loading ? "loading" : auth.user?.id || "signed-out"}</output>;
}
async function settle(action?: () => void) {
  await act(async () => { action?.(); await new Promise((resolve) => setTimeout(resolve, 0)); });
}
async function mount(path = `/reset-password#token=${token}`, account?: User, strict = false) {
  if (account) vi.mocked(api.session).mockResolvedValue(session(account));
  window.history.replaceState(null, "", path);
  const app = <QueryClientProvider client={client}><MemoryRouter initialEntries={[path]}><AuthProvider><Routes><Route path="/login" element={<Login />} /><Route path="/forgot-password" element={<ForgotPassword />} /><Route path="/reset-password" element={<ResetPassword />} /><Route path="/app/admin/users" element={<PlatformUsers />} /><Route path="/app" element={<p>当前工作空间</p>} /></Routes><Probe /></AuthProvider></MemoryRouter></QueryClientProvider>;
  await settle(() => root.render(strict ? <StrictMode>{app}</StrictMode> : app));
  await settle();
}
function find<T extends HTMLElement>(selector: string): T {
  const element = container.querySelector<T>(selector);
  if (!element) throw new Error(`Missing ${selector}`);
  return element;
}
async function click(selector: string) { await settle(() => find<HTMLElement>(selector).click()); }
async function clickText(text: string) {
  const button = Array.from(container.querySelectorAll("button")).find((element) => element.textContent?.trim() === text);
  if (!button) throw new Error(`Missing button ${text}`);
  await settle(() => button.click());
}
function fill(name: string, value: string) { find<HTMLInputElement>(`input[name="${name}"]`).value = value; }
async function submit(confirmation = password, value = password) {
  fill("newPassword", value); fill("confirmPassword", confirmation);
  await settle(() => find<HTMLFormElement>(".recovery-form form").dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}
async function openReset() { await click(`button[aria-label="为 ${user.displayName} 重置密码"]`); }
beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: vi.fn().mockResolvedValue(undefined) } });
  api.csrfToken = "";
  vi.spyOn(api, "session").mockRejectedValue(new ApiError(401, "unauthorized", "unauthorized"));
  vi.spyOn(api, "inspectPasswordReset").mockImplementation(async () => inspection());
  vi.spyOn(api, "resetPassword").mockResolvedValue(undefined);
  vi.spyOn(api, "platformUsers").mockResolvedValue({ users: [user] });
  vi.spyOn(api, "createPasswordResetLink").mockImplementation(async () => created());
  vi.spyOn(api, "login").mockResolvedValue(session(user));
  container = document.createElement("div"); document.body.append(container); root = createRoot(container);
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
});
afterEach(async () => {
  await settle(() => root.unmount()); client.clear(); chatStore.clear(); container.remove();
  api.csrfToken = ""; localStorage.clear(); sessionStorage.clear(); window.history.replaceState(null, "", "/");
  vi.restoreAllMocks(); vi.unstubAllGlobals();
});

describe("public password recovery", () => {
  it("connects login to concise administrator instructions without a fake email form", async () => {
    await mount("/login");
    await click('a[href="/forgot-password"]');
    expect(container.textContent).toContain("核实身份后获取一次性密码重置链接");
    expect(container.querySelector("form")).toBeNull();
    expect(api.inspectPasswordReset).not.toHaveBeenCalled();
    expect(api.platformUsers).not.toHaveBeenCalled();
  });
  it("retains the fragment token across StrictMode initialization, clears the URL promptly, and keeps secrets out of caches/storage", async () => {
    await mount(undefined, undefined, true);
    expect(window.location.hash).toBe("");
    expect(window.location.search).toBe("");
    expect(api.inspectPasswordReset).toHaveBeenCalled();
    for (const call of vi.mocked(api.inspectPasswordReset).mock.calls) expect(call).toEqual([token]);
    expect(api.resetPassword).not.toHaveBeenCalled();
    expect(container.textContent).toContain("t***@example.test");
    expect(container.textContent).not.toContain(token);
    expect(container.innerHTML).not.toContain(token);
    expect(JSON.stringify(client.getQueryCache().getAll())).not.toContain(token);
    expect(localStorage.length).toBe(0); expect(sessionStorage.length).toBe(0);
    await submit();
    expect(api.resetPassword).toHaveBeenCalledExactlyOnceWith(token, password);
    expect(container.textContent).toContain("密码已重置");
  });
  it.each(["/reset-password", "/reset-password#other=value", "/reset-password#token=one&token=two"])("rejects missing or ambiguous capabilities locally: %s", async (path) => {
    await mount(path);
    expect(api.inspectPasswordReset).not.toHaveBeenCalled();
    expect(container.textContent).toContain("链接无法使用");
    expect(window.location.hash).toBe("");
  });
  it("shows the same recovery path for invalid, used, replaced or expired server links", async () => {
    vi.mocked(api.inspectPasswordReset).mockRejectedValue(new ApiError(400, "invalid_reset_link", "invalid"));
    await mount();
    expect(container.textContent).toContain("链接可能已过期、已使用，或被新链接替代");
    expect(container.querySelector('a[href="/forgot-password"]')).not.toBeNull();
    expect(container.querySelector("form")).toBeNull();
    expect(api.resetPassword).not.toHaveBeenCalled();
  });
  it("recognizes known expiry without offering a password submission", async () => {
    vi.mocked(api.inspectPasswordReset).mockResolvedValue({ ...inspection(), expiresAt: new Date(Date.now() - 1).toISOString() });
    await mount();
    expect(container.textContent).toContain("链接已过期");
    expect(container.querySelector("form")).toBeNull();
    expect(api.resetPassword).not.toHaveBeenCalled();
  });
  it("keeps mismatched confirmations and overlong UTF-8 passwords local", async () => {
    await mount();
    await submit("example-different-password");
    expect(api.resetPassword).not.toHaveBeenCalled();
    expect(container.textContent).toContain("两次输入的新密码不一致");
    expect(document.activeElement).toBe(find('input[name="confirmPassword"]'));
    await submit("密".repeat(25), "密".repeat(25));
    expect(api.resetPassword).not.toHaveBeenCalled();
    expect(container.textContent).toContain("72 字节以内");
  });
  it("clears the matching revoked local session and sensitive form after success, without logging in", async () => {
    await mount(undefined, user);
    client.setQueryData(["private-target-data"], { body: "old data" });
    const inputs = Array.from(container.querySelectorAll<HTMLInputElement>('input[type="password"]'));
    await submit();
    expect(api.resetPassword).toHaveBeenCalledExactlyOnceWith(token, password);
    expect(find("[data-account]").textContent).toBe("signed-out");
    expect(api.csrfToken).toBe(""); expect(client.getQueryData(["private-target-data"])).toBeUndefined();
    expect(inputs.map((input) => input.value)).toEqual(["", ""]);
    expect(container.textContent).toContain("密码已重置");
    expect(api.login).not.toHaveBeenCalled();
  });
  it.each(["other", "same-subject"] as const)("preserves a newer %s login when an older successful reset response arrives", async (kind) => {
    const request = deferred<void>(); vi.mocked(api.resetPassword).mockReturnValueOnce(request.promise);
    await mount(undefined, user); await submit();
    const newer = session(kind === "other" ? other : user, "example-newer-session");
    await settle(() => activeAuth.setSession(newer));
    client.setQueryData(["newer-private-data"], "preserved");
    await settle(() => request.resolve(undefined));
    expect(find("[data-account]").textContent).toBe(newer.user.id);
    expect(api.csrfToken).toBe("example-newer-session");
    expect(client.getQueryData(["newer-private-data"])).toBe("preserved");
    expect(container.textContent).toContain("密码已重置");
  });
  it("keeps an unrelated account and its cache after resetting the target", async () => {
    await mount(undefined, other); client.setQueryData(["other-account"], "preserved");
    await submit();
    expect(find("[data-account]").textContent).toBe(other.id);
    expect(api.csrfToken).toBe(session(other).csrfToken);
    expect(client.getQueryData(["other-account"])).toBe("preserved");
    expect(container.textContent).toContain("返回当前账号");
  });
  it("preserves a valid link after an equivalent-password error and permits a corrected retry", async () => {
    vi.mocked(api.resetPassword).mockRejectedValueOnce(new ApiError(400, "password_unchanged", "same"));
    await mount(); await submit();
    expect(container.textContent).toContain("新密码不能与原密码相同");
    expect(find<HTMLInputElement>('input[name="newPassword"]').value).toBe(password);
    expect(api.inspectPasswordReset).toHaveBeenCalledTimes(1);
    await submit("example-another-password", "example-another-password");
    expect(api.resetPassword).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain("密码已重置");
  });
  it("handles a link consumed after inspection without clearing the current account", async () => {
    vi.mocked(api.resetPassword).mockRejectedValue(new ApiError(400, "invalid_reset_link", "used"));
    await mount(undefined, user); await submit();
    expect(container.textContent).toContain("链接无法使用");
    expect(container.querySelector("form")).toBeNull();
    expect(find("[data-account]").textContent).toBe(user.id);
  });
  it("can retry a transient inspection failure and ignores a result after leaving", async () => {
    vi.mocked(api.inspectPasswordReset).mockRejectedValueOnce(new TypeError("network"));
    await mount();
    expect(container.textContent).toContain("暂时无法验证链接");
    const request = deferred<PasswordResetInspection>(); vi.mocked(api.inspectPasswordReset).mockReturnValueOnce(request.promise);
    await clickText("重新验证链接");
    await settle(() => navigate("/forgot-password"));
    await settle(() => request.resolve(inspection()));
    expect(container.querySelector("form")).toBeNull();
    expect(container.textContent).toContain("如何找回账户");
  });
  it("revalidates same-page link navigation and ignores the previous inspection", async () => {
    const previous = deferred<PasswordResetInspection>();
    vi.mocked(api.inspectPasswordReset).mockReturnValueOnce(previous.promise);
    await mount();
    vi.mocked(api.inspectPasswordReset).mockResolvedValueOnce({ ...inspection(), account: { id: other.id, displayName: "新链接账号", emailMasked: "n***@example.test" } });
    await settle(() => navigate("/reset-password#token=example-next-capability"));
    await settle(() => previous.resolve({ ...inspection(), account: { ...inspection().account, displayName: "旧链接账号哨兵" } }));
    expect(api.inspectPasswordReset).toHaveBeenLastCalledWith("example-next-capability");
    expect(container.textContent).toContain("新链接账号");
    expect(container.textContent).not.toContain("旧链接账号哨兵");
  });
  it("inspects an already-used link again when the same route is reopened after success", async () => {
    await mount(); await submit();
    vi.mocked(api.inspectPasswordReset).mockRejectedValueOnce(new ApiError(400, "invalid_reset_link", "used"));
    await settle(() => navigate(`/reset-password#token=${token}`));
    expect(api.inspectPasswordReset).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain("链接无法使用");
  });
  it("captures native hash-only navigation even when the router emits no popstate", async () => {
    await mount(); await submit();
    vi.mocked(api.inspectPasswordReset).mockRejectedValueOnce(new ApiError(400, "invalid_reset_link", "used"));
    await settle(() => {
      const oldURL = window.location.href;
      const newURL = new URL(`/reset-password#token=${token}`, oldURL).href;
      window.history.replaceState(null, "", newURL);
      window.dispatchEvent(new HashChangeEvent("hashchange", { oldURL, newURL }));
    });
    expect(api.inspectPasswordReset).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain("链接无法使用");
    expect(window.location.hash).toBe("");
  });
});

describe("administrator one-time reset link UI", () => {
  it("does not request a user directory for ordinary users", async () => {
    await mount("/app/admin/users", user);
    expect(container.textContent).toContain("需要平台管理员权限");
    expect(api.platformUsers).not.toHaveBeenCalled(); expect(api.createPasswordResetLink).not.toHaveBeenCalled();
  });
  it("generates only after explicit confirmation, copies only on click, and discards the link when closed", async () => {
    await mount("/app/admin/users", admin); await openReset();
    expect(api.createPasswordResetLink).not.toHaveBeenCalled();
    await clickText("生成重置链接");
    expect(api.createPasswordResetLink).toHaveBeenCalledExactlyOnceWith(user.id);
    expect(find<HTMLInputElement>('input[aria-label="一次性重置链接"]').value).toContain(token);
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled();
    expect(container.textContent).toContain("1 小时");
    await click('button[aria-label="复制重置链接"]');
    expect(navigator.clipboard.writeText).toHaveBeenCalledExactlyOnceWith(new URL(created().url, location.origin).href);
    expect(container.textContent).toContain("链接已复制");
    expect(JSON.stringify(client.getQueryCache().getAll().map((query) => query.state.data))).not.toContain(token);
    await clickText("关闭");
    expect(container.innerHTML).not.toContain(token); expect(container.querySelector('[role="dialog"]')).toBeNull();
    await openReset(); expect(container.innerHTML).not.toContain(token);
  });
  it("discards a late issuance after its dialog closes", async () => {
    const request = deferred<CreatedPasswordResetLink>(); vi.mocked(api.createPasswordResetLink).mockReturnValueOnce(request.promise);
    await mount("/app/admin/users", admin); await openReset(); await clickText("生成重置链接");
    await click('button[aria-label="关闭弹窗"]'); await openReset();
    await settle(() => request.resolve(created()));
    expect(container.innerHTML).not.toContain(token); expect(container.textContent).not.toContain("重置链接已生成");
    expect(container.textContent).toContain("生成密码重置链接");
  });
  it.each(["other-admin", "revoked-role"] as const)("never reveals an old admin's issuance to a %s session", async (kind) => {
    const request = deferred<CreatedPasswordResetLink>(); vi.mocked(api.createPasswordResetLink).mockReturnValueOnce(request.promise);
    await mount("/app/admin/users", admin); await openReset(); await clickText("生成重置链接");
    const next = kind === "other-admin" ? { ...other, platformRole: "admin" as const } : { ...admin, platformRole: "user" as const };
    await settle(() => activeAuth.setSession(session(next)));
    await settle(() => request.resolve(created()));
    expect(container.innerHTML).not.toContain(token); expect(container.querySelector('[role="dialog"]')).toBeNull();
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled();
    if (kind === "revoked-role") expect(container.textContent).toContain("需要平台管理员权限");
  });
  it("isolates a late user-directory result by authenticated subject and role", async () => {
    const request = deferred<{ users: User[] }>(); vi.mocked(api.platformUsers).mockReturnValueOnce(request.promise).mockResolvedValue({ users: [{ ...other, displayName: "当前账号可见用户" }] });
    await mount("/app/admin/users", admin);
    await settle(() => activeAuth.setSession(session({ ...other, platformRole: "admin" })));
    await settle(() => request.resolve({ users: [{ ...user, displayName: "旧账号用户哨兵" }] }));
    expect(container.textContent).toContain("当前账号可见用户"); expect(container.textContent).not.toContain("旧账号用户哨兵");
    expect(client.getQueryCache().getAll().map((query) => query.queryKey)).toContainEqual(["platformUsers", other.id, "admin", "", ""]);
  });
  it("clears the raw link while regenerating and exposes only the replacement", async () => {
    await mount("/app/admin/users", admin); await openReset(); await clickText("生成重置链接");
    const request = deferred<CreatedPasswordResetLink>(); vi.mocked(api.createPasswordResetLink).mockReturnValueOnce(request.promise);
    await clickText("重新生成链接"); expect(container.innerHTML).not.toContain(token);
    await settle(() => request.resolve(created("example-replacement-capability")));
    expect(find<HTMLInputElement>('input[aria-label="一次性重置链接"]').value).toContain("example-replacement-capability");
    expect(container.innerHTML).not.toContain(token);
  });
  it("removes the dialog on a server-side administrator denial", async () => {
    vi.mocked(api.createPasswordResetLink).mockRejectedValue(new ApiError(403, "forbidden", "admin only"));
    await mount("/app/admin/users", admin); await openReset(); await clickText("生成重置链接");
    expect(container.textContent).toContain("需要平台管理员权限"); expect(container.querySelector('[role="dialog"]')).toBeNull();
  });
  it("keeps users readable and explains disabled issuance without removing administrator access", async () => {
    vi.mocked(api.createPasswordResetLink).mockRejectedValue(new ApiError(403, "feature_disabled", "disabled"));
    await mount("/app/admin/users", admin); await openReset(); await clickText("生成重置链接");
    expect(container.textContent).toContain("密码重置暂时关闭");
    expect(container.textContent).toContain(user.displayName);
    expect(container.textContent).not.toContain("需要平台管理员权限");
    expect(container.querySelector('[role="dialog"]')).not.toBeNull();
  });
  it("preserves an existing one-time link when new issuance is disabled", async () => {
    await mount("/app/admin/users", admin); await openReset(); await clickText("生成重置链接");
    const original = find<HTMLInputElement>('input[aria-label="一次性重置链接"]').value;
    vi.mocked(api.createPasswordResetLink).mockRejectedValueOnce(new ApiError(403, "feature_disabled", "disabled"));
    await clickText("重新生成链接");
    expect(container.textContent).toContain("密码重置暂时关闭");
    expect(find<HTMLInputElement>('input[aria-label="一次性重置链接"]').value).toBe(original);
    await click('button[aria-label="复制重置链接"]');
    expect(navigator.clipboard.writeText).toHaveBeenCalledExactlyOnceWith(original);
    expect(container.textContent).not.toContain("需要平台管理员权限");
  });
});
