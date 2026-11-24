// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, describe, expect, it } from "vitest";
import type { Message } from "@teamie/api-client";
import {
  buildMultiTermSnippet,
  matchesSearchTerms,
  MultiTermHighlight,
  parseSearchQuery,
} from "./search-highlight";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

const baseMessage: Message = {
  id: "m-1",
  clientMessageId: "cm-1",
  conversationId: "c-1",
  senderId: "u-1",
  senderName: "林舟",
  senderKind: "human",
  seq: 1,
  body: "发布 v0.4.11 架构评审记录",
  createdAt: "2026-11-24T00:00:00Z",
};

describe("search-highlight utilities", () => {
  let host: HTMLDivElement | null = null;

  afterEach(() => {
    host?.remove();
    host = null;
  });

  it("parses multi-term CJK, quoted phrases, deduplication, and limits", () => {
    expect(parseSearchQuery('发布 "error code" v0.4.11 发布')).toEqual({
      terms: ["发布", "error code", "v0.4.11"],
      tooLong: false,
      tooManyTerms: false,
    });
    expect(parseSearchQuery('""   "   " alpha')).toEqual({
      terms: ["alpha"],
      tooLong: false,
      tooManyTerms: false,
    });
    expect(parseSearchQuery('"unclosed phrase')).toEqual({
      terms: ['"unclosed', "phrase"],
      tooLong: false,
      tooManyTerms: false,
    });
    expect(parseSearchQuery("t1 t2 t3 t4 t5 t6 t7 t8 t9").tooManyTerms).toBe(
      true,
    );
    expect(parseSearchQuery("测".repeat(201)).tooLong).toBe(true);
  });

  it("matches body, forwarded snapshot, and optional attachment filenames", () => {
    const withFiles: Message = {
      ...baseMessage,
      body: "查看附件",
      forwarded: {
        sourceConversationId: "c-src",
        sourceMessageId: "m-src",
        senderId: "u-2",
        senderName: "陈工",
        senderKind: "human",
        createdAt: "2026-11-23T00:00:00Z",
        body: "原始部署说明",
        attachments: [
          {
            id: "a-1",
            filename: "Release_Notes_v0411.pdf",
            mediaType: "application/pdf",
            sizeBytes: 1024,
            sha256: "abc",
          },
        ],
      },
    };
    expect(matchesSearchTerms(withFiles, ["查看", "原始部署"])).toBe(true);
    expect(matchesSearchTerms(withFiles, ["release_notes"], false)).toBe(false);
    expect(matchesSearchTerms(withFiles, ["release_notes", "查看"], true)).toBe(
      true,
    );
  });

  it("windows long bodies around the earliest matched term and highlights escaped text", async () => {
    const longBody = `${"前文填充内容".repeat(25)} 发布 <script>alert(1)</script> v0.4.11 ${"后文补充".repeat(25)}`;
    const snippet = buildMultiTermSnippet(longBody, ["v0.4.11", "发布"], 60);
    expect(snippet.truncatedStart).toBe(true);
    expect(snippet.truncatedEnd).toBe(true);
    expect(snippet.text).toContain("发布");
    expect(snippet.text).toContain("v0.4.11");

    host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    await act(async () => {
      root.render(
        <MultiTermHighlight
          text={longBody}
          query="发布 v0.4.11"
          maxLength={80}
          plain
        />,
      );
    });
    const marks = Array.from(host.querySelectorAll("mark")).map(
      (m) => m.textContent,
    );
    expect(marks).toEqual(["发布", "v0.4.11"]);
    expect(host.querySelector("script")).toBeNull();
    expect(host.textContent).toContain("<script>alert(1)</script>");
    await act(async () => {
      root.unmount();
    });
  });
});
