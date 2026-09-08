// Redline mobile PWA service worker — caches static shell only, never /v1 API.
const CACHE = 'redline-mobile-v3';
const SHELL = [
  '/m',
  '/assets/mobile/mobile.css',
  '/assets/mobile/mobile.js',
  '/assets/mobile/icon-192.png',
  '/assets/mobile/icon-512.png',
  '/assets/mobile/manifest.webmanifest',
  '/assets/claude.svg',
  '/assets/codex.svg',
];

self.addEventListener('install', event => {
  event.waitUntil(
    caches.open(CACHE).then(cache => cache.addAll(SHELL)).then(() => self.skipWaiting())
  );
});

self.addEventListener('activate', event => {
  event.waitUntil(
    caches.keys().then(keys =>
      Promise.all(keys.filter(key => key !== CACHE).map(key => caches.delete(key)))
    ).then(() => self.clients.claim())
  );
});

self.addEventListener('fetch', event => {
  const url = new URL(event.request.url);
  // Never intercept API requests — always go to network.
  if (url.pathname.startsWith('/v1/')) {
    event.respondWith(fetch(event.request));
    return;
  }
  // Only GET requests are cacheable.
  if (event.request.method !== 'GET') {
    event.respondWith(fetch(event.request));
    return;
  }
  // Shell resources: stale-while-revalidate. Serve the cached copy for speed,
  // but always refresh it in the background so a redeployed dashboard reaches
  // installed PWAs on the next load instead of being pinned to a stale build.
  // Track the cache write separately from the response so a cache miss never
  // waits on it: the request should be served as soon as the bytes arrive.
  let cacheWrite = null;
  const network = fetch(event.request).then(response => {
    if (response && response.ok && response.type === 'basic') {
      const copy = response.clone();
      cacheWrite = caches.open(CACHE)
        .then(cache => cache.put(event.request, copy))
        .catch(() => {});
    }
    return response;
  });
  // Keep the revalidation and its cache write alive independently of the
  // response promise. The browser may terminate an idle service worker as soon
  // as respondWith settles, which would otherwise kill the background write and
  // pin installed PWAs to a stale build.
  event.waitUntil(
    network.then(() => cacheWrite).catch(() => {})
  );
  // Fall back to any cached copy if the network fails, so an offline load still
  // resolves rather than rejecting respondWith.
  event.respondWith(
    caches.match(event.request).then(cached => cached || network.catch(() => cached))
  );
});

// Drop the cached shell when the dashboard reports an expired session, so a
// re-paired or rotated device never renders another session's cached markup.
self.addEventListener('message', event => {
  if (event.data && event.data.type === 'redline-clear-cache') {
    event.waitUntil(caches.delete(CACHE));
  }
});
