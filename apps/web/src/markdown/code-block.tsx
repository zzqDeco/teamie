import { useEffect, useId, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Check, ChevronDown, ChevronUp, Copy } from "lucide-react";
import { copyToClipboard } from "../clipboard";
import { ICON } from "../icons";
import { highlightHtmlToNodes, loadHighlighter } from "./highlight";

/** Code blocks longer than this start collapsed behind 「展开 N 行」. */
export const CODE_COLLAPSE_LINES = 20;
/** Highlighting very large blocks is skipped to keep the timeline responsive. */
const MAX_HIGHLIGHT_CHARS = 20_000;

type CopyState = "idle" | "copied" | "failed";

function useHighlightedCode(codeText: string, lang: string) {
  const [result, setResult] = useState<{ key: string; nodes: ReactNode[] } | null>(null);
  const key = `${lang}\u0000${codeText}`;
  useEffect(() => {
    if (!lang || codeText.length > MAX_HIGHLIGHT_CHARS) return;
    let active = true;
    loadHighlighter()
      .then((module) => {
        const html = module.highlightToHtml(codeText, lang.toLowerCase());
        const nodes = html === null ? null : highlightHtmlToNodes(html);
        if (active && nodes) setResult({ key, nodes });
      })
      .catch(() => {
        /* Unhighlighted code stays fully readable when the chunk fails to load. */
      });
    return () => {
      active = false;
    };
  }, [codeText, lang, key]);
  return result?.key === key ? result.nodes : null;
}

export function CodeBlock({ codeText, lang }: { codeText: string; lang: string }) {
  const [copyState, setCopyState] = useState<CopyState>("idle");
  const [expanded, setExpanded] = useState(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const codeId = useId();
  useEffect(() => () => clearTimeout(timerRef.current), []);
  const highlighted = useHighlightedCode(codeText, lang);
  const lineCount = codeText.split("\n").length;
  const collapsible = lineCount > CODE_COLLAPSE_LINES;
  const collapsed = collapsible && !expanded;

  async function handleCopy() {
    let next: CopyState = "copied";
    try {
      await copyToClipboard(codeText);
    } catch {
      next = "failed";
    }
    setCopyState(next);
    clearTimeout(timerRef.current);
    timerRef.current = setTimeout(() => setCopyState("idle"), next === "failed" ? 3200 : 1800);
  }

  return (
    <div className={`markdown-code-wrap ${collapsed ? "collapsed" : ""}`}>
      <div className="markdown-code-header">
        <span className="markdown-code-lang">{lang || "code"}</span>
        <button
          type="button"
          className={`markdown-code-copy ${copyState === "idle" ? "" : copyState}`}
          onClick={() => void handleCopy()}
          aria-label={
            copyState === "copied"
              ? "已复制代码"
              : copyState === "failed"
                ? "复制失败，请手动选择代码复制"
                : "复制代码"
          }
        >
          {copyState === "copied" ? <Check size={ICON.xs} /> : <Copy size={ICON.xs} />}
          <span>
            {copyState === "copied" ? "已复制" : copyState === "failed" ? "复制失败" : "复制"}
          </span>
        </button>
      </div>
      {copyState === "failed" && (
        <p className="markdown-code-copy-error" role="alert">
          无法访问剪贴板，请手动选择代码复制。
        </p>
      )}
      <pre id={codeId} className={`markdown-code-block ${collapsed ? "collapsed" : ""}`}>
        <code
          className={lang ? `language-${lang}` : undefined}
          data-language={lang || undefined}
          data-highlighted={highlighted ? "true" : undefined}
        >
          {highlighted ?? codeText}
        </code>
      </pre>
      {collapsible && (
        <button
          type="button"
          className="markdown-code-toggle"
          aria-expanded={expanded}
          aria-controls={codeId}
          onClick={() => setExpanded((value) => !value)}
        >
          {expanded ? <ChevronUp size={ICON.xs} /> : <ChevronDown size={ICON.xs} />}
          {expanded ? "收起代码" : `展开 ${lineCount - CODE_COLLAPSE_LINES} 行`}
        </button>
      )}
    </div>
  );
}
