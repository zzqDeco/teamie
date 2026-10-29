import {
  IDL_ROUTES,
  type RealtimeClientFrame,
  type RealtimeServerFrame,
} from "./generated";

export class ApiError extends Error {
  status: number;
  code: string;
  retryAfterMs?: number;
  constructor(
    status: number,
    code: string,
    message: string,
    retryAfterMs?: number,
  ) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.retryAfterMs = retryAfterMs;
  }
}

export const part = encodeURIComponent;

export function routePath(
  route: { clientPath: string },
  params?: Record<string, string>,
): string {
  if (!params) return route.clientPath;
  return route.clientPath.replace(/:([a-zA-Z0-9_]+)/g, (_, key: string) =>
    part(params[key] ?? ""),
  );
}

export interface HttpTransportState {
  csrfToken: string;
  onUnauthorized?: () => void;
  onForbidden?: () => void;
  onSessionChanged?: () => void;
}

export async function executeTransportRequest<T>(
  state: HttpTransportState,
  path: string,
  options: RequestInit = {},
  responseType: "json" | "blob" = "json",
  authentication: "session" | "capability" = "session",
): Promise<T> {
  const sessionToken = state.csrfToken;
  const headers = new Headers(options.headers);
  if (options.body && !(options.body instanceof FormData))
    headers.set("Content-Type", "application/json");
  // Bind protected reads to the same browser session as writes. Session
  // discovery deliberately remains unbound so a changed cookie can recover.
  if (
    authentication === "session" &&
    path !== IDL_ROUTES.GetSession.clientPath &&
    sessionToken
  )
    headers.set("X-CSRF-Token", sessionToken);
  const response = await fetch(`/api/v1${path}`, {
    ...options,
    headers,
    credentials: authentication === "session" ? "same-origin" : "omit",
    cache: "no-store",
  });
  if (!response.ok) {
    const data = await response.json().catch(() => ({}));
    const current =
      authentication === "session" && sessionToken === state.csrfToken;
    const sessionChanged =
      data.error?.code === "session_changed" ||
      (response.status === 403 &&
        ["csrf_failed", "invalid_csrf"].includes(data.error?.code));
    if (current && sessionChanged) state.onSessionChanged?.();
    if (
      authentication === "session" &&
      sessionToken === state.csrfToken &&
      response.status === 401 &&
      !sessionChanged &&
      !path.startsWith(IDL_ROUTES.Login.clientPath) &&
      !path.startsWith(IDL_ROUTES.Register.clientPath)
    )
      state.onUnauthorized?.();
    if (
      authentication === "session" &&
      sessionToken === state.csrfToken &&
      response.status === 403 &&
      /\/(conversations|workspaces|sync)/.test(path) &&
      !sessionChanged
    )
      state.onForbidden?.();
    const retryAfter = response.headers?.get("Retry-After");
    const retryAfterMs = retryAfter
      ? /^\d+(?:\.\d+)?$/.test(retryAfter)
        ? Number(retryAfter) * 1000
        : Date.parse(retryAfter) - Date.now()
      : undefined;
    throw new ApiError(
      response.status,
      data.error?.code ?? "request_failed",
      data.error?.message ?? `请求失败 (${response.status})`,
      retryAfterMs !== undefined && Number.isFinite(retryAfterMs)
        ? Math.max(0, retryAfterMs)
        : undefined,
    );
  }
  const value =
    response.status === 204
      ? undefined
      : responseType === "blob"
        ? await response.blob()
        : await response.json();
  if (
    response.status !== 204 &&
    authentication === "session" &&
    !/^\/auth\/(?:login|register|session)$/.test(path) &&
    sessionToken !== state.csrfToken
  )
    throw new ApiError(409, "stale_session", "账号已变化，请重试。");
  return value as T;
}

export function encodeClientFrame(frame: RealtimeClientFrame): string {
  return JSON.stringify(frame);
}

export function validateServerFrame(raw: unknown): RealtimeServerFrame | null {
  if (!raw || typeof raw !== "object") return null;
  const candidate = raw as Record<string, unknown>;
  if (typeof candidate.type !== "string" || !candidate.type) return null;
  return candidate as unknown as RealtimeServerFrame;
}
