import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import type { ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@teamie/api-client";
import type { Bootstrap, Message } from "@teamie/api-client";
import { useAuth } from "./auth";
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
  sendTyping: (conversationId: string, value: boolean) => void;
  typing: Record<
    string,
    { userId: string; displayName: string; until: number }[]
  >;
};
const ChatContext = createContext<Chat | null>(null);
export const useChatState = () =>
  useSyncExternalStore(chatStore.subscribe, chatStore.getSnapshot);
export function ChatProvider({ children }: { children: ReactNode }) {
  const { user } = useAuth();
  const client = useQueryClient();
  const query = useQuery({
    queryKey: ["bootstrap", user!.id],
    queryFn: api.bootstrap,
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
  const capabilities = useRef<Record<string, boolean>>({});
  useEffect(() => {
    if (query.data) {
      chatStore.replaceConversations(query.data.conversations);
      capabilities.current = query.data.capabilities;
    }
  }, [query.data]);
  useEffect(() => {
    chatStore.begin(user!.id);
    let stopped = false,
      syncing = false,
      requested = false,
      attempt = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let ws: WebSocket | null = null;
    const revoke = () => {
      chatStore.revoke();
      client.removeQueries({
        predicate: (q) =>
          ["members", "workspaceAgents"].includes(String(q.queryKey[0])),
      });
      void client.resetQueries({ queryKey: ["bootstrap"] });
    };
    api.onForbidden = revoke;
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
          while (hasMore && !stopped) {
            const cursor = chatStore.getSnapshot().cursor;
            const page = await api.sync(cursor);
            if (stopped || generation !== chatStore.generation) return;
            if (page.hasMore && page.cursor === cursor)
              throw new Error("同步游标未前进");
            if (page.events.some((e) => e.type === "membership.changed")) {
              const fresh = await api.bootstrap();
              if (stopped || generation !== chatStore.generation) return;
              chatStore.replaceConversations(fresh.conversations);
              const allowed = new Set(fresh.conversations.map((c) => c.id));
              page.events = page.events.filter((event) => {
                const id =
                  event.conversationId ||
                  event.message?.conversationId ||
                  event.conversation?.id;
                return !id || allowed.has(id);
              });
              client.setQueryData(["bootstrap", user!.id], fresh);
              void client.invalidateQueries({ queryKey: ["members"] });
              void client.invalidateQueries({ queryKey: ["workspaceAgents"] });
            }
            chatStore.apply(page);
            hasMore = page.hasMore;
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
      };
      ws.onmessage = (event) => {
        try {
          const hint = JSON.parse(event.data);
          if (hint.type === "sync.required" || hint.type === "ready")
            void sync();
          if (
            hint.type === "typing" &&
            hint.userId !== user!.id &&
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
      if (timer) clearTimeout(timer);
      if (ws) {
        ws.onclose = null;
        ws.close();
      }
      connect();
      void sync();
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
      api.onForbidden = undefined;
      window.removeEventListener("online", online);
      window.removeEventListener("offline", offline);
      document.removeEventListener("visibilitychange", focus);
    };
  }, [user, client]);
  const sendTyping = useCallback((conversationId: string, value: boolean) => {
    if (
      capabilities.current["im.typing.publish"] &&
      socket.current?.readyState === WebSocket.OPEN
    )
      socket.current.send(
        JSON.stringify({ type: "typing", conversationId, isTyping: value }),
      );
  }, []);
  return (
    <ChatContext.Provider
      value={{
        data: query.data,
        loading: query.isLoading,
        error: query.error ? "无法加载你的工作空间，请稍后重试。" : "",
        connection,
        syncError,
        refresh: query.refetch,
        sync: () => syncRef.current(),
        sendTyping,
        typing,
      }}
    >
      {children}
    </ChatContext.Provider>
  );
}
export function useChat() {
  const ctx = useContext(ChatContext);
  if (!ctx) throw new Error("Chat provider missing");
  return ctx;
}
export function errorText(error: unknown) {
  if (!(error instanceof ApiError))
    return error instanceof Error ? error.message : "请求失败，请稍后重试";
  const messages: Record<string, string> = {
    invalid_credentials: "邮箱或密码不正确，请重新输入。",
    invalid_invite: "邀请码无效、已使用或已失效，请联系管理员获取新的邀请。",
    invalid_invitation:
      "邀请码无效、已使用或已失效，请联系管理员获取新的邀请。",
    forbidden: "你当前没有执行此操作的权限。",
    conflict: "这条消息与之前的发送记录冲突，请刷新后重试。",
    feature_disabled: "此功能已由工作空间管理员关闭。",
    rate_limited: "操作过于频繁，请稍后再试。",
    email_exists: "此邮箱已注册，请直接登录。",
  };
  return messages[error.code] || error.message;
}
export async function sendMessage(message: Message, generation: number) {
  chatStore.merge([{ ...message, status: "sending" }]);
  try {
    const response = await api.send(message.conversationId, {
      clientMessageId: message.clientMessageId,
      body: message.body,
      replyToId: message.replyToId,
    });
    if (chatStore.generation === generation)
      chatStore.merge([response.message]);
  } catch (error) {
    if (chatStore.generation === generation)
      chatStore.merge([
        { ...message, status: "failed", error: errorText(error) },
      ]);
  }
}
