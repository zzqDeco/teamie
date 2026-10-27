import { memo, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Check, Copy } from "lucide-react";
import "katex/dist/katex.min.css";
import type { Mention } from "@teamie/api-client";
import { ICON } from "./icons";
import {
  createMentionSentinelPrefix,
  injectMentions,
  KATEX_DISPLAY_CLASS,
  MAX_BLOCK_DEPTH,
  matchBareUrl,
  matchInlineCode,
  matchInlineMath,
  renderMathNode,
  restoreMentionsText,
  sanitizeLinkUrl,
} from "./markdown/tokens";
import type { ParseContext } from "./markdown/tokens";
import { renderInline, renderPlainWithMentions } from "./markdown/inline";
export { KATEX_DISPLAY_CLASS, matchBareUrl, sanitizeLinkUrl };

function CodeBlock({
  codeText,
  lang,
}: {
  codeText: string;
  lang: string;
}) {
  const [copied, setCopied] = useState(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timerRef.current), []);

  async function handleCopy() {
    try {
      await navigator.clipboard?.writeText(codeText);
      setCopied(true);
      clearTimeout(timerRef.current);
      timerRef.current = setTimeout(() => setCopied(false), 1800);
    } catch {
      /* Clipboard write can fail in restricted contexts. */
    }
  }

  return (
    <div className="markdown-code-wrap">
      <div className="markdown-code-header">
        <span className="markdown-code-lang">{lang || "code"}</span>
        <button
          type="button"
          className={`markdown-code-copy ${copied ? "copied" : ""}`}
          onClick={() => void handleCopy()}
          aria-label={copied ? "已复制代码" : "复制代码"}
        >
          {copied ? <Check size={ICON.xs} /> : <Copy size={ICON.xs} />}
          <span>{copied ? "已复制" : "复制"}</span>
        </button>
      </div>
      <pre className="markdown-code-block">
        <code
          className={lang ? `language-${lang}` : undefined}
          data-language={lang || undefined}
        >
          {codeText}
        </code>
      </pre>
    </div>
  );
}

function splitTableCells(line: string): string[] {
  let trimmed = line.trim();
  if (trimmed.startsWith("|")) trimmed = trimmed.slice(1);
  if (trimmed.endsWith("|") && !trimmed.endsWith("\\|")) {
    trimmed = trimmed.slice(0, -1);
  }
  const cells: string[] = [];
  let current = "";
  let i = 0;
  while (i < trimmed.length) {
    if (trimmed[i] === "\\" && trimmed[i + 1] === "|") {
      current += "|";
      i += 2;
      continue;
    }
    if (trimmed[i] === "`") {
      const code = matchInlineCode(trimmed, i);
      if (code) {
        current += trimmed.slice(i, code.end);
        i = code.end;
        continue;
      }
    }
    if (trimmed[i] === "$") {
      const math = matchInlineMath(trimmed, i);
      if (math) {
        current += trimmed.slice(i, math.end);
        i = math.end;
        continue;
      }
    }
    if (trimmed[i] === "|") {
      cells.push(current.trim());
      current = "";
      i++;
      continue;
    }
    current += trimmed[i];
    i++;
  }
  cells.push(current.trim());
  return cells;
}

function parseTableAlignments(
  delimiterLine: string,
  expectedCols: number,
): Array<"left" | "center" | "right" | undefined> | null {
  if (!delimiterLine.includes("|") && expectedCols <= 1) return null;
  const cells = splitTableCells(delimiterLine);
  if (cells.length !== expectedCols || cells.length === 0) return null;
  const alignments: Array<"left" | "center" | "right" | undefined> = [];
  for (const cell of cells) {
    if (!/^:?-{1,}:?$/.test(cell)) {
      return null;
    }
    const left = cell.startsWith(":");
    const right = cell.endsWith(":");
    if (left && right) alignments.push("center");
    else if (right) alignments.push("right");
    else if (left) alignments.push("left");
    else alignments.push(undefined);
  }
  return alignments;
}

