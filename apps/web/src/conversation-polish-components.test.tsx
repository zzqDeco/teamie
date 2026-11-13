// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Member, Message, ReadReceipts } from "@teamie/api-client";
import { CodeBlock } from "./markdown/code-block";
import { CollapsibleMessageBody } from "./chat/collapsible-body";
import { ReactionChips } from "./chat/reaction-chips";
import { EmojiPanel } from "./chat/emoji-panel";
import { readRecentReactions, recordRecentReaction } from "./chat/emoji-recent";
import { InAppLinkNavigator } from "./chat/in-app-links";
import { OwnMessageReadReceipt } from "./chat/read-receipts";
import { ConversationHeader } from "./chat/conversation-chrome";
import { MessageBody } from "./markdown";
import { LONG_PRESS_MS } from "./chat/long-press";

let receipts: ReadReceipts | null = null;
vi.mock("./engagement-store", async (original) => ({
  ...(await original<typeof import("./engagement-store")>()),
  useConversationReceipts: () => receipts,
}));

let host: HTMLDivElement;
let root: Root;
async function waitFor(check: () => void, timeout = 3000) {
  const started = Date.now();
  for (;;) {
    try {
      check();
      return;
    } catch (error) {
      if (Date.now() - started > timeout) throw error;
      await act(async () => { await new Promise((resolve) => setTimeout(resolve, 10)); });
    }
  }
}
async function render(node: React.ReactNode) {
  await act(async () => root.render(node));
}
function pointer(type: string, target: Element, init: Partial<PointerEventInit> = {}) {
  const event = new MouseEvent(type, { bubbles: true, cancelable: true, clientX: 10, clientY: 10, ...init });
  Object.assign(event, { pointerType: init.pointerType ?? "touch", isPrimary: true, pointerId: 1 });
  target.dispatchEvent(event);
}

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  localStorage.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  receipts = null;
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("long code blocks", () => {
  const code = Array.from({ length: 26 }, (_, i) => `const line${i} = ${i};`).join("\n");
  it("collapse beyond 20 lines behind 「展开 N 行」 and expand/collapse accessibly", async () => {
    await render(<div className="message-body"><CodeBlock codeText={code} lang="" /></div>);
    const pre = host.querySelector("pre")!;
    const toggle = host.querySelector<HTMLButtonElement>(".markdown-code-toggle")!;
    expect(pre.classList.contains("collapsed")).toBe(true);
    expect(toggle.textContent).toBe("展开 6 行");
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.getAttribute("aria-controls")).toBe(pre.id);
    await act(async () => toggle.click());
    expect(pre.classList.contains("collapsed")).toBe(false);
    expect(toggle.textContent).toBe("收起代码");
    // Full code is always in the DOM (find-in-page, copy).
    expect(pre.textContent).toContain("line25");
  });
  it("does not collapse short blocks", async () => {
    await render(<CodeBlock codeText={"a\nb"} lang="" />);
    expect(host.querySelector(".markdown-code-toggle")).toBeNull();
  });
  it("reports a copy failure when the clipboard is unavailable", async () => {
    vi.stubGlobal("navigator", { ...navigator, clipboard: undefined });
    await render(<CodeBlock codeText="x" lang="" />);
    await act(async () => host.querySelector<HTMLButtonElement>(".markdown-code-copy")!.click());
    expect(host.querySelector(".markdown-code-copy")?.textContent).toBe("复制失败");
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("无法访问剪贴板");
  });
  it("reports success only after the clipboard write resolves", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    await render(<CodeBlock codeText="copy me" lang="" />);
    await act(async () => host.querySelector<HTMLButtonElement>(".markdown-code-copy")!.click());
    expect(writeText).toHaveBeenCalledWith("copy me");
    expect(host.querySelector(".markdown-code-copy")?.textContent).toBe("已复制");
  });
  it("highlights a known language after the lazy chunk loads", async () => {
    await render(<CodeBlock codeText={'const a = "b";'} lang="ts" />);
    const codeEl = host.querySelector("code")!;
    await waitFor(() => expect(codeEl.getAttribute("data-highlighted")).toBe("true"));
    expect(codeEl.querySelector(".code-token-keyword")?.textContent).toBe("const");
    expect(codeEl.textContent).toBe('const a = "b";');
  });
});

