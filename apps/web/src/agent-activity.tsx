import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  CircleStop,
  Clock3,
  LoaderCircle,
  RefreshCw,
  RotateCcw,
} from "lucide-react";
import { api, ApiError } from "@teamie/api-client";
import type { Agent, AgentRun, AgentTask } from "@teamie/api-client";
import { useAuth } from "./auth";
import { Button, IconButton, Notice } from "./ui";
import { AgentStatus } from "./agent-ui";
import type { AgentDisplayStatus } from "./agent-ui";
import { displayAgentState } from "./agent-management";
import { errorText } from "./chat";

const statusLabels: Record<string, string> = {
  queued: "等待处理",
  pending: "等待处理",
  waiting_configuration: "待配置模型",
  waiting_input: "等待输入",
  suspended: "已暂停",
  retry: "等待重试",
  running: "处理中",
  cancelling: "正在取消",
  completed: "已完成",
  succeeded: "已完成",
  failed: "失败",
  error: "失败",
  cancelled: "已取消",
  canceled: "已取消",
  interrupted: "已中断",
};
const cancellable = new Set([
  "queued",
  "pending",
  "running",
  "retry",
  "waiting_configuration",
  "waiting_input",
  "suspended",
]);
const retryable = new Set([
  "failed",
  "interrupted",
  "retry",
  "waiting_configuration",
  "canceled",
]);
const runtimeStatuses: Record<string, AgentDisplayStatus> = {
  idle: "idle",
  running: "running",
  disabled: "disabled",
  waiting_configuration: "waiting_configuration",
  error: "failed",
  failed: "failed",
  queued: "pending",
  pending: "pending",
  retry: "pending",
  provisioning: "pending",
  waiting_input: "idle",
  suspended: "paused",
};
const when = (value: string) =>
  new Date(value).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
const retryQuery = (count: number, error: Error) =>
  !(error instanceof ApiError && [401, 403, 404].includes(error.status)) &&
  count < 1;

