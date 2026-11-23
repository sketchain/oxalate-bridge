/*
 * Service worker of the panel. vite.config.ts fills in the precache list
 * (hashed build output plus the app shell) and the version when building.
 *
 * - Precached files are served from the cache; /api/* and /v1/* are never
 *   cached or intercepted (always the network).
 * - A navigation that fails (offline) shows /offline.html.
 * - A new version waits until the page asks it to take over
 *   ({type: "SKIP_WAITING"}), so the page can offer "有新版本，点击刷新".
 * - push shows the notification; notificationclick focuses an open window
 *   (navigating it to the target) or opens one.
 */
const VERSION = "__OB_VERSION__";
const PRECACHE = self.__OB_PRECACHE__;
const CACHE = "ob-precache-" + VERSION;
const OFFLINE = "/offline.html";

self.addEventListener("install", (event) => {
  event.waitUntil(caches.open(CACHE).then((cache) => cache.addAll(PRECACHE)));
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((names) => Promise.all(names.filter((n) => n.startsWith("ob-precache-") && n !== CACHE).map((n) => caches.delete(n))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("message", (event) => {
  if (event.data && event.data.type === "SKIP_WAITING") self.skipWaiting();
  if (event.data && event.data.type === "VERSION" && event.ports[0]) event.ports[0].postMessage(VERSION);
});

function bypass(url) {
  return url.pathname.startsWith("/api/") || url.pathname.startsWith("/v1/") || url.pathname === "/healthz" || url.pathname === "/readyz";
}

self.addEventListener("fetch", (event) => {
  const req = event.request;
  if (req.method !== "GET") return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin || bypass(url)) return;
  if (req.mode === "navigate") {
    event.respondWith(
      fetch(req).catch(() => caches.open(CACHE).then((cache) => cache.match(OFFLINE)).then((r) => r || Response.error())),
    );
    return;
  }
  if (!PRECACHE.includes(url.pathname)) return;
  event.respondWith(caches.open(CACHE).then((cache) => cache.match(url.pathname).then((hit) => hit || fetch(req))));
});

self.addEventListener("push", (event) => {
  let data = {};
  try {
    data = event.data ? event.data.json() : {};
  } catch (e) {
    data = { title: "Oxalate's Bridge", body: event.data ? event.data.text() : "" };
  }
  const title = data.title || "Oxalate's Bridge";
  event.waitUntil(
    self.registration.showNotification(title, {
      body: data.body || "",
      icon: "/icons/icon-192.png",
      badge: "/icons/badge-96.png",
      tag: data.tag || undefined,
      renotify: !!data.tag,
      data: { url: data.url || "/" },
    }),
  );
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const target = new URL((event.notification.data && event.notification.data.url) || "/", self.location.origin).href;
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((wins) => {
      const same = wins.filter((w) => new URL(w.url).origin === self.location.origin);
      const exact = same.find((w) => w.url === target);
      if (exact) return exact.focus();
      if (same.length) {
        const w = same[0];
        return w.focus().then((f) => (f && "navigate" in f ? f.navigate(target) : f));
      }
      return self.clients.openWindow(target);
    }),
  );
});
