import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { Agent, User, Workspace } from "@teamie/api-client";
import { ApiError } from "@teamie/api-client";
import {
  AgentInputError,
  agentProfileChanges,
  agentProfileInput,
  canConfigureModelGateway,
  canManageAgent,
  agentFailureText,
  gatewayFailureText,
  modelGatewayInput,
  snapshotAgentProfile,
} from "./agent-management";
import {
  AgentBadge,
  AgentEnabledStatus,
  GatewayConfigurationFields,
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
  createdAt: "2026-10-16T07:00:00Z",
  updatedAt: "2026-10-16T07:00:00Z",
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
    expect(
      agentProfileChanges(initial, { ...profile, modelId: "deepseek-r1" }),
    ).toEqual({ modelId: "deepseek-r1" });
    const initialWithModel = snapshotAgentProfile({
      ...agent,
      workspaceIds: [workspace.id, "second-team"],
      modelId: "deepseek-r1",
    });
    expect(
      agentProfileChanges(initialWithModel, { ...profile, modelId: "" }),
    ).toEqual({ modelId: "" });
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
  it("shows only the configured enabled state without claiming online or runtime health", () => {
    const enabled = renderToStaticMarkup(<AgentEnabledStatus enabled />);
    const disabled = renderToStaticMarkup(
      <AgentEnabledStatus enabled={false} />,
    );
    expect(enabled).toContain("已启用");
    expect(disabled).toContain("已停用");
    for (const html of [enabled, disabled]) {
      expect(html).not.toMatch(/在线|运行|模型|错误|Session/);
    }
  });
  it("does not display operational errors returned during basic profile changes", () => {
    for (const status of [400, 401, 403, 404, 409, 500]) {
      const text = agentFailureText(
        new ApiError(
          status,
          "internal_error",
          "TASK-DESCRIPTION-SENTINEL Session:private gateway:private",
        ),
      );
      expect(text).not.toMatch(/SENTINEL|Session|gateway|internal_error/);
    }
    expect(
      agentFailureText(new Error("PRIVATE-RUNTIME-SENTINEL")),
    ).not.toContain("PRIVATE-RUNTIME-SENTINEL");
  });
  it("shows the Chinese client-side validation message instead of the generic failure", () => {
    const failure = (run: () => unknown) => {
      try {
        run();
      } catch (error) {
        expect(error).toBeInstanceOf(AgentInputError);
        return agentFailureText(error);
      }
      throw new Error("expected validation failure");
    };
    expect(
      failure(() => agentProfileInput("   ", "", [], [workspace], true)),
    ).toBe("Agent 名称需要 1 至 80 个字符。");
    expect(
      failure(() =>
        agentProfileInput("伙伴", "", ["other-private-team"], [workspace], true),
      ),
    ).toBe("只能选择你当前已加入的工作空间，请刷新后重新选择。");
    // Network failures are TypeErrors too, but are not validation copy.
    expect(agentFailureText(new TypeError("Failed to fetch"))).toBe(
      "操作未完成，请检查资料或稍后重试。",
    );
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
    expect(
      modelGatewayInput({
        ...value,
        webSearchEnabled: true,
        webSearchApiKey: "  tvly-secret  ",
      }),
    ).toMatchObject({
      webSearchEnabled: true,
      webSearchApiKey: "tvly-secret",
    });
    expect(
      modelGatewayInput({
        ...value,
        webSearchEnabled: false,
        webSearchApiKey: "",
        clearWebSearchApiKey: true,
      }),
    ).toMatchObject({
      webSearchEnabled: false,
      clearWebSearchApiKey: true,
    });
    expect(() =>
      modelGatewayInput({
        ...value,
        webSearchApiKey: "tvly-secret",
        clearWebSearchApiKey: true,
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
  it("surfaces gateway validation copy, including malformed URLs, without echoing secrets", () => {
    const failure = (value: Parameters<typeof modelGatewayInput>[0]) => {
      try {
        modelGatewayInput(value);
      } catch (error) {
        expect(error).toBeInstanceOf(AgentInputError);
        return gatewayFailureText(error);
      }
      throw new Error("expected validation failure");
    };
    const value = {
      baseUrl: "https://model.example.test/v1",
      model: "model-id",
      apiKey: "",
      enabled: true,
    };
    for (const baseUrl of ["model.example.test/v1", "not a url", ""])
      expect(failure({ ...value, baseUrl })).toBe(
        "服务地址格式不正确，请填写完整的 http(s) 地址。",
      );
    const leaked = failure({
      ...value,
      baseUrl: "https://model.example.test/v1?api_key=example-secret",
    });
    expect(leaked).toBe("服务地址不得包含密钥、认证信息或查询参数。");
    expect(leaked).not.toContain("example-secret");
    expect(
      failure({ ...value, apiKey: "example-secret", clearApiKey: true }),
    ).toBe("清除密钥和填写新密钥不能同时进行。");
    expect(failure({ ...value, contextWindowTokens: 1024 })).toBe(
      "上下文窗口需为 4096 至 2000000 的整数。",
    );
    expect(gatewayFailureText(new TypeError("Failed to fetch"))).toBe(
      "模型网关请求未完成，请检查配置或稍后重试。",
    );
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
          webSearch: {
            provider: "tavily",
            hasApiKey: true,
            enabled: true,
            configured: true,
          },
        }}
        onSave={async () => true}
      />,
    );
    expect(html).toContain('type="password"');
    expect(html).toContain('name="gatewayApiKey"');
    expect(html).toContain('name="gatewayWebSearchApiKey"');
    expect(html).toContain('name="gatewayClearWebSearchApiKey"');
    expect(html).not.toContain(secret);
    expect(html).toContain("留空保留");
  });
});
