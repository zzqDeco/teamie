// @vitest-environment jsdom
import { act, createRef } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@teamie/api-client";
import type {
  Conversation,
  ConversationAttachmentItem,
} from "@teamie/api-client";
import { clearAttachmentPreviews } from "../attachment-previews";
import { MessageAttachments } from "../attachments";
import { chatStore } from "../store";
import { ConversationGalleryPane } from "./conversation-gallery-pane";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT =
  true;

const conv: Conversation = {
  id: "c-gallery",
  workspaceId: "ws-1",
  type: "channel",
  name: "设计素材",
  memberIds: ["u-1", "u-2"],
  lastSeq: 10,
  readSeq: 10,
  updatedAt: "2026-11-24T08:00:00Z",
};

const imgItem1: ConversationAttachmentItem = {
  attachment: {
    id: "att-img-1",
    filename: "Hero_Banner.png",
    mediaType: "image/png",
    sizeBytes: 24576,
    sha256: "a".repeat(64),
    image: { width: 1280, height: 720 },
  },
  messageId: "m-1",
  messageSeq: 8,
  senderId: "u-2",
  senderName: "陈工",
  senderKind: "human",
  createdAt: "2026-11-20T08:00:00Z",
  isForwarded: false,
};

const imgItem2Legacy: ConversationAttachmentItem = {
  attachment: {
    id: "att-img-2",
    filename: "Legacy_Flow.png",
    mediaType: "image/png",
    sizeBytes: 12288,
    sha256: "b".repeat(64),
  },
  messageId: "m-2",
  messageSeq: 5,
  senderId: "u-2",
  senderName: "陈工",
  senderKind: "human",
  createdAt: "2026-10-15T08:00:00Z",
  isForwarded: true,
};

const fileItem1: ConversationAttachmentItem = {
  attachment: {
    id: "att-file-1",
    filename: "Architecture_Spec_v2.pdf",
    mediaType: "application/pdf",
    sizeBytes: 512 * 1024,
    sha256: "c".repeat(64),
  },
  messageId: "m-3",
  messageSeq: 7,
  senderId: "u-3",
  senderName: "架构助手",
  senderKind: "agent",
  createdAt: "2026-11-21T09:30:00Z",
  isForwarded: false,
};

