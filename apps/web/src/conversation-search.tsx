import { useEffect, useRef, useState } from "react";
import { CornerUpLeft, LoaderCircle, Search } from "lucide-react";
import { api } from "@teamie/api-client";
import type { MessagePage } from "@teamie/api-client";
import { MessageAttachments } from "./attachments";
import { errorText, useChatState } from "./chat";
import { chatStore, reconcileMessages } from "./store";
import { AgentBadge } from "./agent-ui";
import { Button, date, IconButton, Modal, Notice, time } from "./ui";
import { ICON } from "./icons";
import "./conversation-search.css";

type SearchResult = MessagePage & {
  key: string;
  error: string;
  loadingMore: boolean;
};

function Highlight({ text, query }: { text: string; query: string }) {
  if (!query) return text;
  const literal = query.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return text
    .split(new RegExp(`(${literal})`, "giu"))
    .map((part, i) => (i % 2 ? <mark key={i}>{part}</mark> : part));
}

export function ConversationSearch({
  conversationId,
  onClose,
  onLocate,
}: {
  conversationId: string;
  onClose: () => void;
  onLocate: (messageId: string) => void;
}) {
  const [query, setQuery] = useState("");
  const [mode, setMode] = useState<"messages" | "files">("messages");
  const [revision, setRevision] = useState(0);
  const [result, setResult] = useState<SearchResult | null>(null);
  const input = useRef<HTMLInputElement>(null);
  const requests = useRef(new Set<AbortController>());
  const sequence = useRef(0);
  const snapshot = useChatState();
  const generation = chatStore.generation;
  const term = query.trim();
  const valid = Array.from(term).length <= 200;
  const enabled = valid && (mode === "files" || !!term);
  const key = JSON.stringify([
    conversationId,
    generation,
    mode,
    term,
    revision,
  ]);
  const page = result?.key === key ? result : null;
  const pending = enabled && !page;
  const options = {
    q: term,
    ...(mode === "files" ? { content: "files" as const } : {}),
  };

  useEffect(() => {
    input.current?.focus();
  }, [mode]);
  useEffect(() => {
    const operation = ++sequence.current;
    const activeRequests = requests.current;
    const controller = new AbortController();
    const current = () =>
      !controller.signal.aborted &&
      generation === chatStore.generation &&
      operation === sequence.current;
    const timer = enabled
      ? setTimeout(
          () => {
            activeRequests.add(controller);
            void api
              .searchMessages(
                conversationId,
                {
                  q: term,
                  ...(mode === "files" ? { content: "files" as const } : {}),
                },
                controller.signal,
              )
              .then((response) => {
                if (current())
                  setResult({
                    ...response,
                    key,
                    error: "",
                    loadingMore: false,
                  });
              })
              .catch((error) => {
                if (current())
                  setResult({
                    key,
                    messages: [],
                    hasMore: false,
                    loadingMore: false,
                    error: errorText(error),
                  });
              })
              .finally(() => activeRequests.delete(controller));
          },
          term ? 250 : 0,
        )
      : undefined;
    return () => {
      clearTimeout(timer);
      controller.abort();
      activeRequests.forEach((request) => request.abort());
      activeRequests.clear();
    };
  }, [conversationId, generation, mode, term, key, enabled]);

  async function more() {
    if (!page?.hasMore || page.loadingMore || !page.messages.length) return;
    const operation = sequence.current;
    const controller = new AbortController();
    const current = () =>
      !controller.signal.aborted &&
      generation === chatStore.generation &&
      operation === sequence.current;
    requests.current.add(controller);
    setResult({ ...page, error: "", loadingMore: true });
    try {
      const response = await api.searchMessages(
        conversationId,
        { ...options, beforeSeq: page.messages[0].seq },
        controller.signal,
      );
      if (current())
        setResult((old) =>
          old?.key === key
            ? {
                ...old,
                messages: reconcileMessages(old.messages, response.messages),
                hasMore: response.hasMore,
                loadingMore: false,
              }
            : old,
        );
    } catch (error) {
      if (current())
        setResult((old) =>
          old?.key === key
            ? { ...old, error: errorText(error), loadingMore: false }
            : old,
        );
    } finally {
      requests.current.delete(controller);
    }
  }

  // Search pages stay local. Only matching cached versions can update them, so
  // locating/searching history cannot pollute the normal timeline or read cursor.
  const messages = page
    ? reconcileMessages(
        page.messages,
        (snapshot.messages[conversationId] || []).filter((m) =>
          page.messages.some((p) => p.id === m.id),
        ),
      )
        .filter(
          (message) =>
            !message.retractedAt &&
            (mode === "files"
              ? message.attachments?.some((file) =>
                  file.filename.toLowerCase().includes(term.toLowerCase()),
                )
              : message.body.toLowerCase().includes(term.toLowerCase())),
        )
        .reverse()
    : [];
  return (
    <Modal title="搜索会话" onClose={onClose} className="conversation-search">
      <div className="search-modes" role="group" aria-label="搜索范围">
        <button
          type="button"
          aria-pressed={mode === "messages"}
          onClick={() => setMode("messages")}
        >
          消息
        </button>
        <button
          type="button"
          aria-pressed={mode === "files"}
          onClick={() => setMode("files")}
        >
          文件
        </button>
      </div>
      <form
        className="search-form"
        role="search"
        onSubmit={(event) => {
          event.preventDefault();
          if (enabled) setRevision((old) => old + 1);
        }}
      >
        <input
          ref={input}
          type="search"
          aria-label={mode === "files" ? "搜索文件名" : "搜索消息"}
          placeholder={mode === "files" ? "搜索文件名" : "搜索消息"}
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
        <IconButton type="submit" label="搜索" disabled={!enabled}>
          {pending ? (
            <LoaderCircle size={ICON.lg} className="spin" />
          ) : (
            <Search size={ICON.lg} />
          )}
        </IconButton>
      </form>
      {!valid && <Notice>关键词最多 200 个字符</Notice>}
      <div className="search-results" aria-busy={pending}>
        {pending && (
          <p className="search-empty" role="status">
            正在查找…
          </p>
        )}
        {!term && mode === "messages" && (
          <p className="search-empty">输入关键词搜索</p>
        )}
        {page?.error && <Notice>{page.error}</Notice>}
        {page && !page.error && !messages.length && (
          <p className="search-empty" role="status">
            {mode === "files" ? "没有找到文件" : "没有找到消息"}
          </p>
        )}
        <ol className="search-result-list" aria-label="搜索结果">
          {messages.map((message) => (
            <li key={message.id}>
              <div className="search-result-meta">
                <b>{message.senderName}</b>
                {message.senderKind === "agent" && <AgentBadge />}
                <time dateTime={message.createdAt}>
                  {date(message.createdAt)} {time(message.createdAt)}
                </time>
              </div>
              {mode === "messages" ? (
                <button
                  className="search-message-result"
                  onClick={() => onLocate(message.id)}
                  aria-label={`定位消息：${message.body}`}
                >
                  <Highlight text={message.body} query={term} />
                </button>
              ) : (
                <MessageAttachments
                  conversationId={conversationId}
                  attachments={(message.attachments || []).filter((file) =>
                    file.filename.toLowerCase().includes(term.toLowerCase()),
                  )}
                />
              )}
              {mode === "files" && (
                <button
                  className="search-source"
                  onClick={() => onLocate(message.id)}
                >
                  <CornerUpLeft size={ICON.xs} /> 查看消息
                </button>
              )}
            </li>
          ))}
        </ol>
        {page?.hasMore && (
          <Button
            className="secondary search-more"
            busy={page.loadingMore}
            onClick={() => void more()}
          >
            加载更多
          </Button>
        )}
        {page?.error && !page.hasMore && (
          <Button
            className="secondary search-more"
            onClick={() => setRevision((old) => old + 1)}
          >
            重试
          </Button>
        )}
      </div>
    </Modal>
  );
}
