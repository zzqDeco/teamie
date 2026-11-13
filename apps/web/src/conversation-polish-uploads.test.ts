// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@teamie/api-client";
import type { Attachment, Conversation } from "@teamie/api-client";
import { attachmentTransport } from "./chat/attachment-upload";
import {
  addAttachmentDraftFiles,
  readAttachmentDraft,
  removeAttachmentDraftFile,
  UPLOAD_CONCURRENCY,
} from "./attachment-drafts";
import { readDraftPresence, subscribeDraftPresence, writeComposerDraft, writeComposerText, composerDraftEpoch } from "./composer-drafts";
import { chatStore } from "./store";

type FakeXhr = {
  method: string; url: string; headers: Record<string, string>; body: unknown;
  status: number; responseText: string; responseHeaders: Record<string, string>;
  upload: { onprogress: ((event: ProgressEvent) => void) | null };
  onload: (() => void) | null; onerror: (() => void) | null; onabort: (() => void) | null;
  aborted: boolean;
};
let xhrs: FakeXhr[];
function installXhr() {
  xhrs = [];
  vi.stubGlobal("XMLHttpRequest", class {
    state: FakeXhr = {
      method: "", url: "", headers: {}, body: null, status: 0, responseText: "",
      responseHeaders: {}, upload: { onprogress: null }, onload: null, onerror: null, onabort: null, aborted: false,
    };
    withCredentials = false;
    responseType = "";
    upload = this.state.upload;
    constructor() { xhrs.push(this.state); }
    open(method: string, url: string) { this.state.method = method; this.state.url = url; }
    setRequestHeader(name: string, value: string) { this.state.headers[name] = value; }
    getResponseHeader(name: string) { return this.state.responseHeaders[name] ?? null; }
    get status() { return this.state.status; }
    get responseText() { return this.state.responseText; }
    set onload(fn: () => void) { this.state.onload = fn; }
    set onerror(fn: () => void) { this.state.onerror = fn; }
    set onabort(fn: () => void) { this.state.onabort = fn; }
    send(body: unknown) { this.state.body = body; }
    abort() { this.state.aborted = true; this.state.onabort?.(); }
  });
}
const descriptor = (id: string): Attachment => ({
  id, filename: `${id}.bin`, mediaType: "application/octet-stream", sizeBytes: 3, sha256: "x",
});
function respond(xhr: FakeXhr, status: number, body: unknown, headers: Record<string, string> = {}) {
  xhr.status = status;
  xhr.responseText = JSON.stringify(body);
  xhr.responseHeaders = headers;
  xhr.onload?.();
}
const flush = async () => { for (let i = 0; i < 10; i++) await Promise.resolve(); };

