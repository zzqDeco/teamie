import { Fragment } from "react";
import type { ReactNode } from "react";
import {
  ESCAPABLE_PUNCTUATION,
  findClosingDelimiter,
  isAsciiWordChar,
  isWhitespaceChar,
  MAX_INLINE_DEPTH,
  matchBareUrl,
  matchInlineCode,
  matchInlineMath,
  matchMarkdownLink,
  renderMathNode,
  restoreMentionsText,
  sanitizeLinkUrl,
} from "./tokens";
import type { ParseContext } from "./tokens";

export function renderPlainWithMentions(
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

export function renderInline(
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
          const wasInLink = ctx.inLink;
          ctx.inLink = true;
          const label = renderInline(linkMatch.label, ctx, depth + 1);
          ctx.inLink = wasInLink;
          nodes.push(
            <a
              key={ctx.nextKey()}
              className="markdown-link"
              href={safeUrl}
              title={safeUrl}
              target="_blank"
              rel="noopener noreferrer"
            >
              {label}
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
              title={safeUrl}
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

    // 6b. Bare http(s) URLs (GFM extended autolinks)
    if (!ctx.inLink && (text[pos] === "h" || text[pos] === "H")) {
      const bareUrl = matchBareUrl(text, pos);
      const safeUrl = bareUrl ? sanitizeLinkUrl(bareUrl) : null;
      if (bareUrl && safeUrl) {
        flushText();
        nodes.push(
          <a
            key={ctx.nextKey()}
            className="markdown-link"
            href={safeUrl}
            title={safeUrl}
            target="_blank"
            rel="noopener noreferrer"
          >
            {bareUrl}
          </a>,
        );
        pos += bareUrl.length;
        continue;
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
