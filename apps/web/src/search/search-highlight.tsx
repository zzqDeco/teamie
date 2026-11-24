import type { Message } from "@teamie/api-client";
import { plainPreview } from "../chat/plain-preview";
import type { Snippet } from "../chat/plain-preview";

export const MAX_SEARCH_TERMS = 8;
export const MAX_SEARCH_QUERY_CHARS = 200;

export type ParsedSearchQuery = {
  terms: string[];
  tooLong: boolean;
  tooManyTerms: boolean;
};

/**
 * Mirrors backend `domain.ParseSearchTerms`: splits by whitespace while keeping
 * balanced double-quoted phrases together, deduplicates case-insensitively,
 * and enforces the 200-code-point / 8-term ceilings.
 */
export function parseSearchQuery(raw: string): ParsedSearchQuery {
  const trimmed = raw.trim();
  if (!trimmed) {
    return { terms: [], tooLong: false, tooManyTerms: false };
  }
  const chars = Array.from(trimmed);
  if (chars.length > MAX_SEARCH_QUERY_CHARS) {
    return { terms: [], tooLong: true, tooManyTerms: false };
  }
  const tokens: string[] = [];
  let i = 0;
  while (i < chars.length) {
    while (i < chars.length && /\s/u.test(chars[i])) i += 1;
    if (i >= chars.length) break;
    if (chars[i] === '"') {
      let closeIdx = -1;
      for (let j = i + 1; j < chars.length; j += 1) {
        if (chars[j] === '"') {
          closeIdx = j;
          break;
        }
      }
      if (closeIdx >= i + 1) {
        const phrase = chars
          .slice(i + 1, closeIdx)
          .join("")
          .trim();
        if (phrase) tokens.push(phrase);
        i = closeIdx + 1;
        continue;
      }
    }
    const start = i;
    while (i < chars.length && !/\s/u.test(chars[i])) i += 1;
    const token = chars.slice(start, i).join("");
    if (token) tokens.push(token);
  }
  const seen = new Set<string>();
  const terms: string[] = [];
  for (const token of tokens) {
    const key = token.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    terms.push(token);
    if (terms.length > MAX_SEARCH_TERMS) {
      return {
        terms: terms.slice(0, MAX_SEARCH_TERMS),
        tooLong: false,
        tooManyTerms: true,
      };
    }
  }
  return { terms, tooLong: false, tooManyTerms: false };
}

export function parseSearchTerms(raw: string): string[] {
  return parseSearchQuery(raw).terms;
}

export function allMessageAttachments(message: Message) {
  return [
    ...(message.attachments || []),
    ...(message.forwarded?.attachments || []),
  ];
}

export function matchesSearchTerms(
  message: Message,
  terms: string[],
  includeFilenames = false,
): boolean {
  if (!terms.length) return true;
  const bodyLower = message.body.toLowerCase();
  const forwardedLower = (message.forwarded?.body || "").toLowerCase();
  const filenamesLower = includeFilenames
    ? allMessageAttachments(message).map((att) => att.filename.toLowerCase())
    : [];
  return terms.every((term) => {
    const needle = term.toLowerCase();
    if (bodyLower.includes(needle) || forwardedLower.includes(needle)) {
      return true;
    }
    return includeFilenames && filenamesLower.some((name) => name.includes(needle));
  });
}

export function buildMultiTermSnippet(
  rawText: string,
  terms: string[],
  size = 160,
  usePlainPreview = true,
): Snippet {
  const text = usePlainPreview ? plainPreview(rawText) || rawText : rawText;
  const chars = Array.from(text);
  if (chars.length <= size) {
    return { text, truncatedStart: false, truncatedEnd: false };
  }
  const lower = text.toLowerCase();
  let earliestIndex = -1;
  let earliestLen = 0;
  for (const term of terms) {
    const needle = term.toLowerCase();
    if (!needle) continue;
    const idx = lower.indexOf(needle);
    if (idx >= 0 && (earliestIndex < 0 || idx < earliestIndex)) {
      earliestIndex = idx;
      earliestLen = Array.from(needle).length;
    }
  }
  const matchStart =
    earliestIndex < 0 ? 0 : Array.from(text.slice(0, earliestIndex)).length;
  let start = Math.max(0, matchStart - Math.floor((size - earliestLen) / 3));
  start = Math.min(start, chars.length - size);
  const end = start + size;
  return {
    text: chars.slice(start, end).join(""),
    truncatedStart: start > 0,
    truncatedEnd: end < chars.length,
  };
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/**
 * Safely renders escaped plain text with `<mark>` around every matched search
 * term (longest-first alternation). Optionally windows long text around the
 * first matching term.
 */
export function MultiTermHighlight({
  text,
  query,
  terms: explicitTerms,
  maxLength,
  plain = false,
}: {
  text: string;
  query?: string;
  terms?: string[];
  maxLength?: number;
  plain?: boolean;
}) {
  const terms = explicitTerms ?? (query ? parseSearchTerms(query) : []);
  const snippet = maxLength
    ? buildMultiTermSnippet(text, terms, maxLength, plain)
    : {
        text: plain ? plainPreview(text) || text : text,
        truncatedStart: false,
        truncatedEnd: false,
      };
  const activeTerms = [...terms]
    .filter(Boolean)
    .sort((a, b) => b.length - a.length);
  if (!activeTerms.length) {
    return (
      <>
        {snippet.truncatedStart ? "…" : ""}
        {snippet.text}
        {snippet.truncatedEnd ? "…" : ""}
      </>
    );
  }
  const matcher = new RegExp(
    `(${activeTerms.map(escapeRegExp).join("|")})`,
    "giu",
  );
  const parts = snippet.text.split(matcher);
  return (
    <>
      {snippet.truncatedStart ? "…" : ""}
      {parts.map((part, index) =>
        index % 2 === 1 ? <mark key={index}>{part}</mark> : part,
      )}
      {snippet.truncatedEnd ? "…" : ""}
    </>
  );
}