describe("long message collapse", () => {
  it("collapses bodies taller than the threshold behind 「展开全文」 for any sender", async () => {
    vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockImplementation(function (this: HTMLElement) {
      return this.classList.contains("message-body") ? 1400 : 0;
    });
    await render(<CollapsibleMessageBody contentKey="k">长内容</CollapsibleMessageBody>);
    const body = host.querySelector(".message-body")!;
    const toggle = host.querySelector<HTMLButtonElement>(".message-expand-toggle")!;
    expect(body.classList.contains("collapsed")).toBe(true);
    expect(toggle.textContent).toBe("展开全文");
    await act(async () => toggle.click());
    expect(body.classList.contains("collapsed")).toBe(false);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
  });
  it("leaves ordinary messages alone", async () => {
    vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockReturnValue(300);
    await render(<CollapsibleMessageBody contentKey="k">短内容</CollapsibleMessageBody>);
    expect(host.querySelector(".message-expand-toggle")).toBeNull();
    expect(host.querySelector(".message-body.collapsed")).toBeNull();
  });
});

describe("reaction chips", () => {
  const members: Member[] = [{ userId: "peer", displayName: "同事", kind: "agent", role: "member" }];
  const reactions = [{ emoji: "👍", count: 3, userIds: ["me", "peer", "gone-user-id"] }];
  it("long-press on touch lists reactors (「未知成员」 for unknown IDs) without toggling", async () => {
    vi.useFakeTimers();
    const onToggle = vi.fn();
    await render(
      <ReactionChips reactions={reactions} userId="me" members={members} canReact busy={false} onToggle={onToggle} />,
    );
    const chip = host.querySelector<HTMLButtonElement>(".reaction-chip")!;
    expect(chip.getAttribute("aria-label")).toBe("👍 3 人回应：你、同事、未知成员");
    pointer("pointerdown", chip);
    await act(async () => { vi.advanceTimersByTime(LONG_PRESS_MS + 10); });
    pointer("pointerup", chip);
    await act(async () => chip.click());
    const sheet = document.querySelector(".reactor-sheet")!;
    expect(sheet.getAttribute("role")).toBe("dialog");
    expect(sheet.getAttribute("aria-label")).toBe("回应 👍 的成员");
    expect([...sheet.querySelectorAll("li")].map((li) => li.textContent)).toEqual(["你", "同事", "未知成员"]);
    expect(onToggle).not.toHaveBeenCalled();
    await act(async () => { document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })); });
    expect(document.querySelector(".reactor-sheet")).toBeNull();
    // A normal tap still toggles; mouse input never long-presses.
    await act(async () => chip.click());
    expect(onToggle).toHaveBeenCalledWith("👍");
    pointer("pointerdown", chip, { pointerType: "mouse" });
    await act(async () => { vi.advanceTimersByTime(LONG_PRESS_MS + 10); });
    expect(document.querySelector(".reactor-sheet")).toBeNull();
  });
  it("moving the finger cancels the long-press (scrolling)", async () => {
    vi.useFakeTimers();
    await render(<ReactionChips reactions={reactions} userId="me" members={members} canReact busy={false} onToggle={vi.fn()} />);
    const chip = host.querySelector(".reaction-chip")!;
    pointer("pointerdown", chip);
    pointer("pointermove", chip, { clientY: 40 });
    await act(async () => { vi.advanceTimersByTime(LONG_PRESS_MS + 10); });
    expect(document.querySelector(".reactor-sheet")).toBeNull();
  });
});

describe("full emoji panel", () => {
  it("loads lazily, switches categories, searches and shows validated per-user recents", async () => {
    recordRecentReaction("me", "🚀");
    recordRecentReaction("me", "not-an-emoji");
    recordRecentReaction("other", "🐙");
    expect(readRecentReactions("me")).toEqual(["not-an-emoji", "🚀"]);
    const onSelect = vi.fn();
    const reactions: Message["reactions"] = [{ emoji: "🚀", count: 1, userIds: ["me"] }];
    await render(<EmojiPanel userId="me" reactions={reactions} onSelect={onSelect} />);
    expect(host.querySelector('[role="status"]')?.textContent).toContain("正在加载表情");
    await waitFor(() => expect(host.querySelector(".emoji-panel-grid")).not.toBeNull());
    const grid = () => host.querySelector(".emoji-panel-grid")!;
    // Recents first: only allowlisted entries, never another account's.
    expect(grid().getAttribute("aria-label")).toBe("最近使用");
    expect([...grid().querySelectorAll("button")].map((b) => b.textContent)).toEqual(["🚀"]);
    expect(grid().querySelector("button")!.classList.contains("active")).toBe(true);
    await act(async () => host.querySelector<HTMLButtonElement>('[aria-label="动物与自然"]')!.click());
    expect(grid().getAttribute("aria-label")).toBe("动物与自然");
    expect(grid().textContent).toContain("🐙");
    const search = host.querySelector<HTMLInputElement>(".emoji-panel-search")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(search, "party");
      search.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(grid().getAttribute("aria-label")).toBe("搜索结果");
    const party = [...grid().querySelectorAll("button")].find((b) => b.textContent === "🎉")!;
    await act(async () => party.click());
    expect(onSelect).toHaveBeenCalledWith("🎉");
  });
});

