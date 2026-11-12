import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { CornerUpLeft, LoaderCircle, Search } from "lucide-react";
import { api } from "@teamie/api-client";
import type { Attachment, Message, MessagePage } from "@teamie/api-client";
import { MessageAttachments } from "./attachments";
import { useChatState } from "./chat";
import { errorText } from "./errors";
import { chatStore, reconcileMessages } from "./store";
import { AgentBadge } from "./agent-ui";
import { Button, IconButton, Modal, Notice } from "./ui";
import { ICON } from "./icons";
import { plainPreview, searchSnippet } from "./chat/plain-preview";
import { formatDateTime } from "./format-datetime";
import "./styles/conversation-search.css";

type SearchResult = MessagePage & {
  key: string;
  error: string;
  loadingMore: boolean;
};
type SearchMode = "messages" | "files";
type RememberedSearch = { query: string; mode: SearchMode };

// Last keyword/mode per account and conversation, in memory only. The whole
// map is dropped whenever the signed-in account changes (or signs out), so a
// keyword typed by one account is never offered to another.
const remembered = new Map<string, RememberedSearch>();
let rememberedUser = chatStore.getSnapshot().userId;
chatStore.subscribe(() => {
  const userId = chatStore.getSnapshot().userId;
  if (userId === rememberedUser) return;
  rememberedUser = userId;
  remembered.clear();
});
const memoryKey = (userId: string, conversationId: string) =>
  `${userId}\u0000${conversationId}`;

/** Plain-text window around the first match, with literal matches marked. */
function Snippet({ body, query }: { body: string; query: string }) {
  const preview = plainPreview(body);
  const queryLower = query.toLowerCase();
  const source =
    preview &&
    (!queryLower ||
      preview.toLowerCase().includes(queryLower) ||
      !body.toLowerCase().includes(queryLower))
      ? preview
      : body;
  const snippet = searchSnippet(source, query, 120);
  const literal = query.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const parts = query
    ? snippet.text.split(new RegExp(`(${literal})`, "giu"))
    : [snippet.text];
  return (
    <>
      {snippet.truncatedStart && "…"}
      {parts.map((part, i) => (i % 2 ? <mark key={i}>{part}</mark> : part))}
      {snippet.truncatedEnd && "…"}
    </>
  );
}

function matchingAttachments(
  message: Message,
  termLower: string,
): Attachment[] {
  const files = [
    ...(message.attachments || []),
    ...(message.forwarded?.attachments || []),
  ];
  return termLower
    ? files.filter((file) => file.filename.toLowerCase().includes(termLower))
    : files;
}

