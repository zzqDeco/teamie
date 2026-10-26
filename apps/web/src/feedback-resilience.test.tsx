// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@teamie/api-client";
import type { Message } from "@teamie/api-client";
import { errorText, retryTransientFailures, sendMessage } from "./chat";
import { ConnectionBanner } from "./connection-banner";
import { ChatStore, chatStore } from "./store";
import { Modal } from "./ui";

const message: Message = {
  id: "local:c1", conversationId: "room", senderId: "me", senderName: "Me",
  senderKind: "human", clientMessageId: "c1", seq: Number.MAX_SAFE_INTEGER,
  body: "hello", createdAt: "2026-10-17T00:00:00Z",
};
const local = () => chatStore.getSnapshot().messages.room || [];

let host: HTMLDivElement, root: Root;
beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  localStorage.clear();
  chatStore.begin("me");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  chatStore.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("error copy", () => {
  it("never shows raw English transport or server text and maps permanent message-send codes", () => {
    expect(errorText(new TypeError("Failed to fetch"))).toBe("网络连接失败，请检查网络后重试。");
    expect(errorText(new ApiError(503, "upstream", "Service Unavailable"))).toBe("服务暂时不可用，请稍后重试。");
    expect(errorText(new ApiError(400, "invalid_seq", "Read sequence cannot be negative"))).toBe("请求失败，请稍后重试。");
    expect(errorText(new ApiError(409, "conflict", "conflict"))).toContain("冲突");
    expect(errorText(new ApiError(409, "idempotency_conflict", "conflict"))).toContain("删除后重新发送");
    expect(errorText(new ApiError(400, "invalid_mentions", "bad mention"))).toContain("提及的成员");
    expect(errorText(new ApiError(400, "invalid_reply", "bad reply"))).toContain("引用回复");
    expect(errorText(new ApiError(400, "invalid_body", "bad body"))).toContain("8000");
    expect(errorText(new ApiError(400, "too_many_attachments", "too many"))).toContain("8 个文件");
    expect(errorText(new Error("Something English"))).toBe("请求失败，请稍后重试。");
  });
});

