// @vitest-environment jsdom
import { act, useEffect } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type { AttentionItem, AttentionPage, Bootstrap, Conversation, Message, SyncPage, User, Workspace } from "@teamie/api-client";
import { ChatProvider, useChat } from "./chat";
import { useAttentionCount } from "./attention";
import { chatStore, summarizeConversationUnread } from "./store";
import { computeShellAttention } from "./shell-sidebar";
import { fetchAttentionCount } from "./notification-projection";
import { receiptStore, useConversationReceiptsSync } from "./engagement-store";
import { notifySyncedMessages, resetNotificationSession, setActiveNotificationContext, setNotificationPreferences } from "./notifications";

const auth = vi.hoisted(() => ({
  user: { id: "verify-self", email: "self@example.test", displayName: "Self", kind: "human", platformRole: "user", createdAt: "2026-10-27T00:00:00Z" } as User,
  reconcileSession: vi.fn(),
}));
vi.mock("./auth", () => ({ useAuth: () => auth }));

const workspace: Workspace = { id: "verify-w", name: "Workspace", createdBy: auth.user.id, createdAt: auth.user.createdAt };
function conversation(id: string, type: Conversation["type"], level: Conversation["notificationLevel"], lastSeq = 3): Conversation {
  return { id, workspaceId: workspace.id, name: id, type, memberIds: [auth.user.id, "verify-peer"], lastSeq, readSeq: 0, notificationLevel: level, updatedAt: auth.user.createdAt };
}
function message(c: Conversation, seq: number, mentioned = true): Message {
  return { id: `${c.id}-m${seq}`, conversationId: c.id, senderId: "verify-peer", senderName: "Peer", senderKind: "human", clientMessageId: `${c.id}-client${seq}`, seq, body: mentioned ? "@Self attention" : "ordinary activity", mentions: mentioned ? [{ userId: auth.user.id, start: 0, end: 5 }] : undefined, createdAt: auth.user.createdAt };
}
function item(c: Conversation, seq: number): AttentionItem {
  return { message: message(c, seq), conversation: { id: c.id, workspaceId: c.workspaceId, name: c.name, type: c.type }, workspaceName: workspace.name, reasons: ["mention"], isRead: false };
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

class FakeSocket {
  static OPEN = 1;
  static instances: FakeSocket[] = [];
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  constructor() { FakeSocket.instances.push(this); }
  send() {}
  close() { this.readyState = 3; }
}

let host: HTMLDivElement;
let root: Root;
let client: QueryClient;
let currentConversations: Conversation[];
let nextSync: SyncPage;
let manualSync: () => void;
let countData: AttentionPage | undefined;
let countStatus: string;
let countError: unknown;
function CountProbe({ provider = false }: { provider?: boolean }) {
  const result = useAttentionCount();
  useEffect(() => {
    countData = result.data;
    countStatus = result.status;
    countError = result.error;
  }, [result.data, result.status, result.error]);
  return provider ? <ChatControl /> : null;
}
function ChatControl() {
  const { sync } = useChat();
  useEffect(() => { manualSync = sync; }, [sync]);
  return null;
}
function ReceiptProbe({ provider = false }: { provider?: boolean }) {
  useConversationReceiptsSync(currentConversations[0].id, true, true);
  return provider ? <ChatControl /> : null;
}
async function settle(action?: () => void) {
  await act(async () => {
    action?.();
    await new Promise((resolve) => setTimeout(resolve, 10));
  });
}
async function mount(probe: "count" | "receipts", provider = false) {
  const content = probe === "count" ? <CountProbe provider={provider} /> : <ReceiptProbe provider={provider} />;
  await settle(() => root.render(<QueryClientProvider client={client}>{provider ? <ChatProvider>{content}</ChatProvider> : content}</QueryClientProvider>));
  await settle();
}

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  FakeSocket.instances = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
  Object.defineProperty(document, "hidden", { configurable: true, value: false });
  auth.reconcileSession.mockReset();
  chatStore.clear();
  chatStore.begin(auth.user.id);
  receiptStore.reset();
  resetNotificationSession(null);
  currentConversations = [conversation("verify-c", "channel", "all")];
  chatStore.replaceConversations(currentConversations);
  nextSync = { events: [], cursor: "0", hasMore: false };
  countData = undefined;
  countStatus = "pending";
  countError = undefined;
  vi.spyOn(api, "bootstrap").mockImplementation(async () => ({ user: auth.user, workspaces: [workspace], conversations: currentConversations, capabilities: { "im.read_receipts.view": true }, flagsRevision: "verify", syncCursor: "0" }) satisfies Bootstrap);
  vi.spyOn(api, "sync").mockImplementation(async () => {
    const page = nextSync;
    nextSync = { events: [], cursor: page.cursor, hasMore: false };
    return page;
  });
  client = new QueryClient({ defaultOptions: { queries: { retry: false, retryDelay: 0, gcTime: 0 } } });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  if (vi.isFakeTimers()) {
    await act(async () => root.unmount());
    vi.useRealTimers();
  } else {
    await settle(() => root.unmount());
  }
  client.clear();
  chatStore.clear();
  receiptStore.reset();
  resetNotificationSession(null);
  setActiveNotificationContext(undefined, null);
  host.remove();
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("notification projection", () => {
  it("rejects a delayed count response after an account or permission generation changes", async () => {
    const old = deferred<AttentionPage>();
    vi.spyOn(api, "attention").mockReturnValue(old.promise);
    const pending = fetchAttentionCount(new AbortController().signal);
    chatStore.revoke();
    old.resolve({ items: [], unreadCount: 3, channelUnreadCount: 3, attentionUnreadByConversation: [{ conversationId: "verify-c", unreadCount: 3 }] });
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    expect(chatStore.getSnapshot().notificationProjection).toBeUndefined();
  });

  it.each(["deliver", "muted", "read", "retracted", "revoked"] as const)("resolves an uncached reply through the authorized API and rechecks %s state", async (change) => {
    const c = conversation("reply-channel", "channel", "mentions");
    currentConversations = [c];
    chatStore.replaceConversations(currentConversations);
    const incoming = { ...message(c, 2, false), replyToId: "uncached-original" };
    chatStore.merge([incoming]);
    const original = { ...message(c, 1, false), id: "uncached-original", senderId: auth.user.id };
    const response = deferred<{ message: Message }>();
    const request = vi.spyOn(api, "message").mockReturnValue(response.promise);
    const constructed: NotificationOptions[] = [];
    vi.stubGlobal("Notification", class {
      static permission = "granted";
      constructor(_title: string, options: NotificationOptions) { constructed.push(options); }
      close() {}
    });
    resetNotificationSession(auth.user.id);
    setNotificationPreferences({ enabled: true, showPreview: false }, auth.user.id);
    setActiveNotificationContext("another-conversation", null);
    notifySyncedMessages([{ conversationId: c.id, messageId: incoming.id }], chatStore.getSnapshot(), auth.user.id);
    expect(request).toHaveBeenCalledWith(c.id, original.id, expect.any(AbortSignal));
    expect(constructed).toHaveLength(0);
    if (change === "muted") chatStore.setNotificationLevel(c.id, "none");
    if (change === "read") chatStore.markRead(c.id, incoming.seq);
    if (change === "retracted") chatStore.merge([{ ...original, retractedAt: "2026-10-27T00:00:01Z", body: "" }]);
    if (change === "revoked") chatStore.revoke();
    await settle(() => response.resolve({ message: original }));
    expect(constructed).toHaveLength(change === "deliver" ? 1 : 0);
  });
  it("gets all 205 muted counts and authorized zero entries without downloading attention history", async () => {
    const muted = conversation("muted", "channel", "none", 205);
    const ordinary = conversation("ordinary", "channel", "all", 12);
    currentConversations = [muted, ordinary];
    chatStore.replaceConversations(currentConversations);
    const attention = vi.spyOn(api, "attention").mockResolvedValue({ items: [item(muted, 205)], cursor: "more", unreadCount: 205, channelUnreadCount: 205, attentionUnreadByConversation: [{ conversationId: muted.id, unreadCount: 205 }, { conversationId: ordinary.id, unreadCount: 0 }] });
    await mount("count");
    expect(countStatus).toBe("success");
    expect(countData?.items).toHaveLength(1);
    expect(attention).toHaveBeenCalledTimes(1);
    expect(attention).toHaveBeenCalledWith({ limit: 1 }, expect.any(AbortSignal));
    expect(chatStore.getSnapshot().notificationProjection?.byConversation).toEqual({ muted: 205, ordinary: 0 });
    expect(computeShellAttention({ workspaces: [workspace], snapshot: chatStore.getSnapshot(), userId: auth.user.id, attentionData: countData }).urgentCount).toBe(0);
  });

  it("keeps failed quantity requests unknown instead of publishing a partial count", async () => {
    const attention = vi.spyOn(api, "attention").mockRejectedValue(new Error("transient projection failure"));
    await mount("count");
    expect(countStatus).toBe("error");
    expect(countError).toBeInstanceOf(Error);
    expect(countData).toBeUndefined();
    expect(chatStore.getSnapshot().notificationProjection).toBeUndefined();
    expect(attention).toHaveBeenCalledTimes(2);
  });

  it("knows the channel relevant unread count without a custom level or loaded message cache", async () => {
    const newest = conversation("newest", "channel", "all", 3);
    const older = conversation("older", "channel", "all", 3);
    currentConversations = [newest, older];
    chatStore.replaceConversations(currentConversations);
    const allItems = [item(newest, 3), item(older, 3), item(older, 2)];
    const attention = vi.spyOn(api, "attention").mockImplementation(async (options) => {
      if (options?.limit === 1) return { items: allItems.slice(0, 1), cursor: "older-page", unreadCount: 3, channelUnreadCount: 3, attentionUnreadByConversation: [{ conversationId: newest.id, unreadCount: 1 }, { conversationId: older.id, unreadCount: 2 }] };
      return { items: options?.cursor ? allItems.slice(1) : allItems, unreadCount: 3, channelUnreadCount: 3 };
    });
    await mount("count");
    expect(attention).toHaveBeenCalledWith({ limit: 1 }, expect.any(AbortSignal));
    expect(countData?.items).toHaveLength(1);
    expect(summarizeConversationUnread(older, chatStore.getSnapshot(), auth.user.id, countData?.attentionUnreadByConversation).mentionUnread).toBe(2);
  });

  it("does not resurrect muted title unread when a synced edit overtakes the initial quantity request", async () => {
    const muted = conversation("muted", "channel", "none", 101);
    const ordinary = conversation("ordinary", "channel", "all", 10);
    currentConversations = [muted, ordinary];
    chatStore.replaceConversations(currentConversations);
    chatStore.merge([message(muted, 1)]);
    const old = deferred<AttentionPage>();
    let oldSignal: AbortSignal | undefined;
    const attention = vi.spyOn(api, "attention").mockImplementation(async (_options, signal) => {
      if (attention.mock.calls.length === 1) { oldSignal = signal; return old.promise; }
      return { items: [item(muted, 101)], unreadCount: 100, channelUnreadCount: 100, attentionUnreadByConversation: [{ conversationId: muted.id, unreadCount: 100 }, { conversationId: ordinary.id, unreadCount: 0 }] };
    });
    await mount("count", true);
    expect(countStatus).toBe("pending");
    const updated = { ...message(muted, 1, false), editedAt: "2026-10-27T00:00:01Z" };
    nextSync = { events: [{ id: "1", type: "message.updated", conversationId: muted.id, message: updated, createdAt: updated.editedAt }], cursor: "1", hasMore: false };
    await settle(() => manualSync());
    expect(chatStore.getSnapshot().messages[muted.id][0].mentions).toBeUndefined();
    await settle(() => old.resolve({ items: [item(muted, 101)], unreadCount: 101, channelUnreadCount: 101, attentionUnreadByConversation: [{ conversationId: muted.id, unreadCount: 101 }, { conversationId: ordinary.id, unreadCount: 0 }] }));
    expect(oldSignal?.aborted).toBe(true);
    expect(countStatus).toBe("success");
    expect(chatStore.getSnapshot().notificationProjection?.byConversation[muted.id]).toBe(100);
    expect(computeShellAttention({ workspaces: [workspace], snapshot: chatStore.getSnapshot(), userId: auth.user.id, attentionData: countData }).urgentCount).toBe(0);
  });

  it.each(["direct", "group"] as const)("delivers ordinary %s desktop notifications in mentions mode", (type) => {
    const c = conversation(`desktop-${type}`, type, "mentions");
    currentConversations = [c];
    chatStore.replaceConversations(currentConversations);
    const incoming = message(c, 1, false);
    chatStore.merge([incoming]);
    const constructed: NotificationOptions[] = [];
    vi.stubGlobal("Notification", class {
      static permission = "granted";
      constructor(_title: string, options: NotificationOptions) { constructed.push(options); }
      close() {}
    });
    resetNotificationSession(auth.user.id);
    setNotificationPreferences({ enabled: true, showPreview: false }, auth.user.id);
    setActiveNotificationContext("another-conversation", null);
    notifySyncedMessages([{ conversationId: c.id, messageId: incoming.id }], chatStore.getSnapshot(), auth.user.id);
    expect(constructed).toHaveLength(1);
  });
});

describe("receipt recovery", () => {
  it("refetches after window focus and WebSocket open", async () => {
    const fetch = vi.spyOn(api, "readReceipts").mockResolvedValue({ conversationId: currentConversations[0].id, eligibleCount: 1, readSeqCounts: [{ readSeq: 0, count: 1 }] });
    await mount("receipts", true);
    const initial = fetch.mock.calls.length;
    await settle(() => window.dispatchEvent(new Event("focus")));
    expect(fetch.mock.calls.length).toBeGreaterThan(initial);
    const focused = fetch.mock.calls.length;
    await settle(() => {
      const socket = FakeSocket.instances.at(-1)!;
      socket.readyState = FakeSocket.OPEN;
      socket.onopen?.();
    });
    expect(fetch.mock.calls.length).toBeGreaterThan(focused);
  });

  it("recovers a transient receipt failure without requiring a future focus or receipt hint", async () => {
    vi.useFakeTimers();
    const fetch = vi.spyOn(api, "readReceipts")
      .mockRejectedValueOnce(new Error("transient receipt outage"))
      .mockResolvedValue({ conversationId: currentConversations[0].id, eligibleCount: 1, readSeqCounts: [{ readSeq: 3, count: 1 }] });
    await act(async () => {
      root.render(<ReceiptProbe />);
      await Promise.resolve();
    });
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000); });
    expect(fetch.mock.calls.length).toBeGreaterThan(1);
    expect(receiptStore.getSnapshot().byConversation[currentConversations[0].id]?.readSeqCounts[0].readSeq).toBe(3);
  });

  it("removes revoked group readers after durable membership refresh without needing focus", async () => {
    const c = { ...conversation("receipt-group", "group", "all"), memberIds: [auth.user.id, "verify-peer", "removed-peer"] };
    currentConversations = [c];
    chatStore.replaceConversations(currentConversations);
    const fetch = vi.spyOn(api, "readReceipts").mockResolvedValue({ conversationId: c.id, eligibleCount: 2, members: [{ userId: "verify-peer", readSeq: 3 }, { userId: "removed-peer", readSeq: 3 }], readSeqCounts: [{ readSeq: 3, count: 2 }] });
    await mount("receipts", true);
    expect(receiptStore.getSnapshot().byConversation[c.id].eligibleCount).toBe(2);
    const before = fetch.mock.calls.length;
    currentConversations = [{ ...c, memberIds: [auth.user.id, "verify-peer"] }];
    fetch.mockResolvedValue({ conversationId: c.id, eligibleCount: 1, members: [{ userId: "verify-peer", readSeq: 3 }], readSeqCounts: [{ readSeq: 3, count: 1 }] });
    nextSync = { events: [{ id: "1", type: "membership.changed", conversationId: c.id, workspaceId: c.workspaceId, createdAt: auth.user.createdAt }], cursor: "1", hasMore: false };
    await settle(() => manualSync());
    expect(chatStore.getSnapshot().conversations[c.id].memberIds).not.toContain("removed-peer");
    expect(receiptStore.getSnapshot().byConversation[c.id].members?.map((entry) => entry.userId)).not.toContain("removed-peer");
    expect(fetch.mock.calls.length).toBeGreaterThan(before);
  });
});
