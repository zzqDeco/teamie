import { api } from "@teamie/api-client";

const LOGOUT_UNSUBSCRIBE_TIMEOUT_MS = 2_000;

export function isWebPushSupported(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof navigator !== "undefined" &&
    "serviceWorker" in navigator &&
    "PushManager" in window &&
    "Notification" in window &&
    window.isSecureContext !== false
  );
}

export function isIosSafariNotStandalone(): boolean {
  if (typeof window === "undefined" || typeof navigator === "undefined") {
    return false;
  }
  const ua = navigator.userAgent || "";
  const isIos =
    /iPad|iPhone|iPod/i.test(ua) ||
    (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);
  if (!isIos) return false;
  const standalone =
    window.matchMedia?.("(display-mode: standalone)").matches ||
    (navigator as Navigator & { standalone?: boolean }).standalone === true;
  return !standalone;
}

export function urlBase64ToUint8Array(base64Url: string): Uint8Array<ArrayBuffer> {
  const cleaned = base64Url.trim().replace(/-/g, "+").replace(/_/g, "/");
  const padding = "=".repeat((4 - (cleaned.length % 4)) % 4);
  const raw = atob(cleaned + padding);
  const buffer = new ArrayBuffer(raw.length);
  const out = new Uint8Array(buffer);
  for (let i = 0; i < raw.length; i += 1) {
    out[i] = raw.charCodeAt(i);
  }
  return out;
}

function bytesEqual(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i += 1) {
    if (a[i] !== b[i]) return false;
  }
  return true;
}

async function getActiveServiceWorkerRegistration(): Promise<ServiceWorkerRegistration | null> {
  if (
    typeof navigator === "undefined" ||
    !("serviceWorker" in navigator) ||
    typeof navigator.serviceWorker.getRegistration !== "function"
  ) {
    return null;
  }
  const existing = await navigator.serviceWorker.getRegistration();
  if (!existing) return null;
  if (existing.active) return existing;
  if (navigator.serviceWorker.ready) {
    return await Promise.race([
      navigator.serviceWorker.ready,
      new Promise<null>((resolve) => setTimeout(() => resolve(null), 3_000)),
    ]);
  }
  return null;
}

export async function getExistingPushSubscription(): Promise<PushSubscription | null> {
  if (!isWebPushSupported()) return null;
  try {
    const reg = await getActiveServiceWorkerRegistration();
    if (!reg?.pushManager) return null;
    return await reg.pushManager.getSubscription();
  } catch {
    return null;
  }
}

export async function subscribeWebPush(
  vapidPublicKey: string,
): Promise<PushSubscription> {
  if (!isWebPushSupported()) {
    throw new Error("当前浏览器不支持后台推送。");
  }
  if (Notification.permission === "denied") {
    throw new Error("浏览器通知权限已被阻止，请在系统或浏览器设置中开启。");
  }
  if (Notification.permission !== "granted") {
    const result = await Notification.requestPermission();
    if (result !== "granted") {
      throw new Error("未授予浏览器通知权限，无法开启后台推送。");
    }
  }
  const reg = await getActiveServiceWorkerRegistration();
  if (!reg?.pushManager) {
    throw new Error("离线服务尚未就绪，请刷新页面后重试。");
  }

  const targetKey = urlBase64ToUint8Array(vapidPublicKey);
  let subscription = await reg.pushManager.getSubscription();
  let createdFresh = false;

  if (subscription) {
    const existingKeyBuf = subscription.options?.applicationServerKey;
    if (
      existingKeyBuf &&
      !bytesEqual(new Uint8Array(existingKeyBuf), targetKey)
    ) {
      try {
        await api.deletePushSubscription(subscription.endpoint);
      } catch {
        // Best-effort cleanup of rotated subscription on server.
      }
      await subscription.unsubscribe().catch(() => {});
      subscription = null;
    }
  }

  if (!subscription) {
    subscription = await reg.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: targetKey,
    });
    createdFresh = true;
  }

  const json = subscription.toJSON();
  const endpoint = json.endpoint || subscription.endpoint;
  const p256dh = json.keys?.p256dh || "";
  const auth = json.keys?.auth || "";
  if (!endpoint || !p256dh || !auth) {
    if (createdFresh) {
      await subscription.unsubscribe().catch(() => {});
    }
    throw new Error("浏览器未能生成有效的推送密钥，请稍后重试。");
  }

  try {
    await api.upsertPushSubscription({
      endpoint,
      keys: { p256dh, auth },
      userAgent: navigator.userAgent || "",
    });
  } catch (err) {
    if (createdFresh) {
      await subscription.unsubscribe().catch(() => {});
    }
    throw err;
  }

  return subscription;
}

export async function unsubscribeWebPush(): Promise<void> {
  const subscription = await getExistingPushSubscription();
  if (!subscription) return;
  let serverError: unknown = null;
  try {
    await api.deletePushSubscription(subscription.endpoint);
  } catch (err) {
    serverError = err;
  }
  try {
    await subscription.unsubscribe();
  } catch {
    // Ignore browser unsubscribe errors when clearing local state.
  }
  if (serverError) {
    throw serverError;
  }
}

export async function unsubscribeLocalWebPushOnly(): Promise<void> {
  const subscription = await getExistingPushSubscription();
  if (!subscription) return;
  await subscription.unsubscribe().catch(() => {});
}

export async function revokeWebPushOnLogout(): Promise<void> {
  const work = (async () => {
    const subscription = await getExistingPushSubscription();
    if (!subscription) return;
    try {
      await api.deletePushSubscription(subscription.endpoint);
    } catch {
      // Never block logout on transient push server errors.
    }
    await subscription.unsubscribe().catch(() => {});
  })();

  await Promise.race([
    work,
    new Promise<void>((resolve) =>
      setTimeout(resolve, LOGOUT_UNSUBSCRIBE_TIMEOUT_MS),
    ),
  ]);
}
