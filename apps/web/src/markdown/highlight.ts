import { createElement } from "react";
import type { ReactNode } from "react";

type Highlighter = typeof import("./highlight-languages");
let loader: Promise<Highlighter> | null = null;

/** Lazily loads highlight.js core + common grammars (a separate chunk). */
export function loadHighlighter(): Promise<Highlighter> {
  loader ??= import("./highlight-languages").catch((error: unknown) => {
    loader = null; // allow a later retry after a chunk load failure
    throw error;
  });
  return loader;
}

const ENTITIES: Record<string, string> = {
  "&amp;": "&",
  "&lt;": "<",
  "&gt;": ">",
  "&quot;": '"',
  "&#x27;": "'",
  "&#39;": "'",
};

/** highlight.js scopes mapped onto the app's token-coloured classes. */
const TOKEN_CLASS: Record<string, string> = {
  keyword: "code-token-keyword",
  built_in: "code-token-keyword",
  "selector-tag": "code-token-keyword",
  string: "code-token-string",
  regexp: "code-token-string",
  addition: "code-token-string",
  number: "code-token-number",
  literal: "code-token-number",
  symbol: "code-token-number",
  comment: "code-token-comment",
  quote: "code-token-comment",
  meta: "code-token-comment",
  title: "code-token-title",
  section: "code-token-title",
  type: "code-token-title",
  attr: "code-token-attr",
  attribute: "code-token-attr",
  variable: "code-token-attr",
  property: "code-token-attr",
  deletion: "code-token-deletion",
};

function tokenClass(hljsClass: string) {
  for (const part of hljsClass.split(/\s+/)) {
    const scope = part.replace(/^hljs-/, "").split(".")[0];
    if (TOKEN_CLASS[scope]) return TOKEN_CLASS[scope];
  }
  return undefined;
}

/**
 * Converts highlight.js output (only `<span class="hljs-…">`, `</span>` and
 * entity-escaped text) into React nodes, so highlighted code never goes
 * through innerHTML. Anything unexpected falls back to plain text.
 */
export function highlightHtmlToNodes(html: string): ReactNode[] | null {
  const root: ReactNode[] = [];
  const stack: Array<{ className: string; children: ReactNode[] }> = [];
  const pattern = /<span class="([\w\s-]*)">|<\/span>|([^<]+)/gy;
  let key = 0;
  let match: RegExpExecArray | null;
  let consumed = 0;
  while ((match = pattern.exec(html))) {
    consumed = pattern.lastIndex;
    const current = stack.length ? stack[stack.length - 1].children : root;
    if (match[1] !== undefined) {
      stack.push({ className: match[1], children: [] });
    } else if (match[2] !== undefined) {
      current.push(match[2].replace(/&(?:amp|lt|gt|quot|#x27|#39);/g, (e) => ENTITIES[e]));
    } else {
      const done = stack.pop();
      if (!done) return null;
      const parent = stack.length ? stack[stack.length - 1].children : root;
      parent.push(createElement("span", { key: key++, className: tokenClass(done.className) }, done.children));
    }
  }
  if (consumed !== html.length || stack.length) return null;
  return root;
}
