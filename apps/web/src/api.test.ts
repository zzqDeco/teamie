import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, TeamieClient } from "@teamie/api-client";
import type { Agent } from "@teamie/api-client";
afterEach(() => {
  vi.unstubAllGlobals();
});
describe("account-bound transport", () => {
  it("encodes literal history searches and file filters with current read authentication and cancellation", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ messages: [], hasMore: false })));
    vi.stubGlobal("fetch", fetch);
    const client = new TeamieClient(); client.csrfToken = "current-account";
    const controller = new AbortController();
    await client.searchMessages("conversation/id", { q: "10%_ & 报告", content: "files", beforeSeq: 65 }, controller.signal);
    const [url, options] = fetch.mock.calls[0];
    const params = new URL(url, "https://example.test").searchParams;
    expect(url).toContain("/conversations/conversation%2Fid/messages?");
    expect(Object.fromEntries(params)).toEqual({ limit: "50", q: "10%_ & 报告", content: "files", beforeSeq: "65" });
    expect(options.headers.get("X-CSRF-Token")).toBe("current-account");
    expect(options.credentials).toBe("same-origin"); expect(options.cache).toBe("no-store"); expect(options.signal).toBe(controller.signal);
  });
  it("binds protected reads to the browser nonce while leaving session discovery unbound", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response("{}"));
    vi.stubGlobal("fetch", fetch);
    const client = new TeamieClient();
    client.csrfToken = "browser-account";
    await client.bootstrap();
    fetch.mockResolvedValue(new Response("{}"));
    await client.session();
    expect(fetch.mock.calls[0][1].headers.get("X-CSRF-Token")).toBe("browser-account");
    expect(fetch.mock.calls[1][1].headers.has("X-CSRF-Token")).toBe(false);
  });
  it.each([[401, "session_changed"], [403, "csrf_failed"], [403, "invalid_csrf"]])(
    "reconciles a cookie/session mismatch without clearing it as an ordinary denial: %s %s",
    async (status, code) => {
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(
        JSON.stringify({ error: { code, message: "session changed" } }),
        { status: status as number },
      )));
      const client = new TeamieClient();
      client.csrfToken = "browser-account";
      client.onSessionChanged = vi.fn();
      client.onUnauthorized = vi.fn();
      client.onForbidden = vi.fn();
      await expect(client.messages("c1")).rejects.toMatchObject({ status, code });
      expect(client.onSessionChanged).toHaveBeenCalledOnce();
      expect(client.onUnauthorized).not.toHaveBeenCalled();
      expect(client.onForbidden).not.toHaveBeenCalled();
    },
  );
  it("discards an old protected success even when the session changes during body decoding", async () => {
    let finish!: (value: unknown) => void;
    const json = vi.fn(() => new Promise((resolve) => { finish = resolve; }));
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: true, status: 200, json }));
    const client = new TeamieClient();
    client.csrfToken = "previous-account";
    const pending = client.bootstrap();
    await vi.waitFor(() => expect(json).toHaveBeenCalledOnce());
    client.csrfToken = "current-account";
    finish({ conversations: [{ id: "previous-private-conversation" }] });
    await expect(pending).rejects.toMatchObject({ status: 409, code: "stale_session" });
  });
  it("changes the password using the authenticated CSRF-protected endpoint and handles an empty 204", async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetch);
    const client = new TeamieClient();
    client.csrfToken = "current-session";
    expect(await client.changePassword("example-old-password", "example-new-password")).toBeUndefined();
    expect(fetch.mock.calls[0][0]).toBe("/api/v1/auth/password");
    const options = fetch.mock.calls[0][1];
    expect(options.method).toBe("POST");
    expect(options.credentials).toBe("same-origin");
    expect(options.cache).toBe("no-store");
    expect(options.headers.get("X-CSRF-Token")).toBe("current-session");
    expect(options.headers.get("Content-Type")).toBe("application/json");
    expect(JSON.parse(options.body)).toEqual({
      currentPassword: "example-old-password",
      newPassword: "example-new-password",
    });
  });
  it.each([
    [400, "invalid_current_password", false],
    [400, "invalid_password", false],
    [429, "rate_limited", false],
    [401, "unauthorized", true],
  ])("only invalidates a password-change session for an actual 401: %s %s", async (status, code, unauthorized) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(
      JSON.stringify({ error: { code, message: "example error" } }),
      { status: status as number },
    )));
    const client = new TeamieClient();
    client.csrfToken = "current-session";
    client.onUnauthorized = vi.fn();
    await expect(client.changePassword("example-old-password", "example-new-password")).rejects.toMatchObject({ status, code });
    expect(client.onUnauthorized).toHaveBeenCalledTimes(unauthorized ? 1 : 0);
    expect(client.csrfToken).toBe("current-session");
  });
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
  it.each(["profile", "password"] as const)(
    "does not invalidate a new session for an old %s response with 401",
    async (endpoint) => {
      let finish!: (response: Response) => void;
      vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>((resolve) => { finish = resolve; })));
      const client = new TeamieClient();
      client.csrfToken = "old-session";
      client.onUnauthorized = vi.fn();
      const request = endpoint === "profile"
        ? client.profile("旧会话的资料")
        : client.changePassword("example-old-password", "example-new-password");
      client.csrfToken = "new-session";
      finish(new Response(
        JSON.stringify({ error: { code: "unauthorized", message: "expired" } }),
        { status: 401 },
      ));
      await expect(request).rejects.toMatchObject({ status: 401 });
      expect(client.onUnauthorized).not.toHaveBeenCalled();
      expect(client.csrfToken).toBe("new-session");
    },
  );
  it("rechecks the session after parsing an old unauthorized response", async () => {
    let finishBody!: (value: unknown) => void;
    const json = vi.fn(() => new Promise((resolve) => { finishBody = resolve; }));
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false, status: 401, json }));
    const client = new TeamieClient();
    client.csrfToken = "old-session";
    client.onUnauthorized = vi.fn();
    const request = client.profile("旧会话的资料");
    await vi.waitFor(() => expect(json).toHaveBeenCalledOnce());
    client.csrfToken = "new-session";
    finishBody({ error: { code: "unauthorized", message: "expired" } });
    await expect(request).rejects.toMatchObject({ status: 401 });
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
    const fetch = vi.fn().mockResolvedValue(
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
      vi.fn().mockResolvedValue(
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

describe("Agent and model gateway boundaries", () => {
  const profile: Agent = {
    id: "agent-profile",
    principalId: "agent-profile",
    ownerId: "owner",
    ownerName: "创建者",
    name: "团队伙伴",
    description: "整理团队想法",
    workspaceIds: ["visible-team", "private-team"],
    enabled: true,
    createdAt: "2026-10-16T07:00:00Z",
    updatedAt: "2026-10-16T07:00:00Z",
  };
  const unsafeProfile = {
    ...profile,
    revision: 42,
    runtimeState: "running",
    sessionId: "SESSION-SENTINEL",
    lastActiveAt: "ACTIVITY-SENTINEL",
    lastError: "ERROR-SENTINEL",
    deletedAt: "DELETE-SENTINEL",
    activeRunId: "RUN-ID-SENTINEL",
    runs: [{ id: "RUN-ID-SENTINEL", inputCount: 111 }],
    tasks: [
      { id: "TASK-ID-SENTINEL", description: "TASK-DESCRIPTION-SENTINEL" },
    ],
    events: [{ toolName: "TOOL-SENTINEL", errorCode: "ERROR-CODE-SENTINEL" }],
  };
  it("projects only public Agent profile fields from every list/read/write response", async () => {
    const fetch = vi.fn(
      async (url: string) =>
        new Response(
          JSON.stringify(
            url.endsWith("/agents")
              ? { agents: [unsafeProfile], agent: unsafeProfile }
              : { agent: unsafeProfile },
          ),
          { status: 200 },
        ),
    );
    vi.stubGlobal("fetch", fetch);
    const client = new TeamieClient();
    const values = [
      (await client.agents()).agents[0],
      (await client.agent(profile.id)).agent,
      (
        await client.createAgent({
          name: profile.name,
          workspaceIds: profile.workspaceIds,
        })
      ).agent,
      (await client.updateAgent(profile.id, { enabled: false })).agent,
    ];
    for (const value of values) {
      expect(value).toEqual(profile);
      expect(JSON.stringify(value)).not.toContain("SENTINEL");
    }
    expect(fetch.mock.calls.map(([url]) => url)).toEqual([
      "/api/v1/agents",
      "/api/v1/agents/agent-profile",
      "/api/v1/agents",
      "/api/v1/agents/agent-profile",
    ]);
  });
  it("limits discovery metadata to the current workspace even if another selected team is returned", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(JSON.stringify({ agents: [unsafeProfile] }), {
            status: 200,
          }),
      ),
    );
    const discovered = await new TeamieClient().workspaceAgents("visible-team");
    expect(discovered.agents[0]).toEqual({
      ...profile,
      workspaceIds: ["visible-team"],
    });
    expect(JSON.stringify(discovered)).not.toMatch(/private-team|SENTINEL/);
  });
  it("does not provide runtime, task, event or recovery browser methods or a public arbitrary-path transport", () => {
    const client = new TeamieClient();
    for (const method of [
      "request",
      "agentRuntime",
      "agentRuns",
      "agentRun",
      "agentTasks",
      "agentTask",
      "agentEvents",
      "agentEventsURL",
      "cancelAgentRun",
      "cancelAgentTask",
      "retryAgentRun",
    ]) {
      expect(client).not.toHaveProperty(method);
    }
  });
  it("sends only basic profile changes even when an input object includes internal metadata", async () => {
    const fetch = vi.fn<typeof globalThis.fetch>(
      async () =>
        new Response(JSON.stringify({ agent: unsafeProfile }), { status: 200 }),
    );
    vi.stubGlobal("fetch", fetch);
    const client = new TeamieClient();
    await client.createAgent(unsafeProfile);
    await client.updateAgent(profile.id, { ...unsafeProfile, enabled: false });
    for (const call of fetch.mock.calls) {
      const options = call[1] as RequestInit;
      const body = JSON.parse(options.body as string);
      expect(Object.keys(body).sort()).toEqual([
        "description",
        "enabled",
        "name",
        "workspaceIds",
      ]);
      expect(JSON.stringify(body)).not.toContain("SENTINEL");
      expect(body).not.toHaveProperty("ownerId");
    }
  });
  it("strips any accidentally returned credential fields from gateway read and save results", async () => {
    const response = {
      config: {
        baseUrl: "https://model.example.test/v1",
        model: "model-id",
        hasApiKey: true,
        apiKey: "example-never-returned-key",
        encryptedApiKey: "example-ciphertext",
        enabled: true,
        configured: true,
        updatedAt: "2026-10-16T07:00:00Z",
      },
      status: "ready",
      apiKey: "example-top-level-secret",
    };
    const fetch = vi
      .fn()
      .mockImplementation(
        async () => new Response(JSON.stringify(response), { status: 200 }),
      );
    vi.stubGlobal("fetch", fetch);
    const client = new TeamieClient();
    client.csrfToken = "admin-session";
    const read = await client.modelGateway();
    const saved = await client.saveModelGateway({
      baseUrl: response.config.baseUrl,
      model: response.config.model,
      apiKey: "example-request-key",
      enabled: true,
    });
    for (const value of [read, saved]) {
      expect(JSON.stringify(value)).not.toContain("example-never-returned-key");
      expect(JSON.stringify(value)).not.toContain("example-ciphertext");
      expect(JSON.stringify(value)).not.toContain("example-top-level-secret");
      expect(value.config).not.toHaveProperty("apiKey");
    }
    expect(fetch.mock.calls[1][0]).toBe("/api/v1/admin/model-gateway");
    expect(fetch.mock.calls[1][0]).not.toContain("example-request-key");
    expect(fetch.mock.calls[1][1].method).toBe("PUT");
    expect(fetch.mock.calls[1][1].headers.get("X-CSRF-Token")).toBe(
      "admin-session",
    );
  });
  it("keeps Agent ownership checks on the server and creates group members through the shared IM endpoint", async () => {
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            error: { code: "forbidden", message: "owner only" },
          }),
          { status: 403 },
        ),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ conversation: { id: "group-1" } }), {
          status: 200,
        }),
      );
    vi.stubGlobal("fetch", fetch);
    const client = new TeamieClient();
    client.csrfToken = "human-session";
    client.onForbidden = vi.fn();
    await expect(
      client.updateAgent("other-agent", { enabled: false }),
    ).rejects.toMatchObject({ status: 403 });
    expect(client.onForbidden).not.toHaveBeenCalled();
    await client.addConversationMember("group-1", "agent-1");
    expect(fetch.mock.calls[1][0]).toBe(
      "/api/v1/conversations/group-1/members",
    );
    expect(JSON.parse(fetch.mock.calls[1][1].body)).toEqual({
      userId: "agent-1",
    });
    expect(fetch.mock.calls[1][1].headers.get("X-CSRF-Token")).toBe(
      "human-session",
    );
  });
  it("only tests the saved model through an explicit dedicated POST without credentials", async () => {
    const fetch = vi
      .fn()
      .mockResolvedValue(
        new Response(
          JSON.stringify({ ok: true, model: "saved-model", latencyMs: 32 }),
          { status: 200 },
        ),
      );
    vi.stubGlobal("fetch", fetch);
    const client = new TeamieClient();
    client.csrfToken = "admin-session";
    expect(await client.testModelGateway()).toMatchObject({
      ok: true,
      model: "saved-model",
    });
    expect(fetch.mock.calls[0][0]).toBe("/api/v1/admin/model-gateway/test");
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({});
  });
});
