// starter's service worker — deliberately does almost nothing.
//
// It exists to be a real, valid, registered worker (installs, activates,
// takes control immediately) rather than a 404 a browser occasionally
// probes for at the root — but it does NOT intercept fetches or cache
// anything. This app just fixed a real bug caused by an over-eager
// Cache-Control header on static assets (see CONFIGURATION.md's
// misconfiguration table: "edited app.css and the browser still shows the
// old version"); a service worker with its own fetch-level cache is the
// same mistake with a much sharper edge — it can keep serving stale pages,
// stale CSS, even a stale login form, long after both the server and the
// browser's own HTTP cache have moved on, and clearing it requires an
// explicit unregister, not just a hard refresh.
//
// If this app grows real offline/PWA requirements, add a `fetch` listener
// here with an explicit, versioned cache name and a strategy you have
// actually reasoned about (network-first for anything that can change,
// cache-first only for content-hashed, immutable assets this starter does
// not currently have) — not a blanket cache-everything handler.
//
// Served correctly at /sw.js (cmd/server/main.go, right next to /livez and
// /readyz) and registered from every layout via
// resources/static/js/sw-register.js's <script src=...> tag. That tag only
// renders because of github.com/oarkflow/template v0.0.4+ — earlier
// versions hardcoded SecureMode = true in a way Config's own
// SecureMode: false could never undo, so any <script> tag anywhere in
// rendered output failed the whole page (fixed upstream; see
// CONFIGURATION.md's SPL gotchas for the detail, kept there rather than
// here since it's a template-engine note, not a service-worker one).

self.addEventListener("install", (event) => {
  self.skipWaiting();
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    (async () => {
      // Remove any cache a future version of this file (or a future
      // fetch-caching experiment) might have left behind, so turning
      // caching on and back off again doesn't strand stale entries.
      const names = await caches.keys();
      await Promise.all(names.map((name) => caches.delete(name)));
      await self.clients.claim();
    })(),
  );
});
