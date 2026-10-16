import { useState } from "react";
import { Link } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  CheckCheck,
  Clock3,
  KeyRound,
  LoaderCircle,
  ShieldCheck,
} from "lucide-react";
import { api, ApiError } from "@teamie/api-client";
import { useAuth } from "./auth";
import {
  canConfigureModelGateway,
  gatewayFailureText,
  modelGatewayInput,
} from "./agent-management";
import { GatewayConfigurationFields } from "./agent-ui";
import type { GatewayFormValue } from "./agent-ui";
import { Button, Notice } from "./ui";
export function ModelGatewaySettings() {
  const { user } = useAuth();
  const client = useQueryClient();
  const admin = canConfigureModelGateway(user);
  const [denied, setDenied] = useState(false),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [saved, setSaved] = useState(false);
  const [test, setTest] = useState<{
    ok: boolean;
    model: string;
    latencyMs: number;
  } | null>(null);
  const key = ["modelGateway", user!.id];
  const query = useQuery({
    queryKey: key,
    queryFn: api.modelGateway,
    enabled: admin && !denied,
    retry: (count, e) =>
      !(e instanceof ApiError && [401, 403].includes(e.status)) && count < 1,
  });
  const forbidden =
    denied || (query.error instanceof ApiError && query.error.status === 403);
  async function save(value: GatewayFormValue) {
    const sessionToken = api.csrfToken;
    setBusy(true);
    setError("");
    setSaved(false);
    setTest(null);
    try {
      const response = await api.saveModelGateway(modelGatewayInput(value));
      if (sessionToken !== api.csrfToken) return;
      client.setQueryData(key, response);
      setSaved(true);
      await client.invalidateQueries({ queryKey: ["agents"] });
    } catch (e) {
      if (sessionToken === api.csrfToken) {
        if (e instanceof ApiError && e.status === 403) setDenied(true);
        setError(gatewayFailureText(e));
      }
    } finally {
      setBusy(false);
    }
  }
  async function testConnection() {
    const sessionToken = api.csrfToken;
    setBusy(true);
    setError("");
    setTest(null);
    try {
      const result = await api.testModelGateway();
      if (sessionToken === api.csrfToken)
        setTest({
          ok: result.ok,
          model: result.model,
          latencyMs: result.latencyMs,
        });
    } catch (e) {
      if (sessionToken === api.csrfToken) {
        if (e instanceof ApiError && e.status === 403) setDenied(true);
        setError(gatewayFailureText(e));
      }
    } finally {
      setBusy(false);
    }
  }
  if (!admin || forbidden)
    return (
      <div className="unavailable-view">
        <span className="large-symbol">
          <ShieldCheck size={38} />
        </span>
        <h2>需要平台管理员权限</h2>
        <p>模型连接由平台管理员统一配置。你仍可以管理自己的 Agent。</p>
        <Link className="button" to="/app/agents">
          查看我的 Agent
        </Link>
      </div>
    );
  return (
    <div className="admin-view gateway-view">
      <div className="admin-heading">
        <div>
          <div className="eyebrow">
            ONE CONNECTION. A TEAM OF POSSIBILITIES.
          </div>
          <h1>
            模型网关{" "}
            <span className="platform-role-badge">
              <ShieldCheck size={11} />
              平台管理员
            </span>
          </h1>
          <p>配置一个 OpenAI 兼容模型服务，让 Agent 准备好接收新消息。</p>
        </div>
        <span className="admin-heading-icon">
          <KeyRound size={27} />
        </span>
      </div>
      {query.isLoading ? (
        <div className="admin-empty">
          <LoaderCircle className="spin" size={23} />
          <p>正在加载模型配置…</p>
        </div>
      ) : !query.data ? (
        <div className="gateway-load-error">
          <Notice>{gatewayFailureText(query.error)}</Notice>
          <Button className="secondary" onClick={() => void query.refetch()}>
            重新加载
          </Button>
        </div>
      ) : (
        <>
          <div className={`gateway-saved-state ${query.data.status}`}>
            <span>
              {query.data.status === "ready" ? (
                <CheckCheck size={23} />
              ) : (
                <Clock3 size={23} />
              )}
            </span>
            <div>
              <b>
                {query.data.status === "unconfigured"
                  ? "等待管理员填写配置"
                  : query.data.status === "disabled"
                    ? "模型网关已停用"
                    : "模型连接配置已保存"}
              </b>
              <p>
                {query.data.status === "ready"
                  ? "配置已具备运行所需字段。可手动测试已保存的连接，确认服务可用。"
                  : "Agent 暂时不会调用模型或生成回复。保存并启用有效配置后，才会开始处理新消息。"}
              </p>
            </div>
          </div>
          <Notice kind="success">
            {saved ? "配置已保存。密钥不会在页面或后续读取中回显。" : ""}
          </Notice>
          <Notice kind={test?.ok ? "success" : "info"}>
            {test
              ? test.ok
                ? `连接测试通过 · ${test.model} · ${Math.round(test.latencyMs)} ms`
                : "连接测试未通过，请检查服务地址、模型和密钥。"
              : ""}
          </Notice>
          <GatewayConfigurationFields
            key={`${user!.id}:${query.data.config.updatedAt}`}
            initial={query.data.config}
            onSave={save}
            error={error}
            busy={busy}
          />
          <div className="gateway-test-panel">
            <div>
              <h3>测试已保存的配置</h3>
              <p>
                测试会向已保存的模型服务发送一次请求，可能产生调用费用。表单中尚未保存的内容不会用于测试。
              </p>
            </div>
            <Button
              className="secondary"
              busy={busy}
              disabled={
                !query.data.config.configured || !query.data.config.enabled
              }
              onClick={() => void testConnection()}
            >
              发送 1 次模型请求测试
            </Button>
          </div>
        </>
      )}
      <div className="gateway-boundary-note">
        <ShieldCheck size={15} />
        <p>
          普通用户只管理 Agent
          的名称、简介、可见团队和启停状态。模型与密钥配置仅在此处由平台管理员维护。
        </p>
      </div>
    </div>
  );
}
