import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { RefreshCw, WifiOff } from "lucide-react";
import type { Connection } from "./chat";
import { ICON } from "./icons";
import "./styles/connection-banner.css";

type Shown = "offline" | "connecting" | "reconnecting" | "restored" | null;

const MESSAGES: Record<Exclude<Shown, null>, string> = {
  offline: "网络已断开，恢复后会自动同步消息。",
  connecting: "正在连接…",
  reconnecting: "正在重新连接…",
  restored: "已重新连接",
};

// Shell-level connection feedback, overlaid on the main view so it never
// shifts layout. Offline shows at once; (re)connecting only after 3 seconds so
// brief socket blips stay quiet; recovery confirms for 2s. The first connection
// of a session reads 「正在连接」, later ones 「正在重新连接」. A persistent
// visually hidden live region (outside the possibly inert main view) announces
// every state change.
export function ConnectionBanner({
  connection,
  syncError,
  showSyncError,
  onRetry,
}: {
  connection: Connection;
  syncError: string;
  showSyncError: boolean;
  onRetry: () => void;
}) {
  const [shown, setShown] = useState<Shown>(null);
  const hasConnectedOnce = useRef(connection === "connected");
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    if (connection === "offline") timer = setTimeout(() => setShown("offline"), 0);
    else if (connection === "connected") {
      hasConnectedOnce.current = true;
      timer = setTimeout(
        () =>
          setShown((old) =>
            old === "offline" || old === "reconnecting" ? "restored" : old === "connecting" ? null : old,
          ),
        0,
      );
    } else
      timer = setTimeout(
        () => setShown(hasConnectedOnce.current ? "reconnecting" : "connecting"),
        3000,
      );
    return () => clearTimeout(timer);
  }, [connection]);
  useEffect(() => {
    if (shown !== "restored") return;
    const timer = setTimeout(() => setShown(null), 2000);
    return () => clearTimeout(timer);
  }, [shown]);
  const syncVisible = showSyncError && !!syncError && connection === "connected";
  const kind = shown || (syncVisible ? "sync" : null);
  const message = shown ? MESSAGES[shown] : syncVisible ? syncError : "";
  return (
    <>
      {/* Portaled so the region stays announced while <main> is inert. */}
      {createPortal(
        <div className="sr-only connection-live-region" role="status" aria-live="polite">
          {message}
        </div>,
        document.body,
      )}
      {kind && (
        <div className={`connection-banner ${kind}`}>
          {kind === "offline" ? (
            <WifiOff size={ICON.sm} aria-hidden="true" />
          ) : kind !== "restored" ? (
            <RefreshCw
              size={ICON.sm}
              className={kind === "sync" ? undefined : "spin"}
              aria-hidden="true"
            />
          ) : null}
          <span aria-hidden="true">{message}</span>
          {kind !== "offline" && kind !== "restored" && (
            <button type="button" onClick={onRetry}>
              立即重试
            </button>
          )}
        </div>
      )}
    </>
  );
}
