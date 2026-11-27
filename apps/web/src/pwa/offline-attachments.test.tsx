// @vitest-environment jsdom
import { act, createRef } from "react";
import { createRoot } from "react-dom/client";
import type { Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError, defaultAttachmentLimits } from "@teamie/api-client";
import type { Attachment, Conversation, User } from "@teamie/api-client";
import {
  hydrateStagedAttachmentDrafts,
  readAttachmentDraft,
} from "../attachment-drafts";
import {
  AttachmentDraft,
  StagedMessageAttachments,
  useAttachmentDraft,
} from "../attachments";
import { retryTransientFailures, sendMessage } from "../chat";
import { attachmentTransport } from "../chat/attachment-upload";
import { MessageSendStatus } from "../chat/message-send-status";
import { clearUserLocalUiState } from "../logout-cleanup";
import { MessageComposer } from "../message-composer";
import { chatStore } from "../store";
import type { LocalMessage } from "../store";
import {
  bindStagedAttachmentsToMessage,
  createIndexedDbBackend,
  createMemoryOfflineAttachmentBackend,
  listStagedDraftAttachments,
  listStagedMessageAttachments,
  MAX_STAGED_ATTACHMENTS_PER_USER,
  OfflineAttachmentStoreError,
  setOfflineAttachmentBackendForTest,
  STAGED_ATTACHMENT_TTL_MS,
  stageOfflineAttachment,
} from "./offline-attachments";
import type { StagedAttachmentRecord } from "./offline-attachments";

const user: User = {
  id: "u-stage",
  email: "stage@example.test",
  displayName: "林夕",
  kind: "human",
  platformRole: "user",
  createdAt: "2026-10-17T00:00:00Z",
};

const conv: Conversation = {
  id: "c-stage",
  workspaceId: "w-stage",
  type: "channel",
  name: "general",
  memberIds: ["u-stage"],
  lastSeq: 1,
  readSeq: 1,
  updatedAt: "2026-11-27T09:00:00Z",
};

const conv2: Conversation = {
  ...conv,
  id: "c-revoked",
  name: "secret",
};

/** Minimal spec-compliant IDBFactory mock to exercise real createIndexedDbBackend code. */
function createFakeIndexedDbFactory(): IDBFactory {
  const stores = new Map<string, Map<string, StagedAttachmentRecord>>();
  const indexes = new Map<string, Map<string, string | string[]>>();

  const makeReq = <T,>(run: () => T): IDBRequest<T> => {
    const req = {
      result: undefined as unknown as T,
      error: null as DOMException | null,
      onsuccess: null as (() => void) | null,
      onerror: null as (() => void) | null,
    };
    queueMicrotask(() => {
      try {
        req.result = run();
        req.onsuccess?.();
      } catch (err) {
        req.error = err as DOMException;
        req.onerror?.();
      }
    });
    return req as unknown as IDBRequest<T>;
  };

  const matchKey = (
    row: StagedAttachmentRecord,
    keyPath: string | string[],
    query: unknown,
  ): boolean => {
    if (Array.isArray(keyPath)) {
      const q = query as unknown[];
      return keyPath.every(
        (field, i) => (row as Record<string, unknown>)[field] === q[i],
      );
    }
    return (row as Record<string, unknown>)[keyPath] === query;
  };

  const db = {
    objectStoreNames: {
      contains: (name: string) => stores.has(name),
    },
    createObjectStore(name: string) {
      stores.set(name, new Map());
      const idxMap = new Map<string, string | string[]>();
      indexes.set(name, idxMap);
      return {
        createIndex(idxName: string, keyPath: string | string[]) {
          idxMap.set(idxName, keyPath);
        },
      };
    },
    close() {},
    transaction(name: string) {
      const table = stores.get(name)!;
      const idxMap = indexes.get(name)!;
      const tx = {
        error: null as DOMException | null,
        oncomplete: null as (() => void) | null,
        onerror: null as (() => void) | null,
        onabort: null as (() => void) | null,
        objectStore() {
          return {
            put(value: StagedAttachmentRecord) {
              table.set(value.id, { ...value });
              return makeReq(() => value.id);
            },
            get(id: string) {
              return makeReq(() => table.get(id));
            },
            getAll() {
              return makeReq(() => Array.from(table.values()));
            },
            delete(id: string) {
              table.delete(id);
              return makeReq(() => undefined);
            },
            clear() {
              table.clear();
              return makeReq(() => undefined);
            },
            index(idxName: string) {
              const kp = idxMap.get(idxName)!;
              return {
                getAll(query: unknown) {
                  return makeReq(() =>
                    Array.from(table.values()).filter((r) =>
                      matchKey(r, kp, query),
                    ),
                  );
                },
              };
            },
          };
        },
      };
      setTimeout(() => {
        if (!tx.error) tx.oncomplete?.();
      }, 0);
      return tx;
    },
  };

  return {
    open() {
      const req = {
        result: db,
        error: null,
        onupgradeneeded: null as (() => void) | null,
        onsuccess: null as (() => void) | null,
        onerror: null as (() => void) | null,
        onblocked: null as (() => void) | null,
      };
      queueMicrotask(() => {
        req.onupgradeneeded?.();
        req.onsuccess?.();
      });
      return req as unknown as IDBOpenDBRequest;
    },
  } as unknown as IDBFactory;
}

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  setOfflineAttachmentBackendForTest(createMemoryOfflineAttachmentBackend());
  localStorage.clear();
  chatStore.clear();
  chatStore.begin(user.id);
  chatStore.replaceConversations([conv, conv2]);
  api.csrfToken = "csrf-stage";
  api.sessionUnverified = false;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  chatStore.clear();
  setOfflineAttachmentBackendForTest(null);
  api.csrfToken = "";
  api.sessionUnverified = false;
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function flush(ms = 10) {
  await act(async () => {
    await new Promise((r) => setTimeout(r, ms));
  });
}

