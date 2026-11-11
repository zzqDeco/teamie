import { useMatch } from "react-router-dom";
import type { Conversation, Workspace } from "@teamie/api-client";

/** Workspace ID of a conversation path (`/app/w/:ws/c/:conv`), or "". */
export function conversationPathWorkspaceId(path: string) {
  return /^\/app\/w\/([^/]+)\/c\//.exec(path)?.[1] || "";
}

export type CurrentWorkspaceInput = {
  workspaces: Workspace[];
  /** Workspace ID from the current URL, when the route names one. */
  routeWorkspaceId?: string;
  lastChatPath: string;
  lastConversationByWorkspace: Record<string, string>;
  conversations?: Record<string, Conversation>;
};

/**
 * The workspace the user is working in. A route that names a workspace is
 * authoritative (an unknown/revoked ID resolves to no workspace). Pages without
 * a workspace in their URL keep the context of the last visited conversation,
 * then any remembered workspace conversation, and only then the first
 * workspace, so the rail, sidebar, Cmd+K message search and Alt+↑/↓ stay in
 * the space the user came from.
 */
export function resolveCurrentWorkspace({
  workspaces,
  routeWorkspaceId,
  lastChatPath,
  lastConversationByWorkspace,
  conversations,
}: CurrentWorkspaceInput): Workspace | undefined {
  if (routeWorkspaceId) return workspaces.find((w) => w.id === routeWorkspaceId);
  const byId = new Map(workspaces.map((w) => [w.id, w]));
  const fromLastChat = byId.get(conversationPathWorkspaceId(lastChatPath));
  if (fromLastChat) return fromLastChat;
  for (const [workspaceId, conversationId] of Object.entries(lastConversationByWorkspace)) {
    const space = byId.get(workspaceId);
    if (!space) continue;
    const remembered = conversations?.[conversationId];
    if (!conversations || !remembered || remembered.workspaceId === workspaceId) return space;
  }
  return workspaces[0];
}

/** {@link resolveCurrentWorkspace} with the workspace route parameter read from the URL. */
export function useCurrentWorkspace(input: Omit<CurrentWorkspaceInput, "routeWorkspaceId">) {
  const routeMatch = useMatch("/app/w/:workspaceId/*");
  return resolveCurrentWorkspace({
    ...input,
    routeWorkspaceId: routeMatch?.params.workspaceId,
  });
}
