import { useSyncExternalStore } from "react";

export type BeforeInstallPromptEvent = Event & {
  prompt: () => Promise<void>;
  userChoice: Promise<{ outcome: "accepted" | "dismissed"; platform: string }>;
};

const DISMISS_STORAGE_KEY = "teamie.pwa.installDismissed.v1";

type InstallSnapshot = {
  isStandalone: boolean;
  canPromptInstall: boolean;
  showIosGuide: boolean;
  bannerDismissed: boolean;
};

let deferredPrompt: BeforeInstallPromptEvent | null = null;
let installedOverride = false;
let listenersBound = false;
const subscribers = new Set<() => void>();

function readDismissed(): boolean {
  try {
    return window.localStorage?.getItem(DISMISS_STORAGE_KEY) === "1";
  } catch {
    return false;
  }
}

export function detectStandalone(): boolean {
  if (typeof window === "undefined") return false;
  if (installedOverride) return true;
  const mediaStandalone =
    typeof window.matchMedia === "function" &&
    window.matchMedia("(display-mode: standalone)").matches;
  const iosStandalone =
    (navigator as Navigator & { standalone?: boolean }).standalone === true;
  return mediaStandalone || iosStandalone;
}

export function detectIosSafari(): boolean {
  if (typeof navigator === "undefined") return false;
  const ua = navigator.userAgent || "";
  const isIos =
    /iPad|iPhone|iPod/.test(ua) ||
    (navigator.platform === "MacIntel" && (navigator.maxTouchPoints || 0) > 1);
  const isWebKit = /WebKit/i.test(ua);
  const isOtherIosBrowser = /CriOS|FxiOS|EdgiOS|OPiOS/i.test(ua);
  return isIos && isWebKit && !isOtherIosBrowser;
}

let cachedSnapshot: InstallSnapshot = computeSnapshot();

function computeSnapshot(): InstallSnapshot {
  const isStandalone = detectStandalone();
  const canPromptInstall = !isStandalone && deferredPrompt !== null;
  const showIosGuide = !isStandalone && !canPromptInstall && detectIosSafari();
  const bannerDismissed = readDismissed();
  return { isStandalone, canPromptInstall, showIosGuide, bannerDismissed };
}

function emit() {
  const next = computeSnapshot();
  if (
    next.isStandalone === cachedSnapshot.isStandalone &&
    next.canPromptInstall === cachedSnapshot.canPromptInstall &&
    next.showIosGuide === cachedSnapshot.showIosGuide &&
    next.bannerDismissed === cachedSnapshot.bannerDismissed
  ) {
    return;
  }
  cachedSnapshot = next;
  for (const listener of subscribers) listener();
}

function ensureListeners() {
  if (listenersBound || typeof window === "undefined") return;
  listenersBound = true;
  window.addEventListener("beforeinstallprompt", (event) => {
    event.preventDefault();
    deferredPrompt = event as BeforeInstallPromptEvent;
    emit();
  });
  window.addEventListener("appinstalled", () => {
    deferredPrompt = null;
    installedOverride = true;
    emit();
  });
}

function subscribe(listener: () => void) {
  ensureListeners();
  subscribers.add(listener);
  return () => {
    subscribers.delete(listener);
  };
}

function getSnapshot(): InstallSnapshot {
  ensureListeners();
  return cachedSnapshot;
}

export async function promptPwaInstall(): Promise<
  "accepted" | "dismissed" | "unavailable"
> {
  if (!deferredPrompt) return "unavailable";
  const pending = deferredPrompt;
  deferredPrompt = null;
  emit();
  try {
    await pending.prompt();
    const choice = await pending.userChoice;
    if (choice.outcome === "accepted") {
      installedOverride = true;
      emit();
    }
    return choice.outcome;
  } catch {
    return "dismissed";
  }
}

export function dismissPwaInstallBanner(): void {
  try {
    window.localStorage?.setItem(DISMISS_STORAGE_KEY, "1");
  } catch {
    // Ignore storage access errors.
  }
  emit();
}

export function initPwaInstallCapture(): void {
  ensureListeners();
}

ensureListeners();

export function usePwaInstall() {
  const state = useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
  return {
    ...state,
    promptInstall: promptPwaInstall,
    dismissBanner: dismissPwaInstallBanner,
  };
}

export function _resetPwaInstallForTest(): void {
  deferredPrompt = null;
  installedOverride = false;
  cachedSnapshot = computeSnapshot();
  for (const listener of subscribers) listener();
}
