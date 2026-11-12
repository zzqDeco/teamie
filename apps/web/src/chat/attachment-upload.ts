import { api, ApiError, IDL_ROUTES, routePath } from "@teamie/api-client";
import type { Attachment } from "@teamie/api-client";

export type UploadProgress = (fraction: number) => void;
type UploadResult = { attachment: Attachment };

function retryAfterMs(header: string | null) {
  if (!header) return undefined;
  const value = /^\d+(?:\.\d+)?$/.test(header)
    ? Number(header) * 1000
    : Date.parse(header) - Date.now();
  return Number.isFinite(value) ? Math.max(0, value) : undefined;
}

/**
 * XMLHttpRequest version of `api.uploadAttachment` that reports upload
 * progress (fetch cannot). It mirrors the api-client transport semantics:
 * same-origin cookie + `X-CSRF-Token`, optional `Idempotency-Key`, ApiError
 * mapping with Retry-After, session-change/401/403 hooks and the stale-session
 * check after success.
 */
function xhrUpload(
  conversationId: string,
  file: File,
  signal: AbortSignal | undefined,
  idempotencyKey: string | undefined,
  onProgress: UploadProgress | undefined,
): Promise<UploadResult> {
  const sessionToken = api.csrfToken;
  return new Promise<UploadResult>((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException("Aborted", "AbortError"));
      return;
    }
    const xhr = new XMLHttpRequest();
    const path = routePath(IDL_ROUTES.UploadAttachment, { id: conversationId });
    xhr.open(IDL_ROUTES.UploadAttachment.method, `/api/v1${path}`);
    xhr.withCredentials = true;
    xhr.responseType = "text";
    if (sessionToken) xhr.setRequestHeader("X-CSRF-Token", sessionToken);
    if (idempotencyKey) xhr.setRequestHeader("Idempotency-Key", idempotencyKey);
    const abort = () => xhr.abort();
    signal?.addEventListener("abort", abort, { once: true });
    const cleanup = () => signal?.removeEventListener("abort", abort);
    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable && event.total > 0) onProgress?.(Math.min(1, event.loaded / event.total));
    };
    xhr.onabort = () => {
      cleanup();
      reject(new DOMException("Aborted", "AbortError"));
    };
    xhr.onerror = () => {
      cleanup();
      reject(new TypeError("Failed to fetch"));
    };
    xhr.onload = () => {
      cleanup();
      let data: { attachment?: Attachment; error?: { code?: string; message?: string } } = {};
      try {
        data = xhr.responseText ? JSON.parse(xhr.responseText) : {};
      } catch {
        data = {};
      }
      const current = sessionToken === api.csrfToken;
      if (xhr.status < 200 || xhr.status >= 300) {
        const code = data.error?.code;
        const sessionChanged =
          code === "session_changed" ||
          (xhr.status === 403 && (code === "csrf_failed" || code === "invalid_csrf"));
        if (current && sessionChanged) api.onSessionChanged?.();
        if (current && xhr.status === 401 && !sessionChanged) api.onUnauthorized?.();
        if (current && xhr.status === 403 && !sessionChanged) api.onForbidden?.();
        reject(
          new ApiError(
            xhr.status,
            code ?? "request_failed",
            data.error?.message ?? `请求失败 (${xhr.status})`,
            retryAfterMs(xhr.getResponseHeader("Retry-After")),
          ),
        );
        return;
      }
      if (!current) {
        reject(new ApiError(409, "stale_session", "账号已变化，请重试。"));
        return;
      }
      if (!data.attachment) {
        reject(new ApiError(xhr.status, "request_failed", "上传响应无效"));
        return;
      }
      onProgress?.(1);
      resolve({ attachment: data.attachment });
    };
    const form = new FormData();
    form.append("file", file, file.name);
    xhr.send(form);
  });
}

/**
 * Upload transport used by attachment drafts. A mutable object so tests can
 * `vi.spyOn(attachmentTransport, "upload")`; falls back to the api-client
 * fetch upload (no progress) where XMLHttpRequest is unavailable.
 */
export const attachmentTransport = {
  upload(
    conversationId: string,
    file: File,
    signal?: AbortSignal,
    idempotencyKey?: string,
    onProgress?: UploadProgress,
  ): Promise<UploadResult> {
    if (typeof XMLHttpRequest === "undefined") {
      return api.uploadAttachment(conversationId, file, signal, idempotencyKey);
    }
    return xhrUpload(conversationId, file, signal, idempotencyKey, onProgress);
  },
};
