// Redline mobile PWA service worker — caches static shell only, never /v1 API.
const CACHE = 'redline-mobile-v2';
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
  event.respondWith(
    caches.match(event.request).then(cached => {
      const network = fetch(event.request).then(response => {
        if (response && response.ok && response.type === 'basic') {
          const copy = response.clone();
          caches.open(CACHE).then(cache => cache.put(event.request, copy)).catch(() => {});
        }
        return response;
      }).catch(() => cached);
      return cached || network;
    })
  );
});
