/* global self, caches, fetch, Response, URL */
// Teamie minimal, auditable Service Worker (Stage 3A).
// Security invariant: NEVER intercept or cache /api/*, /api/v1/ws, /internal/*,
// non-GET requests, cross-origin requests, or query-bearing URLs.
const CACHE_VERSION = "__TEAMIE_SW_VERSION__";
const CACHE_PREFIX = "teamie-shell-";
const CACHE_NAME = `${CACHE_PREFIX}${CACHE_VERSION}`;

const APP_SHELL_PATH = "/index.html";
// Built JS/CSS assets stamped at production build time so a newly installed
// worker can cold-boot offline immediately after activating and pruning old caches.
const PRECACHE_ASSETS = /*__TEAMIE_SW_ASSETS__*/ [];
const PRECACHE_PATHS = Array.from(
  new Set([
    APP_SHELL_PATH,
    "/manifest.webmanifest",
    "/favicon.svg",
    "/icon-192.png",
    "/icon-512.png",
    "/icon-maskable-512.png",
    "/apple-touch-icon.png",
    ...PRECACHE_ASSETS,
  ]),
);
const STATIC_META_PATHS = new Set([
  "/manifest.webmanifest",
  "/favicon.svg",
  "/icon-192.png",
  "/icon-512.png",
  "/icon-maskable-512.png",
  "/apple-touch-icon.png",
]);

function isBypassPath(pathname) {
  return (
    pathname === "/api" ||
    pathname.startsWith("/api/") ||
    pathname === "/internal" ||
    pathname.startsWith("/internal/") ||
    pathname === "/sw.js" ||
    pathname === "/version.json" ||
    pathname === "/healthz"
  );
}

function sanitizeRelativeAppUrl(raw) {
  if (typeof raw !== "string") return "/app";
  const trimmed = raw.trim();
  if (!trimmed.startsWith("/") || trimmed.startsWith("//") || trimmed.includes("\\")) {
    return "/app";
  }
  try {
    const parsed = new URL(trimmed, self.location.origin);
    if (parsed.origin !== self.location.origin) return "/app";
    return `${parsed.pathname}${parsed.search}${parsed.hash}`;
  } catch {
    return "/app";
  }
}

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches.open(CACHE_NAME).then((cache) => cache.addAll(PRECACHE_PATHS)),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(
          keys
            .filter((key) => key.startsWith(CACHE_PREFIX) && key !== CACHE_NAME)
            .map((key) => caches.delete(key)),
        ),
      )
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("message", (event) => {
  if (event.data && event.data.type === "TEAMIE_SKIP_WAITING") {
    self.skipWaiting();
  }
});

self.addEventListener("fetch", (event) => {
  const { request } = event;
  if (request.method !== "GET") return;

  let url;
  try {
    url = new URL(request.url);
  } catch {
    return;
  }

  if (url.origin !== self.location.origin) return;
  if (isBypassPath(url.pathname)) return;

  // Navigation requests: Network-First, caching only the canonical /index.html
  // app shell key so URL query parameters (e.g. invite codes) are never stored.
  if (request.mode === "navigate") {
    event.respondWith(
      fetch(request)
        .then((response) => {
          const contentType = response.headers.get("content-type") || "";
          if (
            response.ok &&
            response.type === "basic" &&
            contentType.includes("text/html")
          ) {
            const copy = response.clone();
            event.waitUntil(
              caches
                .open(CACHE_NAME)
                .then((cache) => cache.put(APP_SHELL_PATH, copy)),
            );
          }
          return response;
        })
        .catch(async () => {
          const cache = await caches.open(CACHE_NAME);
          const cached = await cache.match(APP_SHELL_PATH);
          if (cached) return cached;
          return new Response("Offline", {
            status: 503,
            statusText: "Service Unavailable",
            headers: { "Content-Type": "text/plain; charset=utf-8" },
          });
        }),
    );
    return;
  }

  // Content-hashed static assets: Cache-First.
  if (url.pathname.startsWith("/assets/")) {
    event.respondWith(
      caches.open(CACHE_NAME).then(async (cache) => {
        const cached =
          (await cache.match(url.pathname)) || (await cache.match(request));
        if (cached) return cached;
        const response = await fetch(request);
        if (response.ok && response.type === "basic") {
          event.waitUntil(cache.put(url.pathname, response.clone()));
        }
        return response;
      }),
    );
    return;
  }

  // Icons & manifest: Stale-While-Revalidate.
  if (STATIC_META_PATHS.has(url.pathname)) {
    event.respondWith(
      caches.open(CACHE_NAME).then(async (cache) => {
        const cached = await cache.match(url.pathname);
        const networkPromise = fetch(request)
          .then((response) => {
            if (response.ok && response.type === "basic") {
              event.waitUntil(cache.put(url.pathname, response.clone()));
            }
            return response;
          })
          .catch(
            () =>
              cached ||
              new Response("", {
                status: 503,
                statusText: "Service Unavailable",
              }),
          );
        return cached || networkPromise;
      }),
    );
  }
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const targetUrl = sanitizeRelativeAppUrl(event.notification.data?.url);

  event.waitUntil(
    self.clients
      .matchAll({ type: "window", includeUncontrolled: true })
      .then(async (windowClients) => {
        const sameOriginClients = windowClients.filter((client) => {
          try {
            return new URL(client.url).origin === self.location.origin;
          } catch {
            return false;
          }
        });
        const preferred =
          sameOriginClients.find((client) => {
            try {
              return new URL(client.url).pathname.startsWith("/app");
            } catch {
              return false;
            }
          }) || sameOriginClients[0];

        if (preferred) {
          try {
            preferred.postMessage({
              type: "TEAMIE_NOTIFICATION_CLICK",
              url: targetUrl,
            });
          } catch {
            // Ignore postMessage failure before focus.
          }
          if (typeof preferred.focus === "function") {
            return preferred.focus();
          }
          return preferred;
        }
        if (typeof self.clients.openWindow === "function") {
          return self.clients.openWindow(targetUrl);
        }
        return undefined;
      }),
  );
});
