import { api } from "@teamie/api-client";
import type { NavigateFunction } from "react-router-dom";
import type { ConversationAppTabsController } from "./marketplace/app-tabs-state";
import { clearPersistedAppPaneWidth } from "./marketplace/app-pane-resize";

const navStorageKey = (userId: string) => `teamie.nav.${userId}`;

export function clearPersistedShellNav(userId?: string) {
  if (!userId) return;
  try {
    localStorage.removeItem(navStorageKey(userId));
  } catch {
    /* Ignore storage access errors. */
  }
}

/**
 * Clears per-user client-side UI preferences/recents on sign-out so a later
 * sign-in on the same device never inherits another user's cached state.
 */
export function clearUserLocalUiState(
  userId?: string,
  options?: {
    appTabs?: Pick<ConversationAppTabsController, "resetAll"> | null;
    clearNav?: (id?: string) => void;
  },
) {
  options?.appTabs?.resetAll();
  if (options?.clearNav) options.clearNav(userId);
  else clearPersistedShellNav(userId);
  clearPersistedAppPaneWidth(userId);
  if (!userId) return;
  try {
    localStorage.removeItem(`teamie.sidebarSections.${userId}`);
    localStorage.removeItem(`teamie.reactions.recent.v1:${userId}`);
  } catch {
    /* Ignore storage access errors. */
  }
}

/**
 * Single logout + cleanup path shared by Shell and standalone ProfileSettings.
 */
export async function logoutAndCleanup({
  userId,
  logout,
  navigate,
  appTabs,
  clearNav,
}: {
  userId?: string;
  logout: () => Promise<void>;
  navigate: NavigateFunction;
  appTabs?: Pick<ConversationAppTabsController, "resetAll"> | null;
  clearNav?: (id?: string) => void;
}) {
  await logout();
  if (!api.csrfToken) {
    clearUserLocalUiState(userId, { appTabs, clearNav });
    navigate("/login");
  }
}
