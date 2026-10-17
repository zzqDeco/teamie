import { afterEach, describe, expect, it, vi } from "vitest";
import { TeamieClient } from "@teamie/api-client";

afterEach(() => vi.unstubAllGlobals());
const token = "example-reset-capability";
const password = "example-new-password";

describe("password reset capability transport", () => {
  it("inspects without consuming and redeems using only JSON bodies, no cookies or CSRF", async () => {
    const inspection = { account: { id: "target", displayName: "伙伴", emailMasked: "p***@example.test" }, expiresAt: "2026-10-17T10:00:00Z" };
    const fetch = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(inspection), { status: 200 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetch);
    const client = new TeamieClient();
    client.csrfToken = "unrelated-current-session";
    expect(await client.inspectPasswordReset(token)).toEqual(inspection);
    expect(await client.resetPassword(token, password)).toBeUndefined();
    expect(fetch.mock.calls.map(([path]) => path)).toEqual(["/api/v1/auth/password-reset/inspect", "/api/v1/auth/password-reset"]);
    for (const [, options] of fetch.mock.calls) {
      expect(options.method).toBe("POST");
      expect(options.credentials).toBe("omit");
      expect(options.cache).toBe("no-store");
      expect(options.headers.get("X-CSRF-Token")).toBeNull();
      expect(options.headers.get("Content-Type")).toBe("application/json");
    }
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({ token });
    expect(JSON.parse(fetch.mock.calls[1][1].body)).toEqual({ token, newPassword: password });
    expect(client.csrfToken).toBe("unrelated-current-session");
  });
  it.each(["inspect", "redeem"] as const)("does not invalidate an unrelated login even if public %s returns 401", async (operation) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { code: "invalid_reset_link", message: "invalid" } }), { status: 401 })));
    const client = new TeamieClient();
    client.csrfToken = "newer-session";
    client.onUnauthorized = vi.fn();
    client.onForbidden = vi.fn();
    await expect(operation === "inspect" ? client.inspectPasswordReset(token) : client.resetPassword(token, password)).rejects.toMatchObject({ status: 401 });
    expect(client.onUnauthorized).not.toHaveBeenCalled();
    expect(client.onForbidden).not.toHaveBeenCalled();
    expect(client.csrfToken).toBe("newer-session");
  });
});

describe("admin password recovery transport", () => {
  it("uses an encoded admin user path with an empty CSRF-protected POST and preserves the one-time response", async () => {
    const created = { reset: { id: "reset-id", createdAt: "2026-10-17T09:00:00Z", expiresAt: "2026-10-17T10:00:00Z" }, url: `/reset-password#token=${token}` };
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(created), { status: 201 }));
    vi.stubGlobal("fetch", fetch);
    const client = new TeamieClient();
    client.csrfToken = "admin-session";
    expect(await client.createPasswordResetLink("user/with?chars")).toEqual(created);
    expect(fetch.mock.calls[0][0]).toBe("/api/v1/auth/admin/users/user%2Fwith%3Fchars/password-reset-links");
    const options = fetch.mock.calls[0][1];
    expect(options.method).toBe("POST");
    expect(options.credentials).toBe("same-origin");
    expect(options.cache).toBe("no-store");
    expect(options.headers.get("X-CSRF-Token")).toBe("admin-session");
    expect(JSON.parse(options.body)).toEqual({});
  });
  it("encodes list filtering and pagination and projects only the public User contract", async () => {
    const user = { id: "target", email: "target@example.test", displayName: "伙伴", kind: "human", platformRole: "user", createdAt: "2026-10-17T09:00:00Z" };
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ users: [{ ...user, passwordHash: "SECRET-SENTINEL", resetToken: token }], cursor: "next/cursor" }), { status: 200 }));
    vi.stubGlobal("fetch", fetch);
    const page = await new TeamieClient().platformUsers({ q: "name & user@example.test", cursor: "cursor?part", limit: 20 });
    const url = new URL(fetch.mock.calls[0][0], "https://teamie.example.test");
    expect(url.pathname).toBe("/api/v1/auth/admin/users");
    expect(url.searchParams.get("q")).toBe("name & user@example.test");
    expect(url.searchParams.get("cursor")).toBe("cursor?part");
    expect(url.searchParams.get("limit")).toBe("20");
    expect(page).toEqual({ users: [user], cursor: "next/cursor" });
    expect(JSON.stringify(page)).not.toMatch(/SECRET-SENTINEL|example-reset-capability/);
  });
  it("preserves admin-only denials without triggering workspace permission handling", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(async () => new Response(JSON.stringify({ error: { code: "forbidden", message: "admin required" } }), { status: 403 })));
    const client = new TeamieClient();
    client.onForbidden = vi.fn();
    await expect(client.platformUsers()).rejects.toMatchObject({ status: 403 });
    await expect(client.createPasswordResetLink("target")).rejects.toMatchObject({ status: 403 });
    expect(client.onForbidden).not.toHaveBeenCalled();
  });
});
