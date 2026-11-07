import { Component, Suspense } from "react";
import type { ReactNode } from "react";
import { useLocation } from "react-router-dom";
import { LoaderCircle, TriangleAlert } from "lucide-react";
import { Button, Logo } from "./ui";
import { ICON } from "./icons";

type BoundaryProps = {
  children: ReactNode;
  fullPage?: boolean;
  resetKey: string;
};

class RouteErrorBoundary extends Component<BoundaryProps, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  componentDidUpdate(previous: BoundaryProps) {
    if (this.state.failed && previous.resetKey !== this.props.resetKey) {
      this.setState({ failed: false });
    }
  }

  render() {
    if (!this.state.failed) return this.props.children;
    return (
      <div className={`route-state ${this.props.fullPage ? "route-state-page" : ""}`} role="alert">
        <TriangleAlert size={ICON.xl} aria-hidden="true" />
        <h2>页面暂时未能加载</h2>
        <p>请检查网络连接并重新加载，当前网址会保留。</p>
        <Button onClick={() => window.location.reload()}>重新加载</Button>
      </div>
    );
  }
}

export function RouteBoundary({ children, fullPage = false }: {
  children: ReactNode;
  fullPage?: boolean;
}) {
  const location = useLocation();
  return (
    <RouteErrorBoundary resetKey={location.pathname} fullPage={fullPage}>
      <Suspense fallback={
        <div className={`route-state ${fullPage ? "route-state-page" : ""}`} role="status" aria-live="polite">
          {fullPage && <Logo />}
          <LoaderCircle className="spin" size={ICON.lg} aria-hidden="true" />
          <p>正在加载页面…</p>
          {!fullPage && <div className="route-loading-lines" aria-hidden="true"><span /><span /><span /></div>}
        </div>
      }>
        {children}
      </Suspense>
    </RouteErrorBoundary>
  );
}
