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
import {
  composerDraftEpoch,
  flushComposerDrafts,
  readComposerDraft,
  readDraftPresence,
  subscribeDraftPresence,
  writeComposerDraft,
  writeComposerText,
} from "./composer-drafts";
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
  it("merges per-tab draft writes without overwriting other open tabs and syncs storage events", () => {
    localStorage.clear();
    chatStore.begin("writer");
    writeComposerDraft("a", { body: "初始本地草稿", mentions: [], replyToId: "" });

    // Simulate another open tab persisting a draft for conversation "b".
    localStorage.setItem(
      "teamie.drafts.v1:writer",
      JSON.stringify({
        a: { body: "初始本地草稿", mentions: [], replyToId: "" },
        b: { body: "另一标签页草稿", mentions: [], replyToId: "" },
      }),
    );

    writeComposerDraft("a", { body: "更新后的本地草稿", mentions: [], replyToId: "" });
    writeComposerText("c", composerDraftEpoch("c"), "输入中草稿", []);
    flushComposerDrafts();

    const merged = JSON.parse(localStorage.getItem("teamie.drafts.v1:writer") || "{}") as Record<
      string,
      { body: string }
    >;
    expect(merged.a?.body).toBe("更新后的本地草稿");
    expect(merged.b?.body).toBe("另一标签页草稿");
    expect(merged.c?.body).toBe("输入中草稿");

    // Clearing local draft "a" removes only "a" and keeps "b" + "c".
    writeComposerDraft("a", { body: "", mentions: [], replyToId: "" });
    const afterClear = JSON.parse(localStorage.getItem("teamie.drafts.v1:writer") || "{}") as Record<
      string,
      { body: string }
    >;
    expect(afterClear.a).toBeUndefined();
    expect(afterClear.b?.body).toBe("另一标签页草稿");
    expect(afterClear.c?.body).toBe("输入中草稿");

    // Cross-tab storage event updates live in-memory drafts and sidebar presence.
    const nextRaw = JSON.stringify({
      d: { body: "跨标签同步草稿", mentions: [], replyToId: "" },
    });
    localStorage.setItem("teamie.drafts.v1:writer", nextRaw);
    window.dispatchEvent(
      new StorageEvent("storage", {
        key: "teamie.drafts.v1:writer",
        newValue: nextRaw,
      }),
    );
    expect(readDraftPresence().has("d")).toBe(true);
    expect(readDraftPresence().has("c")).toBe(false);
    expect(readComposerDraft("d").body).toBe("跨标签同步草稿");

    // A queued storage event carrying a superseded newValue must not clobber a newer local persist.
    writeComposerDraft("d", { body: "最新本地修改", mentions: [], replyToId: "" });
    window.dispatchEvent(
      new StorageEvent("storage", {
        key: "teamie.drafts.v1:writer",
        newValue: nextRaw,
      }),
    );
    expect(readComposerDraft("d").body).toBe("最新本地修改");

    // If setItem throws (e.g. quota exceeded), dirty IDs remain queued until storage recovers.
    const setItemSpy = vi
      .spyOn(Storage.prototype, "setItem")
      .mockImplementationOnce(() => {
        throw new DOMException("QuotaExceededError", "QuotaExceededError");
      });
    writeComposerDraft("e", { body: "暂存失败草稿", mentions: [], replyToId: "" });
    window.dispatchEvent(
      new StorageEvent("storage", {
        key: "teamie.drafts.v1:writer",
        newValue: localStorage.getItem("teamie.drafts.v1:writer"),
      }),
    );
    expect(readComposerDraft("e").body).toBe("暂存失败草稿");
    setItemSpy.mockRestore();

    writeComposerDraft("f", { body: "恢复后新草稿", mentions: [], replyToId: "" });
    const recovered = JSON.parse(localStorage.getItem("teamie.drafts.v1:writer") || "{}") as Record<
      string,
      { body: string }
    >;
    expect(recovered.e?.body).toBe("暂存失败草稿");
    expect(recovered.f?.body).toBe("恢复后新草稿");

    // Legacy sessionStorage migration keeps backup and retries when localStorage initially throws.
    sessionStorage.setItem(
      "teamie.drafts.v1:migrator",
      JSON.stringify({ leg: { body: "旧版会话草稿", mentions: [], replyToId: "" } }),
    );
    const migrateFailSpy = vi
      .spyOn(Storage.prototype, "setItem")
      .mockImplementationOnce(() => {
        throw new DOMException("QuotaExceededError", "QuotaExceededError");
      });
    chatStore.begin("migrator");
    migrateFailSpy.mockRestore();
    expect(readComposerDraft("leg").body).toBe("旧版会话草稿");
    expect(sessionStorage.getItem("teamie.drafts.v1:migrator")).not.toBeNull();
    writeComposerDraft("next", { body: "新草稿", mentions: [], replyToId: "" });
    const migrated = JSON.parse(localStorage.getItem("teamie.drafts.v1:migrator") || "{}") as Record<
      string,
      { body: string }
    >;
    expect(migrated.leg?.body).toBe("旧版会话草稿");
    expect(migrated.next?.body).toBe("新草稿");
    expect(sessionStorage.getItem("teamie.drafts.v1:migrator")).toBeNull();
  });
});
