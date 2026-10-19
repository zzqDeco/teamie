// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, TeamieClient } from "@teamie/api-client";
import type {
  Conversation,
  Member,
  Message,
  Workspace,
  WorkspaceSearchPage,
} from "@teamie/api-client";
import { QuickSwitcher } from "./quick-switcher";
import { chatStore } from "./store";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

const workspace: Workspace = {
  id: "ws-1",
  name: "Teamie 研发空间",
  createdBy: "u-1",
  createdAt: "2026-10-18T00:00:00Z",
};

const channelConv: Conversation = {
  id: "c-chan",
  workspaceId: "ws-1",
  type: "channel",
  name: "架构讨论",
  memberIds: ["u-1", "u-2"],
  lastSeq: 50,
  readSeq: 10,
  updatedAt: "2026-10-18T10:00:00Z",
};

const groupConv: Conversation = {
  id: "c-group",
  workspaceId: "ws-1",
  type: "group",
  name: "前端攻坚组",
  memberIds: ["u-1", "u-2"],
  lastSeq: 30,
  readSeq: 5,
  updatedAt: "2026-10-18T11:00:00Z",
};

const members: Member[] = [
  { userId: "u-1", displayName: "林舟", role: "owner", kind: "human" },
  { userId: "u-2", displayName: "陈工", role: "member", kind: "human" },
];

const makeMsg = (
  id: string,
  conversationId: string,
  seq: number,
  body: string,
  extra: Partial<Message> = {},
): Message => ({
  id,
  clientMessageId: `client-${id}`,
  conversationId,
  senderId: "u-2",
  senderName: "陈工",
  senderKind: "human",
  seq,
  body,
  createdAt: "2026-10-18T12:00:00Z",
  ...extra,
});

