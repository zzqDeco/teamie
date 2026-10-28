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
import { typingStore } from "./typing-store";
import { presenceStore, receiptStore } from "./engagement-store";
import { errorText, isNetworkError } from "./errors";
export { errorText, isNetworkError };
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
      const previous = chatStore.getSnapshot().conversations;
      const changedReaders = appliedBootstrap.current !== null && (
        Object.keys(previous).length !== query.data.conversations.length ||
        query.data.conversations.some((c) => !previous[c.id] || previous[c.id].memberIds.join("\0") !== c.memberIds.join("\0"))
      );
      if (!chatStore.replaceConversations(query.data.conversations, query.data[bootstrapObservation])) {
        void client.invalidateQueries({ queryKey: ["bootstrap", userId] });
        return;
      }
      appliedBootstrap.current = query.data;
      capabilities.current = query.data.capabilities;
      if (changedReaders) receiptStore.invalidateAll(true);
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
    let idleTimer: ReturnType<typeof setTimeout> | undefined;
    let ws: WebSocket | null = null;
    let lastPresenceSent: { state: "active" | "idle" | ""; at: number } = {
      state: "",
      at: 0,
    };
    const sendPresence = (state: "active" | "idle", force = false) => {
      if (
        stopped ||
        !capabilities.current["im.presence.publish"] ||
        !ws ||
        ws.readyState !== WebSocket.OPEN
      ) {
        return;
      }
      const now = Date.now();
      if (
        !force &&
        lastPresenceSent.state === state &&
        (state === "idle" || now - lastPresenceSent.at < 30_000)
      ) {
        return;
      }
      lastPresenceSent = { state, at: now };
      try {
        ws.send(JSON.stringify({ type: "presence", state }));
      } catch {
        /* Ignore transient WebSocket send errors. */
      }
    };
    const scheduleIdlePresence = () => {
      if (idleTimer) clearTimeout(idleTimer);
      idleTimer = setTimeout(() => {
        sendPresence("idle");
      }, 5 * 60_000);
    };
    const recordUserActivity = () => {
      if (document.visibilityState === "hidden") return;
      scheduleIdlePresence();
      sendPresence("active");
    };
    // Applied pages advance the durable cursor. Keep their notification
    // candidates until pagination completes, including across transient retries.
    const notifyCandidates: Array<{
      conversationId: string;
      messageId: string;
    }> = [];
    let notificationGeneration = chatStore.generation;
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
      if (notificationGeneration !== generation) {
        notifyCandidates.length = 0;
        notificationGeneration = generation;
      }
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
              receiptStore.invalidateAll(true);
              resetNotificationSession(userId);
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
            if (initialSyncCompleted) {
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
              notifyCandidates.splice(0),
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
          ) {
            chatStore.revoke();
            notifyCandidates.length = 0;
            initialSyncCompleted = false;
            completedInitialSyncUser.current = null;
          }
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
        lastPresenceSent = { state: "", at: 0 };
        setConnection("connected");
        void sync();
        void retryTransientFailures(chatStore.generation);
        if (capabilities.current["im.read_receipts.view"]) {
          receiptStore.invalidateAll();
        }
        if (document.visibilityState === "hidden") {
          sendPresence("idle", true);
        } else {
          scheduleIdlePresence();
          sendPresence("active", true);
        }
      };
      ws.onmessage = (event) => {
        try {
          const hint = JSON.parse(event.data);
          if (hint.type === "sync.required" || hint.type === "ready")
            void sync();
          if (
            hint.type === "ready" &&
            capabilities.current["im.read_receipts.view"]
          ) {
            receiptStore.invalidateAll();
          }
          if (
            hint.type === "typing" &&
            hint.userId !== userId &&
            capabilities.current["im.typing.publish"]
          ) {
            typingStore.hint(
              hint.conversationId,
              hint.userId,
              hint.displayName,
              !!hint.isTyping,
            );
          }
          if (
            hint.type === "receipts.changed" &&
            typeof hint.conversationId === "string" &&
            capabilities.current["im.read_receipts.view"]
          ) {
            receiptStore.invalidate(hint.conversationId);
          }
          if (
            hint.type === "presence" &&
            typeof hint.userId === "string" &&
            (hint.state === "online" ||
              hint.state === "away" ||
              hint.state === "offline") &&
            capabilities.current["im.presence.publish"]
          ) {
            presenceStore.hint(hint.userId, hint.state);
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
      else {
        receiptStore.invalidateAll();
        void sync();
      }
    };
    const offline = () => {
      setConnection("offline");
      ws?.close();
    };
    const focus = () => {
      if (document.visibilityState === "visible") {
        void sync();
        scheduleIdlePresence();
        sendPresence("active");
      } else {
        if (idleTimer) clearTimeout(idleTimer);
        sendPresence("idle");
      }
    };
    const interval = setInterval(() => {
      if (navigator.onLine) void sync();
    }, 10_000);
    const typingTimer = setInterval(() => typingStore.expire(), 1000);
    connect();
    window.addEventListener("online", online);
    window.addEventListener("offline", offline);
    window.addEventListener("pointerdown", recordUserActivity, { passive: true });
    window.addEventListener("keydown", recordUserActivity, { passive: true });
    document.addEventListener("visibilitychange", focus);
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
      if (idleTimer) clearTimeout(idleTimer);
      clearInterval(interval);
      clearInterval(typingTimer);
      typingStore.reset();
      presenceStore.reset();
      receiptStore.reset();
      if (ws) {
        ws.onclose = null;
        ws.close();
      }
      socket.current = null;
      if (api.onForbidden === refreshPermissions) api.onForbidden = undefined;
      window.removeEventListener("online", online);
      window.removeEventListener("offline", offline);
      window.removeEventListener("pointerdown", recordUserActivity);
      window.removeEventListener("keydown", recordUserActivity);
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
    ],
  );
  return <ChatContext.Provider value={value}>{children}</ChatContext.Provider>;
}
export function useChat() {
  const ctx = useContext(ChatContext);
  if (!ctx) throw new Error("Chat provider missing");
  return ctx;
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
    if (chatStore.generation === generation) {
      const permanent = isPermanentSendError(error);
      const text = errorText(error);
      chatStore.merge([
        {
          ...message,
          status: "failed",
          error:
            permanent && text === "请求失败，请稍后重试。"
              ? "消息内容无法发送，请删除后重新编辑。"
              : text,
          permanent,
        },
      ]);
    }
  }
}
let activeRetryDrain: Promise<void> | null = null;
let activeRetryGeneration = -1;
let retryDrainRequested = false;

