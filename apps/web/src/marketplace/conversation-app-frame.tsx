import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { LoaderCircle, RefreshCw } from "lucide-react";
import { api } from "@teamie/api-client";
import type {
  ConversationAppSessionResp,
  MarketplaceApp,
  User,
} from "@teamie/api-client";
import { errorText } from "../errors";
import { BadgePill, IconButton, Notice } from "../ui";
import { ICON } from "../icons";

// Each (userId, workspaceId, conversationId, appId, webUrl, mcpUrl) mounts an
// isolated instance. Pending requests and iframe refs never cross scopes.
export function ConversationAppFrame({
  app,
  conversationId,
  workspaceId,
  user,
}: {
  app: MarketplaceApp;
  conversationId: string;
  workspaceId: string;
  user: User;
}) {
  const iframeRef = useRef<HTMLIFrameElement | null>(null);
  const requests = useRef<{
    generation: number;
    initialInFlight: Promise<ConversationAppSessionResp> | null;
  }>({ generation: 0, initialInFlight: null });
  const [session, setSession] = useState<ConversationAppSessionResp | null>(
    null,
  );
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const sameOriginBlocked = useMemo(() => {
    try {
      return new URL(app.webUrl).origin === window.location.origin;
    } catch {
      return true;
    }
  }, [app.webUrl]);

  const fetchSession = useCallback(
    async (reuseInitial = false) => {
      if (sameOriginBlocked) return;
      const pending = requests.current;
      const generation = ++pending.generation;
      setLoading(true);
      setError("");
      try {
        let req = reuseInitial ? pending.initialInFlight : null;
        if (!req) {
          const created = api.createConversationAppSession(
            conversationId,
            app.id,
          );
          if (reuseInitial) {
            pending.initialInFlight = created;
            const clearInitial = () => {
              if (pending.initialInFlight === created) {
                pending.initialInFlight = null;
              }
            };
            void created.then(clearInitial, clearInitial);
          }
          req = created;
        }
        const next = await req;
        if (generation !== pending.generation) return;
        if (next.app.id !== app.id || next.app.webUrl !== app.webUrl) {
          throw new Error("应用信息已更新，请重新打开应用。");
        }
        setSession(next);
      } catch (e) {
        if (generation !== pending.generation) return;
        setSession(null);
        setError(errorText(e));
      } finally {
        if (generation === pending.generation) setLoading(false);
      }
    },
    [app.id, app.webUrl, conversationId, sameOriginBlocked],
  );

  useEffect(() => {
    const pending = requests.current;
    void fetchSession(true);
    return () => {
      pending.generation++;
    };
  }, [fetchSession]);

  const postContext = useCallback(() => {
    const win = iframeRef.current?.contentWindow;
    if (
      !win ||
      !session ||
      session.app.id !== app.id ||
      session.app.webUrl !== app.webUrl
    ) {
      return;
    }
    let origin: string;
    try {
      origin = new URL(app.webUrl).origin;
    } catch {
      return;
    }
    if (origin === window.location.origin) return;
    win.postMessage(
      {
        type: "teamie:mcp-app:init",
        payload: {
          appId: session.app.id,
          slug: session.app.slug,
          name: session.app.name,
          webUrl: session.app.webUrl,
          mcpUrl: session.app.mcpUrl,
          token: session.token,
          expiresAt: session.expiresAt,
          workspaceId,
          conversationId,
          user: { id: user.id, displayName: user.displayName },
        },
      },
      origin,
    );
  }, [
    app.id,
    app.webUrl,
    conversationId,
    session,
    user.displayName,
    user.id,
    workspaceId,
  ]);

  // A refreshed credential must reach the existing frame without reloading it.
  useEffect(() => {
    postContext();
  }, [postContext]);

  useEffect(() => {
    if (sameOriginBlocked) return;
    let origin: string;
    try {
      origin = new URL(app.webUrl).origin;
    } catch {
      return;
    }
    const onMessage = (event: MessageEvent) => {
      if (
        event.origin !== origin ||
        event.source !== iframeRef.current?.contentWindow
      )
        return;
      const type = (event.data as { type?: string } | null)?.type;
      if (type === "teamie:mcp-app:ready") {
        if (session) postContext();
        else void fetchSession();
      } else if (type === "teamie:mcp-app:refresh-token") {
        void fetchSession();
      }
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [app.webUrl, fetchSession, postContext, sameOriginBlocked, session]);

  return (
    <>
      <Notice>
        {sameOriginBlocked
          ? "安全限制：小组应用 Web UI 必须部署在独立跨源域名上，禁止与当前主站同源挂载。"
          : error}
      </Notice>
      <div className="mp-frame-bar">
        <span className="mp-frame-meta">
          <b>{app.name}</b>
          <small>· {app.slug}</small>
          {(app.servingDraft || session?.app.servingDraft) && (
            <BadgePill className="mp-badge-draft">测试草稿</BadgePill>
          )}
        </span>
        <IconButton
          label="刷新应用会话"
          disabled={loading || sameOriginBlocked}
          onClick={() => void fetchSession()}
        >
          <RefreshCw size={ICON.xs} />
        </IconButton>
      </div>
      {loading && !session && !sameOriginBlocked && !error && (
        <div className="mp-frame-loading" role="status">
          <LoaderCircle size={ICON.md} className="spin" aria-hidden="true" />
          <span>正在启动应用会话…</span>
        </div>
      )}
      {!sameOriginBlocked && session && (
        <iframe
          ref={iframeRef}
          tabIndex={0}
          title={`小组应用：${app.name}`}
          src={session.app.webUrl}
          sandbox="allow-scripts allow-forms allow-same-origin"
          onLoad={postContext}
          className="mp-iframe"
        />
      )}
    </>
  );
}
