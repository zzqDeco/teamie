// @vitest-environment jsdom
import { act, StrictMode, useEffect } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type { Bootstrap, Conversation, Member, Message, SyncPage, User } from "@teamie/api-client";
import { ChatProvider, useChat, useChatState } from "./chat";
import { markMessageAsUnread } from "./chat/collaboration-ui";
import { ConversationView } from "./messages";
import { ordinaryUnreadCount } from "./notification-model";
import { chatStore } from "./store";
import { conversationUnreadSummary } from "./unread-badge";

const auth = vi.hoisted(() => ({
  user: {
    id: "manual-unread-user",
    email: "unread@example.test",
    displayName: "当前成员",
    kind: "human" as const,
    platformRole: "user" as const,
    createdAt: "2026-10-20T00:00:00Z",
  } as User,
  reconcileSession: vi.fn(),
}));
vi.mock("./auth", () => ({ useAuth: () => auth }));

const members: Member[] = [
  { userId: auth.user.id, displayName: auth.user.displayName, kind: "human", role: "owner" },
  { userId: "peer-1", displayName: "协作者", kind: "human", role: "member" },
];

const baseConversation: Conversation = {
  id: "conv-unread",
  workspaceId: "ws-unread",
  type: "group",
  name: "研发同步",
  memberIds: members.map((m) => m.userId),
  lastSeq: 5,
  readSeq: 5,
  updatedAt: "2026-10-20T01:00:00Z",
};

function makeMessage(seq: number): Message {
  return {
    id: `msg-${seq}`,
    clientMessageId: `client-${seq}`,
    conversationId: baseConversation.id,
    senderId: "peer-1",
    senderName: "协作者",
    senderKind: "human",
    seq,
    body: `消息 #${seq}`,
    createdAt: new Date(Date.parse(auth.user.createdAt) + seq * 60_000).toISOString(),
  };
}

