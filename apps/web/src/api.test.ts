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
      vi
        .fn()
        .mockResolvedValue(
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
