/**
 * Plain-text projection of a Markdown message body for one-line surfaces
 * (reply quotes, retract confirmation, search results). It never renders
 * HTML; it only removes Markdown syntax so users see words, not `**`/`[x](y)`.
 */
export function plainPreview(body: string): string {
  if (!body) return "";
  // Protect backslash-escaped punctuation from the syntax passes below.
  const escaped: string[] = [];
  let text = body
    .replace(/\r\n?/g, "\n")
    .replace(/\\([\\`*_{}[\]()#+\-.!|~>$])/g, (_, char: string) => {
      escaped.push(char);
      return `\uE000${escaped.length - 1}\uE001`;
    });
  // Fences: keep the code, drop the ``` / ~~~ lines.
  text = text.replace(/^ {0,3}(`{3,}|~{3,})[^\n]*$/gm, "");
  // Display math delimiters.
  text = text.replace(/\$\$|\\\[|\\\]/g, " ");
  // Block prefixes: headings, quotes, list markers, task boxes.
  text = text.replace(/^ {0,3}#{1,6}[ \t]+/gm, "");
  text = text.replace(/^(?: {0,3}>[ \t]?)+/gm, "");
  text = text.replace(/^[ \t]*(?:[-*+]|\d+\.)[ \t]+(?:\[[ xX]\][ \t]+)?/gm, "");
  // Horizontal rules and table delimiter rows.
  text = text.replace(/^ {0,3}(?:(?:-\s*){3,}|(?:\*\s*){3,}|(?:_\s*){3,})$/gm, "");
  text = text.replace(/^\s*\|?\s*:?-{1,}:?\s*(?:\|\s*:?-{1,}:?\s*)*\|?\s*$/gm, "");
  // Links/images: keep the label.
  text = text.replace(/!?\[([^\]\n]*)\]\((?:[^()\s]|\([^()\s]*\))*(?:\s+"[^"\n]*")?\)/g, "$1");
  // Inline code and math: keep their content.
  text = text.replace(/(`+)([^`\n]+?)\1/g, "$2");
  text = text.replace(/\\\(|\\\)/g, "");
  text = text.replace(/(^|[^\\$])\$([^$\n]+)\$/g, "$1$2");
  // Emphasis / strikethrough markers.
  text = text.replace(/(\*\*|__|~~)(?=\S)([\s\S]*?\S)\1/g, "$2");
  text = text.replace(/(^|[^\w*])([*_])(?=\S)([^*_\n]*?\S)\2(?!\w)/g, "$1$3");
  // Table pipes, then restore escaped punctuation literally.
  text = text.replace(/\s*\|\s*/g, " ");
  text = text.replace(/\uE000(\d+)\uE001/g, (_, index: string) => escaped[Number(index)] ?? "");
  return text.replace(/\s+/g, " ").trim();
}

/** Plain one-line preview, or 「N 个文件」 for attachment-only messages. */
export function messagePreviewText(
  message: { body: string; attachments?: unknown[] } | undefined,
  fallback = "",
): string {
  if (!message) return fallback;
  return (
    plainPreview(message.body) ||
    (message.attachments?.length ? `${message.attachments.length} 个文件` : fallback)
  );
}

export type Snippet = { text: string; truncatedStart: boolean; truncatedEnd: boolean };

/**
 * A window of about `size` characters (code points) centred on the first
 * case-insensitive literal match of `query`, with ellipses where trimmed.
 */
export function searchSnippet(text: string, query: string, size = 120): Snippet {
  const chars = Array.from(text);
  if (chars.length <= size) {
    return { text, truncatedStart: false, truncatedEnd: false };
  }
  const needle = query.trim().toLocaleLowerCase();
  const unitIndex = needle ? text.toLocaleLowerCase().indexOf(needle) : -1;
  // Convert the UTF-16 match index into a code-point index.
  const matchStart = unitIndex < 0 ? 0 : Array.from(text.slice(0, unitIndex)).length;
  const matchLength = needle ? Array.from(needle).length : 0;
  let start = Math.max(0, matchStart - Math.floor((size - matchLength) / 2));
  start = Math.min(start, chars.length - size);
  const end = start + size;
  return {
    text: chars.slice(start, end).join(""),
    truncatedStart: start > 0,
    truncatedEnd: end < chars.length,
  };
}
