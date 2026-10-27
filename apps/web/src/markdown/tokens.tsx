import type { ReactNode } from "react";
import katex from "katex";
import type { Mention } from "@teamie/api-client";

export type ValidMention = {
  index: number;
  userId: string;
  text: string;
};

export type ParseContext = {
  userId: string;
  mentions: ValidMention[];
  mentionPrefix: string;
  nextKey: () => string;
  /** Set while rendering a link label so bare URLs never nest anchors. */
  inLink?: boolean;
};

export const ESCAPABLE_PUNCTUATION = new Set(
  '!"#$%&\'()*+,-./:;<=>?@[\\]^_`{|}~'.split(""),
);
export const KATEX_DISPLAY_CLASS = "katex-display";
export const MAX_BLOCK_DEPTH = 16;
export const MAX_INLINE_DEPTH = 16;

// Bare URLs stop at whitespace, angle brackets, mention sentinels and CJK
// text/punctuation, which commonly abut links in Chinese messages.
const BARE_URL_PATTERN =
  /^https?:\/\/[^\s<>\uE000\uE001\u3000-\u303f\u3400-\u9fff\uff00-\uffef]+/i;
const BARE_URL_TRAILING_PUNCTUATION = new Set(
  ".,;:!?*_~'\"".split(""),
);

/**
 * GFM-style extended autolink: returns the bare http(s) URL starting at `pos`
 * without trailing punctuation or unbalanced closing parentheses.
 */
export function matchBareUrl(text: string, pos: number): string | null {
  const ch = text[pos];
  if (ch !== "h" && ch !== "H") return null;
  // Like GFM, a bare URL must start a line or follow whitespace or `*_~(`;
  // CJK text and full-width punctuation also count as boundaries.
  const prev = text[pos - 1];
  if (
    prev !== undefined &&
    !isWhitespaceChar(prev) &&
    !"*_~(".includes(prev) &&
    prev.charCodeAt(0) < 0x2e80
  )
    return null;
  const match = BARE_URL_PATTERN.exec(text.slice(pos));
  if (!match) return null;
  let url = match[0];
  for (;;) {
    const last = url[url.length - 1];
    if (BARE_URL_TRAILING_PUNCTUATION.has(last)) {
      url = url.slice(0, -1);
      continue;
    }
    if (last === ")") {
      const opens = url.split("(").length - 1;
      const closes = url.split(")").length - 1;
      if (closes > opens) {
        url = url.slice(0, -1);
        continue;
      }
    }
    break;
  }
  return /^https?:\/\/./i.test(url) ? url : null;
}

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

export function createMentionSentinelPrefix(body: string): string {
  let candidate = "\uE000M";
  let nonce = 0;
  while (body.includes(candidate)) {
    nonce++;
    candidate = `\uE000M${nonce}_`;
  }
  return candidate;
}

export function injectMentions(
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

export function restoreMentionsText(text: string, ctx: ParseContext): string {
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

// Rendered KaTeX HTML is a pure function of (formula, displayMode); a small
// LRU avoids re-typesetting repeated formulas across rows and remounts.
const KATEX_CACHE_LIMIT = 500;
const katexCache = new Map<string, string>();
function renderKatex(formula: string, displayMode: boolean): string {
  const key = `${displayMode ? "display" : "inline"}\u0000${formula}`;
  const cached = katexCache.get(key);
  if (cached !== undefined) {
    katexCache.delete(key);
    katexCache.set(key, cached);
    return cached;
  }
  const html = katex.renderToString(formula, {
    displayMode,
    throwOnError: false,
    trust: false,
    strict: "ignore",
  });
  katexCache.set(key, html);
  if (katexCache.size > KATEX_CACHE_LIMIT) {
    const oldest = katexCache.keys().next();
    if (!oldest.done) katexCache.delete(oldest.value);
  }
  return html;
}

export function renderMathNode(
  rawFormula: string,
  displayMode: boolean,
  ctx: ParseContext,
): ReactNode {
  const formula = restoreMentionsText(rawFormula, ctx);
  const key = ctx.nextKey();
  try {
    const html = renderKatex(formula.trim(), displayMode);
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

export function isWhitespaceChar(ch: string | undefined): boolean {
  return ch === " " || ch === "\t" || ch === "\n" || ch === "\r";
}

export function isDigitChar(ch: string | undefined): boolean {
  return Boolean(ch && ch >= "0" && ch <= "9");
}

export function isAsciiWordChar(ch: string | undefined): boolean {
  if (!ch) return false;
  return (
    (ch >= "a" && ch <= "z") ||
    (ch >= "A" && ch <= "Z") ||
    (ch >= "0" && ch <= "9")
  );
}

export function matchInlineCode(
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

export function matchInlineMath(
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

export function findClosingDelimiter(
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

export function matchMarkdownLink(
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