function makeBootstrap(conversations: Conversation[] = [baseConversation]): Bootstrap {
  return {
    user: auth.user,
    workspaces: [
      { id: baseConversation.workspaceId, name: "协作空间", createdBy: auth.user.id, createdAt: auth.user.createdAt },
    ],
    conversations,
    capabilities: { "im.unread.mark": true },
    flagsRevision: "unread-bootstrap-v1",
    syncCursor: "0",
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

let container: HTMLDivElement;
let root: Root;
let client: QueryClient;
let triggerSync: () => void;

function LiveConversationView() {
  const chat = useChat();
  const snapshot = useChatState();
  useEffect(() => {
    triggerSync = chat.sync;
  }, [chat.sync]);
  const conv = snapshot.conversations[baseConversation.id];
  if (!conv) return null;
  return (
    <ConversationView
      conversation={conv}
      members={members}
      onMembers={() => {}}
      membersOpen={false}
      onMenu={() => {}}
    />
  );
}

async function settle(action?: () => void | Promise<void>) {
  await act(async () => {
    await action?.();
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

function renderProvider() {
  root.render(
    <StrictMode>
      <QueryClientProvider client={client}>
        <ChatProvider>
          <LiveConversationView />
        </ChatProvider>
      </QueryClientProvider>
    </StrictMode>,
  );
}

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal(
    "WebSocket",
    class {
      static OPEN = 1;
      readyState = 0;
      close() {}
    },
  );
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      disconnect() {}
    },
  );
  Object.defineProperty(window, "matchMedia", {
    writable: true,
    value: (query: string) => ({
      matches: false,
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
    }),
  });
  Object.defineProperty(HTMLElement.prototype, "scrollTo", { configurable: true, value: vi.fn() });
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: vi.fn() });

  auth.reconcileSession.mockReset();
  api.csrfToken = "session-csrf";
  chatStore.clear();
  chatStore.begin(auth.user.id);
  vi.spyOn(api, "bootstrap").mockImplementation(async () => makeBootstrap());
  vi.spyOn(api, "sync").mockImplementation(async () => ({
    events: [],
    cursor: chatStore.getSnapshot().cursor,
    hasMore: false,
  }));
  vi.spyOn(api, "messages").mockResolvedValue({
    messages: [makeMessage(1), makeMessage(2), makeMessage(3), makeMessage(4), makeMessage(5)],
    hasMore: false,
  });
  vi.spyOn(api, "listPins").mockResolvedValue({ pins: [], pinsVersion: "1" });
  vi.spyOn(api, "listConversationSavedMessages").mockResolvedValue({ messageIds: [] });
  vi.spyOn(api, "read").mockImplementation(async (_id, seq) => ({ readSeq: seq }));

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

describe("Authoritative bootstrap & manual unread concurrency (#129)", () => {
  it("clears a stale manual unread marker (and updates UI banner/summary) when an authoritative bootstrap refresh omits manualUnreadSeq", async () => {
    vi.mocked(api.bootstrap).mockResolvedValueOnce(
      makeBootstrap([{ ...baseConversation, manualUnreadSeq: 2 }]),
    );
    await settle(renderProvider);
    await settle();

    const snapBefore = chatStore.getSnapshot();
    expect(snapBefore.conversations[baseConversation.id].manualUnreadSeq).toBe(2);
    expect(snapBefore.manualUnreadByConversation[baseConversation.id]).toBe(2);
    expect(conversationUnreadSummary(snapBefore.conversations[baseConversation.id], snapBefore, auth.user.id).badgeCount).toBe(4);
    expect(container.querySelector(".manual-unread-banner")).not.toBeNull();

    // Another device clears the marker; authoritative bootstrap omits manualUnreadSeq.
    vi.mocked(api.bootstrap).mockResolvedValueOnce(makeBootstrap([{ ...baseConversation }]));
    await settle(() => client.invalidateQueries({ queryKey: ["bootstrap", auth.user.id] }));
    await settle();

    const snapAfter = chatStore.getSnapshot();
    expect(snapAfter.conversations[baseConversation.id].manualUnreadSeq).toBeUndefined();
    expect(snapAfter.manualUnreadByConversation[baseConversation.id]).toBeUndefined();
    expect(ordinaryUnreadCount(snapAfter.conversations[baseConversation.id])).toBe(0);
    expect(conversationUnreadSummary(snapAfter.conversations[baseConversation.id], snapAfter, auth.user.id).hasUnread).toBe(false);
    expect(container.querySelector(".manual-unread-banner")).toBeNull();
  });

  it("reconciles manualUnreadSeq to a newer authoritative value while preserving monotonic readSeq", async () => {
    vi.mocked(api.bootstrap).mockResolvedValueOnce(
      makeBootstrap([{ ...baseConversation, lastSeq: 8, readSeq: 8, manualUnreadSeq: 2 }]),
    );
    await settle(renderProvider);
    await settle();

    vi.mocked(api.bootstrap).mockResolvedValueOnce(
      makeBootstrap([{ ...baseConversation, lastSeq: 8, readSeq: 5, manualUnreadSeq: 6 }]),
    );
    await settle(() => client.invalidateQueries({ queryKey: ["bootstrap", auth.user.id] }));
    await settle();

    const conv = chatStore.getSnapshot().conversations[baseConversation.id];
    expect(conv.manualUnreadSeq).toBe(6);
    expect(conv.readSeq).toBe(8);
    expect(chatStore.getSnapshot().manualUnreadByConversation[baseConversation.id]).toBe(6);
    expect(ordinaryUnreadCount(conv)).toBe(3);
  });

  it("rejects a stale in-flight observedBootstrap started before setManualUnread or read.updated and refetches a fresh snapshot", async () => {
    await settle(renderProvider);
    await settle();
    expect(api.bootstrap).toHaveBeenCalledTimes(1);

    const staleBootstrap = deferred<Bootstrap>();
    const freshBootstrap = deferred<Bootstrap>();
    vi.mocked(api.bootstrap)
      .mockReturnValueOnce(staleBootstrap.promise)
      .mockReturnValueOnce(freshBootstrap.promise);

    // Start bootstrap #2 BEFORE local marker change.
    await settle(() => {
      void client.invalidateQueries({ queryKey: ["bootstrap", auth.user.id] });
    });
    expect(api.bootstrap).toHaveBeenCalledTimes(2);

    // Sync delivers a read.updated setting manualUnreadSeq = 3 while bootstrap #2 is still in flight.
    const syncPage: SyncPage = {
      events: [
        {
          id: "1",
          type: "read.updated",
          conversationId: baseConversation.id,
          readSeq: 5,
          manualUnreadSeq: 3,
          createdAt: baseConversation.updatedAt,
        },
      ],
      cursor: "1",
      hasMore: false,
    };
    vi.mocked(api.sync).mockResolvedValueOnce(syncPage);
    await settle(() => triggerSync());
    expect(chatStore.getSnapshot().conversations[baseConversation.id].manualUnreadSeq).toBe(3);

    // Stale bootstrap #2 resolves WITHOUT manualUnreadSeq -> rejected, marker preserved, triggers bootstrap #3!
    await settle(() => staleBootstrap.resolve(makeBootstrap([{ ...baseConversation }])));
    expect(chatStore.getSnapshot().conversations[baseConversation.id].manualUnreadSeq).toBe(3);
    expect(chatStore.getSnapshot().manualUnreadByConversation[baseConversation.id]).toBe(3);
    await vi.waitFor(() => expect(api.bootstrap).toHaveBeenCalledTimes(3));

    // Fresh bootstrap #3 resolves with the committed server state.
    await settle(() => freshBootstrap.resolve(makeBootstrap([{ ...baseConversation, manualUnreadSeq: 3 }])));
    await settle();
    expect(chatStore.getSnapshot().conversations[baseConversation.id].manualUnreadSeq).toBe(3);
    expect(api.bootstrap).toHaveBeenCalledTimes(3);
  });

  it("preserves a newly marked unread state when bootstrap overlaps an in-flight markMessageAsUnread request (both resolution orders)", async () => {
    await settle(renderProvider);
    await settle();

    // Order A: bootstrap starts DURING in-flight markConversationUnread and resolves BEFORE the write settles.
    const markReqA = deferred<{ conversationId: string; manualUnreadSeq: number }>();
    const midFlightBootstrapA = deferred<Bootstrap>();
    vi.spyOn(api, "markConversationUnread").mockReturnValueOnce(markReqA.promise);
    vi.mocked(api.bootstrap).mockReturnValueOnce(midFlightBootstrapA.promise);

    let markDoneA = false;
    let pendingMarkA!: Promise<void>;
    await settle(() => {
      pendingMarkA = markMessageAsUnread(baseConversation.id, makeMessage(3)).then(() => {
        markDoneA = true;
      });
    });
    expect(chatStore.getSnapshot().conversations[baseConversation.id].manualUnreadSeq).toBe(3);

    await settle(() => {
      void client.invalidateQueries({ queryKey: ["bootstrap", auth.user.id] });
    });
    // Server has not committed POST /unread yet, so bootstrap returns without manualUnreadSeq.
    await settle(() => midFlightBootstrapA.resolve(makeBootstrap([{ ...baseConversation }])));
    expect(markDoneA).toBe(false);
    expect(chatStore.getSnapshot().conversations[baseConversation.id].manualUnreadSeq).toBe(3);
    expect(chatStore.getSnapshot().manualUnreadByConversation[baseConversation.id]).toBe(3);

    await settle(() => markReqA.resolve({ conversationId: baseConversation.id, manualUnreadSeq: 3 }));
    await pendingMarkA;
    expect(chatStore.getSnapshot().conversations[baseConversation.id].manualUnreadSeq).toBe(3);
    expect(chatStore.getSnapshot().manualUnreadByConversation[baseConversation.id]).toBe(3);

    // Order B: bootstrap starts DURING in-flight markConversationUnread, write settles FIRST, then stale pre-commit bootstrap resolves.
    const markReqB = deferred<{ conversationId: string; manualUnreadSeq: number }>();
    const preCommitBootstrapB = deferred<Bootstrap>();
    const postCommitBootstrapB = deferred<Bootstrap>();
    vi.mocked(api.markConversationUnread).mockReturnValueOnce(markReqB.promise);
    vi.mocked(api.bootstrap)
      .mockReturnValueOnce(preCommitBootstrapB.promise)
      .mockReturnValueOnce(postCommitBootstrapB.promise);

    let pendingMarkB!: Promise<void>;
    await settle(() => {
      pendingMarkB = markMessageAsUnread(baseConversation.id, makeMessage(2));
    });
    await settle(() => {
      void client.invalidateQueries({ queryKey: ["bootstrap", auth.user.id] });
    });
    const callsBeforeSettle = vi.mocked(api.bootstrap).mock.calls.length;

    // Write settles FIRST with manualUnreadSeq = 2.
    await settle(() => markReqB.resolve({ conversationId: baseConversation.id, manualUnreadSeq: 2 }));
    await pendingMarkB;

    // Pre-commit bootstrap resolves late with stale manualUnreadSeq = 3 -> rejected and refetched!
    await settle(() => preCommitBootstrapB.resolve(makeBootstrap([{ ...baseConversation, manualUnreadSeq: 3 }])));
    expect(chatStore.getSnapshot().conversations[baseConversation.id].manualUnreadSeq).toBe(2);
    expect(chatStore.getSnapshot().manualUnreadByConversation[baseConversation.id]).toBe(2);
    await vi.waitFor(() => expect(vi.mocked(api.bootstrap).mock.calls.length).toBe(callsBeforeSettle + 1));

    await settle(() => postCommitBootstrapB.resolve(makeBootstrap([{ ...baseConversation, manualUnreadSeq: 2 }])));
    await settle();
    expect(chatStore.getSnapshot().conversations[baseConversation.id].manualUnreadSeq).toBe(2);
  });

  it("does not resurrect a cleared manual unread marker when bootstrap overlaps an in-flight clearConversationUnread call", async () => {
    vi.mocked(api.bootstrap).mockResolvedValueOnce(
      makeBootstrap([{ ...baseConversation, manualUnreadSeq: 2 }]),
    );
    const clearReq = deferred<void>();
    vi.spyOn(api, "clearConversationUnread").mockReturnValueOnce(clearReq.promise);
    await settle(renderProvider);
    await settle();

    const bannerBtn = container.querySelector<HTMLButtonElement>(".manual-unread-banner button");
    expect(bannerBtn?.textContent).toBe("标为已读");

    const preClearBootstrap = deferred<Bootstrap>();
    vi.mocked(api.bootstrap).mockReturnValueOnce(preClearBootstrap.promise);

    // Click 标为已读 while clearConversationUnread stays in flight.
    await settle(() => bannerBtn!.click());
    expect(chatStore.getSnapshot().conversations[baseConversation.id].manualUnreadSeq).toBeUndefined();

    // Bootstrap starts while DELETE /unread is still in flight on the server and returns stale manualUnreadSeq: 2.
    await settle(() => {
      void client.invalidateQueries({ queryKey: ["bootstrap", auth.user.id] });
    });
    await settle(() => preClearBootstrap.resolve(makeBootstrap([{ ...baseConversation, manualUnreadSeq: 2 }])));
    expect(chatStore.getSnapshot().conversations[baseConversation.id].manualUnreadSeq).toBeUndefined();
    expect(chatStore.getSnapshot().manualUnreadByConversation[baseConversation.id]).toBeUndefined();

    await settle(() => clearReq.resolve(undefined));
    expect(chatStore.getSnapshot().conversations[baseConversation.id].manualUnreadSeq).toBeUndefined();
    expect(container.querySelector(".manual-unread-banner")).toBeNull();
  });
});
