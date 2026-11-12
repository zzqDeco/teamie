import "./layers.css";
import { StrictMode, useEffect } from "react";
import type { ReactNode } from "react";
import { createRoot } from "react-dom/client";
import {
  BrowserRouter,
  Link,
  Navigate,
  Route,
  Routes,
  useLocation,
} from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AuthProvider, useAuth } from "./auth";
import { lazyView } from "./lazy-view";
import { RouteBoundary } from "./route-boundary";
import { applyTheme, useThemePreference } from "./theme";
import { Button, Loading, Logo, Notice } from "./ui";
import { Toaster } from "./toast";
import "@fontsource-variable/dm-sans";
import "@fontsource-variable/noto-sans-sc";
import "./tokens.css";
import "./styles.css";
import "./layout.css";
import "./styles/password-recovery.css";
import "./styles/feedback.css";
import "./styles/conveniences.css";
applyTheme();
const loadAuthenticatedApp = () =>
  import("./authenticated-app").then((m) => ({ default: m.AuthenticatedApp }));
if (
  typeof window !== "undefined" &&
  window.location.pathname.startsWith("/app")
) {
  void loadAuthenticatedApp();
}
const AuthenticatedApp = lazyView(loadAuthenticatedApp);
const Landing = lazyView(() =>
  import("./public").then((m) => ({ default: m.Landing })),
);
const Login = lazyView(() =>
  import("./public/auth-views").then((m) => ({ default: m.Login })),
);
const Join = lazyView(() =>
  import("./public/auth-views").then((m) => ({ default: m.Join })),
);
const ForgotPassword = lazyView(() =>
  import("./password-recovery").then((m) => ({ default: m.ForgotPassword })),
);
const ResetPassword = lazyView(() =>
  import("./password-recovery").then((m) => ({ default: m.ResetPassword })),
);
const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: true } },
});
function Protected({ children }: { children: ReactNode }) {
  const { user, loading, error, retry } = useAuth();
  const location = useLocation();
  if (loading) return <Loading />;
  if (error)
    return (
      <div className="loading-screen">
        <Logo />
        <Notice>{error}</Notice>
        <Button onClick={retry}>重试连接</Button>
        <Link to="/">回到首页</Link>
      </div>
    );
  if (!user)
    return (
      <Navigate
        to={`/login?next=${encodeURIComponent(location.pathname + location.search)}`}
        replace
      />
    );
  return children;
}
function App() {
  useThemePreference();
  const { user } = useAuth();
  useEffect(() => {
    if (user) {
      void loadAuthenticatedApp();
    }
  }, [user]);
  return (
    <RouteBoundary fullPage>
      <Routes>
        <Route path="/" element={<Landing />} />
        <Route path="/login" element={<Login />} />
        <Route path="/forgot-password" element={<ForgotPassword />} />
        <Route path="/reset-password" element={<ResetPassword />} />
        <Route path="/join/:inviteCode" element={<Join />} />
        <Route
          path="/developers"
          element={<Navigate to="/app/developers" replace />}
        />
        <Route
          path="/developers/docs"
          element={<Navigate to="/app/developers/docs" replace />}
        />
        <Route
          path="/app/*"
          element={
            <Protected>
              <AuthenticatedApp />
            </Protected>
          }
        />
        <Route
          path="*"
          element={
            <div className="not-found">
              <Logo />
              <h1>好像走岔了。</h1>
              <p>这个页面不存在，回到你的工作空间继续聊吧。</p>
              <Link className="button" to="/app">
                回到工作空间
              </Link>
            </div>
          }
        />
      </Routes>
    </RouteBoundary>
  );
}
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <AuthProvider>
          <App />
          <Toaster />
        </AuthProvider>
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
);
