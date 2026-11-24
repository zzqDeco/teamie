// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter, useLocation, useNavigate } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type {
  Bootstrap,
  Conversation,
  GlobalSearchItem,
  Member,
  Message,
  User,
  Workspace,
} from "@teamie/api-client";
import { ConversationSearch } from "../conversation-search";
import { QuickSwitcher } from "../quick-switcher";
import { chatStore } from "../store";
import { GlobalSearchView } from "./global-search";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

const currentUser: User = {
  id: "u-1",
  email: "lin@example.test",
  displayName: "林舟",
  kind: "human",
  platformRole: "user",
  createdAt: "2026-10-01T00:00:00Z",
};

const ws1: Workspace = {
  id: "ws-1",
  name: "研发工作空间",
  createdBy: "u-1",
  createdAt: "2026-10-01T00:00:00Z",
};

const ws2: Workspace = {
  id: "ws-2",
  name: "运维工作空间",
  createdBy: "u-2",
  createdAt: "2026-10-02T00:00:00Z",
};

const conv1: Conversation = {
  id: "c-1",
  workspaceId: "ws-1",
  type: "channel",
  name: "general",
  memberIds: ["u-1", "u-2"],
  lastSeq: 12,
  readSeq: 12,
  updatedAt: "2026-11-24T01:00:00Z",
};

const conv2: Conversation = {
  id: "c-2",
  workspaceId: "ws-2",
  type: "group",
  name: "值班小组",
  memberIds: ["u-1", "u-2"],
  lastSeq: 8,
  readSeq: 8,
  updatedAt: "2026-11-24T02:00:00Z",
};

const membersWs1: Member[] = [
  { userId: "u-1", displayName: "林舟", role: "owner", kind: "human" },
  { userId: "u-2", displayName: "陈工", role: "member", kind: "human" },
];

const bootstrap: Bootstrap = {
  user: currentUser,
  workspaces: [ws1, ws2],
  conversations: [conv1, conv2],
  capabilities: { "agents.discover": true },
  flagsRevision: "test",
  syncCursor: "10",
};

vi.mock("../auth", () => ({
  useAuth: () => ({ user: currentUser, logout: vi.fn() }),
}));

vi.mock("../chat", async (original) => ({
  ...(await original<typeof import("../chat")>()),
  useChat: () => ({
    data: bootstrap,
    loading: false,
    error: "",
    connection: "connected",
    syncError: "",
    sync: vi.fn(),
    refresh: vi.fn(),
    sendTyping: vi.fn(),
    typing: {},
  }),
}));

function makeMessage(
  id: string,
  conversationId: string,
  seq: number,
  body: string,
  extra: Partial<Message> = {},
): Message {
  return {
    id,
    clientMessageId: `cm-${id}`,
    conversationId,
    senderId: "u-2",
    senderName: "陈工",
    senderKind: "human",
    seq,
    body,
    createdAt: "2026-11-24T08:00:00Z",
    ...extra,
  };
}

function LocationProbe() {
  const loc = useLocation();
  return <output data-testid="loc">{`${loc.pathname}${loc.search}`}</output>;
}

