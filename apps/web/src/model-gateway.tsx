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
import { ICON } from "./icons";
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
          <ShieldCheck size={ICON.xl} />
        </span>
        <h2>需要平台管理员权限</h2>
        <p>模型连接由平台管理员统一配置。你仍可以管理自己的 Agent。</p>
        <Link className="button" to="/app/agents">
          管理个人 Agent
        </Link>
      </div>
    );
  return (
    <div className="admin-view gateway-view">
      <div className="admin-heading">
        <div>
          <h1>
            模型网关{" "}
            <span className="platform-role-badge">
              <ShieldCheck size={ICON.xs} />
              平台管理员
            </span>
          </h1>
          <p>所有 Agent 共用的模型连接。</p>
        </div>
        <span className="admin-heading-icon">
          <KeyRound size={ICON.xl} />
        </span>
      </div>
      {query.isLoading ? (
        <div className="admin-empty">
          <LoaderCircle className="spin" size={ICON.xl} />
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
                <CheckCheck size={ICON.xl} />
              ) : (
                <Clock3 size={ICON.xl} />
              )}
            </span>
            <div>
              <b>
                {query.data.status === "unconfigured"
                  ? "尚未配置"
                  : query.data.status === "disabled"
                    ? "模型网关已停用"
                    : "配置已保存"}
              </b>
              <p>
                {query.data.status === "ready"
                  ? "连接尚需测试验证。"
                  : "保存并启用后，Agent 才能调用模型。"}
              </p>
            </div>
          </div>
          <Notice kind="success">{saved ? "配置已保存。" : ""}</Notice>
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
              <p>使用已保存的配置发送 1 次模型请求，可能产生费用。</p>
            </div>
            <Button
              className="secondary"
              busy={busy}
              disabled={
                !query.data.config.configured || !query.data.config.enabled
              }
              onClick={() => void testConnection()}
            >
              测试连接
            </Button>
          </div>
        </>
      )}
    </div>
  );
}