export function AgentActivity({
  agent,
  controlsAllowed,
}: {
  agent: Agent;
  controlsAllowed: boolean;
}) {
  const { user } = useAuth();
  const client = useQueryClient();
  const [tab, setTab] = useState<"runs" | "tasks">("runs");
  const [cursor, setCursor] = useState("");
  const [previous, setPrevious] = useState<string[]>([]);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const key = ["agentActivity", user!.id, agent.id];
  const runtime = useQuery({
    queryKey: [...key, "runtime"],
    queryFn: () => api.agentRuntime(agent.id),
    retry: retryQuery,
    refetchInterval: 5000,
  });
  const runs = useQuery({
    queryKey: [...key, "runs", cursor],
    queryFn: () => api.agentRuns(agent.id, cursor || undefined),
    enabled: tab === "runs",
    retry: retryQuery,
    refetchInterval: cursor ? false : 5000,
  });
  const tasks = useQuery({
    queryKey: [...key, "tasks", cursor],
    queryFn: () => api.agentTasks(agent.id, cursor || undefined),
    enabled: tab === "tasks",
    retry: retryQuery,
    refetchInterval: cursor ? false : 5000,
  });

  useEffect(() => {
    if (typeof EventSource === "undefined") return;
    const stream = new EventSource(api.agentEventsURL(agent.id));
    let timer: ReturnType<typeof setTimeout> | undefined;
    const refresh = () => {
      clearTimeout(timer);
      timer = setTimeout(() => {
        void client.invalidateQueries({
          queryKey: ["agentActivity", user!.id, agent.id],
        });
        void client.invalidateQueries({
          queryKey: ["agent", user!.id, agent.id],
        });
      }, 250);
    };
    stream.addEventListener("runtime", refresh);
    // Safe JSON polling remains active if the event stream is unavailable.
    stream.addEventListener("unavailable", () => stream.close());
    return () => {
      clearTimeout(timer);
      stream.close();
    };
  }, [agent.id, client, user]);

  async function change(
    item: AgentRun | AgentTask,
    action: "cancel" | "retry",
  ) {
    setBusy(`${action}:${item.id}`);
    setError("");
    const token = api.csrfToken;
    try {
      const response =
        action === "retry"
          ? await api.retryAgentRun(agent.id, item.id)
          : tab === "runs"
            ? await api.cancelAgentRun(agent.id, item.id)
            : await api.cancelAgentTask(agent.id, item.id);
      if (token !== api.csrfToken) return;
      if (!response.ok) throw new Error("操作未完成，请刷新后重试。");
      await client.invalidateQueries({ queryKey: key });
      await client.invalidateQueries({
        queryKey: ["agent", user!.id, agent.id],
      });
    } catch (e) {
      if (token === api.csrfToken) {
        setError(
          e instanceof ApiError && e.status === 409
            ? "状态已变化，请刷新后重试。"
            : errorText(e),
        );
        if (e instanceof ApiError && e.status === 409)
          await client.invalidateQueries({ queryKey: key });
      }
    } finally {
      setBusy("");
    }
  }
  const query = tab === "runs" ? runs : tasks;
  const items: (AgentRun | AgentTask)[] =
    tab === "runs" ? runs.data?.runs || [] : tasks.data?.tasks || [];
  const denied = [runtime.error, query.error].some(
    (e) => e instanceof ApiError && [401, 403, 404].includes(e.status),
  );
  const nextCursor = query.data?.cursor;
  const currentStatus = !agent.enabled
    ? "disabled"
    : runtime.data
      ? runtimeStatuses[runtime.data.state] ||
        displayAgentState(agent.runtimeState, true)
      : displayAgentState(agent.runtimeState, agent.enabled);
  return (
    <section className="agent-activity" aria-label="运行与任务">
      <header>
        <h2>运行与任务</h2>
        <IconButton
          label="刷新运行与任务"
          onClick={() => void client.invalidateQueries({ queryKey: key })}
        >
          <RefreshCw
            size={16}
            className={runtime.isFetching || query.isFetching ? "spin" : ""}
          />
        </IconButton>
      </header>
      {denied ? (
        <Notice>暂时无法访问运行记录，请刷新页面。</Notice>
      ) : (
        <>
          <div className="activity-summary">
            <AgentStatus status={currentStatus} />
            {runtime.data && (
              <>
                <span>
                  待处理 <b>{runtime.data.pendingInputs}</b>
                </span>
                <span>
                  进行中任务 <b>{runtime.data.runningTasks}</b>
                </span>
              </>
            )}
            {runtime.isError && (
              <span className="activity-summary-error">状态暂不可用</span>
            )}
          </div>
          <div className="activity-tabs" aria-label="记录类型">
            <button
              aria-pressed={tab === "runs"}
              className={tab === "runs" ? "active" : ""}
              onClick={() => {
                setTab("runs");
                setCursor("");
                setPrevious([]);
                setError("");
              }}
            >
              运行
            </button>
            <button
              aria-pressed={tab === "tasks"}
              className={tab === "tasks" ? "active" : ""}
              onClick={() => {
                setTab("tasks");
                setCursor("");
                setPrevious([]);
                setError("");
              }}
            >
              任务
            </button>
          </div>
          <Notice>{error}</Notice>
          {query.isLoading ? (
            <div className="activity-empty" role="status">
              <LoaderCircle className="spin" size={18} />
              加载记录…
            </div>
          ) : query.error ? (
            <div className="activity-empty">
              <Notice>记录加载失败。</Notice>
              <Button
                className="secondary small-button"
                onClick={() => void query.refetch()}
              >
                重试
              </Button>
            </div>
          ) : items.length ? (
            <div className="activity-list">
              {items.map((item) => {
                const task = "kind" in item ? item : null;
                const run = "inputCount" in item ? item : null;
                return (
                  <article className="activity-row" key={item.id}>
                    <span className={`activity-status ${item.status}`}>
                      <Clock3 size={13} />
                      {statusLabels[item.status] || "状态待确认"}
                    </span>
                    <div className="activity-item-main">
                      <b>
                        {task
                          ? task.description || "后台任务"
                          : `处理 ${run!.inputCount} 条消息`}
                      </b>
                      <span>
                        <time dateTime={item.createdAt}>
                          {when(item.createdAt)}
                        </time>
                        <span className="activity-item-id" title={item.id}>
                          #{item.id.slice(-8)}
                        </span>
                      </span>
                      {run?.errorCode && <small>本次处理未完成</small>}
                      {task?.exitCode !== undefined && task.exitCode !== 0 && (
                        <small>任务未成功完成</small>
                      )}
                    </div>
                    {controlsAllowed && agent.enabled && (
                      <div className="activity-item-actions">
                        {cancellable.has(item.status) && (
                          <Button
                            className="secondary small-button"
                            disabled={!!busy}
                            busy={busy === `cancel:${item.id}`}
                            onClick={() => void change(item, "cancel")}
                          >
                            <CircleStop size={14} />
                            取消
                          </Button>
                        )}
                        {run && retryable.has(item.status) && agent.enabled && (
                          <Button
                            className="secondary small-button"
                            disabled={!!busy}
                            busy={busy === `retry:${item.id}`}
                            onClick={() => void change(item, "retry")}
                          >
                            <RotateCcw size={14} />
                            重试
                          </Button>
                        )}
                      </div>
                    )}
                  </article>
                );
              })}
            </div>
          ) : (
            <div className="activity-empty">
              {tab === "runs" ? "暂无运行记录" : "暂无任务"}
            </div>
          )}
          {(previous.length > 0 || nextCursor) && (
            <div className="activity-pagination">
              <Button
                className="secondary small-button"
                disabled={!previous.length || query.isFetching}
                onClick={() => {
                  setCursor(previous.at(-1) || "");
                  setPrevious(previous.slice(0, -1));
                }}
              >
                较新记录
              </Button>
              <Button
                className="secondary small-button"
                disabled={!nextCursor || query.isFetching}
                onClick={() => {
                  setPrevious([...previous, cursor]);
                  setCursor(nextCursor || "");
                }}
              >
                较早记录
              </Button>
            </div>
          )}
        </>
      )}
    </section>
  );
}
