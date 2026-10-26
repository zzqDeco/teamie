// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api, ApiError } from "@teamie/api-client";
import { clearAttachmentPreviews, subscribeAttachmentPreview } from "./attachment-previews";
const releases: (() => void)[] = [];
const blob = new Blob(["png"], { type: "image/png" });
const flush = async () => { for (let i = 0; i < 10; i++) await Promise.resolve(); };
beforeEach(() => {
  api.csrfToken = "session-a";
  let next = 0;
  vi.stubGlobal("URL", class extends URL {
    static createObjectURL = vi.fn(() => `blob:preview-${++next}`);
    static revokeObjectURL = vi.fn();
  });
});
afterEach(async () => {
  for (const release of releases.splice(0)) release();
  clearAttachmentPreviews();
  await flush();
  api.csrfToken = "";
  vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals();
});
it("shares one transfer across components and eventually delivers all five images", async () => {
  let active = 0, peak = 0;
  const finish: (() => void)[] = [];
  const request = vi.spyOn(api, "attachmentContent").mockImplementation(() => {
    active++; peak = Math.max(peak, active);
    return new Promise<Blob>((resolve) => finish.push(() => { active--; resolve(blob); }));
  });
  const listeners = Array.from({ length: 5 }, () => vi.fn());
  listeners.forEach((listener, i) => releases.push(subscribeAttachmentPreview("c1", `image-${i}`, listener)));
  const duplicate = vi.fn(); releases.push(subscribeAttachmentPreview("c1", "image-0", duplicate));
  expect(request).toHaveBeenCalledTimes(1);
  for (let i = 0; i < 5; i++) { finish.shift()!(); await flush(); }
  expect(peak).toBe(1); expect(request).toHaveBeenCalledTimes(5);
  listeners.forEach((listener) => expect(listener).toHaveBeenLastCalledWith({ url: expect.stringContaining("blob:") }));
  expect(duplicate).toHaveBeenLastCalledWith(listeners[0].mock.lastCall![0]);
});
it("retries 429 using Retry-After, then exposes a manual retry after repeated saturation", async () => {
  vi.useFakeTimers();
  const request = vi.spyOn(api, "attachmentContent").mockRejectedValue(new ApiError(429, "attachment_busy", "busy", 1200));
  const listener = vi.fn(); releases.push(subscribeAttachmentPreview("c1", "i", listener));
  await flush(); await vi.advanceTimersByTimeAsync(1199); expect(request).toHaveBeenCalledTimes(1);
  await vi.advanceTimersByTimeAsync(1201); await flush();
  expect(request).toHaveBeenCalledTimes(3); expect(listener).toHaveBeenLastCalledWith({ retryable: true });
  request.mockResolvedValue(blob);
  releases.push(subscribeAttachmentPreview("c1", "i", listener)); await flush();
  expect(listener).toHaveBeenLastCalledWith({ url: expect.any(String) });
});
it.each([401, 403, 404])("does not retry an HTTP %i denial", async (status) => {
  vi.useFakeTimers();
  const request = vi.spyOn(api, "attachmentContent").mockRejectedValue(new ApiError(status, "denied", "denied"));
  const listener = vi.fn(); releases.push(subscribeAttachmentPreview("c1", "i", listener));
  await vi.runAllTimersAsync(); await flush();
  expect(request).toHaveBeenCalledTimes(1); expect(listener).toHaveBeenLastCalledWith({ retryable: false });
});
it("cancels queued and active work on navigation and ignores a late old-session response", async () => {
  let complete!: (b: Blob) => void;
  const request = vi.spyOn(api, "attachmentContent").mockImplementationOnce(() => new Promise<Blob>((resolve) => { complete = resolve; })).mockResolvedValue(blob);
  const old = vi.fn(); const queued = vi.fn();
  releases.push(subscribeAttachmentPreview("old", "i", old), subscribeAttachmentPreview("old", "queued", queued));
  const signal = request.mock.calls[0][2]!;
  clearAttachmentPreviews(); api.csrfToken = "session-b";
  expect(signal.aborted).toBe(true);
  const next = vi.fn(); releases.push(subscribeAttachmentPreview("new", "i", next));
  complete(blob); await flush();
  expect(request).toHaveBeenCalledTimes(2); expect(old).not.toHaveBeenCalledWith({ url: expect.any(String) });
  expect(queued).not.toHaveBeenCalledWith({ url: expect.any(String) });
  expect(next).toHaveBeenLastCalledWith({ url: expect.any(String) });
});
it("revokes a shared URL only after its last consumer leaves, and never caches it across sessions", async () => {
  const request = vi.spyOn(api, "attachmentContent").mockResolvedValue(blob);
  const first = subscribeAttachmentPreview("c", "i", vi.fn());
  const second = subscribeAttachmentPreview("c", "i", vi.fn());
  await flush(); first(); expect(URL.revokeObjectURL).not.toHaveBeenCalled();
  second(); expect(URL.revokeObjectURL).toHaveBeenCalledTimes(1);
  releases.push(subscribeAttachmentPreview("c", "i", vi.fn())); await flush();
  expect(request).toHaveBeenCalledTimes(2);
});
