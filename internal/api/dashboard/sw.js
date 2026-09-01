// Redline mobile PWA service worker — caches static shell only, never /v1 API.
const CACHE = 'redline-mobile-v1';
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
  // For shell resources: cache-first, fall back to network.
  event.respondWith(
    caches.match(event.request).then(cached => cached || fetch(event.request))
  );
});
