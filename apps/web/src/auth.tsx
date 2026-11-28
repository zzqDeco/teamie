import {
  createContext,
  startTransition,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
} from "react";
import type { ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { api, ApiError } from "@teamie/api-client";
import type { Session, User } from "@teamie/api-client";
import { chatStore } from "./store";
import { clearAttachmentPreviews } from "./attachment-previews";
import { discardAttachmentDrafts } from "./attachment-drafts";
import { isNetworkError } from "./errors";
import { syncAppBadge } from "./pwa/badge";
import {
  clearStagedAttachmentsExceptUser,
  clearStagedAttachmentsForUser,
} from "./pwa/offline-attachments";
import {
  clearOfflineShellSnapshot,
  clearPendingOfflineLogout,
  markPendingOfflineLogout,
  readOfflineShellSnapshot,
  readPendingOfflineLogout,
  saveOfflineUser,
} from "./pwa/offline-cache";
import { markSessionExpired } from "./session-notice";

type Auth = {
  user: User | null;
  loading: boolean;
  offlineMode: boolean;
  error: string;
  setSession: (session: Session) => void;
  updateUser: (user: User) => void;
  logout: () => Promise<void>;
  changePassword: (currentPassword: string, newPassword: string) => Promise<void>;
  resetPassword: (token: string, newPassword: string, accountId: string) => Promise<void>;
  captureSession: () => { subject: string | null; isCurrent: () => boolean };
  reconcileSession: () => void;
  retry: () => void;
};
const AuthContext = createContext<Auth | null>(null);
export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);
  const [offlineMode, setOfflineMode] = useState(false);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const generation = useRef(0);
  const subject = useRef<string | null>(null);
  const offlineReconciling = useRef(false);
  const pendingLogoutInFlight = useRef(false);
  const client = useQueryClient();
  const navigate = useNavigate();
  useEffect(() => () => { api.sessionUnverified = false; }, []);
  const reset = useCallback(() => {
    clearAttachmentPreviews();
    clearOfflineShellSnapshot();
    void clearStagedAttachmentsForUser();
    syncAppBadge(0);
    generation.current += 1;
    subject.current = null;
    api.csrfToken = "";
    api.sessionUnverified = false;
    chatStore.clear();
    client.clear();
    setUser(null);
    setOfflineMode(false);
    setError("");
    setLoading(false);
  }, [client]);
  const setSession = useCallback(
    (session: Session) => {
      clearAttachmentPreviews();
      clearPendingOfflineLogout();
      void clearStagedAttachmentsExceptUser(session.user.id);
      generation.current += 1;
      subject.current = session.user.id;
      client.clear();
      chatStore.begin(session.user.id);
      api.csrfToken = session.csrfToken;
      api.sessionUnverified = false;
      saveOfflineUser(session.user);
      setUser(session.user);
      setOfflineMode(false);
      setError("");
      setLoading(false);
    },
    [client],
  );
  const updateUser = useCallback((nextUser: User) => {
    if (subject.current !== nextUser.id) return;
    saveOfflineUser(nextUser);
    setUser((current) => (current?.id === nextUser.id ? nextUser : current));
  }, []);
  const captureSession = useCallback(() => {
    const requestGeneration = generation.current;
    const requestSubject = subject.current;
    return {
      subject: requestSubject,
      isCurrent: () =>
        requestGeneration === generation.current &&
        requestSubject === subject.current,
    };
  }, []);
  const reconcileSession = useCallback(() => {
    // Set-Cookie is applied before an auth promise settles, even when its form
    // unmounted. Discard account-bound state before discovering that cookie.
    reset();
    setLoading(true);
    setAttempt((n) => n + 1);
  }, [reset]);
  const expire = useCallback(() => {
    if (subject.current) markSessionExpired();
    reset();
  }, [reset]);

  const verifyOfflineSession = useCallback(
    (onTransientFailure?: () => void) => {
      if (offlineReconciling.current) return;
      const requestGeneration = generation.current;
      const expectedUserId = subject.current;
      if (!expectedUserId) return;
      offlineReconciling.current = true;
      void api
        .session()
        .then((session) => {
          if (requestGeneration !== generation.current) return;
          if (session.user.id === expectedUserId) {
            api.csrfToken = session.csrfToken;
            api.sessionUnverified = false;
            saveOfflineUser(session.user);
            setUser(session.user);
            setOfflineMode(false);
            setError("");
          } else {
            setSession(session);
          }
        })
        .catch((err) => {
          if (requestGeneration !== generation.current) return;
          if (err instanceof ApiError && (err.status === 401 || err.status === 403)) {
            expire();
            return;
          }
          onTransientFailure?.();
        })
        .finally(() => {
          offlineReconciling.current = false;
        });
    },
    [expire, setSession],
  );

  const completePendingLogout = useCallback(
    async (session: Session, isCurrent: () => boolean) => {
      if (pendingLogoutInFlight.current) return;
      pendingLogoutInFlight.current = true;
      api.csrfToken = session.csrfToken;
      try {
        await api.logout();
        if (isCurrent()) {
          clearPendingOfflineLogout();
          reset();
        }
      } catch (err) {
        if (!isCurrent()) return;
        if (err instanceof ApiError && err.status === 401) {
          clearPendingOfflineLogout();
        }
        // Keep the local session cleared even if revocation must be retried.
        reset();
      } finally {
        pendingLogoutInFlight.current = false;
      }
    },
    [reset],
  );

  useEffect(() => {
    let active = true;
    const requestGeneration = generation.current;
    const isCurrent = () => active && requestGeneration === generation.current;
    api.onUnauthorized = expire;
    api.onSessionChanged = reconcileSession;
    setLoading(true);
    setError("");
    api
      .session()
      .then(async (session) => {
        if (!isCurrent()) return;
        const pendingLogout = readPendingOfflineLogout();
        if (pendingLogout && pendingLogout.userId === session.user.id) {
          await completePendingLogout(session, isCurrent);
          return;
        }
        setSession(session);
      })
      .catch((e) => {
        if (!isCurrent()) return;
        if (e instanceof ApiError && (e.status === 401 || e.status === 403)) {
          clearPendingOfflineLogout();
          clearOfflineShellSnapshot();
          void clearStagedAttachmentsForUser();
          chatStore.clear();
          syncAppBadge(0);
          setOfflineMode(false);
          return;
        }
        if (readPendingOfflineLogout()) {
          clearOfflineShellSnapshot();
          void clearStagedAttachmentsForUser();
          chatStore.clear();
          syncAppBadge(0);
          setOfflineMode(false);
          return;
        }
        const offline =
          isNetworkError(e) ||
          (typeof navigator !== "undefined" && navigator.onLine === false);
        if (offline) {
          const cached = readOfflineShellSnapshot();
          if (cached) {
            generation.current += 1;
            subject.current = cached.user.id;
            api.csrfToken = "";
            api.sessionUnverified = true;
            chatStore.begin(cached.user.id);
            if (
              Object.keys(chatStore.getSnapshot().conversations).length === 0 &&
              cached.conversations.length > 0
            ) {
              for (const conv of cached.conversations) {
                chatStore.addConversation(conv);
              }
            }
            setUser(cached.user);
            setOfflineMode(true);
            setError("");
            setLoading(false);
            return;
          }
        }
        setError("暂时无法连接 Teamie，请检查网络后重试。");
      })
      .finally(() => {
        if (isCurrent()) setLoading(false);
      });
    return () => {
      active = false;
      if (api.onUnauthorized === expire) api.onUnauthorized = undefined;
      if (api.onSessionChanged === reconcileSession)
        api.onSessionChanged = undefined;
    };
  }, [attempt, completePendingLogout, expire, reconcileSession, setSession]);

  useEffect(() => {
    if (user || !readPendingOfflineLogout()) return;
    let active = true;
    const drainPendingLogout = () => {
      const pending = readPendingOfflineLogout();
      if (!pending || subject.current) return;
      const requestGeneration = generation.current;
      const isCurrent = () =>
        active && requestGeneration === generation.current && !subject.current;
      void api
        .session()
        .then(async (session) => {
          if (!isCurrent()) return;
          if (session.user.id === pending.userId) {
            await completePendingLogout(session, isCurrent);
          } else {
            clearPendingOfflineLogout();
          }
        })
        .catch((err) => {
          if (err instanceof ApiError && err.status === 401) {
            clearPendingOfflineLogout();
          }
        });
    };
    window.addEventListener("online", drainPendingLogout);
    return () => {
      active = false;
      window.removeEventListener("online", drainPendingLogout);
    };
  }, [completePendingLogout, user]);

  useEffect(() => {
    if (!offlineMode) return;
    let cancelled = false;
    let retryAttempt = 0;
    let retryTimer: ReturnType<typeof setTimeout> | undefined;

    const clearTimer = () => {
      if (retryTimer !== undefined) {
        clearTimeout(retryTimer);
        retryTimer = undefined;
      }
    };

    const scheduleRetry = () => {
      clearTimer();
      if (
        cancelled ||
        (typeof navigator !== "undefined" && navigator.onLine === false)
      ) {
        return;
      }
      const delayMs = Math.min(30_000, 2_000 * 2 ** Math.min(retryAttempt, 4));
      retryAttempt += 1;
      retryTimer = setTimeout(() => {
        retryTimer = undefined;
        if (
          cancelled ||
          (typeof navigator !== "undefined" && navigator.onLine === false)
        ) {
          return;
        }
        verifyOfflineSession(scheduleRetry);
      }, delayMs);
    };

    const triggerNow = () => {
      if (
        cancelled ||
        (typeof navigator !== "undefined" && navigator.onLine === false)
      ) {
        return;
      }
      retryAttempt = 0;
      clearTimer();
      verifyOfflineSession(scheduleRetry);
    };

    const onOffline = () => clearTimer();
    const onVisibility = () => {
      if (document.visibilityState === "visible") triggerNow();
    };

    // If navigator already reports online (e.g. initial transient server error
    // during warm offline boot), schedule bounded automatic verification retries.
    if (typeof navigator === "undefined" || navigator.onLine !== false) {
      scheduleRetry();
    }

    window.addEventListener("online", triggerNow);
    window.addEventListener("offline", onOffline);
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      cancelled = true;
      clearTimer();
      window.removeEventListener("online", triggerNow);
      window.removeEventListener("offline", onOffline);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [offlineMode, verifyOfflineSession]);

  const logout = async () => {
    const request = captureSession();
    if (offlineMode && !api.csrfToken) {
      if (request.subject) markPendingOfflineLogout(request.subject);
      reset();
      return;
    }
    // Delete pending uploads while this session can still authorize it.
    await discardAttachmentDrafts();
    if (!request.isCurrent()) return;
    try {
      await api.logout();
    } catch (err) {
      if (
        request.isCurrent() &&
        request.subject &&
        (isNetworkError(err) ||
          (typeof navigator !== "undefined" && navigator.onLine === false))
      ) {
        markPendingOfflineLogout(request.subject);
        reset();
        return;
      }
      throw err;
    }
    if (!request.isCurrent()) return;
    clearPendingOfflineLogout();
    reset();
  };
  const changePassword = async (
    currentPassword: string,
    newPassword: string,
  ) => {
    const requestGeneration = generation.current;
    const requestSubject = subject.current;
    await api.changePassword(currentPassword, newPassword);
    if (
      requestGeneration !== generation.current ||
      requestSubject !== subject.current
    )
      return;
    // Update the auth guard and route together so its redirect cannot win.
    startTransition(() => {
      reset();
      navigate("/login?passwordChanged=1", { replace: true });
    });
  };
  const resetPassword = async (
    token: string,
    newPassword: string,
    accountId: string,
  ) => {
    const request = captureSession();
    await api.resetPassword(token, newPassword);
    // The reset endpoint never writes a cookie. Only discard the captured,
    // revoked local session; a newer login (even for this account) survives.
    if (request.subject === accountId && request.isCurrent()) reset();
  };
  const retry = useCallback(() => {
    if (offlineMode) {
      verifyOfflineSession();
    } else {
      setAttempt((n) => n + 1);
    }
  }, [offlineMode, verifyOfflineSession]);
  return (
    <AuthContext.Provider
      value={{
        user,
        loading,
        offlineMode,
        error,
        setSession,
        updateUser,
        logout,
        changePassword,
        resetPassword,
        captureSession,
        reconcileSession,
        retry,
      }}
    >
      {children}
    </AuthContext.Provider>
  );
}
export function useAuth() {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("Auth provider missing");
  return ctx;
}