beforeEach(() => {
  api.csrfToken = "csrf-a";
  installXhr();
});
afterEach(() => {
  api.csrfToken = "";
  if (chatStore.getSnapshot().userId) chatStore.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("XHR attachment transport", () => {
  it("posts multipart with CSRF/idempotency headers and reports upload progress", async () => {
    const progress = vi.fn();
    const file = new File(["abc"], "a.bin");
    const pending = attachmentTransport.upload("conv/1", file, undefined, "key-1", progress);
    const [xhr] = xhrs;
    expect(xhr.method).toBe("POST");
    expect(xhr.url).toBe("/api/v1/conversations/conv%2F1/attachments");
    expect(xhr.headers).toMatchObject({ "X-CSRF-Token": "csrf-a", "Idempotency-Key": "key-1" });
    expect((xhr.body as FormData).get("file")).toBeInstanceOf(File);
    xhr.upload.onprogress?.({ lengthComputable: true, loaded: 1, total: 4 } as ProgressEvent);
    expect(progress).toHaveBeenLastCalledWith(0.25);
    respond(xhr, 201, { attachment: descriptor("f1") });
    await expect(pending).resolves.toEqual({ attachment: descriptor("f1") });
    expect(progress).toHaveBeenLastCalledWith(1);
  });
  it("maps errors to ApiError with Retry-After and session hooks like the api client", async () => {
    const changed = vi.fn();
    api.onSessionChanged = changed;
    const busy = attachmentTransport.upload("c", new File(["a"], "a"), undefined, undefined, undefined);
    respond(xhrs[0], 429, { error: { code: "attachment_busy", message: "忙" } }, { "Retry-After": "2" });
    await expect(busy).rejects.toMatchObject({ status: 429, code: "attachment_busy", retryAfterMs: 2000 });
    const csrf = attachmentTransport.upload("c", new File(["a"], "a"));
    respond(xhrs[1], 403, { error: { code: "csrf_failed" } });
    await expect(csrf).rejects.toBeInstanceOf(ApiError);
    expect(changed).toHaveBeenCalledOnce();
    api.onSessionChanged = undefined;
  });
  it("rejects a success that arrives after the account changed and supports abort", async () => {
    const stale = attachmentTransport.upload("c", new File(["a"], "a"));
    api.csrfToken = "csrf-b";
    respond(xhrs[0], 201, { attachment: descriptor("late") });
    await expect(stale).rejects.toMatchObject({ code: "stale_session" });
    const controller = new AbortController();
    const aborted = attachmentTransport.upload("c", new File(["a"], "a"), controller.signal);
    controller.abort();
    await expect(aborted).rejects.toMatchObject({ name: "AbortError" });
    expect(xhrs[1].aborted).toBe(true);
  });
});

describe("parallel draft uploads", () => {
  it(`runs at most ${UPLOAD_CONCURRENCY} uploads at a time in FIFO order with per-file progress`, async () => {
    chatStore.begin("uploader");
    const conversation = { id: "conv", workspaceId: "w", type: "group", memberIds: ["uploader"], lastSeq: 0, readSeq: 0, updatedAt: "" } as Conversation;
    chatStore.replaceConversations([conversation]);
    const calls: Array<{ name: string; resolve: () => void; progress?: (f: number) => void }> = [];
    vi.spyOn(attachmentTransport, "upload").mockImplementation((_c, file, _s, _k, onProgress) =>
      new Promise((resolve) => {
        calls.push({ name: file.name, progress: onProgress, resolve: () => resolve({ attachment: descriptor(file.name) }) });
      }),
    );
    vi.spyOn(api, "deletePendingAttachment").mockResolvedValue(undefined);
    const files = ["one", "two", "three"].map((name, i) => ({
      id: `d${i}`, uploadKey: `k${i}`, file: new File(["x"], name), state: "uploading" as const,
    }));
    addAttachmentDraftFiles("conv", files);
    await flush();
    expect(calls.map((c) => c.name)).toEqual(["one", "two"]);
    calls[0].progress?.(0.42);
    expect(readAttachmentDraft("conv").find((f) => f.id === "d0")?.progress).toBe(42);
    calls[1].resolve();
    await flush();
    expect(calls.map((c) => c.name)).toEqual(["one", "two", "three"]);
    expect(readAttachmentDraft("conv").find((f) => f.id === "d1")?.state).toBe("ready");
    // Removing a waiting/in-flight file never starts it again.
    removeAttachmentDraftFile("conv", "d2");
    calls[0].resolve();
    calls[2].resolve();
    await flush();
    expect(readAttachmentDraft("conv").map((f) => [f.id, f.state])).toEqual([["d0", "ready"], ["d1", "ready"]]);
  });
});

describe("draft presence for the sidebar marker", () => {
  it("tracks conversations with draft text without notifying on every keystroke", () => {
    chatStore.begin("writer");
    const listener = vi.fn();
    const unsubscribe = subscribeDraftPresence(listener);
    writeComposerDraft("a", { body: "草稿", mentions: [], replyToId: "" });
    expect(readDraftPresence().has("a")).toBe(true);
    expect(listener).toHaveBeenCalledTimes(1);
    writeComposerText("a", composerDraftEpoch("a"), "草稿更多", []);
    writeComposerText("a", composerDraftEpoch("a"), "草稿更多字", []);
    expect(listener).toHaveBeenCalledTimes(1);
    writeComposerText("a", composerDraftEpoch("a"), "", []);
    expect(readDraftPresence().has("a")).toBe(false);
    expect(listener).toHaveBeenCalledTimes(2);
    // Reply-only drafts are not text drafts.
    writeComposerDraft("b", { body: "", mentions: [], replyToId: "m1" });
    expect(readDraftPresence().has("b")).toBe(false);
    unsubscribe();
  });
  it("persists drafts to per-user localStorage and never shows them to another account", () => {
    chatStore.begin("writer");
    writeComposerDraft("a", { body: "我的草稿", mentions: [], replyToId: "" });
    expect(localStorage.getItem("teamie.drafts.v1:writer")).toContain("我的草稿");
    chatStore.begin("someone-else");
    expect(readDraftPresence().size).toBe(0);
    expect(localStorage.getItem("teamie.drafts.v1:writer")).toBeNull();
    expect(localStorage.getItem("teamie.drafts.v1:someone-else")).toBeNull();
  });
});
