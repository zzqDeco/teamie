import { api, ApiError } from "@teamie/api-client";

type Preview = { url?: string; retryable?: boolean };
type Listener = (preview: Preview) => void;
type Entry = {
  key: string;
  conversationId: string;
  attachmentId: string;
  session: string;
  controller: AbortController;
  listeners: Set<Listener>;
  preview: Preview;
};
const entries = new Map<string, Entry>();
const queue: Entry[] = [];
let running = false;

function publish(entry: Entry, preview: Preview) {
  entry.preview = preview;
  for (const listener of entry.listeners) listener(preview);
}
function dispose(entry: Entry) {
  entry.controller.abort();
  if (entry.preview.url) URL.revokeObjectURL(entry.preview.url);
  if (entries.get(entry.key) === entry) entries.delete(entry.key);
  publish(entry, {});
}
export function clearAttachmentPreviews() {
  for (const entry of entries.values()) dispose(entry);
  queue.length = 0;
}
function delay(ms: number, signal: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    const abort = () => {
      clearTimeout(timer);
      reject(new DOMException("Aborted", "AbortError"));
    };
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", abort);
      resolve();
    }, ms);
    signal.addEventListener("abort", abort, { once: true });
    if (signal.aborted) abort();
  });
}
async function load(entry: Entry) {
  const signal = entry.controller.signal;
  for (let attempt = 0; ; attempt++) {
    signal.throwIfAborted();
    if (entry.session !== api.csrfToken) return;
    try {
      const blob = await api.attachmentContent(entry.conversationId, entry.attachmentId, signal);
      if (!signal.aborted && entry.session === api.csrfToken && entry.listeners.size) {
        publish(entry, { url: URL.createObjectURL(blob) });
      }
      return;
    } catch (error) {
      if (signal.aborted) return;
      if (!(error instanceof ApiError) || error.status !== 429 || attempt >= 2) throw error;
      await delay(error.retryAfterMs ?? 300 * 2 ** attempt + Math.random() * 200, signal);
    }
  }
}
// One preview transfer leaves the server's second slot for explicit file actions.
function pump() {
  if (running) return;
  const entry = queue.shift();
  if (!entry) return;
  if (entry.controller.signal.aborted || !entry.listeners.size) { pump(); return; }
  running = true;
  void load(entry).catch((error: unknown) => {
    if (!entry.controller.signal.aborted) {
      const denied = error instanceof ApiError && [401, 403, 404, 409].includes(error.status);
      publish(entry, { retryable: !denied });
      // Failed requests are not cached: a manual retry gets a fresh ACL check.
      if (entries.get(entry.key) === entry) entries.delete(entry.key);
    }
  }).finally(() => { running = false; pump(); });
}
export function subscribeAttachmentPreview(conversationId: string, attachmentId: string, listener: Listener) {
  const key = JSON.stringify([api.csrfToken, conversationId, attachmentId]);
  let entry = entries.get(key);
  if (!entry) {
    entry = { key, conversationId, attachmentId, session: api.csrfToken, controller: new AbortController(), listeners: new Set(), preview: {} };
    entries.set(key, entry);
    queue.push(entry);
  }
  entry.listeners.add(listener);
  listener(entry.preview);
  pump();
  return () => {
    entry.listeners.delete(listener);
    if (!entry.listeners.size) dispose(entry);
  };
}
