import { Fragment } from "react";
import type { ReactNode } from "react";
import katex from "katex";
import "katex/dist/katex.min.css";
import type { Mention } from "@teamie/api-client";

type ValidMention = {
  index: number;
  userId: string;
  text: string;
};

type ParseContext = {
  userId: string;
  mentions: ValidMention[];
  mentionPrefix: string;
  nextKey: () => string;
};

const ESCAPABLE_PUNCTUATION = new Set(
  '!"#$%&\'()*+,-./:;<=>?@[\\]^_`{|}~'.split(""),
);
export const KATEX_DISPLAY_CLASS = "katex-display";
const MAX_BLOCK_DEPTH = 16;
const MAX_INLINE_DEPTH = 16;

export function sanitizeLinkUrl(raw: string): string | null {
  const cleaned = raw.trim();
  if (!cleaned) return null;
  for (let i = 0; i < cleaned.length; i++) {
    const code = cleaned.charCodeAt(i);
    if (code <= 0x1f || code === 0x7f) return null;
  }
  if (!/^(?:https?:\/\/|mailto:)/i.test(cleaned)) {
    return null;
  }
  try {
    const parsed = new URL(cleaned);
    const protocol = parsed.protocol.toLowerCase();
    if (
      protocol !== "http:" &&
      protocol !== "https:" &&
      protocol !== "mailto:"
    ) {
      return null;
    }
    return cleaned;
  } catch {
    return null;
  }
}

function createMentionSentinelPrefix(body: string): string {
  let candidate = "\uE000M";
  let nonce = 0;
  while (body.includes(candidate)) {
    nonce++;
    candidate = `\uE000M${nonce}_`;
  }
  return candidate;
}

function injectMentions(
  body: string,
  mentions: Mention[],
  prefix: string,
): { annotated: string; validMentions: ValidMention[] } {
  if (!mentions.length) {
    return { annotated: body, validMentions: [] };
  }
  const validMentions: ValidMention[] = [];
  let cursor = 0;
  let annotated = "";
  for (const m of mentions) {
    if (
      !Number.isInteger(m.start) ||
      !Number.isInteger(m.end) ||
      m.start < cursor ||
      m.end <= m.start ||
      m.end > body.length ||
      body[m.start] !== "@"
    ) {
      continue;
    }
    const slice = body.slice(m.start, m.end);
    if (slice.includes("\n") || slice.includes("\r")) {
      continue;
    }
    const index = validMentions.length;
    validMentions.push({
      index,
      userId: m.userId,
      text: slice,
    });
    annotated += body.slice(cursor, m.start) + `${prefix}${index}\uE001`;
    cursor = m.end;
  }
  annotated += body.slice(cursor);
  return { annotated, validMentions };
}

function restoreMentionsText(text: string, ctx: ParseContext): string {
  if (!ctx.mentions.length || !text.includes(ctx.mentionPrefix)) {
    return text;
  }
  const escapedPrefix = ctx.mentionPrefix.replace(
    /[.*+?^${}()|[\]\\]/g,
    "\\$&",
  );
  const re = new RegExp(`${escapedPrefix}(\\d+)\\uE001`, "g");
  return text.replace(re, (_, id: string) => {
    const m = ctx.mentions[Number(id)];
    return m ? m.text : "";
  });
}

function renderMathNode(
  rawFormula: string,
  displayMode: boolean,
  ctx: ParseContext,
): ReactNode {
  const formula = restoreMentionsText(rawFormula, ctx);
  const key = ctx.nextKey();
  try {
    const html = katex.renderToString(formula.trim(), {
      displayMode,
      throwOnError: false,
      trust: false,
      strict: "ignore",
    });
    return (
      <span
        key={key}
        className={
          displayMode ? "markdown-math-display" : "markdown-math-inline"
        }
        dangerouslySetInnerHTML={{ __html: html }}
      />
    );
  } catch {
    return (
      <span key={key} className="katex-error">
        {formula}
      </span>
    );
  }
}

function isWhitespaceChar(ch: string | undefined): boolean {
  return ch === " " || ch === "\t" || ch === "\n" || ch === "\r";
}