describe("PR-3 conversation media/file gallery pane and shared full-res lightbox", () => {
  let host: HTMLDivElement;
  let root: Root;
  let nextBlobId = 0;

  beforeEach(() => {
    vi.useFakeTimers();
    nextBlobId = 0;
    api.csrfToken = "gallery-session";
    vi.stubGlobal(
      "URL",
      class extends URL {
        static createObjectURL = vi.fn(() => `blob:gallery-${++nextBlobId}`);
        static revokeObjectURL = vi.fn();
      },
    );
    // Simulate all observed tiles intersecting in jsdom.
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        private cb: IntersectionObserverCallback;
        constructor(cb: IntersectionObserverCallback) {
          this.cb = cb;
        }
        observe(target: Element) {
          this.cb(
            [{ target, isIntersecting: true } as IntersectionObserverEntry],
            this as unknown as IntersectionObserver,
          );
        }
        unobserve() {}
        disconnect() {}
      },
    );
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    chatStore.begin("u-1");
    chatStore.replaceConversations([conv]);
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    host.remove();
    clearAttachmentPreviews();
    chatStore.clear();
    api.csrfToken = "";
    vi.useRealTimers();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  async function flush(ms = 300) {
    await act(async () => {
      vi.advanceTimersByTime(ms);
      for (let i = 0; i < 6; i++) await Promise.resolve();
    });
  }

  function setInputValue(el: HTMLInputElement, value: string) {
    const setter = Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      "value",
    )?.set;
    setter?.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
    el.dispatchEvent(new Event("change", { bubbles: true }));
  }

  it("renders month-grouped images, upgrades lightbox from thumbnail to full-res with carousel/zoom/locate, and reconciles live retractions", async () => {
    const onLocate = vi.fn();
    const onClose = vi.fn();
    const listSpy = vi
      .spyOn(api, "listConversationAttachments")
      .mockImplementation(async (_id, opts) => {
        if (opts?.kind === "file") {
          return { items: [fileItem1] };
        }
        if (opts?.cursor === "img-page-2") {
          return { items: [imgItem2Legacy] };
        }
        return { items: [imgItem1], cursor: "img-page-2" };
      });
    const thumbSpy = vi
      .spyOn(api, "attachmentThumbnail")
      .mockResolvedValue(new Blob(["thumb"], { type: "image/jpeg" }));

    let resolveFullRes!: (b: Blob) => void;
    const contentSpy = vi
      .spyOn(api, "attachmentContent")
      .mockImplementationOnce(
        () => new Promise<Blob>((resolve) => { resolveFullRes = resolve; }),
      )
      .mockResolvedValue(new Blob(["full-2"], { type: "image/png" }));

    await act(async () => {
      root.render(
        <ConversationGalleryPane
          paneRef={createRef<HTMLElement>()}
          overlayMembers={false}
          mobile={false}
          sidebar={false}
          conversation={conv}
          onClose={onClose}
          onLocateMessage={onLocate}
        />,
      );
    });
    await flush(50);

    expect(listSpy).toHaveBeenCalledWith(
      "c-gallery",
      { kind: "image", limit: 40 },
      expect.any(AbortSignal),
    );
    expect(thumbSpy).toHaveBeenCalledWith(
      "c-gallery",
      "att-img-1",
      expect.any(AbortSignal),
    );
    expect(host.textContent).toContain("2026年11月");

    // Load page 2 (legacy non-thumbnail image in a different month).
    const moreBtn = host.querySelector<HTMLButtonElement>(".gallery-more")!;
    expect(moreBtn).not.toBeNull();
    await act(async () => {
      moreBtn.click();
      for (let i = 0; i < 6; i++) await Promise.resolve();
    });
    expect(host.textContent).toContain("2026年10月");
    expect(host.querySelectorAll(".gallery-image-tile")).toHaveLength(2);

    // Click first tile -> lightbox opens immediately with thumbnail while full-res loads in background!
    const tiles = host.querySelectorAll<HTMLButtonElement>(".gallery-image-tile");
    await act(async () => {
      tiles[0].click();
      await Promise.resolve();
    });
    const modal = document.querySelector(".attachment-lightbox-modal");
    expect(modal).not.toBeNull();
    const imgEl = modal!.querySelector<HTMLImageElement>(
      ".attachment-lightbox-image",
    )!;
    const thumbSrc = imgEl.getAttribute("src");
    expect(thumbSrc).toContain("blob:gallery-");
    expect(modal!.textContent).toContain("1 / 2");
    expect(modal!.textContent).toContain("正在加载原图");

    // Resolve full-res /content -> src upgrades seamlessly to the full-res blob URL!
    await act(async () => {
      resolveFullRes(new Blob(["full-res-bytes"], { type: "image/png" }));
      for (let i = 0; i < 6; i++) await Promise.resolve();
    });
    expect(contentSpy).toHaveBeenCalledWith(
      "c-gallery",
      "att-img-1",
      expect.any(AbortSignal),
    );
    expect(imgEl.getAttribute("src")).not.toBe(thumbSrc);

    // Zoom toggle switches between fit and actual size
    const zoomBtn = modal!.querySelector<HTMLButtonElement>(
      ".attachment-lightbox-zoom-btn",
    )!;
    await act(async () => {
      zoomBtn.click();
    });
    expect(imgEl.classList.contains("is-zoomed")).toBe(true);

    // ArrowRight navigates to slide 2 (legacy image) and resets zoom
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight" }));
      for (let i = 0; i < 6; i++) await Promise.resolve();
    });
    expect(modal!.textContent).toContain("Legacy_Flow.png");
    expect(modal!.textContent).toContain("2 / 2");
    expect(imgEl.classList.contains("is-zoomed")).toBe(false);

    // Clicking "定位到原消息" closes lightbox and calls onLocateMessage("m-2")
    const locateBtn = Array.from(modal!.querySelectorAll("button")).find((b) =>
      b.textContent?.includes("定位到原消息"),
    )!;
    await act(async () => {
      locateBtn.click();
    });
    expect(onLocate).toHaveBeenCalledWith("m-2");
    expect(document.querySelector(".attachment-lightbox-modal")).toBeNull();

    // Live message retraction immediately removes m-1's tile from the grid!
    await act(async () => {
      chatStore.merge([
        {
          id: "m-1",
          clientMessageId: "cm-1",
          conversationId: "c-gallery",
          senderId: "u-2",
          senderName: "陈工",
          senderKind: "human",
          seq: 8,
          body: "",
          createdAt: imgItem1.createdAt,
          retractedAt: "2026-11-24T10:00:00Z",
        },
      ]);
    });
    expect(host.querySelectorAll(".gallery-image-tile")).toHaveLength(1);
  });

  it("filters files by filename in the Files tab and supports locate + download", async () => {
    const onLocate = vi.fn();
    const listSpy = vi
      .spyOn(api, "listConversationAttachments")
      .mockResolvedValue({ items: [fileItem1] });

    await act(async () => {
      root.render(
        <ConversationGalleryPane
          paneRef={createRef<HTMLElement>()}
          overlayMembers={false}
          mobile={false}
          sidebar={false}
          conversation={conv}
          onClose={vi.fn()}
          onLocateMessage={onLocate}
        />,
      );
    });
    await flush(50);

    // Switch to "文件" tab
    const tabBtns = host.querySelectorAll<HTMLButtonElement>(".gallery-tabs button");
    await act(async () => {
      tabBtns[1].click();
    });
    await flush(50);
    expect(listSpy).toHaveBeenLastCalledWith(
      "c-gallery",
      { kind: "file", limit: 40 },
      expect.any(AbortSignal),
    );
    expect(host.textContent).toContain("Architecture_Spec_v2.pdf");

    // Search filename
    const searchInput = host.querySelector<HTMLInputElement>(
      '.gallery-file-search input[type="search"]',
    )!;
    await act(async () => {
      setInputValue(searchInput, "Spec");
    });
    await flush(300);
    expect(listSpy).toHaveBeenLastCalledWith(
      "c-gallery",
      { kind: "file", q: "Spec", limit: 40 },
      expect.any(AbortSignal),
    );

    // Locate button calls onLocateMessage("m-3")
    const locateFileBtn = host.querySelector<HTMLButtonElement>(
      'button[aria-label*="定位到包含 Architecture_Spec_v2.pdf"]',
    )!;
    await act(async () => {
      locateFileBtn.click();
    });
    expect(onLocate).toHaveBeenCalledWith("m-3");
  });

  it("supports multi-image carousel inside MessageAttachments using the shared AttachmentLightbox", async () => {
    vi.spyOn(api, "attachmentThumbnail").mockResolvedValue(
      new Blob(["thumb"], { type: "image/png" }),
    );
    vi.spyOn(api, "attachmentContent").mockResolvedValue(
      new Blob(["full"], { type: "image/png" }),
    );

    await act(async () => {
      root.render(
        <MessageAttachments
          conversationId="c-gallery"
          attachments={[
            imgItem1.attachment,
            {
              ...imgItem1.attachment,
              id: "att-img-second",
              filename: "Second_Shot.png",
            },
          ]}
        />,
      );
    });
    await flush(50);

    const thumbBtns = host.querySelectorAll<HTMLButtonElement>(
      ".attachment-thumbnail-button",
    );
    expect(thumbBtns).toHaveLength(2);
    await act(async () => {
      thumbBtns[0].click();
      await Promise.resolve();
    });
    const modal = document.querySelector(".attachment-lightbox-modal")!;
    expect(modal.textContent).toContain("Hero_Banner.png");
    expect(modal.textContent).toContain("1 / 2");

    const nextBtn = modal.querySelector<HTMLButtonElement>(
      ".attachment-lightbox-nav.next",
    )!;
    await act(async () => {
      nextBtn.click();
      await Promise.resolve();
    });
    expect(modal.textContent).toContain("Second_Shot.png");
    expect(modal.textContent).toContain("2 / 2");
  });

  it("keeps initial-load retry visible even when chatStore already holds cached attachments", async () => {
    chatStore.merge([
      {
        id: "m-1",
        conversationId: "c-gallery",
        senderId: "u-1",
        senderName: "林夕",
        senderKind: "human",
        clientMessageId: "c-m-1",
        seq: 10,
        body: "cached image",
        attachments: [imgItem1.attachment],
        createdAt: imgItem1.createdAt,
      },
    ]);
    const listSpy = vi
      .spyOn(api, "listConversationAttachments")
      .mockRejectedValueOnce(new Error("network failed"))
      .mockResolvedValueOnce({ items: [imgItem1] });
    vi.spyOn(api, "attachmentThumbnail").mockResolvedValue(
      new Blob(["thumb"], { type: "image/jpeg" }),
    );

    await act(async () => {
      root.render(
        <ConversationGalleryPane
          paneRef={createRef<HTMLElement>()}
          overlayMembers={false}
          mobile={false}
          sidebar={false}
          conversation={conv}
          onClose={vi.fn()}
          onLocateMessage={vi.fn()}
        />,
      );
    });
    await flush(50);

    expect(listSpy).toHaveBeenCalledTimes(1);
    const retryBtn = host.querySelector<HTMLButtonElement>(".gallery-error button");
    expect(retryBtn).not.toBeNull();
    expect(retryBtn!.textContent).toContain("重试");
    expect(host.querySelectorAll(".gallery-image-tile")).toHaveLength(0);

    await act(async () => {
      retryBtn!.click();
    });
    await flush(50);

    expect(listSpy).toHaveBeenCalledTimes(2);
    expect(host.querySelector(".gallery-error")).toBeNull();
    expect(host.querySelectorAll(".gallery-image-tile")).toHaveLength(1);
  });
});