describe("failed messages", () => {
  it("marks unfixable failures permanent, migrates and sanitizes legacy failures, and deduplicates overlapping transient retry drains", async () => {
    const send = vi.spyOn(api, "send")
      .mockRejectedValueOnce(new ApiError(409, "conflict", "conflict"))
      .mockRejectedValueOnce(new TypeError("Failed to fetch"));
    await sendMessage(message, chatStore.generation);
    await sendMessage({ ...message, id: "local:c2", clientMessageId: "c2" }, chatStore.generation);
    expect(local().find((m) => m.clientMessageId === "c1")).toMatchObject({
      status: "failed",
      permanent: true,
    });
    const permanentError = local().find((m) => m.clientMessageId === "c1")?.error;

    // Seed a legacy failed message without `permanent` and with raw English error text
    const rawKey = "teamie.sync.v1.me";
    const snapshotWithLegacy = JSON.parse(localStorage.getItem(rawKey)!);
    snapshotWithLegacy.messages.room.push({
      ...message,
      id: "local:legacy",
      clientMessageId: "legacy-1",
      status: "failed",
      error: "Raw English server diagnostic",
    });
    localStorage.setItem(rawKey, JSON.stringify(snapshotWithLegacy));

    const reloaded = new ChatStore(localStorage);
    reloaded.begin("me");
    expect(
      reloaded.getSnapshot().messages.room?.find((m) => m.clientMessageId === "c1")?.error,
    ).toBe(permanentError);
    const migratedLegacy = reloaded
      .getSnapshot()
      .messages.room?.find((m) => m.clientMessageId === "legacy-1");
    expect(migratedLegacy?.error).toBe("上次发送未确认，请重试");
    expect(migratedLegacy?.permanent).toBeUndefined();

    expect(local().find((m) => m.clientMessageId === "c2")).toMatchObject({
      status: "failed",
      permanent: false,
    });

    let releaseFirst!: () => void;
    const order: string[] = [];
    chatStore.merge([
      { ...message, id: "local:legacy", clientMessageId: "legacy-1", status: "failed", error: "上次发送未确认，请重试" },
      { ...message, id: "local:c2", clientMessageId: "c2", status: "failed", permanent: false },
      { ...message, id: "local:c3", clientMessageId: "c3", status: "failed", permanent: false },
    ]);
    send.mockImplementationOnce((_id, draft) => {
      order.push(`start:${draft.clientMessageId}`);
      return new Promise((resolve) => {
        releaseFirst = () => {
          order.push(`end:${draft.clientMessageId}`);
          resolve({ message: { ...message, id: "m2", clientMessageId: "c2", seq: 2 } });
        };
      });
    });
    send.mockImplementationOnce(async (_id, draft) => {
      order.push(`start:${draft.clientMessageId}`);
      return { message: { ...message, id: "m3", clientMessageId: "c3", seq: 3 } };
    });
    const retryPromise1 = retryTransientFailures(chatStore.generation);
    // Second overlapping reconnect drain while first is awaiting c2 must not start c3 early
    const retryPromise2 = retryTransientFailures(chatStore.generation);
    expect(order).toEqual(["start:c2"]);
    releaseFirst();
    await Promise.all([retryPromise1, retryPromise2]);
    // Legacy unclassified record (legacy-1) was not auto-retried, and c2 -> c3 ran serially
    expect(order).toEqual(["start:c2", "end:c2", "start:c3"]);
    expect(local().find((m) => m.clientMessageId === "legacy-1")?.status).toBe("failed");
    expect(local().find((m) => m.id === "m2")?.status).toBeUndefined();
    expect(local().find((m) => m.id === "m3")?.status).toBeUndefined();

    // Subsequent reconnect drain in the same generation must still run after the first drain settles
    chatStore.merge([
      { ...message, id: "local:c4", clientMessageId: "c4", status: "failed", permanent: false },
    ]);
    send.mockImplementationOnce(async (_id, draft) => {
      order.push(`start:${draft.clientMessageId}`);
      return { message: { ...message, id: "m4", clientMessageId: "c4", seq: 4 } };
    });
    await retryTransientFailures(chatStore.generation);
    expect(order).toEqual(["start:c2", "end:c2", "start:c3", "start:c4"]);
    expect(local().find((m) => m.id === "m4")?.status).toBeUndefined();

    chatStore.discardLocal("room", "c1");
    expect(local().some((m) => m.clientMessageId === "c1")).toBe(false);
    // Confirmed messages are never discarded.
    chatStore.discardLocal("room", "c2");
    expect(local().some((m) => m.id === "m2")).toBe(true);
  });
});

describe("connection banner", () => {
  async function show(connection: "connected" | "reconnecting" | "offline") {
    await act(async () => {
      root.render(<ConnectionBanner connection={connection} syncError="" showSyncError onRetry={() => {}} />);
    });
  }
  it("shows offline at once, reconnecting only after 3s, then confirms recovery briefly", async () => {
    vi.useFakeTimers();
    await show("connected");
    expect(host.textContent).toBe("");
    await show("reconnecting");
    await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
    expect(host.textContent).toBe("");
    await act(async () => { await vi.advanceTimersByTimeAsync(1100); });
    expect(host.textContent).toContain("正在重新连接");
    await show("connected");
    await act(async () => { await vi.advanceTimersByTimeAsync(10); });
    expect(host.textContent).toContain("已重新连接");
    await act(async () => { await vi.advanceTimersByTimeAsync(2100); });
    expect(host.textContent).toBe("");
    await show("offline");
    await act(async () => { await vi.advanceTimersByTimeAsync(10); });
    expect(host.textContent).toContain("网络已断开");
  });
});

describe("modal dismissal", () => {
  it("blocks Escape, backdrop and the close button alike when not dismissible", async () => {
    const close = vi.fn();
    await act(async () => {
      root.render(<Modal title="T" onClose={close} dismissible={false}><input /></Modal>);
    });
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
      host.querySelector(".modal-backdrop")!.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
      host.querySelector<HTMLButtonElement>(".modal-close")!.click();
    });
    expect(close).not.toHaveBeenCalled();
    expect(host.querySelector(".modal-close")!.getAttribute("aria-disabled")).toBe("true");
  });
});