describe("in-app message links", () => {
  function Where() {
    const location = useLocation();
    return <output data-testid="where">{location.pathname + location.search}</output>;
  }
  it("navigates same-origin /app links inside the SPA and opens others in a new tab", async () => {
    const internal = `${window.location.origin}/app/w/team/c/general?message=m1`;
    await render(
      <MemoryRouter initialEntries={["/app"]}>
        <InAppLinkNavigator />
        <Routes><Route path="*" element={<Where />} /></Routes>
        <div className="message-body">
          <MessageBody body={`[跳转](${internal}) 和 https://outside.example/x`} userId="me" />
        </div>
      </MemoryRouter>,
    );
    const [inApp, external] = [...host.querySelectorAll<HTMLAnchorElement>("a")];
    expect(inApp.getAttribute("target")).toBeNull();
    expect(inApp.getAttribute("data-app-link")).toBe("/app/w/team/c/general?message=m1");
    expect(inApp.querySelector(".markdown-link-host")).toBeNull();
    expect(external.getAttribute("target")).toBe("_blank");
    await act(async () => inApp.click());
    expect(host.querySelector("output")?.textContent).toBe("/app/w/team/c/general?message=m1");
  });
});

describe("group read-receipt popover", () => {
  it("caps its height via CSS class, moves focus into the list and supports arrow keys and Escape", async () => {
    receipts = {
      conversationId: "g", eligibleCount: 3,
      readSeqCounts: [{ readSeq: 9, count: 2 }, { readSeq: 1, count: 1 }],
      members: [{ userId: "a", readSeq: 9 }, { userId: "b", readSeq: 9 }, { userId: "c", readSeq: 1 }],
    };
    const members: Member[] = [
      { userId: "a", displayName: "甲", kind: "human", role: "member" },
      { userId: "b", displayName: "Agent 乙", kind: "agent", role: "member" },
    ];
    await render(
      <OwnMessageReadReceipt conversationId="g" conversationType="group" seq={5} userId="me"
        senderReadSeq={9} allMembers={members} isLatestOwn />,
    );
    const trigger = host.querySelector<HTMLButtonElement>(".message-read-receipt")!;
    await act(async () => trigger.click());
    const popover = host.querySelector<HTMLElement>(".read-receipt-popover")!;
    const items = [...popover.querySelectorAll<HTMLElement>("li")];
    expect(items.map((li) => li.textContent)).toEqual(["甲", "Agent 乙", "未知成员"]);
    expect(document.activeElement).toBe(items[0]);
    const key = async (k: string) =>
      act(async () => { document.activeElement!.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true })); });
    await key("ArrowDown");
    expect(document.activeElement).toBe(items[1]);
    await key("End");
    expect(document.activeElement).toBe(items[2]);
    await key("Home");
    expect(document.activeElement).toBe(items[0]);
    await act(async () => { document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })); });
    expect(host.querySelector(".read-receipt-popover")).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });
});

describe("notification level menu", () => {
  it("focuses the current level on open, supports arrows/Home/End and returns focus on Escape", async () => {
    await render(
      <ConversationHeader
        conversation={{ id: "c", workspaceId: "w", type: "group", name: "G", memberIds: ["me"], lastSeq: 0, readSeq: 0, updatedAt: "", notificationLevel: "mentions" }}
        name="G" membersOpen={false} onMenu={vi.fn()} onSearchOpen={vi.fn()} onMembers={vi.fn()}
        canConfigureNotifications
      />,
    );
    const trigger = host.querySelector<HTMLButtonElement>('[aria-haspopup="menu"]')!;
    await act(async () => trigger.click());
    const items = [...host.querySelectorAll<HTMLElement>('[role="menuitemradio"]')];
    expect(document.activeElement).toBe(items[1]);
    const key = async (k: string) =>
      act(async () => { document.activeElement!.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true })); });
    await key("ArrowDown");
    expect(document.activeElement).toBe(items[2]);
    await key("ArrowDown");
    expect(document.activeElement).toBe(items[0]);
    await key("End");
    expect(document.activeElement).toBe(items[2]);
    await key("Home");
    expect(document.activeElement).toBe(items[0]);
    await act(async () => { document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })); });
    expect(host.querySelector('[role="menu"]')).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });
});
