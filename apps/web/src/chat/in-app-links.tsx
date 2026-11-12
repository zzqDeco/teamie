import { useEffect } from "react";
import { useNavigate } from "react-router-dom";

/**
 * Message links to this app (`a[data-app-link]`, same-origin `/app/...`)
 * navigate inside the SPA instead of reloading or opening a new tab.
 * Modified clicks (new tab/window) keep the browser default.
 */
export function InAppLinkNavigator() {
  const navigate = useNavigate();
  useEffect(() => {
    const onClick = (event: MouseEvent) => {
      if (
        event.defaultPrevented ||
        event.button !== 0 ||
        event.metaKey ||
        event.ctrlKey ||
        event.shiftKey ||
        event.altKey
      )
        return;
      const anchor = (event.target as Element | null)?.closest?.("a[data-app-link]");
      const path = anchor?.getAttribute("data-app-link");
      if (!path || !(path === "/app" || path.startsWith("/app/"))) return;
      event.preventDefault();
      navigate(path);
    };
    document.addEventListener("click", onClick);
    return () => document.removeEventListener("click", onClick);
  }, [navigate]);
  return null;
}
