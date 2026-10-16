import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
} from "react";
import type { ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
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
  retry: () => void;
};
const AuthContext = createContext<Auth | null>(null);
export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const client = useQueryClient();
  const reset = useCallback(() => {
    api.csrfToken = "";
    chatStore.clear();
    client.clear();
    setUser(null);
  }, [client]);
  const setSession = useCallback(
    (session: Session) => {
      client.clear();
      chatStore.begin(session.user.id);
      api.csrfToken = session.csrfToken;
      setUser(session.user);
      setError("");
      setLoading(false);
    },
    [client],
  );
  useEffect(() => {
    let active = true;
    api.onUnauthorized = reset;
    setLoading(true);
    setError("");
    api
      .session()
      .then((session) => {
        if (active) setSession(session);
      })
      .catch((e) => {
        if (active && !(e instanceof ApiError && e.status === 401))
          setError("暂时无法连接 Teamie，请检查网络后重试。");
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
      api.onUnauthorized = undefined;
    };
  }, [attempt, reset, setSession]);
  const logout = async () => {
    await api.logout();
    reset();
  };
  return (
    <AuthContext.Provider
      value={{
        user,
        loading,
        error,
        setSession,
        updateUser: setUser,
        logout,
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