function isDigitChar(ch: string | undefined): boolean {
  return Boolean(ch && ch >= "0" && ch <= "9");
}

function isAsciiWordChar(ch: string | undefined): boolean {
  if (!ch) return false;
  return (
    (ch >= "a" && ch <= "z") ||
    (ch >= "A" && ch <= "Z") ||
    (ch >= "0" && ch <= "9")
  );
}

function matchInlineCode(
  text: string,
  pos: number,
): { content: string; end: number } | null {
  if (text[pos] !== "`") return null;
  let tickEnd = pos;
  while (tickEnd < text.length && text[tickEnd] === "`") {
    tickEnd++;
  }
  const tickLen = tickEnd - pos;
  const ticks = text.slice(pos, tickEnd);
  let searchPos = tickEnd;
  while (searchPos < text.length) {
    const idx = text.indexOf(ticks, searchPos);
    if (idx === -1) return null;
    const beforeOk = idx === 0 || text[idx - 1] !== "`";
    const afterIdx = idx + tickLen;
    const afterOk = afterIdx >= text.length || text[afterIdx] !== "`";
    if (beforeOk && afterOk) {
      let content = text.slice(tickEnd, idx);
      if (
        content.length >= 2 &&
        content.startsWith(" ") &&
        content.endsWith(" ") &&
        content.trim().length > 0
      ) {
        content = content.slice(1, -1);
      }
      return { content, end: afterIdx };
    }
    searchPos = idx + 1;
  }
  return null;
}

function matchInlineMath(
  text: string,
  pos: number,
): { formula: string; displayMode: boolean; end: number } | null {
  // 1. Display math $$...$$
  if (text.startsWith("$$", pos)) {
    let search = pos + 2;
    while (search < text.length) {
      const closeIdx = text.indexOf("$$", search);
      if (closeIdx === -1) break;
      if (text[closeIdx - 1] !== "\\") {
        const formula = text.slice(pos + 2, closeIdx);
        if (formula.trim().length > 0) {
          return { formula, displayMode: true, end: closeIdx + 2 };
        }
        break;
      }
      search = closeIdx + 1;
    }
    return null;
  }

  // 2. Display math \[...\]
  if (text.startsWith("\\[", pos)) {
    const closeIdx = text.indexOf("\\]", pos + 2);
    if (closeIdx !== -1) {
      const formula = text.slice(pos + 2, closeIdx);
      if (formula.trim().length > 0) {
        return { formula, displayMode: true, end: closeIdx + 2 };
      }
    }
    return null;
  }

  // 3. Inline math \(...\)
  if (text.startsWith("\\(", pos)) {
    const closeIdx = text.indexOf("\\)", pos + 2);
    if (closeIdx !== -1) {
      const formula = text.slice(pos + 2, closeIdx);
      if (formula.trim().length > 0 && !formula.includes("\n")) {
        return { formula, displayMode: false, end: closeIdx + 2 };
      }
    }
    return null;
  }

  // 4. Inline math $...$ (with strict currency avoidance)
  if (text[pos] === "$" && text[pos + 1] !== "$") {
    if (pos > 0 && (text[pos - 1] === "$" || isDigitChar(text[pos - 1]))) {
      return null;
    }
    const firstInner = text[pos + 1];
    if (!firstInner || isWhitespaceChar(firstInner)) {
      return null;
    }
    let j = pos + 1;
    while (j < text.length) {
      const ch = text[j];
      if (ch === "\n" || ch === "\r") {
        return null;
      }
      if (ch === "\\") {
        j += 2;
        continue;
      }
      if (ch === "$") {
        if (text[j + 1] === "$") {
          return null;
        }
        const prevChar = text[j - 1];
        const nextChar = text[j + 1];
        if (isWhitespaceChar(prevChar) || isDigitChar(nextChar)) {
          return null;
        }
        const formula = text.slice(pos + 1, j);
        if (formula.trim().length === 0) {
          return null;
        }
        return { formula, displayMode: false, end: j + 1 };
      }
      j++;
    }
  }

  return null;
}