describe("PR-2 global cross-conversation search UI", () => {
  let host: HTMLDivElement;
  let root: Root;
  let queryClient: QueryClient;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.stubGlobal("matchMedia", () => ({
      matches: false,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
      configurable: true,
      value: vi.fn(),
    });
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false, gcTime: 0 } },
    });
    chatStore.begin(currentUser.id);
    chatStore.replaceConversations([conv1, conv2]);
    vi.spyOn(api, "members").mockResolvedValue({ members: membersWs1 });
    vi.spyOn(api, "workspaceAgents").mockResolvedValue({ agents: [] });
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
      queryClient.clear();
    });
    host.remove();
    chatStore.clear();
    vi.useRealTimers();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  async function flush(ms = 300) {
    await act(async () => {
      vi.advanceTimersByTime(ms);
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  function setInputValue(el: HTMLInputElement | HTMLSelectElement, value: string) {
    const proto =
      el instanceof HTMLSelectElement
        ? HTMLSelectElement.prototype
        : HTMLInputElement.prototype;
    const setter = Object.getOwnPropertyDescriptor(proto, "value")?.set;
    setter?.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
    el.dispatchEvent(new Event("change", { bubbles: true }));
  }

  it("searches across workspaces with multi-term highlights, filters, cursor pagination, and live reconciliation", async () => {
    const hit1: GlobalSearchItem = {
      message: makeMessage("m-1", "c-1", 10, "发布 v0.4.11 架构评审通过", {
        attachments: [
          {
            id: "a-1",
            filename: "release_v0411.pdf",
            mediaType: "application/pdf",
            sizeBytes: 2048,
            sha256: "h1",
          },
        ],
      }),
      conversation: {
        id: "c-1",
        workspaceId: "ws-1",
        name: "general",
        type: "channel",
      },
      workspaceName: "研发工作空间",
    };
    const hit2: GlobalSearchItem = {
      message: makeMessage("m-2", "c-2", 6, "发布 v0.4.11 运维上线确认"),
      conversation: {
        id: "c-2",
        workspaceId: "ws-2",
        name: "值班小组",
        type: "group",
      },
      workspaceName: "运维工作空间",
    };
    const hit3: GlobalSearchItem = {
      message: makeMessage("m-3", "c-1", 5, "历史发布 v0.4.11 记录"),
      conversation: {
        id: "c-1",
        workspaceId: "ws-1",
        name: "general",
        type: "channel",
      },
      workspaceName: "研发工作空间",
    };

    const searchSpy = vi
      .spyOn(api, "searchGlobalMessages")
      .mockImplementation(async (options) => {
        if (options?.cursor === "page-2") {
          return { items: [hit3] };
        }
        return { items: [hit1, hit2], cursor: "page-2" };
      });

    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <MemoryRouter initialEntries={["/app/search?q=%E5%8F%91%E5%B8%83%20v0.4.11"]}>
            <GlobalSearchView onMenu={vi.fn()} />
            <LocationProbe />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });

    await flush(300);
    expect(searchSpy).toHaveBeenCalledWith(
      { q: "发布 v0.4.11", limit: 20 },
      expect.any(AbortSignal),
    );

    const cards = host.querySelectorAll(".global-search-card");
    expect(cards).toHaveLength(2);
    expect(cards[0].getAttribute("href")).toBe("/app/w/ws-1/c/c-1?message=m-1");
    expect(cards[1].getAttribute("href")).toBe("/app/w/ws-2/c/c-2?message=m-2");
    expect(host.textContent).toContain("研发工作空间");
    expect(host.textContent).toContain("运维工作空间");
    expect(host.textContent).toContain("release_v0411.pdf");

    // Multi-term highlights render <mark> for both terms
    const marks = Array.from(cards[0].querySelectorAll("mark")).map(
      (el) => el.textContent,
    );
    expect(marks).toContain("发布");
    expect(marks).toContain("v0.4.11");

    // Load next page via cursor
    const moreBtn = host.querySelector<HTMLButtonElement>(".global-search-more");
    expect(moreBtn).not.toBeNull();
    await act(async () => {
      moreBtn!.click();
      await Promise.resolve();
    });
    expect(searchSpy).toHaveBeenLastCalledWith(
      { q: "发布 v0.4.11", cursor: "page-2", limit: 20 },
      expect.any(AbortSignal),
    );
    expect(host.querySelectorAll(".global-search-card")).toHaveLength(3);

    // Live retraction immediately hides m-2 without a refetch
    await act(async () => {
      chatStore.merge([
        { ...hit2.message, body: "", retractedAt: "2026-11-24T09:00:00Z" },
      ]);
    });
    expect(host.querySelectorAll(".global-search-card")).toHaveLength(2);

    // Toggle "仅含附件" filter updates URL & triggers filtered API call
    const filesChip = host.querySelector<HTMLButtonElement>(".search-filter-chip")!;
    await act(async () => {
      filesChip.click();
    });
    await flush(300);
    expect(searchSpy).toHaveBeenLastCalledWith(
      { q: "发布 v0.4.11", hasFiles: true, limit: 20 },
      expect.any(AbortSignal),
    );
    expect(host.querySelector('[data-testid="loc"]')?.textContent).toContain(
      "hasFiles=true",
    );

    // Clear query while keeping hasFiles=true -> pure filter-only search still runs!
    const searchInput = host.querySelector<HTMLInputElement>("#global-search-input")!;
    await act(async () => {
      setInputValue(searchInput, "");
    });
    await flush(300);
    expect(searchSpy).toHaveBeenLastCalledWith(
      { hasFiles: true, limit: 20 },
      expect.any(AbortSignal),
    );
  });

  it("supports switching QuickSwitcher message search to all workspaces and opening /app/search", async () => {
    const onSelect = vi.fn();
    const globalSpy = vi.spyOn(api, "searchGlobalMessages").mockResolvedValue({
      items: [
        {
          message: makeMessage("m-9", "c-2", 7, "跨空间 发布 验证"),
          conversation: {
            id: "c-2",
            workspaceId: "ws-2",
            name: "值班小组",
            type: "group",
          },
          workspaceName: "运维工作空间",
        },
      ],
    });
    vi.spyOn(api, "searchWorkspaceMessages").mockResolvedValue({ items: [] });

    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <QuickSwitcher
            workspaces={[ws1, ws2]}
            conversations={[conv1, conv2]}
            currentWorkspaceId="ws-1"
            members={membersWs1}
            userId="u-1"
            recentConversationIds={["c-1"]}
            onSelect={onSelect}
            onClose={vi.fn()}
          />
        </QueryClientProvider>,
      );
    });

    // Switch to "搜索消息" tab
    const modeButtons = host.querySelectorAll<HTMLButtonElement>(
      ".quick-switcher-modes button",
    );
    await act(async () => {
      modeButtons[1].click();
    });

    // Switch scope to "全部工作空间"
    const scopeButtons = host.querySelectorAll<HTMLButtonElement>(
      ".quick-switcher-scope-btn",
    );
    expect(scopeButtons).toHaveLength(2);
    await act(async () => {
      scopeButtons[1].click();
    });

    const input = host.querySelector<HTMLInputElement>(
      '.quick-switcher-input input[type="search"]',
    )!;
    await act(async () => {
      setInputValue(input, "发布 验证");
    });
    await flush(300);

    expect(globalSpy).toHaveBeenCalledWith(
      { q: "发布 验证", limit: 20 },
      expect.any(AbortSignal),
    );
    expect(host.textContent).toContain("运维工作空间");
    expect(host.textContent).toContain("值班小组");

    // Clicking "在完整搜索页查看与筛选" navigates to /app/search?q=...
    const fullBtn = host.querySelector<HTMLButtonElement>(
      ".quick-switcher-full-search",
    )!;
    await act(async () => {
      fullBtn.click();
    });
    expect(onSelect).toHaveBeenCalledWith(
      `/app/search?q=${encodeURIComponent("发布 验证").replace(/%20/g, "+")}`,
    );
  });

  it("keeps multi-term server hits in ConversationSearch, filters out non-matching mock rows, and drops non-matching local edits", async () => {
    const serverHit = makeMessage("m-20", "c-1", 11, "发布记录：v0.4.11 上线成功");
    const unrelatedMock = makeMessage("m-21", "c-1", 12, "无关讨论内容");
    vi.spyOn(api, "searchMessages").mockResolvedValue({
      messages: [serverHit, unrelatedMock],
      hasMore: false,
    });

    await act(async () => {
      root.render(
        <ConversationSearch
          conversationId="c-1"
          onClose={vi.fn()}
          onLocate={vi.fn()}
        />,
      );
    });

    const input = host.querySelector<HTMLInputElement>('input[type="search"]')!;
    await act(async () => {
      setInputValue(input, "发布 上线");
    });
    await flush(350);
    // Multi-term hit is preserved while non-matching row is filtered out.
    expect(host.querySelectorAll("[data-search-result]")).toHaveLength(1);

    // Local sync edit removing the matched words still drops the stale hit.
    await act(async () => {
      chatStore.merge([
        { ...serverHit, body: "已修改为无关内容", editedAt: "2026-11-24T09:10:00Z" },
      ]);
    });
    expect(host.querySelectorAll("[data-search-result]")).toHaveLength(0);
  });

  it("handles external URL navigation, final conversation revocation, empty-page pagination, and selected sender preservation", async () => {
    const hitA: GlobalSearchItem = {
      message: makeMessage("m-a", "c-1", 10, "first-query hit"),
      conversation: { id: "c-1", workspaceId: "ws-1", name: "general", type: "channel" },
      workspaceName: "研发工作空间",
    };
    const hitB: GlobalSearchItem = {
      message: makeMessage("m-b", "c-1", 9, "second-query page2"),
      conversation: { id: "c-1", workspaceId: "ws-1", name: "general", type: "channel" },
      workspaceName: "研发工作空间",
    };

    const searchSpy = vi
      .spyOn(api, "searchGlobalMessages")
      .mockImplementation(async (options) => {
        if (options?.cursor === "next-page") {
          return { items: [hitB] };
        }
        return { items: [hitA], cursor: "next-page" };
      });

    function NavTrigger() {
      const navigate = useNavigate();
      return (
        <button
          type="button"
          data-testid="ext-nav"
          onClick={() => navigate("/app/search?q=second")}
        >
          nav
        </button>
      );
    }

    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <MemoryRouter initialEntries={["/app/search?q=first"]}>
            <GlobalSearchView onMenu={vi.fn()} />
            <LocationProbe />
            <NavTrigger />
          </MemoryRouter>
        </QueryClientProvider>,
      );
    });
    await flush(300);
    expect(host.querySelectorAll(".global-search-card")).toHaveLength(1);

    // 1) External navigation to /app/search?q=second is adopted cleanly without being overwritten by old state.
    await act(async () => {
      host.querySelector<HTMLButtonElement>('[data-testid="ext-nav"]')!.click();
    });
    await flush(300);
    expect(host.querySelector('[data-testid="loc"]')?.textContent).toBe(
      "/app/search?q=second",
    );
    expect(
      host.querySelector<HTMLInputElement>("#global-search-input")?.value,
    ).toBe("second");

    // 2) Retracting every hit on page 1 while cursor exists keeps "加载更多" visible and loads page 2!
    await act(async () => {
      chatStore.merge([
        { ...hitA.message, body: "", retractedAt: "2026-11-24T09:30:00Z" },
      ]);
    });
    expect(host.querySelectorAll(".global-search-card")).toHaveLength(0);
    const loadMoreBtn = host.querySelector<HTMLButtonElement>(".global-search-more");
    expect(loadMoreBtn).not.toBeNull();
    await act(async () => {
      loadMoreBtn!.click();
      await Promise.resolve();
    });
    expect(searchSpy).toHaveBeenLastCalledWith(
      { q: "second", cursor: "next-page", limit: 20 },
      expect.any(AbortSignal),
    );
    expect(host.querySelectorAll(".global-search-card")).toHaveLength(1);

    // 3) Authoritative empty conversation list after final membership revocation hides all hits!
    await act(async () => {
      chatStore.replaceConversations([]);
    });
    expect(host.querySelectorAll(".global-search-card")).toHaveLength(0);

    // 4) Selected sender stays represented in <select> when pinyin filter narrows options, and clears on workspace change.
    const senderSelect = host.querySelector<HTMLSelectElement>(
      "#global-search-sender-select",
    )!;
    await act(async () => {
      setInputValue(senderSelect, "u-2");
    });
    expect(senderSelect.value).toBe("u-2");
    const senderFilterInput = host.querySelector<HTMLInputElement>(
      ".search-sender-filter-input",
    )!;
    await act(async () => {
      setInputValue(senderFilterInput, "不存在的姓名");
    });
    expect(senderSelect.value).toBe("u-2");
    expect(
      Array.from(senderSelect.options).some((opt) => opt.value === "u-2"),
    ).toBe(true);

    // Switching workspace clears senderId cleanly.
    const wsSelect = host.querySelector<HTMLSelectElement>(
      'select[aria-label="按工作空间筛选"]',
    )!;
    await act(async () => {
      setInputValue(wsSelect, "ws-2");
    });
    expect(senderSelect.value).toBe("");
    expect(host.querySelector('[data-testid="loc"]')?.textContent).not.toContain(
      "senderId=",
    );
  });
});
