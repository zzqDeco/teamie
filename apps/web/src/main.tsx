import { StrictMode } from "react";
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
import { ChatProvider } from "./chat";
import { Landing, Login, Join } from "./public";
import { ForgotPassword, ResetPassword } from "./password-recovery";
import { Shell } from "./shell";
import { applyTheme, useThemePreference } from "./theme";
import { Button, Loading, Logo, Notice } from "./ui";
import { Toaster } from "./toast";
import "@fontsource-variable/dm-sans";
import "./tokens.css";
import "./styles.css";
import "./layout.css";
import "./password-recovery.css";
import "./feedback.css";
applyTheme();
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
  return (
    <Routes>
      <Route path="/" element={<Landing />} />
      <Route path="/login" element={<Login />} />
      <Route path="/forgot-password" element={<ForgotPassword />} />
      <Route path="/reset-password" element={<ResetPassword />} />
      <Route path="/join/:inviteCode" element={<Join />} />
      <Route
        path="/app/*"
        element={
          <Protected>
            <ChatProvider>
              <Shell />
            </ChatProvider>
          </Protected>
        }
      />
      <Route
        path="*"
        element={
          <div className="not-found">
            <Logo />
            <h1>好像走岔了。</h1>
            <p>这个页面不存在，回到你的团队继续聊吧。</p>
            <Link className="button" to="/app">
              回到工作空间
            </Link>
          </div>
        }
      />
    </Routes>
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
