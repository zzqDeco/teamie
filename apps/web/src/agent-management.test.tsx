import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { Agent, User, Workspace } from "@teamie/api-client";
import { ApiError } from "@teamie/api-client";
import {
  agentProfileChanges,
  agentProfileInput,
  canConfigureModelGateway,
  canManageAgent,
  displayAgentState,
  gatewayFailureText,
  modelGatewayInput,
  snapshotAgentProfile,
} from "./agent-management";
import {
  AgentBadge,
  GatewayConfigurationFields,
  WaitingForConfiguration,
  WorkspaceVisibilityFields,
} from "./agent-ui";
import { enrichParticipants } from "./participants";
const user = (
  id: string,
  platformRole: User["platformRole"] = "user",
  kind: User["kind"] = "human",
): User => ({
  id,
  platformRole,
  kind,
  displayName: id,
  email: `${id}@example.test`,
  createdAt: "2026-10-16T07:00:00Z",
});
const workspace: Workspace = {
  id: "workspace-1",
  name: "可见团队",
  createdBy: "owner",
  createdAt: "2026-10-16T07:00:00Z",
};
const agent: Agent = {
  id: "agent-1",
  principalId: "agent-1",
  ownerId: "owner",
  ownerName: "创建者",
  name: "产品伙伴",
  description: "整理团队想法",
  workspaceIds: [workspace.id],
  enabled: true,
  revision: 1,
  createdAt: "2026-10-16T07:00:00Z",
  updatedAt: "2026-10-16T07:00:00Z",
  runtimeState: "waiting_configuration",
  sessionId: "agent:agent-1",
};
describe("Agent ownership and visible teams", () => {
  it("lets only the human owner manage an Agent, without a platform admin bypass", () => {
    expect(canManageAgent(user("owner"), agent)).toBe(true);
    expect(canManageAgent(user("other-admin", "admin"), agent)).toBe(false);
    expect(canManageAgent(user("owner", "admin", "agent"), agent)).toBe(false);
    expect(canManageAgent(null, agent)).toBe(false);
  });
  it("allows only human platform admins to configure the model gateway", () => {
    expect(canConfigureModelGateway(user("admin", "admin"))).toBe(true);
    expect(canConfigureModelGateway(user("owner"))).toBe(false);
    expect(canConfigureModelGateway(user("agent", "admin", "agent"))).toBe(
      false,
    );
    expect(canConfigureModelGateway(null)).toBe(false);
  });
  it("submits only membership-backed visibility and basic profile fields", () => {
    const profile = agentProfileInput(
      "  产品伙伴  ",
      "  公开简介  ",
      [workspace.id, workspace.id],
      [workspace],
      true,
    );
    expect(profile).toEqual({
      name: "产品伙伴",
      description: "公开简介",
      workspaceIds: [workspace.id],
      enabled: true,
    });
    expect(() =>
      agentProfileInput("伙伴", "", ["other-private-team"], [workspace], true),
    ).toThrow("已加入的工作空间");
    expect(
      agentProfileInput("伙伴", "", [], [workspace], false).workspaceIds,
    ).toEqual([]);
    expect(profile).not.toHaveProperty("ownerId");
    expect(profile).not.toHaveProperty("model");
    expect(profile).not.toHaveProperty("prompt");
  });
  it("renders explicit team checkboxes and checked scope without inventing other memberships", () => {
    const html = renderToStaticMarkup(
      <WorkspaceVisibilityFields
        workspaces={[workspace]}
        selectedIds={[workspace.id]}
        onChange={() => {}}
      />,
    );
    expect(html).toContain("可见团队");
    expect(html).toContain('type="checkbox"');
    expect(html).toContain("checked");
    expect(html).not.toContain("other-private-team");
  });
  it("omits unchanged admission and enablement when a profile form outlives moderator revocation", () => {
    const latest = { ...agent, workspaceIds: [...agent.workspaceIds] };
    const initial = snapshotAgentProfile(latest);
    const profile = agentProfileInput(
      "新的公开名称",
      initial.description,
      [...initial.workspaceIds],
      [workspace],
      initial.enabled,
    );
    // Polling can replace or prune the live Agent while this form stays open.
    latest.workspaceIds.splice(0);
    latest.enabled = false;
    expect(agentProfileChanges(initial, profile)).toEqual({
      name: "新的公开名称",
    });
    expect(initial.workspaceIds).toEqual([workspace.id]);
    expect(initial.enabled).toBe(true);
  });
  it("preserves only explicit team or enablement changes and ignores scope ordering", () => {
    const initial = snapshotAgentProfile({
      ...agent,
      workspaceIds: [workspace.id, "second-team"],
    });
    const profile = {
      name: initial.name,
      description: initial.description,
      enabled: initial.enabled,
      workspaceIds: ["second-team", workspace.id, workspace.id],
    };
    expect(agentProfileChanges(initial, profile)).toEqual({});
    expect(
      agentProfileChanges(initial, {
        ...profile,
        workspaceIds: [workspace.id, "new-explicit-team"],
      }),
    ).toEqual({ workspaceIds: [workspace.id, "new-explicit-team"] });
    expect(
      agentProfileChanges(initial, { ...profile, enabled: false }),
    ).toEqual({ enabled: false });
    expect(
      agentProfileChanges(initial, { ...profile, description: "新的简介" }),
    ).toEqual({ description: "新的简介" });
  });
  it("enriches people and Agents through the same participant list with provenance, excluding disabled Agents from new selection", () => {
    const participants = enrichParticipants(
      [{ userId: "human", displayName: "伙伴", role: "member", kind: "human" }],
      [
        agent,
        { ...agent, id: "disabled", principalId: "disabled", enabled: false },
      ],
    );
    expect(participants.map((member) => member.userId)).toEqual([
      "human",
      "agent-1",
      "disabled",
    ]);
    expect(participants[1]).toMatchObject({
      kind: "agent",
      ownerName: "创建者",
      selectable: true,
    });
    expect(participants[2].selectable).toBe(false);
    const html = renderToStaticMarkup(<AgentBadge ownerName="创建者" />);
    expect(html).toContain("Agent");
    expect(html).toContain("创建者");
  });
  it("shows waiting configuration explicitly and never presents a fabricated model response", () => {
    expect(displayAgentState("waiting_configuration", true)).toBe(
      "waiting_configuration",
    );
    expect(displayAgentState("running", false)).toBe("disabled");
    const html = renderToStaticMarkup(<WaitingForConfiguration />);
    expect(html).toContain("模型尚未配置");
    expect(html).toContain("请联系平台管理员配置模型");
    expect(html).not.toContain("<button");
  });
});
describe("write-only gateway credentials", () => {
  it("omits empty keys to preserve the secret and requires explicit removal", () => {
    const value = {
      baseUrl: "https://model.example.test/v1",
      model: "model-id",
      apiKey: "",
      enabled: true,
    };
    expect(modelGatewayInput(value)).not.toHaveProperty("apiKey");
    expect(modelGatewayInput({ ...value, clearApiKey: true })).toMatchObject({
      clearApiKey: true,
    });
    expect(() =>
      modelGatewayInput({
        ...value,
        apiKey: "example-secret",
        clearApiKey: true,
      }),
    ).toThrow();
  });
  it("rejects credentials or tokens placed in provider URLs", () => {
    for (const baseUrl of [
      "https://user:password@model.example.test/v1",
      "https://model.example.test/v1?api_key=example-secret",
      "https://model.example.test/v1#example-secret",
    ])
      expect(() =>
        modelGatewayInput({
          baseUrl,
          model: "model-id",
          apiKey: "",
          enabled: true,
        }),
      ).toThrow();
  });
  it("never echoes a key in gateway error text or reads a saved key into the password input", () => {
    const secret = "example-sensitive-key";
    expect(
      gatewayFailureText(
        new ApiError(500, "provider_failure", `provider echoed ${secret}`),
      ),
    ).not.toContain(secret);
    const html = renderToStaticMarkup(
      <GatewayConfigurationFields
        initial={{
          baseUrl: "https://model.example.test/v1",
          model: "model-id",
          hasApiKey: true,
          enabled: true,
        }}
        onSave={async () => {}}
      />,
    );
    expect(html).toContain('type="password"');
    expect(html).toContain('name="gatewayApiKey"');
    expect(html).not.toContain(secret);
    expect(html).toContain("留空保留");
  });
});
