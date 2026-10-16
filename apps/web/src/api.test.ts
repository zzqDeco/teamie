import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, TeamieClient } from "@teamie/api-client";
import type { Agent } from "@teamie/api-client";
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
