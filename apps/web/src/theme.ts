import { useEffect, useState } from "react";

export type ThemePreference = "system" | "light" | "dark";
export const THEME_STORAGE_KEY = "teamie.theme";

let fallbackPreference: ThemePreference | null = null;
const listeners = new Set<(pref: ThemePreference) => void>();

export function getThemePreference(): ThemePreference {
  if (fallbackPreference) return fallbackPreference;
  try {
    const raw = window.localStorage?.getItem(THEME_STORAGE_KEY);
    if (raw === "light" || raw === "dark" || raw === "system") return raw;
  } catch {
    // Ignore storage access errors in restricted contexts
  }
  return "system";
}

export function resolveTheme(
  pref: ThemePreference = getThemePreference(),
): "light" | "dark" {
  if (pref === "light" || pref === "dark") return pref;
  if (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-color-scheme: dark)").matches
  ) {
    return "dark";
  }
  return "light";
}

export function applyTheme(
  pref: ThemePreference = getThemePreference(),
): "light" | "dark" {
  const resolved = resolveTheme(pref);
  if (typeof document !== "undefined" && document.documentElement) {
    document.documentElement.dataset.theme = resolved;
    document.documentElement.dataset.themePreference = pref;
  }
  return resolved;
}

export function setThemePreference(pref: ThemePreference): void {
  try {
    window.localStorage?.setItem(THEME_STORAGE_KEY, pref);
    fallbackPreference = null;
  } catch {
    fallbackPreference = pref;
  }
  applyTheme(pref);
  for (const listener of listeners) listener(pref);
}

export function useThemePreference(): [
  ThemePreference,
  (next: ThemePreference) => void,
  "light" | "dark",
] {
  const [pref, setPref] = useState<ThemePreference>(() => getThemePreference());
  const [resolved, setResolved] = useState<"light" | "dark">(() =>
    applyTheme(getThemePreference()),
  );

  useEffect(() => {
    const sync = (nextPref: ThemePreference = getThemePreference()) => {
      setPref(nextPref);
      setResolved(applyTheme(nextPref));
    };
    listeners.add(sync);

    const media =
      typeof window.matchMedia === "function"
        ? window.matchMedia("(prefers-color-scheme: dark)")
        : null;
    const onMediaChange = () => {
      if (getThemePreference() === "system") {
        setResolved(applyTheme("system"));
      }
    };
    const onStorage = (event: StorageEvent) => {
      if (!event.key || event.key === THEME_STORAGE_KEY) {
        fallbackPreference = null;
        sync();
      }
    };

    media?.addEventListener("change", onMediaChange);
    window.addEventListener("storage", onStorage);
    sync();
    return () => {
      listeners.delete(sync);
      media?.removeEventListener("change", onMediaChange);
      window.removeEventListener("storage", onStorage);
    };
  }, []);

  return [pref, setThemePreference, resolved];
}
