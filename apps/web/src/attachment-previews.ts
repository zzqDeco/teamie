import { api, ApiError } from "@teamie/api-client";

type Preview = { url?: string; retryable?: boolean };
type Listener = (preview: Preview) => void;
type Entry = {
  key: string;
  conversationId: string;
  attachmentId: string;
  kind?: "content" | "thumbnail";
  mediaType?: string;
  session: string;
  controller: AbortController;
  listeners: Set<Listener>;
  preview: Preview;
  bytes?: number;
};
const entries = new Map<string, Entry>();
const queue: Entry[] = [];
let running = false;

/**
 * Loaded previews whose last subscriber left (row unmounted, conversation
 * switched, far off-screen) stay cached in a small LRU, so scrolling or
 * switching back shows them instantly without a refetch. Map order is the
 * recency order. Failed loads are never cached.
 */
const RETAIN_LIMITS = {
  thumbnail: { count: 96, bytes: 24 * 1024 * 1024 },
  content: { count: 12, bytes: 32 * 1024 * 1024 },
} as const;
const retained = new Map<string, Entry>();
function retainedUsage(kind: "content" | "thumbnail") {
  let count = 0;
  let bytes = 0;
  for (const entry of retained.values()) {
    if ((entry.kind ?? "content") !== kind) continue;
    count++;
    bytes += entry.bytes ?? 0;
  }
  return { count, bytes };
}
function retain(entry: Entry) {
  const kind = entry.kind ?? "content";
  retained.delete(entry.key);
  retained.set(entry.key, entry);
  const limit = RETAIN_LIMITS[kind];
  let usage = retainedUsage(kind);
  for (const candidate of [...retained.values()]) {
    if (usage.count <= limit.count && usage.bytes <= limit.bytes) break;
    if ((candidate.kind ?? "content") !== kind || candidate === entry) continue;
    retained.delete(candidate.key);
    dispose(candidate);
    usage = retainedUsage(kind);
  }
}
function release(entry: Entry) {
  // Keep successfully loaded blobs of the current session; abort the rest.
  if (entry.preview.url && entry.session === api.csrfToken && entries.get(entry.key) === entry) {
    retain(entry);
  } else {
    dispose(entry);
  }
}
function revive(entry: Entry) {
  retained.delete(entry.key);
}

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
  retained.clear();
  queue.length = 0;
}
/** Test/diagnostic hook: number of loaded previews cached without subscribers. */
export const retainedPreviewCount = () => retained.size;
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
      const raw =
        entry.kind === "thumbnail"
          ? await api.attachmentThumbnail(entry.conversationId, entry.attachmentId, signal)
          : await api.attachmentContent(entry.conversationId, entry.attachmentId, signal);
      const blob =
        raw instanceof Blob
          ? raw
          : new Blob([raw as BlobPart], { type: entry.mediaType || "application/octet-stream" });
      if (!signal.aborted && entry.session === api.csrfToken && entry.listeners.size) {
        entry.bytes = blob.size;
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
  const key = JSON.stringify(["content", api.csrfToken, conversationId, attachmentId]);
  let entry = entries.get(key);
  if (!entry) {
    entry = { key, conversationId, attachmentId, kind: "content", session: api.csrfToken, controller: new AbortController(), listeners: new Set(), preview: {} };
    entries.set(key, entry);
    queue.push(entry);
  }
  revive(entry);
  entry.listeners.add(listener);
  listener(entry.preview);
  pump();
  return () => {
    entry.listeners.delete(listener);
    if (!entry.listeners.size) release(entry);
  };
}
export function subscribeAttachmentThumbnail(
  conversationId: string,
  attachmentId: string,
  mediaType: string,
  listener: Listener,
) {
  const key = JSON.stringify(["thumbnail", api.csrfToken, conversationId, attachmentId]);
  let entry = entries.get(key);
  if (!entry) {
    entry = {
      key,
      conversationId,
      attachmentId,
      kind: "thumbnail",
      mediaType,
      session: api.csrfToken,
      controller: new AbortController(),
      listeners: new Set(),
      preview: {},
    };
    entries.set(key, entry);
    queue.push(entry);
  }
  revive(entry);
  entry.listeners.add(listener);
  listener(entry.preview);
  pump();
  return () => {
    entry.listeners.delete(listener);
    if (!entry.listeners.size) release(entry);
  };
}
