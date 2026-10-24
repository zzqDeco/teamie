// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type {
  Bootstrap,
  Conversation,
  Member,
  Message,
  User,
} from "@teamie/api-client";
import { MessageBody, sanitizeLinkUrl } from "./markdown";
import { ConversationView } from "./messages";
import { chatStore } from "./store";

const user: User = {
  id: "human-one",
  displayName: "林舟",
  kind: "human",
  platformRole: "user",
  email: "linzhou@example.test",
  createdAt: "2026-10-22T00:00:00Z",
};

const members: Member[] = [
  {
    userId: user.id,
    displayName: user.displayName,
    kind: "human",
    role: "member",
  },
  {
    userId: "human-two",
    displayName: "陈默",
    kind: "human",
    role: "member",
  },
  {
    userId: "agent-one",
    displayName: "算力助手",
    kind: "agent",
    role: "member",
  },
];

const conversation: Conversation = {
  id: "conv-md",
  workspaceId: "ws-md",
  type: "channel",
  name: "研发讨论",
  memberIds: members.map((m) => m.userId),
  lastSeq: 10,
  readSeq: 10,
  updatedAt: user.createdAt,
};

const bootstrap: Bootstrap = {
  user,
  workspaces: [],
  conversations: [conversation],
  capabilities: { "im.message.mentions": true, "im.message.edit": true },
  flagsRevision: "md-katex",
  syncCursor: "0",
};

const sendTyping = vi.fn();
const emptyTyping = {};
vi.mock("./auth", () => ({ useAuth: () => ({ user }) }));
vi.mock("./chat", async (original) => ({
  ...(await original<typeof import("./chat")>()),
  useChat: () => ({
    data: bootstrap,
    syncError: "",
    sync: vi.fn(),
    sendTyping,
    typing: emptyTyping,
  }),
}));

let host: HTMLDivElement;
let root: Root;