// Transient failures retry with the same clientMessageId, which the server
// treats idempotently, so a retry after a lost response cannot duplicate.
export async function retryTransientFailures(generation: number) {
  if (chatStore.generation !== generation) return;
  if (activeRetryDrain && activeRetryGeneration === generation) {
    retryDrainRequested = true;
    return activeRetryDrain;
  }
  const run = (async () => {
    do {
      retryDrainRequested = false;
      if (chatStore.generation !== generation) return;
      const queued = Object.values(chatStore.getSnapshot().messages).flatMap(
        (messages) =>
          messages.filter(
            (m) => m.status === "failed" && m.permanent === false,
          ),
      );
      for (const item of queued) {
        if (chatStore.generation !== generation) return;
        const current = (
          chatStore.getSnapshot().messages[item.conversationId] || []
        ).find((m) => m.clientMessageId === item.clientMessageId);
        if (
          current &&
          current.status === "failed" &&
          current.permanent === false
        )
          await sendMessage(current, generation);
      }
    } while (retryDrainRequested && chatStore.generation === generation);
  })();
  const tracked = run.finally(() => {
    if (activeRetryDrain === tracked) {
      activeRetryDrain = null;
      activeRetryGeneration = -1;
    }
  });
  activeRetryGeneration = generation;
  activeRetryDrain = tracked;
  return tracked;
}
