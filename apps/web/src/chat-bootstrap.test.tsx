// @vitest-environment jsdom
import { act, StrictMode, useEffect } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@teamie/api-client";
import type { Bootstrap, Conversation, Message, SyncPage, User } from "@teamie/api-client";
import { ChatProvider, useChat } from "./chat";
import { setActiveNotificationContext, setNotificationPreferences } from "./notifications";
import { chatStore } from "./store";

const auth = vi.hoisted(() => ({
  user: {
    id: "bootstrap-user",
    email: "bootstrap@example.test",
    displayName: "Current user",
    kind: "human" as const,
    platformRole: "user" as const,
    createdAt: "2026-10-17T00:00:00Z",
  } as User,
  reconcileSession: vi.fn(),
}));
vi.mock("./auth", () => ({ useAuth: () => auth }));

const first: Conversation = {
  id: "first-conversation",
  workspaceId: "workspace",
  type: "channel",
  name: "First channel",
  memberIds: [],
  lastSeq: 0,
  readSeq: 0,
  updatedAt: "2026-10-17T00:00:00Z",
};
const second: Conversation = { ...first, id: "new-conversation", name: "New channel" };
const message: Message = {
  id: "new-message",
  conversationId: second.id,
  senderId: auth.user.id,
  senderName: auth.user.displayName,
  senderKind: "human",
  clientMessageId: "new-send",
  seq: 1,
  body: "A message already accepted by sync",
  createdAt: first.updatedAt,
};
function bootstrap(conversations = [first]): Bootstrap {
  return {
    user: auth.user,
    workspaces: [{ id: first.workspaceId, name: "Workspace", createdBy: auth.user.id, createdAt: first.updatedAt }],
    conversations,
    capabilities: {},
    flagsRevision: "bootstrap-test",
    syncCursor: "0",
  };
}
function membershipPage(): SyncPage {
  return { events: [{ id: "1", type: "membership.changed", workspaceId: first.workspaceId, createdAt: first.updatedAt }], cursor: "1", hasMore: false };
}
function createdPage(): SyncPage {
  return {
    events: [
      { id: "2", type: "conversation.created", conversationId: second.id, conversation: second, createdAt: second.updatedAt },
      { id: "3", type: "message.created", conversationId: second.id, message, createdAt: message.createdAt },
    ],
    cursor: "3",
    hasMore: false,
  };
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

let container: HTMLDivElement;
let root: Root;
let client: QueryClient;
let sync: () => void;
function Probe() {
  const chat = useChat();
  useEffect(() => { sync = chat.sync; }, [chat.sync]);
  return null;
}
async function settle(action?: () => void) {
  await act(async () => {
    action?.();
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}
function render() {
  root.render(<StrictMode><QueryClientProvider client={client}><ChatProvider><Probe /></ChatProvider></QueryClientProvider></StrictMode>);
}
async function mount() {
  await settle(render);
  await settle();
  expect(api.bootstrap).toHaveBeenCalledOnce();
  expect(chatStore.getSnapshot().conversations[first.id]).toBeDefined();
}
function expectNewConversation(cursor: string) {
  expect(chatStore.getSnapshot().cursor).toBe(cursor);
  expect(chatStore.getSnapshot().conversations[second.id]).toBeDefined();
  expect(chatStore.getSnapshot().messages[second.id]).toEqual([message]);
}
beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal("WebSocket", class {
    static OPEN = 1;
    readyState = 0;
    close() {}
  });
  auth.user = { ...auth.user, displayName: "Current user" };
  auth.reconcileSession.mockReset();
  api.csrfToken = "current-session";
  chatStore.clear();
  chatStore.begin(auth.user.id);
  vi.spyOn(api, "bootstrap").mockImplementation(async () => bootstrap());
  vi.spyOn(api, "sync").mockImplementation(async () => ({ events: [], cursor: chatStore.getSnapshot().cursor, hasMore: false }));
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
});
afterEach(async () => {
  await settle(() => root.unmount());
  client.clear();
  chatStore.clear();
  api.csrfToken = "";
  container.remove();
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("ChatProvider bootstrap ordering", () => {
  it.each(["unchanged", "retracted", "read", "revoked", "reset"] as const)(
    "retains committed notification candidates across a failed sync page and reconciles %s messages on retry",
    async (change) => {
      const notifications: NotificationOptions[] = [];
      vi.stubGlobal("Notification", class {
        static permission = "granted";
        constructor(_title: string, options: NotificationOptions) {
          notifications.push(options);
        }
        close() {}
      });
      setActiveNotificationContext(null);
      setNotificationPreferences({ enabled: true }, auth.user.id);
      await mount();
      const incoming: Message = {
        ...message,
        conversationId: first.id,
        senderId: "other-user",
        senderName: "Other user",
        mentions: [{ userId: auth.user.id, start: 0, end: 1 }],
      };
      vi.mocked(api.sync)
        .mockResolvedValueOnce({
          events: [{ id: "1", type: "message.created", conversationId: first.id, message: incoming, createdAt: incoming.createdAt }],
          cursor: "1",
          hasMore: true,
        })
        .mockRejectedValueOnce(new Error("Second page unavailable"));
      await settle(() => sync());
      expect(chatStore.getSnapshot().cursor).toBe("1");
      expect(chatStore.getSnapshot().messages[first.id]).toEqual([incoming]);
      expect(notifications).toHaveLength(0);
      if (change === "reset") {
        vi.mocked(api.sync).mockRejectedValueOnce(new ApiError(409, "invalid_cursor", "Cursor expired"));
        await settle(() => sync());
        expect(chatStore.getSnapshot().cursor).toBe("0");
      }

      // A profile update must not discard the pending candidates either.
      auth.user = { ...auth.user, displayName: "Updated profile" };
      await settle(render);
      const retryPage: SyncPage = { events: [], cursor: "1", hasMore: false };
      if (change === "retracted") {
        retryPage.events = [{ id: "2", type: "message.retracted", conversationId: first.id, message: { ...incoming, body: "", retractedAt: "2026-10-17T00:01:00Z" }, createdAt: incoming.createdAt }];
      } else if (change === "read") {
        retryPage.events = [{ id: "2", type: "read.updated", conversationId: first.id, readSeq: incoming.seq, createdAt: incoming.createdAt }];
      } else if (change === "revoked") {
        retryPage.events = [{ id: "2", type: "membership.changed", workspaceId: first.workspaceId, createdAt: incoming.createdAt }];
        vi.mocked(api.bootstrap).mockResolvedValueOnce(bootstrap([]));
      } else if (change === "reset") {
        retryPage.events = [
          { id: "1", type: "conversation.created", conversationId: first.id, conversation: first, createdAt: first.updatedAt },
          { id: "2", type: "message.created", conversationId: first.id, message: incoming, createdAt: incoming.createdAt },
        ];
      }
      if (retryPage.events.length) retryPage.cursor = "2";
      vi.mocked(api.sync).mockResolvedValueOnce(retryPage);
      await settle(() => sync());
      await settle();
      expect(api.sync).toHaveBeenLastCalledWith(change === "reset" ? "0" : "1");
      expect(notifications).toHaveLength(change === "unchanged" ? 1 : 0);
      if (change === "unchanged") {
        expect(notifications[0].tag).toBe(`teamie-message-${incoming.id}`);
      }
      await settle(() => sync());
      expect(notifications).toHaveLength(change === "unchanged" ? 1 : 0);
    },
  );

  it("refreshes attention projections after durable remote reads and message changes", async () => {
    await mount();
    const invalidate = vi.spyOn(client, "invalidateQueries");
    vi.mocked(api.sync).mockResolvedValueOnce({ events: [{ id: "1", type: "read.updated", conversationId: first.id, readSeq: 1, createdAt: first.updatedAt }], cursor: "1", hasMore: false });
    await settle(() => sync());
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["attention", auth.user.id] });
    invalidate.mockClear();
    vi.mocked(api.sync).mockResolvedValueOnce({ events: [{ id: "2", type: "message.retracted", conversationId: first.id, message: { ...message, conversationId: first.id, body: "", retractedAt: first.updatedAt }, createdAt: first.updatedAt }], cursor: "2", hasMore: false });
    await settle(() => sync());
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["attention", auth.user.id] });
  });
  it("does not reapply an accepted membership snapshot after newer sync and a profile change", async () => {
    await mount();
    vi.mocked(api.sync).mockResolvedValueOnce(membershipPage());
    await settle(() => sync());
    await settle();
    expect(api.bootstrap).toHaveBeenCalledTimes(2);
    chatStore.apply(createdPage());
    expectNewConversation("3");
    auth.user = { ...auth.user, displayName: "Updated profile" };
    await settle(render);
    await settle();
    expectNewConversation("3");
    expect(api.bootstrap).toHaveBeenCalledTimes(2);
  });

  it("keeps the next synced conversation between setQueryData and the query effect", async () => {
    await mount();
    vi.mocked(api.sync).mockResolvedValueOnce(membershipPage()).mockResolvedValueOnce(createdPage());
    const writeQuery = client.setQueryData.bind(client);
    let queued = false;
    vi.spyOn(client, "setQueryData").mockImplementation((key, updater, options) => {
      const result = writeQuery(key, updater, options);
      if (key[0] === "bootstrap" && !queued) {
        queued = true;
        // The active serial sync loop queues another pass before React Query
        // notifies its observer. This models a new WebSocket hint arriving.
        sync();
      }
      return result;
    });
    await settle(() => sync());
    await settle();
    expect(queued).toBe(true);
    expectNewConversation("3");
    expect(api.bootstrap).toHaveBeenCalledTimes(2);
    await settle();
    expect(api.bootstrap).toHaveBeenCalledTimes(2);
  });

  it("rechecks an invalidated membership snapshot before pruning or advancing its cursor", async () => {
    await mount();
    const stale = deferred<Bootstrap>();
    const current = deferred<Bootstrap>();
    vi.mocked(api.bootstrap).mockReturnValueOnce(stale.promise).mockReturnValueOnce(current.promise);
    vi.mocked(api.sync).mockResolvedValueOnce(membershipPage());
    await settle(() => sync());
    expect(api.bootstrap).toHaveBeenCalledTimes(2);
    chatStore.addConversation(second);
    chatStore.merge([message]);
    await settle(() => stale.resolve(bootstrap()));
    expect(api.bootstrap).toHaveBeenCalledTimes(3);
    expectNewConversation("0");
    await settle(() => current.resolve(bootstrap([first, second])));
    await settle();
    expectNewConversation("1");
    expect(api.bootstrap).toHaveBeenCalledTimes(3);
    await settle();
    expect(api.bootstrap).toHaveBeenCalledTimes(3);
  });
});
