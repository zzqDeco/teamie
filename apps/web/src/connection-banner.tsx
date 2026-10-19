import { useEffect, useState } from "react";
import { RefreshCw, WifiOff } from "lucide-react";
import type { Connection } from "./chat";
import "./connection-banner.css";

type Shown = "offline" | "reconnecting" | "restored" | null;

// Shell-level connection feedback. Offline shows at once; reconnecting only
// after 3 seconds so brief socket blips stay quiet; recovery confirms for 2s.
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
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    if (connection === "offline") timer = setTimeout(() => setShown("offline"), 0);
    else if (connection === "connected")
      timer = setTimeout(
        () =>
          setShown((old) =>
            old === "offline" || old === "reconnecting" ? "restored" : old,
          ),
        0,
      );
    else timer = setTimeout(() => setShown("reconnecting"), 3000);
    return () => clearTimeout(timer);
  }, [connection]);
  useEffect(() => {
    if (shown !== "restored") return;
    const timer = setTimeout(() => setShown(null), 2000);
    return () => clearTimeout(timer);
  }, [shown]);
  const syncVisible = showSyncError && !!syncError && connection === "connected";
  if (!shown && !syncVisible) return null;
  const kind = shown === "restored" ? "restored" : shown || "sync";
  return (
    <div className={`connection-banner ${kind}`} role="status" aria-live="polite">
      {shown === "offline" ? (
        <>
          <WifiOff size={15} />
          <span>网络已断开，恢复后会自动同步消息。</span>
        </>
      ) : shown === "reconnecting" ? (
        <>
          <RefreshCw size={15} className="spin" />
          <span>正在重新连接…</span>
          <button type="button" onClick={onRetry}>
            立即重试
          </button>
        </>
      ) : shown === "restored" ? (
        <span>已重新连接</span>
      ) : (
        <>
          <RefreshCw size={15} />
          <span>{syncError}</span>
          <button type="button" onClick={onRetry}>
            立即重试
          </button>
        </>
      )}
    </div>
  );
}
