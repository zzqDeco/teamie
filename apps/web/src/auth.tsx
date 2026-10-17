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

type Auth = {
  user: User | null;
  loading: boolean;
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
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const generation = useRef(0);
  const subject = useRef<string | null>(null);
  const client = useQueryClient();
  const navigate = useNavigate();
  const reset = useCallback(() => {
    generation.current += 1;
    subject.current = null;
    api.csrfToken = "";
    chatStore.clear();
    client.clear();
    setUser(null);
    setError("");
    setLoading(false);
  }, [client]);
  const setSession = useCallback(
    (session: Session) => {
      generation.current += 1;
      subject.current = session.user.id;
      client.clear();
      chatStore.begin(session.user.id);
      api.csrfToken = session.csrfToken;
      setUser(session.user);
      setError("");
      setLoading(false);
    },
    [client],
  );
  const updateUser = useCallback((nextUser: User) => {
    if (subject.current !== nextUser.id) return;
    setUser((current) => current?.id === nextUser.id ? nextUser : current);
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
  useEffect(() => {
    let active = true;
    const requestGeneration = generation.current;
    const isCurrent = () => active && requestGeneration === generation.current;
    api.onUnauthorized = reset;
    api.onSessionChanged = reconcileSession;
    setLoading(true);
    setError("");
    api
      .session()
      .then((session) => {
        if (isCurrent()) setSession(session);
      })
      .catch((e) => {
        if (isCurrent() && !(e instanceof ApiError && e.status === 401))
          setError("暂时无法连接 Teamie，请检查网络后重试。");
      })
      .finally(() => {
        if (isCurrent()) setLoading(false);
      });
    return () => {
      active = false;
      if (api.onUnauthorized === reset) api.onUnauthorized = undefined;
      if (api.onSessionChanged === reconcileSession) api.onSessionChanged = undefined;
    };
  }, [attempt, reconcileSession, reset, setSession]);
  const logout = async () => {
    const requestGeneration = generation.current;
    const requestSubject = subject.current;
    await api.logout();
    if (
      requestGeneration !== generation.current ||
      requestSubject !== subject.current
    )
      return;
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
  return (
    <AuthContext.Provider
      value={{
        user,
        loading,
        error,
        setSession,
        updateUser,
        logout,
        changePassword,
        resetPassword,
        captureSession,
        reconcileSession,
        retry: () => setAttempt((n) => n + 1),
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