async function render(node: ReactNode) {
  await act(async () => {
    root.render(node);
  });
}

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      disconnect() {}
    },
  );
  vi.stubGlobal("matchMedia", () => ({ matches: false }));
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
    callback(0);
    return 0;
  });
  Object.defineProperty(HTMLElement.prototype, "scrollTo", {
    configurable: true,
    value: vi.fn(),
  });
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
    configurable: true,
    value: vi.fn(),
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  chatStore.begin(user.id);
  chatStore.replaceConversations([conversation]);
  vi.spyOn(api, "messages").mockResolvedValue({ messages: [], hasMore: false });
  vi.spyOn(api, "read").mockResolvedValue({ readSeq: 10 });
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  chatStore.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("Markdown message rendering", () => {
  it("renders headings, bold, italic, strikethrough, blockquotes, lists, task lists, and horizontal rules", async () => {
    const markdown = [
      "# 一级标题",
      "### 三级标题",
      "",
      "这是 **粗体**、*斜体*、***粗斜体***、~~删除线~~ 以及 snake_case_identifier 变量名。",
      "",
      "> 引用第一行",
      "> 引用中的 **重点**",
      "",
      "- 无序项 A",
      "- 无序项 B",
      "",
      "1. 有序项一",
      "2. 有序项二",
      "",
      "- [ ] 待办任务",
      "- [x] 已完成任务",
      "",
      "---",
    ].join("\n");

    await render(<MessageBody body={markdown} userId={user.id} />);

    expect(host.querySelector("h1")?.textContent).toBe("一级标题");
    expect(host.querySelector("h3")?.textContent).toBe("三级标题");
    expect(host.querySelector("strong")?.textContent).toBe("粗体");
    expect(host.querySelector("em")?.textContent).toBe("斜体");
    expect(host.querySelector("strong em")?.textContent).toBe("粗斜体");
    expect(host.querySelector("del")?.textContent).toBe("删除线");
    expect(host.textContent).toContain("snake_case_identifier");

    const quote = host.querySelector("blockquote");
    expect(quote).not.toBeNull();
    expect(quote?.querySelector("strong")?.textContent).toBe("重点");

    expect(host.querySelectorAll("ul")).toHaveLength(2);
    expect(host.querySelectorAll("ol > li")).toHaveLength(2);

    const checkboxes = host.querySelectorAll<HTMLInputElement>(
      'input[type="checkbox"]',
    );
    expect(checkboxes).toHaveLength(2);
    expect(checkboxes[0].checked).toBe(false);
    expect(checkboxes[0].disabled).toBe(true);
    expect(checkboxes[1].checked).toBe(true);
    expect(checkboxes[1].disabled).toBe(true);

    expect(host.querySelector("hr")).not.toBeNull();
  });

  it("renders inline code and fenced code blocks without interpreting markdown, math, or HTML inside", async () => {
    const markdown = [
      "行内代码 `const a = $100$ + **not bold** + <script>` 测试。",
      "",
      "```ts",
      "const formula = '$E = mc^2$';",
      "const html = '<img src=x onerror=alert(1)>';",
      "// @林舟 should stay plain text in code",
      "```",
    ].join("\n");

    await render(<MessageBody body={markdown} userId={user.id} />);

    const inlineCode = host.querySelector("p code.markdown-inline-code");
    expect(inlineCode?.textContent).toBe(
      "const a = $100$ + **not bold** + <script>",
    );
    expect(inlineCode?.querySelector("strong")).toBeNull();
    expect(inlineCode?.querySelector(".katex")).toBeNull();

    const blockCode = host.querySelector("pre.markdown-code-block > code");
    expect(blockCode).not.toBeNull();
    expect(blockCode?.className).toContain("language-ts");
    expect(blockCode?.getAttribute("data-language")).toBe("ts");
    expect(blockCode?.textContent).toContain("const formula = '$E = mc^2$';");
    expect(blockCode?.querySelector(".katex")).toBeNull();
    expect(host.querySelector(".markdown-code-lang")?.textContent).toBe("ts");
    expect(host.querySelector("script")).toBeNull();
    expect(host.querySelector("img")).toBeNull();
  });

  it("copies fenced code block content to clipboard, shows copied feedback, resets after 1.8s, and falls back to code label", async () => {
    vi.useFakeTimers();
    try {
      const writeText = vi.fn().mockResolvedValue(undefined);
      Object.defineProperty(navigator, "clipboard", {
        configurable: true,
        value: { writeText },
      });

      const markdown = [
        "```python",
        "def greet(name: str) -> str:",
        "    return f'Hello, {name}!'",
        "```",
        "",
        "```",
        "plain block without language",
        "```",
      ].join("\n");

      await render(<MessageBody body={markdown} userId={user.id} />);

      const langLabels = host.querySelectorAll(".markdown-code-lang");
      expect(langLabels).toHaveLength(2);
      expect(langLabels[0]?.textContent).toBe("python");
      expect(langLabels[1]?.textContent).toBe("code");

      const copyBtn = host.querySelector<HTMLButtonElement>(
        "button.markdown-code-copy",
      );
      expect(copyBtn).not.toBeNull();
      expect(copyBtn?.textContent).toBe("复制");

      await act(async () => {
        copyBtn?.click();
      });

      expect(writeText).toHaveBeenCalledWith(
        "def greet(name: str) -> str:\n    return f'Hello, {name}!'",
      );
      expect(copyBtn?.textContent).toBe("已复制");
      expect(copyBtn?.classList.contains("copied")).toBe(true);

      await act(async () => {
        vi.advanceTimersByTime(1800);
      });
      expect(copyBtn?.textContent).toBe("复制");
      expect(copyBtn?.classList.contains("copied")).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });

  it("renders GFM tables with alignments, inline formatting, and escaped pipes", async () => {
    const markdown = [
      "| 模块 | 状态 | 复杂度 |",
      "| :--- | :---: | ---: |",
      "| **消息渲染** | `已完成` | $O(n)$ |",
      "| 管道转义 \\| 测试 | 正常 | 100% |",
    ].join("\n");

    await render(<MessageBody body={markdown} userId={user.id} />);

    const table = host.querySelector("table.markdown-table");
    expect(table).not.toBeNull();
    const headers = table!.querySelectorAll<HTMLTableCellElement>("thead th");
    expect(headers).toHaveLength(3);
    expect(headers[0].style.textAlign).toBe("left");
    expect(headers[1].style.textAlign).toBe("center");
    expect(headers[2].style.textAlign).toBe("right");

    const cells = table!.querySelectorAll("tbody td");
    expect(cells).toHaveLength(6);
    expect(cells[0].querySelector("strong")?.textContent).toBe("消息渲染");
    expect(cells[1].querySelector("code")?.textContent).toBe("已完成");
    expect(cells[2].querySelector(".katex")).not.toBeNull();
    expect(cells[3].textContent).toBe("管道转义 | 测试");
  });

  it("permits only http://, https://, and mailto: links with noopener noreferrer and blocks unsafe schemes and raw HTML", async () => {
    expect(sanitizeLinkUrl("https://teamie.example/docs")).toBe(
      "https://teamie.example/docs",
    );
    expect(sanitizeLinkUrl("http://example.com")).toBe("http://example.com");
    expect(sanitizeLinkUrl("mailto:security@teamie.example")).toBe(
      "mailto:security@teamie.example",
    );
    expect(sanitizeLinkUrl("javascript:alert(1)")).toBeNull();
    expect(sanitizeLinkUrl("  JaVaScRiPt:alert(1)  ")).toBeNull();
    expect(sanitizeLinkUrl("data:text/html,<script>alert(1)</script>")).toBeNull();
    expect(sanitizeLinkUrl("vbscript:msgbox(1)")).toBeNull();

    const markdown = [
      "安全链接 [官方文档](https://teamie.example/docs) 与 [邮件联系](mailto:hi@teamie.example)",
      "危险链接 [点击执行](javascript:alert(1)) 与 [数据注入](data:text/html,<script>alert(1)</script>) 与 [VB脚本](vbscript:evil)",
      "原始 HTML <script>window.xss = true</script> <img src=x onerror=alert(1)> <iframe src='https://evil.example'></iframe>",
    ].join("\n\n");

    await render(<MessageBody body={markdown} userId={user.id} />);

    const links = host.querySelectorAll<HTMLAnchorElement>("a");
    expect(links).toHaveLength(2);
    expect(links[0].getAttribute("href")).toBe("https://teamie.example/docs");
    expect(links[0].getAttribute("target")).toBe("_blank");
    expect(links[0].getAttribute("rel")).toBe("noopener noreferrer");
    expect(links[0].textContent).toBe("官方文档");

    expect(links[1].getAttribute("href")).toBe("mailto:hi@teamie.example");
    expect(links[1].getAttribute("target")).toBe("_blank");
    expect(links[1].getAttribute("rel")).toBe("noopener noreferrer");

    expect(host.textContent).toContain("点击执行");
    expect(host.textContent).toContain("数据注入");
    expect(host.textContent).toContain("VB脚本");
    expect(host.querySelector("script")).toBeNull();
    expect(host.querySelector("img")).toBeNull();
    expect(host.querySelector("iframe")).toBeNull();
    expect(host.textContent).toContain("<img src=x onerror=alert(1)>");
  });
});

describe("KaTeX formula rendering", () => {
  it("renders inline ($...$, \\(...\\)) and display ($$...$$, \\[...\\]) math formulas", async () => {
    const body = [
      "爱因斯坦质能方程 $E = mc^2$，以及勾股定理 \\(a^2 + b^2 = c^2\\)。",
      "",
      "$$\\int_0^\\infty e^{-x} dx = 1$$",
      "",
      "\\[",
      "\\sum_{n=1}^{\\infty} \\frac{1}{n^2} = \\frac{\\pi^2}{6}",
      "\\]",
    ].join("\n");

    await render(<MessageBody body={body} userId={user.id} />);

    const inlineMath = host.querySelectorAll(".markdown-math-inline .katex");
    expect(inlineMath).toHaveLength(2);
    expect(inlineMath[0].textContent).toContain("E = mc^2");
    expect(inlineMath[1].textContent).toContain("a^2 + b^2 = c^2");

    const displayMath = host.querySelectorAll(".katex-display");
    expect(displayMath).toHaveLength(2);
    expect(displayMath[0].textContent).toContain("\\int_0^\\infty e^{-x} dx = 1");
    expect(displayMath[1].textContent).toContain("\\frac{\\pi^2}{6}");
  });

  it("degrades gracefully on invalid LaTeX without crashing and avoids false positives on currency ($100)", async () => {
    const body = [
      "预算范围是 $100 and $200，或者 $100 - $200，单项 $100，转义 \\$500。",
      "同一行有价格 $100 与公式 $x + y = z$ 也能正常区分。",
      "未闭合或语法错误的公式：$\\frac{1}{$ 与 $$\\left( x$$ 不会导致崩溃。",
    ].join("\n\n");

    await render(<MessageBody body={body} userId={user.id} />);

    // Only the valid $x + y = z$ should render a normal .katex without .katex-error
    const validKatex = host.querySelectorAll(".katex");
    expect(validKatex).toHaveLength(1);
    expect(validKatex[0].textContent).toContain("x + y = z");

    // The 2 invalid formulas render .katex-error gracefully
    const errors = host.querySelectorAll(".katex-error");
    expect(errors).toHaveLength(2);
    expect(errors[0].textContent).toContain("\\frac{1}{");
    expect(errors[1].textContent).toContain("\\left( x");

    // Currency strings remain intact in textContent
    expect(host.textContent).toContain("$100 and $200");
    expect(host.textContent).toContain("$100 - $200");
    expect(host.textContent).toContain("$500");
  });
});

describe("Combined @mention pills, Markdown, and KaTeX in IM messages", () => {
  it("preserves exact @mention pills and UTF-16 offsets alongside Markdown and KaTeX in ConversationView", async () => {
    const prefix = "请看 `config.ts` 的 **核心结论** ";
    const selfMention = "@林舟";
    const middle = " 和 ";
    const agentMention = "@算力助手";
    const suffix =
      "：当 $E = mc^2$ 成立时，查看 [报告](https://teamie.example/report)\n\n$$\\int_0^\\infty e^{-x} dx = 1$$";

    const fullBody = prefix + selfMention + middle + agentMention + suffix;
    const selfStart = prefix.length;
    const selfEnd = selfStart + selfMention.length;
    const agentStart = selfEnd + middle.length;
    const agentEnd = agentStart + agentMention.length;

    const richMessage: Message = {
      id: "msg-rich-1",
      clientMessageId: "client-rich-1",
      conversationId: conversation.id,
      senderId: "human-two",
      senderName: "陈默",
      senderKind: "human",
      seq: 10,
      body: fullBody,
      mentions: [
        { userId: user.id, start: selfStart, end: selfEnd },
        { userId: "agent-one", start: agentStart, end: agentEnd },
      ],
      createdAt: user.createdAt,
      editedAt: "2026-10-22T00:05:00Z",
    };

    vi.mocked(api.messages).mockResolvedValue({
      messages: [richMessage],
      hasMore: false,
    });

    await render(
      <ConversationView
        conversation={conversation}
        members={members}
        onMembers={vi.fn()}
        membersOpen={false}
        onMenu={vi.fn()}
      />,
    );

    const bodyEl = host.querySelector("#message-msg-rich-1 .message-body")!;
    expect(bodyEl).not.toBeNull();

    // 1. Mention pills preserved with exact classes & self-highlight
    const pills = bodyEl.querySelectorAll(".message-mention.mention-pill");
    expect(pills).toHaveLength(2);
    expect(pills[0].textContent).toBe("@林舟");
    expect(pills[0].classList.contains("mention-self")).toBe(true);
    expect(pills[1].textContent).toBe("@算力助手");
    expect(pills[1].classList.contains("mention-self")).toBe(false);

    // 2. Markdown elements rendered
    expect(bodyEl.querySelector("code.markdown-inline-code")?.textContent).toBe(
      "config.ts",
    );
    expect(bodyEl.querySelector("strong")?.textContent).toBe("核心结论");
    const link = bodyEl.querySelector<HTMLAnchorElement>("a.markdown-link");
    expect(link?.getAttribute("href")).toBe("https://teamie.example/report");
    expect(link?.getAttribute("target")).toBe("_blank");
    expect(link?.getAttribute("rel")).toBe("noopener noreferrer");

    // 3. Inline and display KaTeX rendered
    expect(
      bodyEl.querySelector(".markdown-math-inline .katex")?.textContent,
    ).toContain("E = mc^2");
    expect(bodyEl.querySelector(".katex-display")?.textContent).toContain(
      "\\int_0^\\infty e^{-x} dx = 1",
    );

    // 4. Edited label preserved
    expect(bodyEl.querySelector(".edited-label")?.textContent).toBe("（已编辑）");
  });

  it("handles pathological 7,900+ deeply nested blockquotes and inline markers without stack overflow", async () => {
    const quotes = ">".repeat(7900);
    const mentionLabel = "@林舟";
    const body = `${quotes} ${mentionLabel} deep quote`;
    const start = quotes.length + 1;
    const end = start + mentionLabel.length;

    const deepMessage: Message = {
      id: "msg-deep-1",
      clientMessageId: "client-deep-1",
      conversationId: conversation.id,
      senderId: "human-two",
      senderName: "陈默",
      senderKind: "human",
      seq: 10,
      body,
      mentions: [{ userId: user.id, start, end }],
      createdAt: user.createdAt,
    };

    vi.mocked(api.messages).mockResolvedValue({
      messages: [deepMessage],
      hasMore: false,
    });

    await render(
      <ConversationView
        conversation={conversation}
        members={members}
        onMembers={vi.fn()}
        membersOpen={false}
        onMenu={vi.fn()}
      />,
    );

    const bodyEl = host.querySelector("#message-msg-deep-1 .message-body")!;
    expect(bodyEl).not.toBeNull();
    expect(bodyEl.textContent).toContain("deep quote");
    expect(bodyEl.querySelector(".mention-self")?.textContent).toBe("@林舟");
    expect(
      bodyEl.querySelectorAll("blockquote.markdown-blockquote").length,
    ).toBeLessThanOrEqual(16);

    // Also verify deeply nested inline delimiters
    const deepInline =
      "**".repeat(40) + "inner @林舟 text" + "**".repeat(40);
    await render(
      <MessageBody
        body={deepInline}
        mentions={[{ userId: user.id, start: 86, end: 89 }]}
        userId={user.id}
      />,
    );
    expect(host.textContent).toContain("inner @林舟 text");
    expect(host.querySelector(".mention-self")?.textContent).toBe("@林舟");
  });

  it("preserves trailing text on the closing line of multi-line and single-line display math blocks", async () => {
    const body = [
      "$$",
      "a + b = c",
      "$$ 同一行的说明文字一",
      "",
      "\\[",
      "x^2 + y^2 = z^2",
      "\\] 同一行的说明文字二",
    ].join("\n");

    await render(<MessageBody body={body} userId={user.id} />);

    const displayMath = host.querySelectorAll(".katex-display");
    expect(displayMath).toHaveLength(2);
    expect(displayMath[0].textContent).toContain("a + b = c");
    expect(displayMath[1].textContent).toContain("x^2 + y^2 = z^2");
    expect(host.textContent).toContain("同一行的说明文字一");
    expect(host.textContent).toContain("同一行的说明文字二");
  });

  it("preserves non-whitespace trailing # in ATX headings (# C#, ## F#) while stripping whitespace-preceded closing #", async () => {
    const body = [
      "# C#",
      "## F# Programming",
      "### C###",
      "#### Heading ####",
      "##### #####",
    ].join("\n");

    await render(<MessageBody body={body} userId={user.id} />);

    expect(host.querySelector("h1")?.textContent).toBe("C#");
    expect(host.querySelector("h2")?.textContent).toBe("F# Programming");
    expect(host.querySelector("h3")?.textContent).toBe("C###");
    expect(host.querySelector("h4")?.textContent).toBe("Heading");
    expect(host.querySelector("h5")?.textContent).toBe("");
  });
});
