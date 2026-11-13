// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Member, ReadReceipts } from "@teamie/api-client";
import { formatDateTime, sameLocalDay } from "./format-datetime";
import { messagePreviewText, plainPreview, searchSnippet } from "./chat/plain-preview";
import { linkPresentation } from "./markdown/links";
import { highlightHtmlToNodes, loadHighlighter } from "./markdown/highlight";
import { agentTone } from "./agent-ui";
import { rankMentionMembers } from "./mentions";
import { prefersEnterToSend, resetKeyboardSourceForTests } from "./chat/keyboard-source";
import { COMMON_REACTION_EMOJIS, computeMessageReadSummary } from "./engagement-store";
import { isReactionEmoji, searchEmojis, allowedEmojiCount } from "./chat/emoji-catalog";
import { FULLY_QUALIFIED_COUNT } from "./chat/emoji-data.generated";
import { thumbnailBox } from "./attachments";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
  resetKeyboardSourceForTests();
});

describe("formatDateTime", () => {
  const now = new Date(2026, 10, 11, 15, 0);
  it("labels date separators 今天/昨天/weekday and adds the year across years", () => {
    expect(formatDateTime(new Date(2026, 10, 11, 9, 5), "day", now)).toBe("今天");
    expect(formatDateTime(new Date(2026, 10, 10, 23, 59), "day", now)).toBe("昨天");
    expect(formatDateTime(new Date(2026, 10, 2, 9, 5), "day", now)).toBe("11月2日星期一");
    expect(formatDateTime(new Date(2025, 10, 2, 9, 5), "day", now)).toBe("2025年11月2日星期日");
  });
  it("formats time, compact datetime, short date and the full tooltip", () => {
    const value = new Date(2026, 10, 2, 9, 5);
    expect(formatDateTime(value, "time", now)).toBe("09:05");
    expect(formatDateTime(new Date(2026, 10, 11, 9, 5), "datetime", now)).toBe("今天 09:05");
    expect(formatDateTime(new Date(2026, 10, 10, 9, 5), "datetime", now)).toBe("昨天 09:05");
    expect(formatDateTime(value, "datetime", now)).toBe("11月2日 09:05");
    expect(formatDateTime(new Date(2024, 0, 3, 18, 30), "datetime", now)).toBe("2024年1月3日 18:30");
    expect(formatDateTime(value, "date", now)).toBe("11月2日");
    expect(formatDateTime(value, "full", now)).toBe("2026年11月2日星期一 09:05");
    expect(formatDateTime("not a date", "full", now)).toBe("");
  });
  it("compares local calendar days", () => {
    expect(sameLocalDay(new Date(2026, 0, 1, 0, 1), new Date(2026, 0, 1, 23, 59))).toBe(true);
    expect(sameLocalDay(new Date(2026, 0, 1, 23, 59), new Date(2026, 0, 2, 0, 1))).toBe(false);
  });
  it("is the only date formatter in chat surfaces (no ad-hoc toLocale*(\"zh-CN\"))", () => {
    for (const file of ["ui.tsx", "message-item.tsx", "conversation-search.tsx", "chat/conversation-chrome.tsx"]) {
      const source = readFileSync(join(__dirname, file), "utf8");
      expect(source, file).not.toMatch(/toLocale(?:Date|Time)?String\(/);
    }
  });
});

describe("plain previews and search snippets", () => {
  it("strips Markdown syntax but keeps the words", () => {
    expect(
      plainPreview("# 标题\n> **重要** 请看 [文档](https://a.example/x) 和 `code`\n- [x] 完成 ~~旧~~\n```ts\nconst a = 1;\n```"),
    ).toBe("标题 重要 请看 文档 和 code 完成 旧 const a = 1;");
    expect(plainPreview("| a | b |\n|---|---|\n| 1 | 2 |")).toBe("a b 1 2");
    expect(plainPreview("价格 \\*不是强调\\*")).toBe("价格 *不是强调*");
  });
  it("describes attachment-only messages", () => {
    expect(messagePreviewText({ body: "", attachments: [{}, {}] })).toBe("2 个文件");
    expect(messagePreviewText(undefined, "较早的消息")).toBe("较早的消息");
  });
  it("centres a ~120 character window on the first match with ellipses", () => {
    const text = `${"甲".repeat(300)}命中词${"乙".repeat(300)}`;
    const snippet = searchSnippet(text, "命中词");
    expect(Array.from(snippet.text)).toHaveLength(120);
    expect(snippet.text).toContain("命中词");
    expect(snippet.truncatedStart && snippet.truncatedEnd).toBe(true);
    const index = snippet.text.indexOf("命中词");
    expect(Math.abs(index - (120 - 3 - index))).toBeLessThanOrEqual(2);
    expect(searchSnippet("short text", "text")).toEqual({ text: "short text", truncatedStart: false, truncatedEnd: false });
    expect(searchSnippet(`${"x".repeat(200)}END`, "end").text.endsWith("END")).toBe(true);
  });
});

describe("message links", () => {
  it("adds the real host when the label does not name it, and routes same-origin /app links in-app", () => {
    expect(linkPresentation("https://docs.example/a", "设计文档").hostSuffix).toBe("docs.example");
    expect(linkPresentation("https://evil.example/", "https://safe.example").hostSuffix).toBe("evil.example");
    expect(linkPresentation("https://www.docs.example/a", "docs.example/a").hostSuffix).toBeUndefined();
    expect(linkPresentation("https://xn--fsq.example/", "例.example").href).toBe("https://xn--fsq.example/");
    const internal = linkPresentation(`${window.location.origin}/app/w/t/c/x?message=m#top`, "看这里");
    expect(internal.appPath).toBe("/app/w/t/c/x?message=m#top");
    expect(internal.hostSuffix).toBeUndefined();
    expect(linkPresentation(`${window.location.origin}/login`, "登录").appPath).toBeUndefined();
    expect(linkPresentation("mailto:hi@x.example", "邮件")).toEqual({ href: "mailto:hi@x.example" });
  });
});

describe("lazy syntax highlighting", () => {
  it("highlights known languages into token classes without innerHTML", async () => {
    const module = await loadHighlighter();
    const html = module.highlightToHtml('const answer = "42"; // note', "typescript")!;
    const nodes = highlightHtmlToNodes(html)!;
    expect(nodes.length).toBeGreaterThan(1);
    expect(JSON.stringify(nodes)).toContain("code-token-keyword");
    expect(module.highlightToHtml("x", "no-such-language")).toBeNull();
    expect(highlightHtmlToNodes("<img src=x onerror=alert(1)>")).toBeNull();
    expect(highlightHtmlToNodes("a &lt;b&gt; &amp; c")).toEqual(["a <b> & c"]);
  });
});

describe("Agent identity", () => {
  it("derives a stable token palette tone from the Agent ID", () => {
    expect(agentTone("agent-1")).toBe(agentTone("agent-1"));
    const tones = new Set(["a", "b", "c", "d", "e", "f", "g", "h", "i", "j"].map((id) => agentTone(`agent-${id}`)));
    expect(tones.size).toBeGreaterThan(2);
    for (const tone of tones) expect(["lilac", "mint", "peach", "blue", "rose"]).toContain(tone);
  });
});

describe("mention candidates", () => {
  const member = (userId: string, displayName: string, extra: Partial<Member & { agentEnabled: boolean }> = {}) =>
    ({ userId, displayName, kind: "human", role: "member", ...extra }) as Member & { agentEnabled?: boolean };
  it("ranks prefix > word start > substring, excludes self and keeps disabled Agents last", () => {
    const members = [
      member("me", "Annie"),
      member("sub", "Joanna"),
      member("word", "Big Ann"),
      member("prefix", "Anna"),
      member("off", "Ann Bot", { kind: "agent", agentEnabled: false }),
    ];
    expect(rankMentionMembers(members, "ann", "me").map((m) => m.userId)).toEqual([
      "prefix", "word", "sub", "off",
    ]);
    expect(rankMentionMembers(members, "", "me").map((m) => m.userId)).not.toContain("me");
  });
});

describe("Enter behaviour by keyboard source", () => {
  const coarse = (matches: boolean) => vi.stubGlobal("matchMedia", () => ({ matches }));
  const enter = { key: "Enter", shiftKey: false, metaKey: false, ctrlKey: false, altKey: false, keyCode: 13 };
  it("sends on fine pointers, keeps Shift+Enter as newline and Ctrl/Cmd+Enter always sends", () => {
    coarse(false);
    expect(prefersEnterToSend(enter)).toBe(true);
    expect(prefersEnterToSend({ ...enter, shiftKey: true })).toBe(false);
    coarse(true);
    vi.stubGlobal("visualViewport", { height: 300, scale: 1 });
    expect(prefersEnterToSend({ ...enter, ctrlKey: true })).toBe(true);
    expect(prefersEnterToSend({ ...enter, metaKey: true })).toBe(true);
  });
  it("on touch devices sends only when no on-screen keyboard covers the viewport", () => {
    coarse(true);
    vi.stubGlobal("innerHeight", 800);
    vi.stubGlobal("visualViewport", { height: 420, scale: 1 });
    expect(prefersEnterToSend(enter)).toBe(false);
    vi.stubGlobal("visualViewport", { height: 790, scale: 1 });
    expect(prefersEnterToSend(enter)).toBe(true);
    expect(prefersEnterToSend({ ...enter, keyCode: 229 })).toBe(false);
  });
  it("falls back to recent hardware-only keys without a visual viewport", () => {
    coarse(true);
    vi.stubGlobal("visualViewport", undefined);
    expect(prefersEnterToSend(enter)).toBe(false);
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab" }));
    expect(prefersEnterToSend(enter)).toBe(true);
  });
});

describe("reaction emoji allowlist", () => {
  it("is generated from the pinned Unicode Emoji 17.0 data and validates per contract", () => {
    expect(FULLY_QUALIFIED_COUNT).toBe(3944);
    expect(allowedEmojiCount()).toBe(3944);
    for (const emoji of COMMON_REACTION_EMOJIS) expect(isReactionEmoji(emoji)).toBe(true);
    expect(isReactionEmoji("👍🏽")).toBe(true);
    expect(isReactionEmoji("❤")).toBe(false); // unqualified
    expect(isReactionEmoji("🏽")).toBe(false); // component alone
    expect(isReactionEmoji(":smile:")).toBe(false);
    expect(isReactionEmoji("👍👍")).toBe(false);
    expect(searchEmojis("rocket")).toContain("🚀");
    expect(searchEmojis("旗帜").length).toBeGreaterThan(100);
  });
});

describe("read receipts", () => {
  it("names members missing from the directory 「未知成员」, never by ID", () => {
    const receipts: ReadReceipts = {
      conversationId: "g", eligibleCount: 2,
      readSeqCounts: [{ readSeq: 5, count: 2 }],
      members: [{ userId: "known", readSeq: 5 }, { userId: "0123456789abcdef", readSeq: 5 }],
    };
    const summary = computeMessageReadSummary({
      receipts, seq: 5, senderId: "me", senderReadSeq: 5, conversationType: "group",
      members: [{ userId: "known", displayName: "已知", kind: "agent", role: "member" }],
    });
    expect(summary?.readMembers.map((m) => m.displayName)).toEqual(["已知", "未知成员"]);
    expect(JSON.stringify(summary)).not.toContain("01234567\"");
  });
});

describe("thumbnail reservation", () => {
  it("reserves the final box from server dimensions within 320×220", () => {
    expect(thumbnailBox(640, 360)).toEqual({ width: "320px", aspectRatio: "640 / 360" });
    expect(thumbnailBox(300, 600)).toEqual({ width: "110px", aspectRatio: "300 / 600" });
    expect(thumbnailBox(100, 50)).toEqual({ width: "100px", aspectRatio: "100 / 50" });
  });
});