describe("IndexedDB offline attachment backend", () => {
  it("exercises real IndexedDB store creation, staging, binding, unbinding, TTL pruning, capacity cap, and account isolation", async () => {
    const backend = createIndexedDbBackend(() => createFakeIndexedDbFactory());
    const now = 1_700_000_000_000;
    const makeRec = (
      id: string,
      overrides: Partial<StagedAttachmentRecord> = {},
    ): StagedAttachmentRecord => ({
      id,
      uploadKey: `key-${id}`,
      userId: "u-stage",
      conversationId: "c-stage",
      clientMessageId: "",
      name: `${id}.txt`,
      type: "text/plain",
      size: 5,
      lastModified: now,
      blob: new Blob(["hello"], { type: "text/plain" }),
      createdAt: now,
      ...overrides,
    });

    await backend.stage(makeRec("f1"), now);
    await backend.stage(makeRec("f2", { createdAt: now + 10 }), now);
    await backend.stage(
      makeRec("expired", { createdAt: now - STAGED_ATTACHMENT_TTL_MS - 1 }),
      now,
    );
    await backend.stage(makeRec("other-user", { userId: "u-other" }), now);

    const drafts = await backend.listDrafts("u-stage", "c-stage", now);
    expect(drafts.map((d) => d.id)).toEqual(["f1", "f2"]);

    // Bind to message, list by message, then unbind back to draft.
    const bound = await backend.bindToMessage(
      "u-stage",
      "c-stage",
      ["f1", "f2"],
      "msg-1",
    );
    expect(bound).toHaveLength(2);
    expect(await backend.listDrafts("u-stage", "c-stage", now)).toHaveLength(0);
    expect(
      (await backend.listForMessage("u-stage", "msg-1", now)).map((r) => r.id),
    ).toEqual(["f1", "f2"]);

    await backend.unbindToDraft("u-stage", "c-stage", "msg-1");
    expect(
      (await backend.listDrafts("u-stage", "c-stage", now)).map((r) => r.id),
    ).toEqual(["f1", "f2"]);

    // Enforce 32-file per-user staging limit.
    for (let i = 3; i <= MAX_STAGED_ATTACHMENTS_PER_USER; i++) {
      await backend.stage(makeRec(`f${i}`, { createdAt: now + i }), now);
    }
    await expect(
      backend.stage(makeRec("f-over"), now),
    ).rejects.toBeInstanceOf(OfflineAttachmentStoreError);

    // Account isolation: clearExceptUser drops u-other while preserving u-stage.
    await backend.clearExceptUser("u-stage");
    expect(await backend.listDrafts("u-other", "c-stage", now)).toHaveLength(0);
    expect(
      (await backend.listDrafts("u-stage", "c-stage", now)).length,
    ).toBe(MAX_STAGED_ATTACHMENTS_PER_USER);

    // Prune to allowed conversations drops revoked conversation blobs.
    await backend.clearForUser("u-stage");
    await backend.stage(makeRec("keep", { conversationId: "c-stage" }), now);
    await backend.stage(makeRec("drop", { conversationId: "c-revoked" }), now);
    await backend.pruneToConversations("u-stage", new Set(["c-stage"]));
    expect(await backend.listDrafts("u-stage", "c-revoked", now)).toHaveLength(0);
    expect(await backend.listDrafts("u-stage", "c-stage", now)).toHaveLength(1);
  });

  it("maps unavailable indexedDB cleanly to OfflineAttachmentStoreError", async () => {
    const unavailable = createIndexedDbBackend(() => undefined);
    await expect(
      unavailable.stage({
        id: "x",
        uploadKey: "k",
        userId: "u-stage",
        conversationId: "c-stage",
        clientMessageId: "",
        name: "a.txt",
        type: "text/plain",
        size: 1,
        lastModified: 1,
        blob: new Blob(["a"]),
        createdAt: Date.now(),
      }),
    ).rejects.toMatchObject({ code: "storage_unavailable" });
  });
});

