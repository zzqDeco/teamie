import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, TeamieClient } from "@teamie/api-client";
afterEach(() => {
  vi.unstubAllGlobals();
});
describe("account-bound transport", () => {
  it("sends relative same-origin writes with the current CSRF token", async () => {
    const fetch = vi
      .fn()
      .mockResolvedValue(
        new Response(JSON.stringify({ readSeq: 1 }), { status: 200 }),
      );
    vi.stubGlobal("fetch", fetch);
    const client = new TeamieClient();
    client.csrfToken = "current-session";
    await client.read("conversation/id", 1);
    expect(fetch.mock.calls[0][0]).toBe(
      "/api/v1/conversations/conversation%2Fid/read",
    );
    const options = fetch.mock.calls[0][1];
    expect(options.credentials).toBe("same-origin");
    expect(options.headers.get("X-CSRF-Token")).toBe("current-session");
    expect(JSON.parse(options.body)).toEqual({ seq: 1 });
  });
  it("does not let a late unauthorized response from the previous account log out the new account", async () => {
    let finish!: (response: Response) => void;
    vi.stubGlobal(
      "fetch",
      vi.fn(
        () =>
          new Promise<Response>((resolve) => {
            finish = resolve;
          }),
      ),
    );
    const client = new TeamieClient();
    client.csrfToken = "old-session";
    client.onUnauthorized = vi.fn();
    const request = client.bootstrap();
    client.csrfToken = "new-session";
    finish(
      new Response(
        JSON.stringify({ error: { code: "unauthorized", message: "expired" } }),
        { status: 401 },
      ),
    );
    await expect(request).rejects.toBeInstanceOf(ApiError);
    expect(client.onUnauthorized).not.toHaveBeenCalled();
  });
  it("invalidates protected cache on a current-account membership denial", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            error: { code: "forbidden", message: "removed" },
          }),
          { status: 403 },
        ),
      ),
    );
    const client = new TeamieClient();
    client.onForbidden = vi.fn();
    await expect(client.messages("c1")).rejects.toBeInstanceOf(ApiError);
    expect(client.onForbidden).toHaveBeenCalledOnce();
  });
});

describe("platform registration invitation transport", () => {
  it("fetches the public invite-only policy without caching initialization state", async () => {
    const fetch = vi
      .fn()
      .mockResolvedValue(
        new Response(
          JSON.stringify({ mode: "invite", bootstrapRequired: true }),
          { status: 200 },
        ),
      );
    vi.stubGlobal("fetch", fetch);
    const client = new TeamieClient();
    expect(await client.registration()).toEqual({
      mode: "invite",
      bootstrapRequired: true,
    });
    expect(fetch.mock.calls[0][0]).toBe("/api/v1/auth/registration");
    expect(fetch.mock.calls[0][1].cache).toBe("no-store");
    expect(fetch.mock.calls[0][1].body).toBeUndefined();
  });
  it("preserves the server-selected account role and does not send a registration role", async () => {
    const user = {
      id: "u1",
      displayName: "First partner",
      email: "first@example.test",
      kind: "human",
      platformRole: "admin",
      createdAt: "2026-10-16T07:00:00Z",
    };
    const fetch = vi
      .fn()
      .mockResolvedValue(
        new Response(JSON.stringify({ user, csrfToken: "session-token" }), {
          status: 201,
        }),
      );
    vi.stubGlobal("fetch", fetch);
    const client = new TeamieClient();
    const session = await client.register({
      email: user.email,
      displayName: user.displayName,
      password: "example-only-password",
      inviteCode: "example-single-use-invite",
    });
    expect(session.user.platformRole).toBe("admin");
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({
      email: user.email,
      displayName: user.displayName,
      password: "example-only-password",
      inviteCode: "example-single-use-invite",
    });
    expect(JSON.parse(fetch.mock.calls[0][1].body)).not.toHaveProperty(
      "platformRole",
    );
  });
  it("creates and revokes registration invitations through Identity with CSRF", async () => {
    const created = {
      invite: {
        id: "invite/id",
        code: "example-one-time-code",
        createdAt: "2026-10-16T07:00:00Z",
        expiresAt: "2026-10-23T07:00:00Z",
      },
      url: "/login?mode=register&inviteCode=example-one-time-code",
    };
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify(created), { status: 201 }),
      )
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetch);
    const client = new TeamieClient();
    client.csrfToken = "admin-session";
    expect(await client.createRegistrationInvite()).toEqual(created);
    expect(fetch.mock.calls[0][0]).toBe("/api/v1/auth/admin/invites");
    expect(fetch.mock.calls[0][1].method).toBe("POST");
    expect(fetch.mock.calls[0][1].body).toBe("{}");
    expect(fetch.mock.calls[0][1].headers.get("X-CSRF-Token")).toBe(
      "admin-session",
    );
    await client.revokeRegistrationInvite("invite/id");
    expect(fetch.mock.calls[1][0]).toBe(
      "/api/v1/auth/admin/invites/invite%2Fid",
    );
    expect(fetch.mock.calls[1][1].method).toBe("DELETE");
    expect(fetch.mock.calls[1][1].headers.get("X-CSRF-Token")).toBe(
      "admin-session",
    );
  });
  it("surfaces server administrator denial without granting workspace access or clearing membership data", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValue(
          new Response(
            JSON.stringify({
              error: { code: "forbidden", message: "admin required" },
            }),
            { status: 403 },
          ),
        ),
    );
    const client = new TeamieClient();
    client.onForbidden = vi.fn();
    await expect(client.registrationInvites()).rejects.toMatchObject({
      status: 403,
      code: "forbidden",
    });
    expect(client.onForbidden).not.toHaveBeenCalled();
  });
});