function findClosingDelimiter(
  text: string,
  startPos: number,
  delimiter: string,
): number {
  const delimChar = delimiter[0];
  const delimLen = delimiter.length;
  let i = startPos;
  while (i < text.length) {
    if (text[i] === "\\") {
      i += 2;
      continue;
    }
    if (text[i] === "`") {
      const code = matchInlineCode(text, i);
      if (code) {
        i = code.end;
        continue;
      }
    }
    if (text[i] === "$" || (text[i] === "\\" && (text[i + 1] === "(" || text[i + 1] === "["))) {
      const math = matchInlineMath(text, i);
      if (math) {
        i = math.end;
        continue;
      }
    }
    if (text.startsWith(delimiter, i)) {
      // Ensure exact run match for single-char * or _ so ** inside *italic* is skipped properly
      if (delimLen === 1 && (delimChar === "*" || delimChar === "_")) {
        if (text[i + 1] === delimChar) {
          // Skip multi-char run when searching for single-char delimiter
          let runEnd = i + 1;
          while (runEnd < text.length && text[runEnd] === delimChar) {
            runEnd++;
          }
          i = runEnd;
          continue;
        }
      }
      if (!isWhitespaceChar(text[i - 1])) {
        if (delimChar === "_" && isAsciiWordChar(text[i + delimLen])) {
          i += delimLen;
          continue;
        }
        return i;
      }
    }
    i++;
  }
  return -1;
}

function matchMarkdownLink(
  text: string,
  pos: number,
): { label: string; url: string; end: number } | null {
  if (text[pos] !== "[") return null;
  let depth = 1;
  let i = pos + 1;
  while (i < text.length && depth > 0) {
    if (text[i] === "\\") {
      i += 2;
      continue;
    }
    if (text[i] === "`") {
      const code = matchInlineCode(text, i);
      if (code) {
        i = code.end;
        continue;
      }
    }
    if (text[i] === "[") depth++;
    else if (text[i] === "]") depth--;
    i++;
  }
  if (depth !== 0) return null;
  const closeBracket = i - 1;
  if (text[i] !== "(") return null;

  const label = text.slice(pos + 1, closeBracket);
  let j = i + 1;
  while (j < text.length && (text[j] === " " || text[j] === "\t")) j++;

  let url = "";
  if (text[j] === "<") {
    const closeAngle = text.indexOf(">", j + 1);
    if (closeAngle === -1 || text.slice(j + 1, closeAngle).includes("\n")) {
      return null;
    }
    url = text.slice(j + 1, closeAngle);
    j = closeAngle + 1;
  } else {
    let parenDepth = 0;
    const urlStart = j;
    while (j < text.length) {
      const ch = text[j];
      if (ch === "\n" || ch === "\r") return null;
      if (ch === "\\") {
        j += 2;
        continue;
      }
      if (ch === "(") {
        parenDepth++;
      } else if (ch === ")") {
        if (parenDepth === 0) break;
        parenDepth--;
      } else if (ch === " " || ch === "\t") {
        break;
      }
      j++;
    }
    url = text.slice(urlStart, j);
  }

  // Optional link title
  while (j < text.length && (text[j] === " " || text[j] === "\t")) j++;
  if (j < text.length && (text[j] === '"' || text[j] === "'")) {
    const quote = text[j];
    j++;
    while (j < text.length && text[j] !== quote) {
      if (text[j] === "\n") return null;
      if (text[j] === "\\") j += 2;
      else j++;
    }
    if (text[j] !== quote) return null;
    j++;
    while (j < text.length && (text[j] === " " || text[j] === "\t")) j++;
  }

  if (text[j] !== ")") return null;
  return { label, url, end: j + 1 };
}

function renderPlainWithMentions(
  text: string,
  ctx: ParseContext,
): ReactNode[] {
  if (!ctx.mentions.length || !text.includes(ctx.mentionPrefix)) {
    return text ? [text] : [];
  }
  const nodes: ReactNode[] = [];
  let pos = 0;
  let buffer = "";
  while (pos < text.length) {
    if (text.startsWith(ctx.mentionPrefix, pos)) {
      const afterPrefix = pos + ctx.mentionPrefix.length;
      const endSentinel = text.indexOf("\uE001", afterPrefix);
      if (endSentinel !== -1) {
        const idStr = text.slice(afterPrefix, endSentinel);
        if (/^\d+$/.test(idStr)) {
          const mention = ctx.mentions[Number(idStr)];
          if (mention) {
            if (buffer) {
              nodes.push(buffer);
              buffer = "";
            }
            nodes.push(
              <span
                key={ctx.nextKey()}
                className={`message-mention mention-pill${mention.userId === ctx.userId ? " mention-self" : ""}`}
                data-user-id={mention.userId}
              >
                {mention.text}
              </span>,
            );
            pos = endSentinel + 1;
            continue;
          }
        }
      }
    }
    buffer += text[pos];
    pos++;
  }
  if (buffer) nodes.push(buffer);
  return nodes;
}