describe("composer offline staging, idempotent replay, edit/cancel, and cleanup", () => {
  it("stages offline files in composer within limits, hydrates after reload, and auto-uploads on reconnect", async () => {
    let draftApi!: ReturnType<typeof useAttachmentDraft>;
    function Harness({ offline }: { offline: boolean }) {
      draftApi = useAttachmentDraft("c-stage", defaultAttachmentLimits, offline);
      return (
        <AttachmentDraft
          files={draftApi.files}
          error={draftApi.error}
          onRemove={draftApi.remove}
          onRetry={draftApi.retry}
        />
      );
    }

    const uploadSpy = vi
      .spyOn(attachmentTransport, "upload")
      .mockResolvedValue({
        attachment: {
          id: "att-srv-1",
          filename: "report.pdf",
          mediaType: "application/pdf",
          sizeBytes: 12,
          sha256: "abc",
        },
      });

    await act(async () => {
      root.render(<Harness offline />);
    });
    await flush();

    // Oversized file (>4 MiB) is rejected; valid file is staged offline without network call.
    const big = new File([new Uint8Array(defaultAttachmentLimits.maxFileBytes + 1)], "huge.bin");
    const valid = new File(["pdf-contents"], "report.pdf", { type: "application/pdf" });
    await act(async () => {
      draftApi.add([big, valid]);
    });
    await flush();

    expect(uploadSpy).not.toHaveBeenCalled();
    expect(host.textContent).toContain("huge.bin");
    expect(host.textContent).toContain("report.pdf");
    expect(host.textContent).toContain("已暂存，联网后自动上传");
    expect(draftApi.ready).toBe(true);
    expect(draftApi.blocked).toBe(false);
    expect(await listStagedDraftAttachments(user.id, "c-stage")).toHaveLength(1);

    // Reconnecting automatically promotes staged draft to upload using its uploadKey and clears IDB blob.
    const stagedUploadKey = draftApi.files[0].uploadKey;
    await act(async () => {
      root.render(<Harness offline={false} />);
    });
    await flush();

    expect(uploadSpy).toHaveBeenCalledWith(
      "c-stage",
      expect.any(File),
      expect.any(AbortSignal),
      stagedUploadKey,
      expect.any(Function),
    );
    expect(draftApi.files[0].state).toBe("ready");
    expect(await listStagedDraftAttachments(user.id, "c-stage")).toHaveLength(0);
  });

  it("queues offline message with staged attachments, replays idempotently after partial upload failure, and keeps localStorage free of binary blobs", async () => {
    const inputRef = createRef<HTMLTextAreaElement>();
    let draftApi!: ReturnType<typeof useAttachmentDraft>;
    function ComposerHarness() {
      draftApi = useAttachmentDraft("c-stage", defaultAttachmentLimits, true);
      return (
        <MessageComposer
          conversation={conv}
          user={user}
          name="general"
          draftEpoch="e1"
          replyToId=""
          attachments={draftApi}
          attachmentLimits={defaultAttachmentLimits}
          uploadsEnabled
          attachmentBlocked={draftApi.blocked}
          mentionMembers={[]}
          mentionsEnabled={false}
          inputRef={inputRef}
          editing={null}
          located={false}
          latestLoaded
          latest={1}
          canEdit={false}
          offline
          messages={[]}
          sendTyping={vi.fn()}
          onClearReply={vi.fn()}
          onStartEdit={vi.fn()}
          onSent={vi.fn()}
        />
      );
    }

    await act(async () => {
      root.render(<ComposerHarness />);
    });
    await flush();

    const file1 = new File(["binary-payload-1"], "one.png", { type: "image/png" });
    const file2 = new File(["binary-payload-2"], "two.pdf", { type: "application/pdf" });
    await act(async () => {
      draftApi.add([file1, file2]);
    });
    await flush();
    expect(draftApi.files).toHaveLength(2);
    const key1 = draftApi.files[0].uploadKey;
    const key2 = draftApi.files[1].uploadKey;

    // Submit while offline (attachment-only message is allowed!).
    const sendBtn = host.querySelector<HTMLButtonElement>(".send-button")!;
    expect(sendBtn.disabled).toBe(false);
    await act(async () => {
      sendBtn.click();
    });
    await flush();

    const queuedList = chatStore.getSnapshot().messages["c-stage"] || [];
    expect(queuedList).toHaveLength(1);
    const queued = queuedList[0];
    expect(queued.status).toBe("queued");
    expect(queued.stagedAttachments).toHaveLength(2);
    expect(draftApi.files).toHaveLength(0);

    // localStorage contains metadata only — zero binary payload bytes.
    chatStore.flush();
    const persistedRaw = localStorage.getItem(`teamie.sync.v1.${user.id}`)!;
    expect(persistedRaw).toContain("one.png");
    expect(persistedRaw).not.toContain("binary-payload-1");

    // Verify staged attachment timeline badge rendering.
    await act(async () => {
      root.render(<StagedMessageAttachments items={queued.stagedAttachments!} />);
    });
    expect(host.textContent).toContain("one.png");
    expect(host.textContent).toContain("待上传");

    // First reconnect attempt: file 1 succeeds, file 2 hits transient 503.
    const att1: Attachment = {
      id: "att-1",
      filename: "one.png",
      mediaType: "image/png",
      sizeBytes: file1.size,
      sha256: "h1",
    };
    const att2: Attachment = {
      id: "att-2",
      filename: "two.pdf",
      mediaType: "application/pdf",
      sizeBytes: file2.size,
      sha256: "h2",
    };
    const uploadSpy = vi
      .spyOn(api, "uploadAttachment")
      .mockResolvedValueOnce({ attachment: att1 })
      .mockRejectedValueOnce(new ApiError(503, "upstream", "busy"))
      .mockResolvedValueOnce({ attachment: att2 });
    const sendSpy = vi.spyOn(api, "send").mockResolvedValueOnce({
      message: {
        id: "srv-msg-1",
        conversationId: "c-stage",
        senderId: user.id,
        senderName: user.displayName,
        senderKind: "human",
        clientMessageId: queued.clientMessageId,
        seq: 2,
        body: "",
        attachments: [att1, att2],
        createdAt: "2026-11-27T09:05:00Z",
      },
    });

    await retryTransientFailures(chatStore.generation);

    expect(uploadSpy).toHaveBeenCalledTimes(2);
    expect(uploadSpy).toHaveBeenNthCalledWith(1, "c-stage", expect.any(File), undefined, key1);
    expect(uploadSpy).toHaveBeenNthCalledWith(2, "c-stage", expect.any(File), undefined, key2);
    expect(sendSpy).not.toHaveBeenCalled();

    // File 1's descriptor is persisted on the failed message and its IDB blob is already freed.
    const afterPartial = chatStore.getSnapshot().messages["c-stage"][0];
    expect(afterPartial.status).toBe("failed");
    expect(afterPartial.permanent).toBe(false);
    expect(afterPartial.stagedAttachments![0].attachment?.id).toBe("att-1");
    expect(afterPartial.stagedAttachments![1].attachment).toBeUndefined();
    expect(
      (await listStagedMessageAttachments(user.id, queued.clientMessageId)).map((r) => r.name),
    ).toEqual(["two.pdf"]);

    // Second reconnect attempt skips file 1, retries ONLY file 2 with the exact same uploadKey, then sends!
    await retryTransientFailures(chatStore.generation);

    expect(uploadSpy).toHaveBeenCalledTimes(3);
    expect(uploadSpy).toHaveBeenNthCalledWith(3, "c-stage", expect.any(File), undefined, key2);
    expect(sendSpy).toHaveBeenCalledWith(
      "c-stage",
      expect.objectContaining({
        clientMessageId: queued.clientMessageId,
        attachmentIds: ["att-1", "att-2"],
      }),
    );
    const confirmed = chatStore.getSnapshot().messages["c-stage"][0];
    expect(confirmed.id).toBe("srv-msg-1");
    expect(confirmed.status).toBeUndefined();
    expect(await listStagedMessageAttachments(user.id, queued.clientMessageId)).toHaveLength(0);
  });

  it("restores staged attachments back to composer on Edit and wipes them on Cancel, revocation, and logout", async () => {
    const rec: StagedAttachmentRecord = {
      id: "st-edit-1",
      uploadKey: "uk-edit-1",
      userId: user.id,
      conversationId: "c-stage",
      clientMessageId: "",
      name: "notes.txt",
      type: "text/plain",
      size: 6,
      lastModified: Date.now(),
      blob: new Blob(["draft!"], { type: "text/plain" }),
      createdAt: Date.now(),
    };
    await stageOfflineAttachment(rec);
    await bindStagedAttachmentsToMessage(user.id, "c-stage", [rec.id], "cmid-edit");

    const queuedMsg: LocalMessage = {
      id: "local:cmid-edit",
      clientMessageId: "cmid-edit",
      conversationId: "c-stage",
      senderId: user.id,
      senderName: user.displayName,
      senderKind: "human",
      seq: Number.MAX_SAFE_INTEGER,
      body: "附带笔记",
      createdAt: "2026-11-27T09:10:00Z",
      status: "queued",
      permanent: false,
      stagedAttachments: [
        {
          id: rec.id,
          uploadKey: rec.uploadKey,
          filename: rec.name,
          mediaType: rec.type,
          sizeBytes: rec.size,
        },
      ],
    };
    chatStore.merge([queuedMsg]);

    const inputRef = createRef<HTMLTextAreaElement>();
    await act(async () => {
      root.render(
        <>
          <textarea ref={inputRef} />
          <MessageSendStatus
            message={queuedMsg}
            conversationId="c-stage"
            inputRef={inputRef}
          />
        </>,
      );
    });
    expect(host.textContent).toContain("等待网络恢复后上传附件并发送");

    // Click "编辑": restores staged blob back to draft (clientMessageId: "") instead of deleting it!
    const editBtn = Array.from(host.querySelectorAll<HTMLButtonElement>("button")).find(
      (b) => b.textContent?.includes("编辑"),
    )!;
    await act(async () => {
      editBtn.click();
    });
    await flush();

    expect(chatStore.getSnapshot().messages["c-stage"] || []).toHaveLength(0);
    expect(readAttachmentDraft("c-stage").map((f) => f.id)).toEqual(["st-edit-1"]);
    expect(await listStagedDraftAttachments(user.id, "c-stage")).toHaveLength(1);

    // Revoking conversation c-revoked prunes its staged IDB blobs automatically.
    await stageOfflineAttachment({
      ...rec,
      id: "st-revoked",
      conversationId: "c-revoked",
      clientMessageId: "",
    });
    expect(await listStagedDraftAttachments(user.id, "c-revoked")).toHaveLength(1);
    chatStore.replaceConversations([conv]);
    await flush();
    expect(await listStagedDraftAttachments(user.id, "c-revoked")).toHaveLength(0);

    // Logout cleanup wipes all remaining staged attachments for this user.
    clearUserLocalUiState(user.id);
    await flush();
    expect(await listStagedDraftAttachments(user.id, "c-stage")).toHaveLength(0);

    // Hydrate respects generation/user and XHR transport blocks unverified offline sessions.
    api.sessionUnverified = true;
    await expect(
      attachmentTransport.upload("c-stage", new File(["a"], "a.txt")),
    ).rejects.toMatchObject({ status: 503, code: "offline_unverified" });

    // Re-hydrating after clear is a clean no-op.
    await hydrateStagedAttachmentDrafts("c-stage");
    expect(readAttachmentDraft("c-stage")).toHaveLength(1); // in-memory until chatStore.clear()
    chatStore.clear();
    expect(readAttachmentDraft("c-stage")).toHaveLength(0);
  });

  it("blocks direct sendMessage when api.sessionUnverified is true and keeps staged message queued", async () => {
    api.sessionUnverified = true;
    const uploadSpy = vi.spyOn(api, "uploadAttachment");
    const sendSpy = vi.spyOn(api, "send");
    await sendMessage(
      {
        id: "local:cmid-unverified",
        clientMessageId: "cmid-unverified",
        conversationId: "c-stage",
        senderId: user.id,
        senderName: user.displayName,
        senderKind: "human",
        seq: Number.MAX_SAFE_INTEGER,
        body: "未验证离线发送",
        createdAt: "2026-11-27T09:15:00Z",
        stagedAttachments: [
          {
            id: "s1",
            uploadKey: "k1",
            filename: "a.txt",
            mediaType: "text/plain",
            sizeBytes: 3,
          },
        ],
      },
      chatStore.generation,
    );
    expect(uploadSpy).not.toHaveBeenCalled();
    expect(sendSpy).not.toHaveBeenCalled();
    expect(chatStore.getSnapshot().messages["c-stage"][0].status).toBe("queued");
  });

  it("deduplicates concurrent draft hydration against an in-flight multi-file staging batch without false overflow", async () => {
    const base = createMemoryOfflineAttachmentBackend();
    let stageCalls = 0;
    let releaseThird!: () => void;
    const thirdGate = new Promise<void>((resolve) => {
      releaseThird = resolve;
    });
    setOfflineAttachmentBackendForTest({
      ...base,
      async stage(record, now) {
        stageCalls += 1;
        if (stageCalls === 3) await thirdGate;
        return base.stage(record, now);
      },
    });

    const uploadSpy = vi
      .spyOn(attachmentTransport, "upload")
      .mockImplementation(async (_convId, file) => ({
        attachment: {
          id: `srv-${file.name}`,
          filename: file.name,
          mediaType: file.type || "application/octet-stream",
          sizeBytes: file.size,
          sha256: "h",
        },
      }));

    let draftApi!: ReturnType<typeof useAttachmentDraft>;
    function Harness({ offline }: { offline: boolean }) {
      draftApi = useAttachmentDraft(
        "c-stage",
        { ...defaultAttachmentLimits, maxFilesPerMessage: 3 },
        offline,
      );
      return (
        <AttachmentDraft
          files={draftApi.files}
          error={draftApi.error}
          onRemove={draftApi.remove}
          onRetry={draftApi.retry}
        />
      );
    }

    await act(async () => {
      root.render(<Harness offline={true} />);
    });
    await flush();

    // Select 3 files (exact per-message limit) while offline; 3rd write pauses.
    await act(async () => {
      draftApi.add([
        new File(["a"], "f1.txt", { type: "text/plain" }),
        new File(["b"], "f2.txt", { type: "text/plain" }),
        new File(["c"], "f3.txt", { type: "text/plain" }),
      ]);
    });
    await flush();

    // Connectivity returns mid-loop: reconnect hydration loads f1 & f2 first.
    await act(async () => {
      root.render(<Harness offline={false} />);
    });
    await flush();
    expect(readAttachmentDraft("c-stage").map((f) => f.file.name)).toEqual([
      "f1.txt",
      "f2.txt",
    ]);

    // Now the 3rd file finishes staging: no duplicates and f3 is NOT dropped as overflow!
    releaseThird();
    await flush();
    await flush();

    const names = readAttachmentDraft("c-stage").map((f) => f.file.name);
    expect(names).toEqual(["f1.txt", "f2.txt", "f3.txt"]);
    expect(draftApi.error).toBe("");
    expect(uploadSpy).toHaveBeenCalledTimes(3);
  });

  it("treats temporary IndexedDB read failures as retryable instead of permanently failing queued sends", async () => {
    const base = createMemoryOfflineAttachmentBackend();
    let failReads = true;
    setOfflineAttachmentBackendForTest({
      ...base,
      async listForMessage(uid, cmid, now) {
        if (failReads) {
          throw new OfflineAttachmentStoreError(
            "storage_unavailable",
            "idb busy",
          );
        }
        return base.listForMessage(uid, cmid, now);
      },
    });

    await stageOfflineAttachment({
      id: "st-idb-err",
      uploadKey: "uk-idb-err",
      userId: user.id,
      conversationId: "c-stage",
      clientMessageId: "cmid-idb-err",
      name: "doc.pdf",
      type: "application/pdf",
      size: 4,
      lastModified: Date.now(),
      blob: new Blob(["pdf!"], { type: "application/pdf" }),
      createdAt: Date.now(),
    });
    const queued: LocalMessage = {
      id: "local:cmid-idb-err",
      clientMessageId: "cmid-idb-err",
      conversationId: "c-stage",
      senderId: user.id,
      senderName: user.displayName,
      senderKind: "human",
      seq: Number.MAX_SAFE_INTEGER,
      body: "重试存储异常",
      createdAt: "2026-11-27T09:20:00Z",
      status: "queued",
      permanent: false,
      stagedAttachments: [
        {
          id: "st-idb-err",
          uploadKey: "uk-idb-err",
          filename: "doc.pdf",
          mediaType: "application/pdf",
          sizeBytes: 4,
        },
      ],
    };
    chatStore.merge([queued]);

    const att: Attachment = {
      id: "att-recovered",
      filename: "doc.pdf",
      mediaType: "application/pdf",
      sizeBytes: 4,
      sha256: "h-rec",
    };
    const uploadSpy = vi
      .spyOn(api, "uploadAttachment")
      .mockResolvedValueOnce({ attachment: att });
    const sendSpy = vi.spyOn(api, "send").mockResolvedValueOnce({
      message: {
        id: "srv-msg-rec",
        conversationId: "c-stage",
        senderId: user.id,
        senderName: user.displayName,
        senderKind: "human",
        clientMessageId: "cmid-idb-err",
        seq: 3,
        body: "重试存储异常",
        attachments: [att],
        createdAt: "2026-11-27T09:21:00Z",
      },
    });

    // First replay hits transient IDB error → retryable failure (permanent: false).
    await retryTransientFailures(chatStore.generation);
    const afterErr = chatStore.getSnapshot().messages["c-stage"][0];
    expect(afterErr.status).toBe("failed");
    expect(afterErr.permanent).toBe(false);
    expect(uploadSpy).not.toHaveBeenCalled();

    // Once storage recovers, automatic replay uploads and sends cleanly!
    failReads = false;
    await retryTransientFailures(chatStore.generation);
    expect(uploadSpy).toHaveBeenCalledTimes(1);
    expect(sendSpy).toHaveBeenCalledTimes(1);
    expect(chatStore.getSnapshot().messages["c-stage"][0].id).toBe("srv-msg-rec");
  });

  it("recovers orphaned message bindings left by a crash before queue persistence on startup hydration", async () => {
    // Simulate a crash after bindStagedAttachmentsToMessage committed but before LocalMessage persisted.
    await stageOfflineAttachment({
      id: "st-orphan",
      uploadKey: "uk-orphan",
      userId: user.id,
      conversationId: "c-stage",
      clientMessageId: "cmid-never-persisted",
      name: "recovered.png",
      type: "image/png",
      size: 5,
      lastModified: Date.now(),
      blob: new Blob(["image"], { type: "image/png" }),
      createdAt: Date.now(),
    });
    // Also store a legitimate queued message binding that MUST stay bound.
    await stageOfflineAttachment({
      id: "st-live",
      uploadKey: "uk-live",
      userId: user.id,
      conversationId: "c-stage",
      clientMessageId: "cmid-persisted",
      name: "queued.png",
      type: "image/png",
      size: 5,
      lastModified: Date.now(),
      blob: new Blob(["valid"], { type: "image/png" }),
      createdAt: Date.now(),
    });
    chatStore.merge([
      {
        id: "local:cmid-persisted",
        clientMessageId: "cmid-persisted",
        conversationId: "c-stage",
        senderId: user.id,
        senderName: user.displayName,
        senderKind: "human",
        seq: Number.MAX_SAFE_INTEGER,
        body: "",
        createdAt: "2026-11-27T09:25:00Z",
        status: "queued",
        permanent: false,
        stagedAttachments: [
          {
            id: "st-live",
            uploadKey: "uk-live",
            filename: "queued.png",
            mediaType: "image/png",
            sizeBytes: 5,
          },
        ],
      },
    ]);

    await hydrateStagedAttachmentDrafts("c-stage", false);

    // Orphaned binding was recovered into the composer draft; live queued binding stayed intact.
    expect(readAttachmentDraft("c-stage").map((f) => f.id)).toEqual(["st-orphan"]);
    expect((await listStagedDraftAttachments(user.id, "c-stage")).map((r) => r.id)).toEqual([
      "st-orphan",
    ]);
    expect(
      (await listStagedMessageAttachments(user.id, "cmid-persisted")).map((r) => r.id),
    ).toEqual(["st-live"]);
  });

  it("reserves queued messages against automatic replay while Edit awaits restoration and rolls back on failure", async () => {
    const base = createMemoryOfflineAttachmentBackend();
    let resolveUnbind!: (rows: StagedAttachmentRecord[]) => void;
    let rejectUnbind!: (err: unknown) => void;
    let mode: "hold" | "reject" = "hold";
    setOfflineAttachmentBackendForTest({
      ...base,
      unbindToDraft(uid, cid, cmid) {
        if (mode === "hold") {
          return new Promise<StagedAttachmentRecord[]>((resolve, reject) => {
            resolveUnbind = (rows) => resolve(rows);
            rejectUnbind = reject;
          });
        }
        return base.unbindToDraft(uid, cid, cmid);
      },
    });

    const att: Attachment = {
      id: "att-preuploaded",
      filename: "ready.png",
      mediaType: "image/png",
      sizeBytes: 1024,
      sha256: "h-pre",
    };
    const queuedMsg: LocalMessage = {
      id: "local:cmid-race",
      clientMessageId: "cmid-race",
      conversationId: "c-stage",
      senderId: user.id,
      senderName: user.displayName,
      senderKind: "human",
      seq: Number.MAX_SAFE_INTEGER,
      body: "正在编辑",
      createdAt: "2026-11-27T09:30:00Z",
      status: "queued",
      permanent: false,
      stagedAttachments: [
        {
          id: "st-race",
          uploadKey: "uk-race",
          filename: "ready.png",
          mediaType: "image/png",
          sizeBytes: 1024,
          attachment: att,
        },
      ],
    };
    chatStore.merge([queuedMsg]);

    const sendSpy = vi.spyOn(api, "send").mockResolvedValue({
      message: {
        id: "srv-race",
        conversationId: "c-stage",
        senderId: user.id,
        senderName: user.displayName,
        senderKind: "human",
        clientMessageId: "cmid-race",
        seq: 4,
        body: "正在编辑",
        attachments: [att],
        createdAt: "2026-11-27T09:31:00Z",
      },
    });

    const inputRef = createRef<HTMLTextAreaElement>();
    await act(async () => {
      root.render(
        <>
          <textarea ref={inputRef} />
          <MessageSendStatus
            message={queuedMsg}
            conversationId="c-stage"
            inputRef={inputRef}
          />
        </>,
      );
    });

    const editBtn = Array.from(host.querySelectorAll<HTMLButtonElement>("button")).find(
      (b) => b.textContent?.includes("编辑"),
    )!;
    // Start Edit (awaits unbindToDraft) and concurrently kick off automatic replay.
    await act(async () => {
      editBtn.click();
    });
    await retryTransientFailures(chatStore.generation);
    expect(sendSpy).not.toHaveBeenCalled();

    // Complete restoration: message is removed from queue and attachment restored without sending!
    resolveUnbind([]);
    await flush();
    await flush();
    expect(sendSpy).not.toHaveBeenCalled();
    expect(chatStore.getSnapshot().messages["c-stage"] || []).toHaveLength(0);
    expect(readAttachmentDraft("c-stage").map((f) => f.attachment?.id)).toEqual([
      "att-preuploaded",
    ]);

    // Rollback test: if unbind fails, reservation releases and message stays queued / replayable.
    const queuedMsg2: LocalMessage = {
      ...queuedMsg,
      id: "local:cmid-rollback",
      clientMessageId: "cmid-rollback",
    };
    chatStore.merge([queuedMsg2]);
    await act(async () => {
      root.render(
        <>
          <textarea ref={inputRef} />
          <MessageSendStatus
            message={queuedMsg2}
            conversationId="c-stage"
            inputRef={inputRef}
          />
        </>,
      );
    });
    const editBtn2 = Array.from(host.querySelectorAll<HTMLButtonElement>("button")).find(
      (b) => b.textContent?.includes("编辑"),
    )!;
    mode = "hold";
    await act(async () => {
      editBtn2.click();
    });
    rejectUnbind(
      new OfflineAttachmentStoreError("storage_unavailable", "idb failed"),
    );
    await flush();
    await flush();
    // Because navigator.onLine is true in jsdom, releasing reservation after rollback triggers replay!
    expect(sendSpy).toHaveBeenCalledTimes(1);
    expect(sendSpy).toHaveBeenCalledWith(
      "c-stage",
      expect.objectContaining({ clientMessageId: "cmid-rollback" }),
    );
  });
});
