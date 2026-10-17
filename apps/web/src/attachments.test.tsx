// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, useEffect } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import {
  api,
  ApiError,
  defaultAttachmentLimits,
  TeamieClient,
} from "@teamie/api-client";
import type { Attachment } from "@teamie/api-client";
import {
  AttachmentDraft,
  MessageAttachments,
  useAttachmentDraft,
  validateFile,
} from "./attachments";
import { ChatStore, reconcileMessages } from "./store";
import type { Message, SyncPage } from "@teamie/api-client";

const descriptor: Attachment = {
  id: "file-1",
  filename: "<img src=x onerror=alert(1)>.bin",
  mediaType: "text/html",
  sizeBytes: 3,
  sha256: "a".repeat(64),
};
let root: Root | undefined;
let host: HTMLDivElement;
let draft: ReturnType<typeof useAttachmentDraft>;
function Harness() {
  const value = useAttachmentDraft("conversation", defaultAttachmentLimits);
  useEffect(() => {
    draft = value;
  });
  return (
    <AttachmentDraft
      files={value.files}
      error={value.error}
      onRemove={value.remove}
      onRetry={value.retry}
    />
  );
}
async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<Harness />);
  });
}
beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
});
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  root = undefined;
  host?.remove();
  vi.restoreAllMocks();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("conversation file drafts", () => {
  it("queues multiple uploads, removes pending files and consumes only descriptors for a file-only send", async () => {
    const upload = vi
      .spyOn(api, "uploadAttachment")
      .mockResolvedValueOnce({ attachment: descriptor })
      .mockResolvedValueOnce({
        attachment: { ...descriptor, id: "file-2", filename: "second.bin" },
      });
    const remove = vi
      .spyOn(api, "deletePendingAttachment")
      .mockResolvedValue(undefined);
    await mount();
    await act(async () => {
      draft.add([
        new File([new Uint8Array([0, 255, 128])], "first.bin"),
        new File([new Uint8Array([0, 255, 128])], "second.bin"),
      ]);
    });
    expect(upload).toHaveBeenCalledTimes(2);
    expect(draft.ready).toBe(true);
    expect(draft.blocked).toBe(false);
    expect(host.textContent).toContain("已就绪");
    await act(async () => {
      draft.remove(draft.files[0].id);
    });
    expect(remove).toHaveBeenCalledWith("conversation", descriptor.id);
    let attachments: Attachment[] = [];
    await act(async () => {
      attachments = draft.consume();
    });
    expect(attachments.map((a) => a.id)).toEqual(["file-2"]);
    expect(draft.files).toEqual([]);
    await act(async () => root!.unmount());
    root = undefined;
    expect(remove).toHaveBeenCalledTimes(1);
  });
  it("retains a failed upload for retry and rejects oversized or excessive files before transmission", async () => {
    const upload = vi
      .spyOn(api, "uploadAttachment")
      .mockRejectedValueOnce(new ApiError(429, "attachment_busy", "busy"))
      .mockResolvedValue({ attachment: descriptor });
    vi.spyOn(api, "deletePendingAttachment").mockResolvedValue(undefined);
    await mount();
    await act(async () => {
      draft.add([new File(["abc"], "retry.bin")]);
    });
    expect(draft.files[0].state).toBe("failed");
    expect(draft.blocked).toBe(true);
    expect(host.textContent).toContain("稍后重试");
    await act(async () => {
      draft.retry(draft.files[0]);
    });
    expect(draft.ready).toBe(true);
    expect(upload).toHaveBeenCalledTimes(2);
    expect(upload.mock.calls[0][3]).toBeTruthy();
    expect(upload.mock.calls[1][3]).toBe(upload.mock.calls[0][3]);
    await act(async () => {
      draft.add(
        Array.from(
          { length: 9 },
          (_, index) => new File(["x"], `${index}.bin`),
        ),
      );
    });
    expect(draft.files).toHaveLength(8);
    expect(draft.error).toContain("最多 8");
    expect(
      validateFile({ name: "huge.bin", size: 4194305 }, 4194304),
    ).toContain("上限");
    expect(validateFile({ name: "empty", size: 0 }, 4194304)).toContain(
      "空文件",
    );
    expect(validateFile({ name: "../escape", size: 1 }, 4194304)).toContain(
      "无效",
    );
  });
  it("cancels an unmounted upload and drops its late result without deleting sent content", async () => {
    let finish!: (value: { attachment: Attachment }) => void;
    const upload = vi.spyOn(api, "uploadAttachment").mockImplementation(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    const remove = vi
      .spyOn(api, "deletePendingAttachment")
      .mockResolvedValue(undefined);
    await mount();
    await act(async () => {
      draft.add([new File(["abc"], "late.bin")]);
    });
    const signal = upload.mock.calls[0][2]!;
    await act(async () => root!.unmount());
    root = undefined;
    expect(signal.aborted).toBe(true);
    await act(async () => {
      finish({ attachment: descriptor });
    });
    expect(remove).toHaveBeenCalledWith("conversation", descriptor.id);
  });
});

describe("file rendering and transport", () => {
  it("receives an Agent file-only result once across sync/reconnect and downloads the original bytes", async () => {
    vi.useFakeTimers();
    const attachment = { ...descriptor, filename: "result.bin" };
    const message: Message = {
      id: "agent-result",
      conversationId: "source",
      senderId: "agent",
      senderName: "Researcher",
      senderKind: "agent",
      clientMessageId: "stable-result",
      body: "",
      seq: 1,
      createdAt: "2026-10-17T00:00:00Z",
      attachments: [attachment],
    };
    const storage = new Map<string, string>();
    const cache = {
      getItem: (key: string) => storage.get(key) || null,
      setItem: (key: string, value: string) => void storage.set(key, value),
      removeItem: (key: string) => void storage.delete(key),
    };
    const page: SyncPage = {
      events: [{ id: "1", type: "message.created", conversationId: "source", message, createdAt: message.createdAt }],
      cursor: "1",
      hasMore: false,
    };
    const store = new ChatStore(cache);
    store.begin("recipient");
    store.apply(page);
    store.apply(page);
    const resumed = new ChatStore(cache);
    resumed.begin("recipient");
    resumed.merge([message]);
    expect(resumed.getSnapshot().messages.source).toHaveLength(1);
    const result = resumed.getSnapshot().messages.source[0];
    expect(result.senderKind).toBe("agent");
    expect(result.attachments).toEqual([attachment]);

    const blob = new Blob([new Uint8Array([0, 255, 128])]);
    const download = vi.spyOn(api, "attachmentContent").mockResolvedValue(blob);
    const create = vi.fn(() => "blob:agent-result");
    const revoke = vi.fn();
    vi.stubGlobal("URL", class extends URL {
      static createObjectURL = create;
      static revokeObjectURL = revoke;
    });
    const clicks: { href: string; filename: string }[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      clicks.push({ href: this.href, filename: this.download });
    });
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => {
      root!.render(<MessageAttachments conversationId={result.conversationId} attachments={result.attachments!} />);
    });
    const buttons = host.querySelectorAll("button");
    expect(buttons).toHaveLength(1);
    await act(async () => buttons[0].click());
    expect(download).toHaveBeenCalledExactlyOnceWith("source", attachment.id);
    expect(create).toHaveBeenCalledExactlyOnceWith(blob);
    expect(clicks).toEqual([{ href: "blob:agent-result", filename: "result.bin" }]);
    expect(host.textContent).toContain("result.bin");
    expect(host.textContent).not.toMatch(/workspace|session|tool|sha256|base64/i);
    await act(async () => vi.advanceTimersByTime(1000));
    expect(revoke).toHaveBeenCalledExactlyOnceWith("blob:agent-result");
  });
  it("renders file names as escaped text with a download action and no untrusted HTML or URL", () => {
    const html = renderToStaticMarkup(
      <MessageAttachments conversationId="source" attachments={[descriptor]} />,
    );
    expect(html).toContain("&lt;img");
    expect(html).not.toContain("<img");
    expect(html).not.toContain("javascript:");
    expect(html).toContain("下载文件");
    expect(html).not.toContain('src="x"');
  });
  it("uses browser multipart boundaries and CSRF; download returns a Blob instead of parsing bytes as JSON", async () => {
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ attachment: descriptor }), {
          status: 201,
        }),
      )
      .mockResolvedValueOnce(
        new Response(new Uint8Array([0, 255, 128]), { status: 200 }),
      );
    vi.stubGlobal("fetch", fetch);
    const client = new TeamieClient();
    client.csrfToken = "csrf";
    await client.uploadAttachment("source/id", new File(["abc"], "safe.bin"));
    expect(fetch.mock.calls[0][0]).toBe(
      "/api/v1/conversations/source%2Fid/attachments",
    );
    const options = fetch.mock.calls[0][1];
    expect(options.body).toBeInstanceOf(FormData);
    expect(options.headers.has("Content-Type")).toBe(false);
    expect(options.headers.get("X-CSRF-Token")).toBe("csrf");
    const blob = await client.attachmentContent("source/id", "file/id");
    expect(Array.from(new Uint8Array(await blob.arrayBuffer()))).toEqual([
      0, 255, 128,
    ]);
    expect(fetch.mock.calls[1][0]).toContain("file%2Fid/content");
  });
  it("persists descriptors across history/sync/reconnect, then erases them on a tombstone", () => {
    const m: Message = {
      id: "message",
      conversationId: "c",
      senderId: "u",
      senderKind: "human",
      senderName: "U",
      clientMessageId: "key",
      body: "",
      seq: 1,
      createdAt: "2026-10-17T00:00:00Z",
      attachments: [descriptor],
    };
    const storage = new Map<string, string>();
    const cache = {
      getItem: (key: string) => storage.get(key) || null,
      setItem: (key: string, value: string) => {
        storage.set(key, value);
      },
      removeItem: (key: string) => {
        storage.delete(key);
      },
    };
    const store = new ChatStore(cache);
    store.begin("u");
    store.merge([m]);
    const resumed = new ChatStore(cache);
    resumed.begin("u");
    expect(resumed.getSnapshot().messages.c[0].attachments).toEqual([
      descriptor,
    ]);
    const retracted = { ...m, retractedAt: "2026-10-17T00:01:00Z" };
    expect(reconcileMessages([m], [retracted])[0].attachments).toBeUndefined();
    expect(reconcileMessages([], [retracted])[0].attachments).toBeUndefined();
    expect(reconcileMessages([retracted], [m])[0].retractedAt).toBeTruthy();
  });
});