function matchesMessageBody(message: Message, termLower: string): boolean {
  return (
    Boolean(termLower) &&
    (message.body.toLowerCase().includes(termLower) ||
      Boolean(message.forwarded?.body.toLowerCase().includes(termLower)))
  );
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
  const snapshot = useChatState();
  // The account this dialog was opened for; keywords are only remembered
  // while that account is still the signed-in one.
  const [owner] = useState(() => chatStore.getSnapshot().userId);
  const [restored] = useState(() =>
    owner ? remembered.get(memoryKey(owner, conversationId)) : undefined,
  );
  const [query, setQuery] = useState(restored?.query ?? "");
  const [mode, setMode] = useState<SearchMode>(restored?.mode ?? "messages");
  const [revision, setRevision] = useState(0);
  const [result, setResult] = useState<SearchResult | null>(null);
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLOListElement>(null);
  const selectRestored = useRef(!!restored?.query);
  const requests = useRef(new Set<AbortController>());
  const sequence = useRef(0);
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
    if (selectRestored.current) input.current?.select();
    selectRestored.current = false;
  }, [mode]);
  useEffect(() => {
    if (!owner || chatStore.getSnapshot().userId !== owner) return;
    const entry = memoryKey(owner, conversationId);
    if (query.trim() || mode !== "messages") remembered.set(entry, { query, mode });
    else remembered.delete(entry);
  }, [owner, conversationId, query, mode]);
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

  // Arrow keys move focus between result buttons ("定位"/"查看消息"); Enter
  // activates the focused button natively. ArrowUp on the first result
  // returns to the search box.
  function resultButtons() {
    return Array.from(
      list.current?.querySelectorAll<HTMLButtonElement>("[data-search-result]") ||
        [],
    );
  }
  function onInputKey(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key !== "ArrowDown" || event.nativeEvent.isComposing) return;
    const first = resultButtons()[0];
    if (!first) return;
    event.preventDefault();
    first.focus();
  }
  function onResultKey(event: KeyboardEvent<HTMLButtonElement>) {
    const buttons = resultButtons();
    const index = buttons.indexOf(event.target as HTMLButtonElement);
    if (index < 0) return;
    const target =
      event.key === "ArrowDown"
        ? buttons[Math.min(index + 1, buttons.length - 1)]
        : event.key === "ArrowUp"
          ? index === 0
            ? input.current
            : buttons[index - 1]
          : event.key === "Home"
            ? buttons[0]
            : event.key === "End"
              ? buttons[buttons.length - 1]
              : undefined;
    if (!target) return;
    event.preventDefault();
    target.focus();
  }

  // Search pages stay local. Only matching cached versions can update them, so
  // locating/searching history cannot pollute the normal timeline or read cursor.
  const termLower = term.toLowerCase();
  const pageById = new Map((page?.messages || []).map((p) => [p.id, p]));
  const messages = page
    ? reconcileMessages(
        page.messages,
        (snapshot.messages[conversationId] || []).filter((m) =>
          pageById.has(m.id),
        ),
      )
        .map((message) => {
          if (message.retractedAt || message.forwarded) return message;
          const fromPage = pageById.get(message.id);
          return fromPage?.forwarded
            ? { ...message, forwarded: fromPage.forwarded }
            : message;
        })
        .filter(
          (message) =>
            !message.retractedAt &&
            (mode === "files"
              ? matchingAttachments(message, termLower).length > 0
              : matchesMessageBody(message, termLower)),
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
          onKeyDown={onInputKey}
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
        <ol
          ref={list}
          className="search-result-list"
          aria-label="搜索结果"
        >
          {messages.map((message) => {
            const sent = formatDateTime(message.createdAt, "datetime");
            const forwarded = message.forwarded;
            const forwardedMatches = Boolean(
              forwarded?.body &&
                forwarded.body.toLowerCase().includes(termLower),
            );
            return (
              <li key={message.id}>
                <div className="search-result-meta">
                  <b>{message.senderName}</b>
                  {message.senderKind === "agent" && <AgentBadge />}
                  <time
                    dateTime={message.createdAt}
                    title={formatDateTime(message.createdAt, "full")}
                  >
                    {sent}
                  </time>
                </div>
                {mode === "messages" ? (
                  <button
                    type="button"
                    data-search-result=""
                    className="search-message-result"
                    onClick={() => onLocate(message.id)}
                    onKeyDown={onResultKey}
                    aria-label={`${message.senderName} · ${sent}`}
                  >
                    {(message.body || !forwardedMatches) && (
                      <span className="search-comment-snippet">
                        <Snippet body={message.body} query={term} />
                      </span>
                    )}
                    {forwardedMatches && forwarded && (
                      <span className="search-forwarded-snippet">
                        <span className="search-forwarded-label">
                          转发自 {forwarded.senderName}：
                        </span>
                        <Snippet body={forwarded.body} query={term} />
                      </span>
                    )}
                  </button>
                ) : (
                  <MessageAttachments
                    conversationId={conversationId}
                    attachments={matchingAttachments(message, termLower)}
                  />
                )}
                {mode === "files" && (
                  <button
                    type="button"
                    data-search-result=""
                    className="search-source"
                    onClick={() => onLocate(message.id)}
                    onKeyDown={onResultKey}
                  >
                    <CornerUpLeft size={ICON.xs} /> 查看消息
                  </button>
                )}
              </li>
            );
          })}
        </ol>
        {page?.hasMore && (
          <Button
            variant="secondary"
            className="search-more"
            busy={page.loadingMore}
            onClick={() => void more()}
          >
            加载更多
          </Button>
        )}
        {page?.error && !page.hasMore && (
          <Button
            variant="secondary"
            className="search-more"
            onClick={() => setRevision((old) => old + 1)}
          >
            重试
          </Button>
        )}
      </div>
    </Modal>
  );
}