function isHorizontalRule(line: string): boolean {
  return /^ {0,3}(?:(?:-\s*){3,}|(?:\*\s*){3,}|(?:_\s*){3,})$/.test(line);
}

function matchHeading(line: string): { level: number; text: string } | null {
  const m = /^ {0,3}(#{1,6})(.*)$/.exec(line);
  if (!m) return null;
  const rawRest = m[2];
  // Require space/tab after # unless the line has nothing after the opening #s
  if (rawRest.length > 0 && !/^[ \t]/.test(rawRest)) return null;
  let content = rawRest.trim();
  // CommonMark §4.2: closing # sequence is stripped only when preceded by whitespace or when the whole body is #s
  if (/^#+$/.test(content)) {
    content = "";
  } else {
    content = content.replace(/[ \t]+#+$/, "").trimEnd();
  }
  return { level: m[1].length, text: content };
}

function matchFenceOpen(
  line: string,
): { fenceChar: string; fenceLen: number; lang: string } | null {
  const m = /^ {0,3}(`{3,}|~{3,})([^\n`]*)$/.exec(line);
  if (!m) return null;
  const fence = m[1];
  const rawLang = m[2].trim().split(/\s+/)[0] || "";
  const lang = /^[a-zA-Z0-9_+-]+$/.test(rawLang) ? rawLang : "";
  return { fenceChar: fence[0], fenceLen: fence.length, lang };
}

function matchListItem(
  line: string,
): { indent: number; ordered: boolean; startNum: number; content: string } | null {
  if (isHorizontalRule(line)) return null;
  const ul = /^( *)([-*+])\s+(.*)$/.exec(line);
  if (ul) {
    return {
      indent: ul[1].length,
      ordered: false,
      startNum: 1,
      content: ul[3],
    };
  }
  const ol = /^( *)(\d+)\.\s+(.*)$/.exec(line);
  if (ol) {
    return {
      indent: ol[1].length,
      ordered: true,
      startNum: Number(ol[2]),
      content: ol[3],
    };
  }
  return null;
}

function matchDisplayMathBlock(
  lines: string[],
  startIndex: number,
): { formula: string; nextIndex: number; trailingText?: string } | null {
  const line = lines[startIndex];

  // Single-line or multi-line $$...$$ starting at beginning of line
  if (/^ {0,3}\$\$/.test(line)) {
    const firstRest = line.replace(/^ {0,3}\$\$/, "");
    const sameLineClose = firstRest.indexOf("$$");
    if (sameLineClose !== -1) {
      const inner = firstRest.slice(0, sameLineClose);
      const after = firstRest.slice(sameLineClose + 2);
      if (inner.trim().length > 0 && !after.includes("$$")) {
        return {
          formula: inner,
          nextIndex: startIndex + 1,
          trailingText: after.trim() ? after.trimStart() : undefined,
        };
      }
      return null;
    }
    const collected: string[] = [firstRest];
    for (let j = startIndex + 1; j < lines.length; j++) {
      const closePos = lines[j].indexOf("$$");
      if (closePos !== -1) {
        collected.push(lines[j].slice(0, closePos));
        const after = lines[j].slice(closePos + 2);
        const formula = collected.join("\n");
        if (formula.trim().length > 0) {
          return {
            formula,
            nextIndex: j + 1,
            trailingText: after.trim() ? after.trimStart() : undefined,
          };
        }
        break;
      }
      collected.push(lines[j]);
    }
  }

  // Single-line or multi-line \[...\] starting at beginning of line
  if (/^ {0,3}\\\[/.test(line)) {
    const firstRest = line.replace(/^ {0,3}\\\[/, "");
    const sameLineClose = firstRest.indexOf("\\]");
    if (sameLineClose !== -1) {
      const inner = firstRest.slice(0, sameLineClose);
      const after = firstRest.slice(sameLineClose + 2);
      if (inner.trim().length > 0 && !after.includes("\\[")) {
        return {
          formula: inner,
          nextIndex: startIndex + 1,
          trailingText: after.trim() ? after.trimStart() : undefined,
        };
      }
      return null;
    }
    const collected: string[] = [firstRest];
    for (let j = startIndex + 1; j < lines.length; j++) {
      const closePos = lines[j].indexOf("\\]");
      if (closePos !== -1) {
        collected.push(lines[j].slice(0, closePos));
        const after = lines[j].slice(closePos + 2);
        const formula = collected.join("\n");
        if (formula.trim().length > 0) {
          return {
            formula,
            nextIndex: j + 1,
            trailingText: after.trim() ? after.trimStart() : undefined,
          };
        }
        break;
      }
      collected.push(lines[j]);
    }
  }

  return null;
}

function isBlockBoundary(lines: string[], idx: number): boolean {
  const line = lines[idx];
  if (line.trim() === "") return true;
  if (matchFenceOpen(line)) return true;
  if (matchDisplayMathBlock(lines, idx)) return true;
  if (matchHeading(line)) return true;
  if (isHorizontalRule(line)) return true;
  if (/^ {0,3}>/.test(line)) return true;
  if (matchListItem(line)) return true;
  if (
    line.includes("|") &&
    idx + 1 < lines.length &&
    parseTableAlignments(lines[idx + 1], splitTableCells(line).length) !== null
  ) {
    return true;
  }
  return false;
}

function parseListBlock(
  lines: string[],
  startIndex: number,
  ctx: ParseContext,
  depth = 0,
): { node: ReactNode; nextIndex: number } {
  const firstMatch = matchListItem(lines[startIndex])!;
  const baseIndent = firstMatch.indent;
  const ordered = firstMatch.ordered;
  const startNum = firstMatch.startNum;
  const items: ReactNode[] = [];
  let i = startIndex;

  while (i < lines.length) {
    if (lines[i].trim() === "") {
      // A blank line ends the list unless the next line is another item at this indent
      if (i + 1 < lines.length) {
        const nextMatch = matchListItem(lines[i + 1]);
        if (
          nextMatch &&
          nextMatch.indent === baseIndent &&
          nextMatch.ordered === ordered
        ) {
          i++;
          continue;
        }
      }
      break;
    }

    const itemMatch = matchListItem(lines[i]);
    if (!itemMatch) break;
    if (itemMatch.indent < baseIndent) break;
    if (
      itemMatch.indent === baseIndent &&
      itemMatch.ordered !== ordered
    )
      break;

    if (itemMatch.indent > baseIndent && depth < MAX_BLOCK_DEPTH) {
      // Nested list when encountered directly
      const nested = parseListBlock(lines, i, ctx, depth + 1);
      items.push(<li key={ctx.nextKey()}>{nested.node}</li>);
      i = nested.nextIndex;
      continue;
    }

    const itemLines: string[] = [itemMatch.content];
    i++;
    const nestedNodes: ReactNode[] = [];

    while (i < lines.length) {
      if (lines[i].trim() === "") break;
      const subItem = matchListItem(lines[i]);
      if (subItem) {
        if (subItem.indent > baseIndent && depth < MAX_BLOCK_DEPTH) {
          const nested = parseListBlock(lines, i, ctx, depth + 1);
          nestedNodes.push(nested.node);
          i = nested.nextIndex;
          continue;
        }
        break;
      }
      if (isBlockBoundary(lines, i)) break;
      const lineIndent = /^ */.exec(lines[i])?.[0].length ?? 0;
      if (lineIndent > baseIndent) {
        itemLines.push(lines[i].trimStart());
        i++;
      } else {
        break;
      }
    }

    const itemText = itemLines.join("\n");
    const taskMatch = /^\[([ xX])\]\s+([\s\S]*)$/.exec(itemText);
    if (taskMatch) {
      const checked = taskMatch[1].toLowerCase() === "x";
      items.push(
        <li key={ctx.nextKey()} className="task-list-item">
          <input
            type="checkbox"
            className="task-list-checkbox"
            checked={checked}
            disabled
            readOnly
          />
          <span>{renderInline(taskMatch[2], ctx)}</span>
          {nestedNodes}
        </li>,
      );
    } else {
      items.push(
        <li key={ctx.nextKey()}>
          {renderInline(itemText, ctx)}
          {nestedNodes}
        </li>,
      );
    }
  }

  if (ordered) {
    return {
      node: (
        <ol
          key={ctx.nextKey()}
          className="markdown-list"
          start={startNum !== 1 ? startNum : undefined}
        >
          {items}
        </ol>
      ),
      nextIndex: i,
    };
  }
  return {
    node: (
      <ul key={ctx.nextKey()} className="markdown-list">
        {items}
      </ul>
    ),
    nextIndex: i,
  };
}

function parseBlocks(
  text: string,
  ctx: ParseContext,
  depth = 0,
): ReactNode[] {
  if (depth >= MAX_BLOCK_DEPTH) {
    const flattened = text.replace(/^(?: {0,3}> ?)+/gm, "");
    return [
      <p key={ctx.nextKey()} className="markdown-paragraph">
        {renderInline(flattened, ctx)}
      </p>,
    ];
  }
  const lines = text.split("\n");
  const blocks: ReactNode[] = [];
  let i = 0;

  while (i < lines.length) {
    const line = lines[i];

    // Skip blank lines between blocks
    if (line.trim() === "") {
      i++;
      continue;
    }

    // 1. Fenced code block
    const fence = matchFenceOpen(line);
    if (fence) {
      const closeRe = new RegExp(
        `^ {0,3}${fence.fenceChar}{${fence.fenceLen},}\\s*$`,
      );
      const codeLines: string[] = [];
      i++;
      while (i < lines.length && !closeRe.test(lines[i])) {
        codeLines.push(lines[i]);
        i++;
      }
      if (i < lines.length) {
        i++; // consume closing fence
      }
      const codeText = restoreMentionsText(codeLines.join("\n"), ctx);
      blocks.push(
        <CodeBlock
          key={ctx.nextKey()}
          codeText={codeText}
          lang={fence.lang}
        />,
      );
      continue;
    }

    // 2. Display math block ($$...$$ or \[...\])
    const mathBlock = matchDisplayMathBlock(lines, i);
    if (mathBlock) {
      blocks.push(
        <div key={ctx.nextKey()} className="markdown-math-block">
          {renderMathNode(mathBlock.formula, true, ctx)}
        </div>,
      );
      if (mathBlock.trailingText) {
        lines[mathBlock.nextIndex - 1] = mathBlock.trailingText;
        i = mathBlock.nextIndex - 1;
      } else {
        i = mathBlock.nextIndex;
      }
      continue;
    }

    // 3. Heading
    const heading = matchHeading(line);
    if (heading) {
      const Tag = `h${heading.level}` as
        | "h1"
        | "h2"
        | "h3"
        | "h4"
        | "h5"
        | "h6";
      blocks.push(
        <Tag key={ctx.nextKey()} className="markdown-heading">
          {renderInline(heading.text, ctx)}
        </Tag>,
      );
      i++;
      continue;
    }

    // 4. Horizontal rule
    if (isHorizontalRule(line)) {
      blocks.push(<hr key={ctx.nextKey()} className="markdown-hr" />);
      i++;
      continue;
    }

    // 5. Blockquote
    if (/^ {0,3}>/.test(line)) {
      const quoteLines: string[] = [];
      const atMaxDepth = depth + 1 >= MAX_BLOCK_DEPTH;
      while (i < lines.length && /^ {0,3}>/.test(lines[i])) {
        quoteLines.push(
          atMaxDepth
            ? lines[i].replace(/^(?: {0,3}> ?)+/, "")
            : lines[i].replace(/^ {0,3}> ?/, ""),
        );
        i++;
      }
      blocks.push(
        <blockquote key={ctx.nextKey()} className="markdown-blockquote">
          {parseBlocks(quoteLines.join("\n"), ctx, depth + 1)}
        </blockquote>,
      );
      continue;
    }

    // 6. GFM Table
    if (line.includes("|") && i + 1 < lines.length) {
      const headerCells = splitTableCells(line);
      const alignments = parseTableAlignments(lines[i + 1], headerCells.length);
      if (alignments) {
        i += 2;
        const rows: string[][] = [];
        while (
          i < lines.length &&
          lines[i].trim() !== "" &&
          lines[i].includes("|") &&
          !matchFenceOpen(lines[i]) &&
          !matchHeading(lines[i]) &&
          !/^ {0,3}>/.test(lines[i])
        ) {
          rows.push(splitTableCells(lines[i]));
          i++;
        }
        blocks.push(
          <div key={ctx.nextKey()} className="markdown-table-wrap">
            <table className="markdown-table">
              <thead>
                <tr>
                  {headerCells.map((cell, cIdx) => (
                    <th
                      key={cIdx}
                      style={
                        alignments[cIdx]
                          ? { textAlign: alignments[cIdx] }
                          : undefined
                      }
                    >
                      {renderInline(cell, ctx)}
                    </th>
                  ))}
                </tr>
              </thead>
              {rows.length > 0 && (
                <tbody>
                  {rows.map((row, rIdx) => (
                    <tr key={rIdx}>
                      {headerCells.map((_, cIdx) => (
                        <td
                          key={cIdx}
                          style={
                            alignments[cIdx]
                              ? { textAlign: alignments[cIdx] }
                              : undefined
                          }
                        >
                          {renderInline(row[cIdx] ?? "", ctx)}
                        </td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              )}
            </table>
          </div>,
        );
        continue;
      }
    }

    // 7. Lists (ordered, unordered, task lists)
    if (matchListItem(line)) {
      const listResult = parseListBlock(lines, i, ctx, depth);
      blocks.push(listResult.node);
      i = listResult.nextIndex;
      continue;
    }

    // 8. Paragraph
    const paraLines: string[] = [line];
    i++;
    while (i < lines.length && !isBlockBoundary(lines, i)) {
      paraLines.push(lines[i]);
      i++;
    }
    blocks.push(
      <p key={ctx.nextKey()} className="markdown-paragraph">
        {renderInline(paraLines.join("\n"), ctx)}
      </p>,
    );
  }

  return blocks;
}

function renderMarkdownNodes(
  body: string,
  mentions: Mention[],
  userId: string,
): ReactNode[] {
  const mentionPrefix = createMentionSentinelPrefix(body);
  const { annotated, validMentions } = injectMentions(
    body,
    mentions,
    mentionPrefix,
  );
  const normalized = annotated.replace(/\r\n?/g, "\n");
  let keyCounter = 0;
  const ctx: ParseContext = {
    userId,
    mentions: validMentions,
    mentionPrefix,
    nextKey: () => `md-${++keyCounter}`,
  };
  try {
    return parseBlocks(normalized, ctx, 0);
  } catch {
    return renderPlainWithMentions(normalized, ctx);
  }
}

const noMentions: Mention[] = [];

// Parsing is a pure function of (body, mentions, userId); memoizing it keeps
// unrelated timeline re-renders from re-running the block parser.
export const MessageBody = memo(function MessageBody({
  body,
  mentions = noMentions,
  userId,
}: {
  body: string;
  mentions?: Mention[];
  userId: string;
}) {
  const nodes = useMemo(
    () => (body ? renderMarkdownNodes(body, mentions, userId) : null),
    [body, mentions, userId],
  );
  if (!body) return null;
  return <>{nodes}</>;
});