function renderInline(
  text: string,
  ctx: ParseContext,
  depth = 0,
): ReactNode[] {
  if (depth >= MAX_INLINE_DEPTH) {
    return renderPlainWithMentions(text, ctx);
  }
  const nodes: ReactNode[] = [];
  let buffer = "";
  let pos = 0;

  const flushText = () => {
    if (buffer) {
      nodes.push(buffer);
      buffer = "";
    }
  };

  while (pos < text.length) {
    // 1. Mention sentinel
    if (
      ctx.mentions.length > 0 &&
      text.startsWith(ctx.mentionPrefix, pos)
    ) {
      const afterPrefix = pos + ctx.mentionPrefix.length;
      const endSentinel = text.indexOf("\uE001", afterPrefix);
      if (endSentinel !== -1) {
        const idStr = text.slice(afterPrefix, endSentinel);
        if (/^\d+$/.test(idStr)) {
          const mention = ctx.mentions[Number(idStr)];
          if (mention) {
            flushText();
            nodes.push(
              <span
                key={ctx.nextKey()}
                className={`message-mention mention-pill${mention.userId === ctx.userId ? " mention-self" : ""}`}
                data-user-id={mention.userId}
              >
                {mention.text}
              </span>,
            );
            pos = endSentinel + 1;
            continue;
          }
        }
      }
    }

    // 2. Inline code
    if (text[pos] === "`") {
      const codeMatch = matchInlineCode(text, pos);
      if (codeMatch) {
        flushText();
        nodes.push(
          <code key={ctx.nextKey()} className="markdown-inline-code">
            {restoreMentionsText(codeMatch.content, ctx)}
          </code>,
        );
        pos = codeMatch.end;
        continue;
      }
      // Unmatched run of backticks stays literal
      let tickEnd = pos;
      while (tickEnd < text.length && text[tickEnd] === "`") tickEnd++;
      buffer += text.slice(pos, tickEnd);
      pos = tickEnd;
      continue;
    }

    // 3. KaTeX Math ($$...$$, \[...\], \(...\), $...$)
    if (
      text[pos] === "$" ||
      (text[pos] === "\\" && (text[pos + 1] === "[" || text[pos + 1] === "("))
    ) {
      const mathMatch = matchInlineMath(text, pos);
      if (mathMatch) {
        flushText();
        nodes.push(
          renderMathNode(mathMatch.formula, mathMatch.displayMode, ctx),
        );
        pos = mathMatch.end;
        continue;
      }
    }

    // 4. Backslash escapes
    if (text[pos] === "\\" && pos + 1 < text.length) {
      const next = text[pos + 1];
      if (ESCAPABLE_PUNCTUATION.has(next)) {
        buffer += next;
        pos += 2;
        continue;
      }
    }

    // 5. Markdown links [label](url)
    if (text[pos] === "[") {
      const linkMatch = matchMarkdownLink(text, pos);
      if (linkMatch) {
        flushText();
        const rawUrl = restoreMentionsText(linkMatch.url, ctx);
        const safeUrl = sanitizeLinkUrl(rawUrl);
        if (safeUrl) {
          nodes.push(
            <a
              key={ctx.nextKey()}
              className="markdown-link"
              href={safeUrl}
              target="_blank"
              rel="noopener noreferrer"
            >
              {renderInline(linkMatch.label, ctx, depth + 1)}
            </a>,
          );
        } else {
          nodes.push(
            <Fragment key={ctx.nextKey()}>
              {renderInline(linkMatch.label, ctx, depth + 1)}
            </Fragment>,
          );
        }
        pos = linkMatch.end;
        continue;
      }
    }

    // 6. Autolinks <https://...> or <mailto:...>
    if (text[pos] === "<") {
      const autoMatch = /^<((?:https?:\/\/|mailto:)[^\s<>]+)>/i.exec(
        text.slice(pos),
      );
      if (autoMatch) {
        const rawUrl = restoreMentionsText(autoMatch[1], ctx);
        const safeUrl = sanitizeLinkUrl(rawUrl);
        if (safeUrl) {
          flushText();
          nodes.push(
            <a
              key={ctx.nextKey()}
              className="markdown-link"
              href={safeUrl}
              target="_blank"
              rel="noopener noreferrer"
            >
              {rawUrl}
            </a>,
          );
          pos += autoMatch[0].length;
          continue;
        }
      }
    }

    // 7. Strikethrough ~~...~~
    if (
      text.startsWith("~~", pos) &&
      text[pos + 2] !== "~" &&
      !isWhitespaceChar(text[pos + 2])
    ) {
      const closeIdx = findClosingDelimiter(text, pos + 2, "~~");
      if (closeIdx !== -1 && closeIdx > pos + 2) {
        flushText();
        const inner = text.slice(pos + 2, closeIdx);
        nodes.push(
          <del key={ctx.nextKey()}>
            {renderInline(inner, ctx, depth + 1)}
          </del>,
        );
        pos = closeIdx + 2;
        continue;
      }
    }

    // 8. Bold + Italic, Bold, Italic (* and _)
    if (text[pos] === "*" || text[pos] === "_") {
      const ch = text[pos];
      const prevChar = pos > 0 ? text[pos - 1] : undefined;
      const intrawordBlocked = ch === "_" && isAsciiWordChar(prevChar);

      if (!intrawordBlocked) {
        const triple = ch + ch + ch;
        const double = ch + ch;

        if (
          text.startsWith(triple, pos) &&
          text[pos + 3] !== ch &&
          !isWhitespaceChar(text[pos + 3])
        ) {
          const closeIdx = findClosingDelimiter(text, pos + 3, triple);
          if (closeIdx !== -1 && closeIdx > pos + 3) {
            flushText();
            const inner = text.slice(pos + 3, closeIdx);
            nodes.push(
              <strong key={ctx.nextKey()}>
                <em>{renderInline(inner, ctx, depth + 1)}</em>
              </strong>,
            );
            pos = closeIdx + 3;
            continue;
          }
        }

        if (
          text.startsWith(double, pos) &&
          text[pos + 2] !== ch &&
          !isWhitespaceChar(text[pos + 2])
        ) {
          const closeIdx = findClosingDelimiter(text, pos + 2, double);
          if (closeIdx !== -1 && closeIdx > pos + 2) {
            flushText();
            const inner = text.slice(pos + 2, closeIdx);
            nodes.push(
              <strong key={ctx.nextKey()}>
                {renderInline(inner, ctx, depth + 1)}
              </strong>,
            );
            pos = closeIdx + 2;
            continue;
          }
        }

        if (text[pos + 1] !== ch && !isWhitespaceChar(text[pos + 1])) {
          const closeIdx = findClosingDelimiter(text, pos + 1, ch);
          if (closeIdx !== -1 && closeIdx > pos + 1) {
            flushText();
            const inner = text.slice(pos + 1, closeIdx);
            nodes.push(
              <em key={ctx.nextKey()}>
                {renderInline(inner, ctx, depth + 1)}
              </em>,
            );
            pos = closeIdx + 1;
            continue;
          }
        }
      }
    }

    buffer += text[pos];
    pos++;
  }

  flushText();
  return nodes;
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
        <pre key={ctx.nextKey()} className="markdown-code-block">
          <code
            className={fence.lang ? `language-${fence.lang}` : undefined}
            data-language={fence.lang || undefined}
          >
            {codeText}
          </code>
        </pre>,
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

export function MessageBody({
  body,
  mentions = [],
  userId,
}: {
  body: string;
  mentions?: Mention[];
  userId: string;
}) {
  if (!body) return null;
  return <>{renderMarkdownNodes(body, mentions, userId)}</>;
}
