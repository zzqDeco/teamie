import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import type { ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@teamie/api-client";
import type { Bootstrap, Message } from "@teamie/api-client";
import { useAuth } from "./auth";
import {
  notifySyncedMessages,
  resetNotificationSession,
} from "./notifications";
import { chatStore } from "./store";
export type Connection =
  "connecting" | "connected" | "reconnecting" | "offline";
type Chat = {
  data?: Bootstrap;
  loading: boolean;
  error: string;
  connection: Connection;
  syncError: string;
  refresh: () => Promise<unknown>;
  sync: () => void;
  reconnect: () => void;
  sendTyping: (conversationId: string, value: boolean) => void;
  typing: Record<
    string,
    { userId: string; displayName: string; until: number }[]
  >;
};
const ChatContext = createContext<Chat | null>(null);
const bootstrapObservation = Symbol("bootstrapObservation");
type ObservedBootstrap = Bootstrap & {
  [bootstrapObservation]?: { generation: number; conversationVersion: number };
};
async function observedBootstrap(): Promise<ObservedBootstrap> {
  const observed = {
    generation: chatStore.generation,
    conversationVersion: chatStore.conversationVersion,
  };
  return { ...await api.bootstrap(), [bootstrapObservation]: observed };
}
export const useChatState = () =>
  useSyncExternalStore(chatStore.subscribe, chatStore.getSnapshot);
export function ChatProvider({ children }: { children: ReactNode }) {
  const { user, reconcileSession } = useAuth();
  const client = useQueryClient();
  const appliedBootstrap = useRef<ObservedBootstrap | null>(null);
  const query = useQuery({
    queryKey: ["bootstrap", user!.id],
    queryFn: observedBootstrap,
    structuralSharing: false,
    staleTime: 30_000,
    refetchInterval: 60_000,
    retry: (count, e) =>
      !(e instanceof ApiError && [401, 403].includes(e.status)) && count < 2,
  });
  const [connection, setConnection] = useState<Connection>("connecting");
  const [syncError, setSyncError] = useState("");
  const [typing, setTyping] = useState<Chat["typing"]>({});
  const socket = useRef<WebSocket | null>(null);
  const syncRef = useRef<() => void>(() => {});
  const reconnectRef = useRef<() => void>(() => {});
  const capabilities = useRef<Record<string, boolean>>({});
  const userId = user!.id;
  const completedInitialSyncUser = useRef<string | null>(null);
  useEffect(() => {
    if (query.data) {
      if (query.data.user.id !== userId) {
        reconcileSession();
        return;
      }
      if (appliedBootstrap.current === query.data) return;
      if (!chatStore.replaceConversations(query.data.conversations, query.data[bootstrapObservation])) {
        void client.invalidateQueries({ queryKey: ["bootstrap", userId] });
        return;
      }
      appliedBootstrap.current = query.data;
      capabilities.current = query.data.capabilities;
    }
  }, [client, query.data, reconcileSession, userId]);
  useEffect(() => {
    chatStore.begin(userId);
    resetNotificationSession(userId);
    if (completedInitialSyncUser.current !== userId) {
      completedInitialSyncUser.current = null;
    }
    let stopped = false,
      syncing = false,
      requested = false,
      initialSyncCompleted = completedInitialSyncUser.current === userId,
      attempt = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let ws: WebSocket | null = null;
    const refreshPermissions = () => {
      // A 403 may reject an owner-only operation or a disabled write. Fetch
      // current permissions before pruning content that is still readable.
      void client.invalidateQueries({ queryKey: ["bootstrap"] });
    };
    api.onForbidden = refreshPermissions;
    async function sync() {
      if (stopped) return;
      if (syncing) {
        requested = true;
        return;
      }
      syncing = true;
      const generation = chatStore.generation;
      try {
        do {
          requested = false;
          let hasMore = true;
          const notifyCandidates: Array<{
            conversationId: string;
            messageId: string;
          }> = [];
          while (hasMore && !stopped) {
            const cursor = chatStore.getSnapshot().cursor;
            const page = await api.sync(cursor);
            if (stopped || generation !== chatStore.generation) return;
            if (page.hasMore && page.cursor === cursor)
              throw new Error("同步游标未前进");
            if (page.events.some((e) => e.type === "membership.changed")) {
              let fresh: ObservedBootstrap;
              // A local creation or another accepted bootstrap can invalidate
              // this snapshot while its response is in flight. Re-read before
              // filtering the page or advancing its durable cursor.
              for (;;) {
                fresh = await observedBootstrap();
                if (stopped || generation !== chatStore.generation) return;
                if (fresh.user.id !== userId) {
                  reconcileSession();
                  return;
                }
                if (chatStore.replaceConversations(fresh.conversations, fresh[bootstrapObservation])) break;
              }
              appliedBootstrap.current = fresh;
              capabilities.current = fresh.capabilities;
              const allowed = new Set(fresh.conversations.map((c) => c.id));
              page.events = page.events.filter((event) => {
                const id =
                  event.conversationId ||
                  event.message?.conversationId ||
                  event.conversation?.id;
                return !id || allowed.has(id);
              });
              client.setQueryData(["bootstrap", userId], fresh);
              void client.invalidateQueries({ queryKey: ["members"] });
              void client.invalidateQueries({ queryKey: ["workspaceAgents"] });
            }
            chatStore.apply(page);
            if (initialSyncCompleted && cursor !== "0") {
              for (const event of page.events) {
                if (event.type === "message.created" && event.message) {
                  notifyCandidates.push({
                    conversationId: event.message.conversationId,
                    messageId: event.message.id,
                  });
                }
              }
            }
            if (page.events.some((event) => event.type.startsWith("message.") || event.type === "read.updated" || event.type === "membership.changed"))
              void client.invalidateQueries({ queryKey: ["attention", userId] });
            hasMore = page.hasMore;
          }
          initialSyncCompleted = true;
          completedInitialSyncUser.current = userId;
          if (
            !stopped &&
            generation === chatStore.generation &&
            notifyCandidates.length > 0
          ) {
            notifySyncedMessages(
              notifyCandidates,
              chatStore.getSnapshot(),
              userId,
            );
          }
        } while (requested && !stopped);
        setSyncError("");
      } catch (error) {
        if (!stopped && !(error instanceof ApiError && error.status === 401)) {
          if (
            error instanceof ApiError &&
            ["invalid_cursor", "cursor_invalid", "future_cursor"].includes(
              error.code,
            )
          )
            chatStore.revoke();
          setSyncError("消息同步暂时中断，正在自动重试");
        }
      } finally {
        syncing = false;
      }
    }
    syncRef.current = () => {
      void sync();
    };
    function connect() {
      if (stopped) return;
      if (!navigator.onLine) {
        setConnection("offline");
        return;
      }
      setConnection(attempt === 0 ? "connecting" : "reconnecting");
      void sync();
      ws = new WebSocket(
        `${location.protocol === "https:" ? "wss:" : "ws:"}//${location.host}/api/v1/ws`,
      );
      socket.current = ws;
      ws.onopen = () => {
        attempt = 0;
        setConnection("connected");
        void sync();
        void retryTransientFailures(chatStore.generation);
      };
      ws.onmessage = (event) => {
        try {
          const hint = JSON.parse(event.data);
          if (hint.type === "sync.required" || hint.type === "ready")
            void sync();
          if (
            hint.type === "typing" &&
            hint.userId !== userId &&
            capabilities.current["im.typing.publish"]
          ) {
            setTyping((old) => ({
              ...old,
              [hint.conversationId]: [
                ...(old[hint.conversationId] || []).filter(
                  (x) => x.userId !== hint.userId,
                ),
                ...(hint.isTyping
                  ? [
                      {
                        userId: hint.userId,
                        displayName: hint.displayName,
                        until: Date.now() + 5000,
                      },
                    ]
                  : []),
              ],
            }));
          }
        } catch {
          /* Unknown ephemeral frames cannot alter durable state. */
        }
      };
      ws.onclose = () => {
        if (stopped) return;
        setConnection(navigator.onLine ? "reconnecting" : "offline");
        attempt += 1;
        timer = setTimeout(
          connect,
          Math.min(30_000, 800 * 2 ** Math.min(attempt, 5)) *
            (0.75 + Math.random() * 0.5),
        );
      };
      ws.onerror = () => ws?.close();
    }
    const online = () => {
      if (timer) {
        clearTimeout(timer);
        timer = undefined;
      }
      if (ws) {
        ws.onclose = null;
        ws.close();
      }
      connect();
      void sync();
    };
    reconnectRef.current = () => {
      if (stopped) return;
      if (!ws || ws.readyState !== WebSocket.OPEN) online();
      else void sync();
    };
    const offline = () => {
      setConnection("offline");
      ws?.close();
    };
    const focus = () => {
      if (document.visibilityState === "visible") void sync();
    };
    const interval = setInterval(() => {
      if (navigator.onLine) void sync();
    }, 10_000);
    const typingTimer = setInterval(() => {
      setTyping((old) => {
        let changed = false;
        const next: Chat["typing"] = {};
        for (const [id, entries] of Object.entries(old)) {
          const active = entries.filter((x) => x.until > Date.now());
          if (active.length !== entries.length || active.length === 0)
            changed = true;
          if (active.length)
            next[id] = active.length === entries.length ? entries : active;
        }
        return changed ? next : old;
      });
    }, 1000);
    connect();
    window.addEventListener("online", online);
    window.addEventListener("offline", offline);
    document.addEventListener("visibilitychange", focus);
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
      clearInterval(interval);
      clearInterval(typingTimer);
      if (ws) {
        ws.onclose = null;
        ws.close();
      }
      socket.current = null;
      if (api.onForbidden === refreshPermissions) api.onForbidden = undefined;
      window.removeEventListener("online", online);
      window.removeEventListener("offline", offline);
      document.removeEventListener("visibilitychange", focus);
      resetNotificationSession(null);
    };
  }, [userId, client, reconcileSession]);
  const sendTyping = useCallback((conversationId: string, value: boolean) => {
    if (
      capabilities.current["im.typing.publish"] &&
      socket.current?.readyState === WebSocket.OPEN
    )
      socket.current.send(
        JSON.stringify({ type: "typing", conversationId, isTyping: value }),
      );
  }, []);
  const sync = useCallback(() => {
    syncRef.current();
  }, []);
  const reconnect = useCallback(() => {
    reconnectRef.current();
  }, []);
  const data = query.data?.user.id === user!.id ? query.data : undefined;
  const error = query.error ? "无法加载你的工作空间，请稍后重试。" : "";
  const value = useMemo(
    () => ({
      data,
      loading: query.isLoading,
      error,
      connection,
      syncError,
      refresh: query.refetch,
      sync,
      reconnect,
      sendTyping,
      typing,
    }),
    [
      data,
      query.isLoading,
      error,
      connection,
      syncError,
      query.refetch,
      sync,
      reconnect,
      sendTyping,
      typing,
    ],
  );
  return <ChatContext.Provider value={value}>{children}</ChatContext.Provider>;
}
export function useChat() {
  const ctx = useContext(ChatContext);
  if (!ctx) throw new Error("Chat provider missing");
  return ctx;
}
export function isNetworkError(error: unknown) {
  return (
    error instanceof TypeError ||
    (error instanceof Error && /failed to fetch|load failed|networkerror/i.test(error.message))
  );
}
export function errorText(error: unknown) {
  if (!(error instanceof ApiError)) {
    if (error instanceof DOMException && error.name === "AbortError")
      return "请求已取消";
    if (isNetworkError(error)) return "网络连接失败，请检查网络后重试。";
    return "请求失败，请稍后重试。";
  }
  const messages: Record<string, string> = {
    invalid_credentials: "邮箱或密码不正确，请重新输入。",
    invalid_current_password: "当前密码不正确，请重新输入。",
    invalid_password: "密码需至少 12 个字符，且不超过 72 字节。",
    password_unchanged: "新密码不能与当前密码相同。",
    unauthorized: "登录已失效，请重新登录。",
    invalid_invite: "邀请码无效、已使用或已失效，请联系管理员获取新的邀请。",
    invalid_invitation:
      "邀请码无效、已使用或已失效，请联系管理员获取新的邀请。",
    forbidden: "你当前没有执行此操作的权限。",
    conflict: "这条消息与之前的发送记录冲突，请刷新后重试。",
    feature_disabled: "此功能已由工作空间管理员关闭。",
    rate_limited: "操作过于频繁，请稍后再试。",
    attachment_busy: "文件传输繁忙，请稍后重试。",
    attachment_capacity: "待发送文件过多，请移除部分文件后重试。",
    attachment_too_large: "文件不能超过 4 MiB。",
    invalid_attachment: "文件已过期或不可用，请重新上传。",
    invalid_filename: "文件名无效，请重命名后上传。",
    email_exists: "此邮箱已注册，请直接登录。",
    email_in_use: "此邮箱已注册，请直接登录。",
    invalid_email: "邮箱格式不正确，请检查后重试。",
    invalid_name: "名称格式不正确，请修改后重试。",
    invalid_request: "请求内容无效，请检查后重试。",
    invalid_query: "搜索关键词无效，请调整后重试（最多 200 个字符）。",
    upload_conflict: "这个文件与之前的上传记录冲突，请重新选择文件。",
    attachment_upload_unavailable: "文件已过期或已被移除，请重新上传。",
    attachment_upload_not_found: "没有找到这次上传，请重新上传。",
    invalid_reset_link: "重置链接无效或已过期，请联系管理员重新生成。",
    session_changed: "登录状态已变化，请刷新页面后重试。",
  };
  if (messages[error.code]) return messages[error.code];
  if (error.status >= 500) return "服务暂时不可用，请稍后重试。";
  if (error.status === 404) return "内容不存在或已无法访问。";
  return "请求失败，请稍后重试。";
}
export function isPermanentSendError(error: unknown) {
  return (
    error instanceof ApiError &&
    error.status !== 429 &&
    error.status < 500 &&
    error.status !== 401
  );
}
export async function sendMessage(message: Message, generation: number) {
  chatStore.merge([
    { ...message, status: "sending", error: undefined, permanent: undefined },
  ]);
  try {
    const response = await api.send(message.conversationId, {
      clientMessageId: message.clientMessageId,
      body: message.body,
      replyToId: message.replyToId,
      ...(message.mentions?.length ? { mentions: message.mentions } : {}),
      ...(message.attachments?.length
        ? { attachmentIds: message.attachments.map((a) => a.id) }
        : {}),
    });
    if (chatStore.generation === generation)
      chatStore.merge([response.message]);
  } catch (error) {
    if (chatStore.generation === generation)
      chatStore.merge([
        {
          ...message,
          status: "failed",
          error: errorText(error),
          ...(isPermanentSendError(error) ? { permanent: true } : {}),
        },
      ]);
  }
}
// Transient failures retry with the same clientMessageId, which the server
// treats idempotently, so a retry after a lost response cannot duplicate.
export async function retryTransientFailures(generation: number) {
  if (chatStore.generation !== generation) return;
  const queued = Object.values(chatStore.getSnapshot().messages).flatMap(
    (messages) =>
      messages.filter((m) => m.status === "failed" && !m.permanent),
  );
  for (const item of queued) {
    if (chatStore.generation !== generation) return;
    const current = (
      chatStore.getSnapshot().messages[item.conversationId] || []
    ).find((m) => m.clientMessageId === item.clientMessageId);
    if (current && current.status === "failed" && !current.permanent)
      await sendMessage(current, generation);
  }
}
