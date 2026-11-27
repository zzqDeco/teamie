type BadgingNavigator = Navigator & {
  setAppBadge?: (contents?: number) => Promise<void>;
  clearAppBadge?: () => Promise<void>;
};

/**
 * Syncs the OS/PWA app icon unread badge with Teamie's single attention source
 * of truth (`urgentCount`). Safely no-ops on unsupported browsers.
 */
export function syncAppBadge(count: number): void {
  if (typeof navigator === "undefined") return;
  const nav = navigator as BadgingNavigator;
  try {
    if (Number.isFinite(count) && count > 0) {
      if (typeof nav.setAppBadge === "function") {
        void Promise.resolve(nav.setAppBadge(Math.floor(count))).catch(() => {});
      }
    } else if (typeof nav.clearAppBadge === "function") {
      void Promise.resolve(nav.clearAppBadge()).catch(() => {});
    }
  } catch {
    // Ignore Badging API errors in restricted contexts.
  }
}
