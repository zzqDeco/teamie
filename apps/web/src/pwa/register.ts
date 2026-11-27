import {
  navigateFromNotification,
  setNotificationServiceWorkerRegistration,
} from "../notifications";
import { toast } from "../toast";

type SwMessageEvent = { data?: { type?: unknown; url?: unknown } };

export type ServiceWorkerContainerLike = {
  controller?: ServiceWorker | null;
  ready?: Promise<ServiceWorkerRegistration>;
  register: (
    scriptURL: string,
    options?: RegistrationOptions,
  ) => Promise<ServiceWorkerRegistration>;
  addEventListener: (
    type: string,
    listener: EventListenerOrEventListenerObject,
  ) => void;
  removeEventListener?: (
    type: string,
    listener: EventListenerOrEventListenerObject,
  ) => void;
};

let initialized = false;

/**
 * Registers `/sw.js`, wires waiting-worker update prompts, routes SW
 * notification click messages back into the SPA router, and shares the active
 * registration with `notifications.ts`.
 */
export function initServiceWorker(
  container: ServiceWorkerContainerLike,
  options?: {
    onReload?: () => void;
    onNavigate?: (url: string) => void;
  },
): () => void {
  let userRequestedUpdate = false;
  let reloading = false;
  const reload =
    options?.onReload ||
    (() => {
      window.location.reload();
    });
  const onNavigate = options?.onNavigate || navigateFromNotification;

  const promptUpdate = (waiting: ServiceWorker) => {
    toast.info("新版本已就绪", {
      duration: 0,
      action: {
        label: "点击刷新",
        onClick: () => {
          userRequestedUpdate = true;
          waiting.postMessage({ type: "TEAMIE_SKIP_WAITING" });
        },
      },
    });
  };

  const onControllerChange = () => {
    if (!userRequestedUpdate || reloading) return;
    reloading = true;
    reload();
  };

  const onMessage = (event: Event) => {
    const payload = (event as unknown as SwMessageEvent).data;
    if (
      payload?.type === "TEAMIE_NOTIFICATION_CLICK" &&
      typeof payload.url === "string"
    ) {
      onNavigate(payload.url);
    }
  };

  container.addEventListener("controllerchange", onControllerChange);
  container.addEventListener("message", onMessage);

  let disposed = false;
  void container.ready
    ?.then((reg) => {
      if (!disposed) setNotificationServiceWorkerRegistration(reg);
    })
    .catch(() => {});

  void container
    .register("/sw.js", { scope: "/" })
    .then((registration) => {
      if (disposed) return;
      if (registration.active) {
        setNotificationServiceWorkerRegistration(registration);
      }
      if (registration.waiting && container.controller) {
        promptUpdate(registration.waiting);
      }
      registration.addEventListener("updatefound", () => {
        const installing = registration.installing;
        if (!installing) return;
        installing.addEventListener("statechange", () => {
          if (disposed) return;
          if (installing.state === "activated") {
            setNotificationServiceWorkerRegistration(registration);
          } else if (
            installing.state === "installed" &&
            container.controller &&
            registration.waiting
          ) {
            promptUpdate(registration.waiting);
          }
        });
      });
    })
    .catch(() => {
      // Service Worker registration failure must never disrupt the app.
    });

  return () => {
    disposed = true;
    container.removeEventListener?.("controllerchange", onControllerChange);
    container.removeEventListener?.("message", onMessage);
  };
}

export function registerServiceWorker(): void {
  if (
    initialized ||
    typeof window === "undefined" ||
    !("serviceWorker" in navigator) ||
    !import.meta.env.PROD
  ) {
    return;
  }
  initialized = true;
  const start = () => {
    initServiceWorker(navigator.serviceWorker);
  };
  if (document.readyState === "complete") {
    start();
  } else {
    window.addEventListener("load", start, { once: true });
  }
}
