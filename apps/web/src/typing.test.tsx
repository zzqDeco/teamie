// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type { Bootstrap, Conversation, User } from "@teamie/api-client";
import { ChatProvider } from "./chat";
import { ConversationView } from "./messages";
import { chatStore } from "./store";

const user: User = {
  id: "human",
  kind: "human",
  platformRole: "user",
  displayName: "参与者",
  email: "typing@example.invalid",
  createdAt: "2026-10-17T00:00:00Z",
};
const conversation: Conversation = {
  id: "current",
  workspaceId: "team",
  type: "group",
  name: "当前会话",
  memberIds: [user.id, "agent", "peer"],
  lastSeq: 0,
  readSeq: 0,
  updatedAt: user.createdAt,
};
let bootstrap: Bootstrap;
const client = { removeQueries: vi.fn(), resetQueries: vi.fn() };
const refetch = vi.fn();
vi.mock("./auth", () => ({ useAuth: () => ({ user }) }));
vi.mock("@tanstack/react-query", () => ({
  useQueryClient: () => client,
  useQuery: () => ({
    data: bootstrap,
    isLoading: false,
    error: null,
    refetch,
  }),
}));

class TypingSocket {
  static OPEN = 1;
  static current: TypingSocket;
  readyState = TypingSocket.OPEN;
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  send = vi.fn();
  close = vi.fn();
  constructor() {
    TypingSocket.current = this;
  }
  message(value: object) {
    this.onmessage?.({ data: JSON.stringify(value) });
  }
}

let container: HTMLDivElement;
let root: Root;
const noop = () => {};
const indicator = () =>
  container.querySelector(".composer-footer span")?.textContent;
async function hint(
  isTyping: boolean,
  userId = "agent",
  displayName = "产品伙伴",
  conversationId = conversation.id,
) {
  await act(async () => {
    TypingSocket.current.message({
      type: "typing",
      userId,
      displayName,
      conversationId,
      isTyping,
    });
  });
}
async function advance(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}
async function mount() {
  await act(async () => {
    root.render(
      <ChatProvider>
        <ConversationView
          conversation={conversation}
          members={[]}
          onMembers={noop}
          membersOpen={false}
          onMenu={noop}
        />
      </ChatProvider>,
    );
  });
}
beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date(user.createdAt));
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal("WebSocket", TypingSocket);
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      disconnect() {}
    },
  );
  Object.defineProperty(HTMLElement.prototype, "scrollTo", {
    configurable: true,
    value: vi.fn(),
  });
  bootstrap = {
    user,
    workspaces: [],
    conversations: [conversation],
    capabilities: { "im.typing.publish": true },
    flagsRevision: "typing-test",
    syncCursor: "0",
  };
  chatStore.clear();
  vi.spyOn(api, "messages").mockResolvedValue({ messages: [], hasMore: false });
  vi.spyOn(api, "sync").mockResolvedValue({
    events: [],
    cursor: "0",
    hasMore: false,
  });
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  chatStore.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  delete (HTMLElement.prototype as Partial<HTMLElement>).scrollTo;
});

describe("ordinary participant typing in IM", () => {
  it("shows Agents and humans identically in only the active conversation and clears immediately", async () => {
    await mount();
    await hint(true);
    expect(indicator()).toBe("产品伙伴 正在输入…");
    await hint(true, "peer", "设计伙伴");
    expect(indicator()).toBe("产品伙伴、设计伙伴 正在输入…");
    await hint(true, user.id, "自己");
    await hint(true, "other-agent", "其他伙伴", "another-private-conversation");
    expect(indicator()).toBe("产品伙伴、设计伙伴 正在输入…");
    await hint(false);
    expect(indicator()).toBe("设计伙伴 正在输入…");
    await hint(false, "peer", "设计伙伴");
    expect(indicator()).toBe("");
  });

  it("refreshes the existing five-second TTL and removes a crashed publisher on the cleanup tick", async () => {
    await mount();
    await hint(true);
    await advance(3000);
    await hint(true);
    await advance(3000);
    expect(indicator()).toBe("产品伙伴 正在输入…");
    await advance(2000);
    expect(indicator()).toBe("");
  });

  it("expires a lost stop without a durable message or a sync activity event", async () => {
    await mount();
    await hint(true);
    await advance(4999);
    expect(indicator()).toBe("产品伙伴 正在输入…");
    await advance(1);
    expect(indicator()).toBe("");
    expect(chatStore.getSnapshot().messages[conversation.id] || []).toEqual([]);
    expect(chatStore.getSnapshot().cursor).toBe("0");
  });

  it("honors the existing capability when no typing can be published", async () => {
    bootstrap.capabilities["im.typing.publish"] = false;
    await mount();
    await hint(true);
    expect(indicator()).toBe("");
  });
});