describe("PR-8 workspace-wide message search in QuickSwitcher and API client", () => {
  let host: HTMLDivElement;
  let root: Root;
  let queryClient: QueryClient;
  const onSelect = vi.fn();
  const onClose = vi.fn();

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
    chatStore.begin("u-1");
    chatStore.replaceConversations([channelConv, groupConv]);
    vi.spyOn(api, "members").mockResolvedValue({ members });
    vi.spyOn(api, "workspaceAgents").mockResolvedValue({ agents: [] });
    vi.spyOn(api, "read").mockResolvedValue({ readSeq: 50 });
    onSelect.mockClear();
    onClose.mockClear();
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

  async function renderSwitcher() {
    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <QuickSwitcher
            workspaces={[workspace]}
            conversations={[channelConv, groupConv]}
            currentWorkspaceId="ws-1"
            members={members}
            userId="u-1"
            recentConversationIds={["c-chan"]}
            onSelect={onSelect}
            onClose={onClose}
          />
        </QueryClientProvider>,
      );
    });
  }

  async function typeQuery(value: string) {
    await act(async () => {
      const input = host.querySelector<HTMLInputElement>(
        'input[type="search"]',
      )!;
      Object.getOwnPropertyDescriptor(
        HTMLInputElement.prototype,
        "value",
      )!.set!.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
  }

  async function tick(ms = 300) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ms);
    });
  }

  it("calls GET /api/v1/workspaces/:id/messages/search with encoded parameters", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ items: [], cursor: "next-1" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    const client = new TeamieClient();
    client.csrfToken = "csrf-tok";
    const controller = new AbortController();
    const res = await client.searchWorkspaceMessages(
      "ws/1",
      { q: "alpha 10%", cursor: "cur-0", limit: 20 },
      controller.signal,
    );
    expect(res.cursor).toBe("next-1");
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    const [url, init] = fetchSpy.mock.calls[0];
    expect(url).toBe(
      "/api/v1/workspaces/ws%2F1/messages/search?q=alpha+10%25&cursor=cur-0&limit=20",
    );
    expect(init?.signal).toBe(controller.signal);
  });

  it("groups workspace search results by conversation, highlights literal matches safely, supports cursor pagination, and navigates via ?message=", async () => {
    const m1 = makeMsg(
      "m-1",
      "c-chan",
      25,
      "部署 Alpha 10%_ <script>alert(1)</script>",
    );
    const m2 = makeMsg("m-2", "c-group", 18, "评审 alpha 方案通过", {
      senderKind: "agent",
      senderName: "架构助手",
    });
    const m3 = makeMsg("m-3", "c-chan", 12, "早期 alpha 讨论记录");

    const searchSpy = vi
      .spyOn(api, "searchWorkspaceMessages")
      .mockImplementation(async (_wsId, options) => {
        if (options.cursor === "cursor-page-2") {
          return {
            items: [
              {
                message: m3,
                conversation: {
                  id: channelConv.id,
                  workspaceId: channelConv.workspaceId,
                  name: channelConv.name,
                  type: channelConv.type,
                },
              },
            ],
          };
        }
        return {
          items: [
            {
              message: m1,
              conversation: {
                id: channelConv.id,
                workspaceId: channelConv.workspaceId,
                name: channelConv.name,
                type: channelConv.type,
              },
            },
            {
              message: m2,
              conversation: {
                id: groupConv.id,
                workspaceId: groupConv.workspaceId,
                name: groupConv.name,
                type: groupConv.type,
              },
            },
          ],
          cursor: "cursor-page-2",
        };
      });

    await renderSwitcher();

    // Switch to "搜索消息" mode
    const modeButtons = host.querySelectorAll<HTMLButtonElement>(
      ".quick-switcher-modes button",
    );
    expect(modeButtons).toHaveLength(2);
    await act(async () => {
      modeButtons[1].click();
    });

    await typeQuery("  Alpha  ");
    await tick(300);

    expect(searchSpy).toHaveBeenCalledWith(
      "ws-1",
      { q: "Alpha", limit: 20 },
      expect.any(AbortSignal),
    );

    // Results are grouped into 2 conversations (架构讨论 and 前端攻坚组)
    const groups = host.querySelectorAll(".quick-switcher-group");
    expect(groups).toHaveLength(2);
    expect(
      groups[0].querySelector(".quick-switcher-group-header")?.textContent,
    ).toContain("架构讨论");
    expect(
      groups[1].querySelector(".quick-switcher-group-header")?.textContent,
    ).toContain("前端攻坚组");

    // Highlighted literal matches without injecting <script>
    const marks = Array.from(host.querySelectorAll("mark")).map(
      (el) => el.textContent,
    );
    expect(marks).toEqual(["Alpha", "alpha"]);
    expect(host.querySelector("script")).toBeNull();
    expect(host.textContent).toContain("<script>alert(1)</script>");

    // Load more via cursor pagination appends m3 into the existing 架构讨论 group
    const moreBtn = host.querySelector<HTMLButtonElement>(
      ".quick-switcher-more",
    )!;
    expect(moreBtn).not.toBeNull();
    await act(async () => {
      moreBtn.click();
    });
    expect(searchSpy).toHaveBeenLastCalledWith(
      "ws-1",
      { q: "Alpha", cursor: "cursor-page-2", limit: 20 },
      expect.any(AbortSignal),
    );
    expect(
      host
        .querySelectorAll(".quick-switcher-group")[0]
        .querySelectorAll('[role="option"]'),
    ).toHaveLength(2);

    // Navigating via keyboard ArrowDown + Enter selects the 2nd option in group 1 (m-3)
    const input = host.querySelector<HTMLInputElement>('input[type="search"]')!;
    await act(async () => {
      input.dispatchEvent(
        new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }),
      );
    });
    await act(async () => {
      input.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Enter", bubbles: true }),
      );
    });
    expect(onSelect).toHaveBeenCalledWith("/app/w/ws-1/c/c-chan?message=m-3");
    // Does not mark messages read or pollute chatStore timeline
    expect(api.read).not.toHaveBeenCalled();
    expect(chatStore.getSnapshot().messages["c-chan"]).toBeUndefined();
  });

  it("reconciles live sync edits/retractions and aborts stale requests on query change", async () => {
    let resolveFirst!: (page: WorkspaceSearchPage) => void;
    const firstPromise = new Promise<WorkspaceSearchPage>((r) => {
      resolveFirst = r;
    });
    const m1 = makeMsg("m-1", "c-chan", 25, "包含关键字 beta 的原消息");
    const m2 = makeMsg("m-2", "c-chan", 26, "另一条 beta 消息");

    const signals: AbortSignal[] = [];
    vi.spyOn(api, "searchWorkspaceMessages")
      .mockImplementationOnce((_wsId, _opts, signal) => {
        if (signal) signals.push(signal);
        return firstPromise;
      })
      .mockImplementationOnce(async (_wsId, _opts, signal) => {
        if (signal) signals.push(signal);
        return {
          items: [
            {
              message: m1,
              conversation: {
                id: channelConv.id,
                workspaceId: channelConv.workspaceId,
                name: channelConv.name,
                type: channelConv.type,
              },
            },
            {
              message: m2,
              conversation: {
                id: channelConv.id,
                workspaceId: channelConv.workspaceId,
                name: channelConv.name,
                type: channelConv.type,
              },
            },
          ],
        };
      });

    await renderSwitcher();
    await act(async () => {
      host
        .querySelectorAll<HTMLButtonElement>(".quick-switcher-modes button")[1]
        .click();
    });

    await typeQuery("stale");
    await tick(300);
    expect(signals).toHaveLength(1);

    // Changing query aborts the in-flight request
    await typeQuery("beta");
    expect(signals[0].aborted).toBe(true);
    await tick(300);

    // Resolving the aborted first request is ignored
    await act(async () => {
      resolveFirst({
        items: [
          {
            message: makeMsg("m-stale", "c-chan", 1, "stale 结果"),
            conversation: {
              id: channelConv.id,
              workspaceId: channelConv.workspaceId,
              name: channelConv.name,
              type: channelConv.type,
            },
          },
        ],
      });
    });
    expect(host.textContent).not.toContain("stale 结果");
    expect(host.querySelectorAll('[role="option"]')).toHaveLength(2);

    // Live sync retracts m-1 and edits m-2 -> m-1 is removed and m-2 updates immediately
    await act(async () => {
      chatStore.merge([
        {
          ...m1,
          body: "",
          retractedAt: "2026-10-18T12:05:00Z",
        },
        {
          ...m2,
          body: "已编辑的 beta 最新内容",
          editedAt: "2026-10-18T12:06:00Z",
        },
      ]);
    });

    const options = host.querySelectorAll('[role="option"]');
    expect(options).toHaveLength(1);
    expect(options[0].textContent).toContain("已编辑的 beta 最新内容");
  });

  it("keeps pagination available when reconciliation empties a page, preserves loaded results on load-more error, and avoids locale-dependent false negatives", async () => {
    const mRetracted = makeMsg("m-ret", "c-chan", 30, "INPUT 规划");
    const mOlder = makeMsg("m-old", "c-chan", 15, "INPUT 历史定稿");

    // Simulate local retraction of mRetracted before search
    chatStore.merge([
      {
        ...mRetracted,
        body: "",
        retractedAt: "2026-10-18T12:10:00Z",
      },
    ]);

    // Simulate Turkish-like toLocaleLowerCase where "I" -> "ı"
    const origToLocaleLower = String.prototype.toLocaleLowerCase;
    vi.spyOn(String.prototype, "toLocaleLowerCase").mockImplementation(
      function (this: string) {
        return origToLocaleLower.call(this).replace(/i/g, "ı");
      },
    );

    let call = 0;
    vi.spyOn(api, "searchWorkspaceMessages").mockImplementation(
      async (_wsId, opts) => {
        call += 1;
        if (!opts.cursor) {
          return {
            items: [
              {
                message: mRetracted,
                conversation: {
                  id: channelConv.id,
                  workspaceId: channelConv.workspaceId,
                  name: channelConv.name,
                  type: channelConv.type,
                },
              },
            ],
            cursor: "cursor-after-empty-page",
          };
        }
        if (call === 2) {
          throw new Error("分页服务暂时不可用");
        }
        return {
          items: [
            {
              message: mOlder,
              conversation: {
                id: channelConv.id,
                workspaceId: channelConv.workspaceId,
                name: channelConv.name,
                type: channelConv.type,
              },
            },
          ],
          cursor: "cursor-page-3",
        };
      },
    );

    await renderSwitcher();
    await act(async () => {
      host
        .querySelectorAll<HTMLButtonElement>(".quick-switcher-modes button")[1]
        .click();
    });

    await typeQuery("input");
    await tick(300);

    // Page 1 was emptied by local retraction reconciliation, but cursor pagination remains available
    const moreBtn = host.querySelector<HTMLButtonElement>(
      ".quick-switcher-more",
    )!;
    expect(moreBtn).not.toBeNull();
    expect(moreBtn.textContent).toContain("加载更多");

    // First click on 加载更多 fails -> error notice is shown AND 加载更多 remains available
    await act(async () => {
      moreBtn.click();
    });
    expect(host.textContent).toContain("请求失败，请稍后重试。");
    expect(host.querySelector(".quick-switcher-more")).not.toBeNull();

    // Retry 加载更多 succeeds -> mOlder ("INPUT 历史定稿") is kept despite Turkish toLocaleLowerCase
    await act(async () => {
      host.querySelector<HTMLButtonElement>(".quick-switcher-more")!.click();
    });
    expect(host.querySelectorAll('[role="option"]')).toHaveLength(1);
    expect(host.textContent).toContain("INPUT 历史定稿");
  });
});
