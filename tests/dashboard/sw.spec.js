// Behavioural coverage for the real service worker.
//
// Every service-worker regression in this area shipped invisibly because the
// only coverage was a source-string grep: the shell was pinned to a stale
// build, the offline fallback was dropped, and a cache miss blocked on the
// cache write. Each test below fails if its fix is reverted.
const { test, expect } = require('@playwright/test');
const { loadServiceWorker, FakeResponse } = require('./sw-harness');

// Registered under the mobile project only (see playwright.config.js), so
// these run once rather than per browser project.

function shellResponder(body = 'shell-v1') {
  return request => {
    if (new URL(request.url).pathname.startsWith('/v1/')) {
      return new FakeResponse('{"live":true}');
    }
    return new FakeResponse(body);
  };
}

test('API requests always hit the network and are never cached', async () => {
  const worker = loadServiceWorker({ respond: shellResponder() });
  await worker.install();

  const call = worker.fetch('/v1/dashboard');
  const response = await call.response;
  await call.extended;

  expect(await response.text()).toBe('{"live":true}');
  // Nothing under /v1/ may be persisted: responses are per-session and
  // authenticated.
  for (const name of await worker.cacheStorage.keys()) {
    const stored = [...worker.cacheStorage.snapshot(name).keys()];
    expect(stored.some(url => url.includes('/v1/'))).toBe(false);
  }
});

test('non-GET requests bypass the cache and are never served from it', async () => {
  // A POST must reach the network even when a GET for the same URL is cached,
  // or a mutation would be answered with a stale cached body.
  const worker = loadServiceWorker({
    respond: request => new FakeResponse(request.method === 'POST' ? 'from-network' : 'from-cache'),
  });
  await worker.install();

  const call = worker.fetch('/m', { method: 'POST' });
  const response = await call.response;
  await call.extended;

  expect(await response.text()).toBe('from-network');
  expect(worker.fetchCalls.some(c => c.method === 'POST')).toBe(true);
  // The POST response must not displace the cached GET entry.
  const cached = worker.cacheStorage.snapshot(worker.cacheName());
  const entry = [...cached.entries()].find(([url]) => url.endsWith('/m'));
  expect(await entry[1].text()).toBe('from-cache');
});

test('a redeployed shell reaches the client on the next load', async () => {
  // The bug that started this: installed PWAs kept running the build they
  // first cached, so a redeploy never reached the device.
  let body = 'shell-v1';
  const worker = loadServiceWorker({ respond: () => new FakeResponse(body) });
  await worker.install();

  body = 'shell-v2'; // operator redeploys
  const first = worker.fetch('/assets/mobile/mobile.js');
  expect(await (await first.response).text()).toBe('shell-v1'); // stale, as designed
  await first.extended; // revalidation completes in the background

  const second = worker.fetch('/assets/mobile/mobile.js');
  expect(await (await second.response).text()).toBe('shell-v2');
});

test('a cache miss is served without waiting on the cache write', async () => {
  // respondWith must not be chained to cache.put, or the user waits on a disk
  // write before receiving bytes.
  let releaseWrite;
  const blocked = new Promise(resolve => { releaseWrite = resolve; });
  const worker = loadServiceWorker({
    respond: shellResponder('fresh'),
    beforePut: () => blocked,
  });

  // No install: this URL is not in the cache, so it is a genuine miss.
  const call = worker.fetch('/assets/mobile/late.js');
  const response = await Promise.race([
    call.response.then(r => r.text()),
    new Promise((_, reject) => setTimeout(() => reject(new Error('response blocked on the cache write')), 1000)),
  ]);
  expect(response).toBe('fresh');

  releaseWrite();
  await call.extended;
});

test('the background cache write survives after the response is delivered', async () => {
  // waitUntil must keep the revalidation alive; otherwise the browser may kill
  // the worker before the write lands and the shell stays pinned.
  let releaseWrite;
  const blocked = new Promise(resolve => { releaseWrite = resolve; });
  const worker = loadServiceWorker({
    respond: shellResponder('written'),
    beforePut: () => blocked,
  });

  const call = worker.fetch('/assets/mobile/mobile.css');
  await call.response;
  releaseWrite();
  await call.extended;

  const cached = worker.cacheStorage.snapshot(worker.cacheName());
  const entry = [...cached.entries()].find(([url]) => url.endsWith('mobile.css'));
  expect(entry).toBeDefined();
  expect(await entry[1].text()).toBe('written');
});

test('an offline load falls back to the cached shell', async () => {
  let online = true;
  const worker = loadServiceWorker({
    respond: request => {
      if (!online) throw new Error('offline');
      return new FakeResponse('cached-shell');
    },
  });
  await worker.install();

  online = false;
  const call = worker.fetch('/assets/mobile/mobile.js');
  const response = await call.response;
  await call.extended;
  expect(await response.text()).toBe('cached-shell');
});

test('an offline load with nothing cached resolves instead of throwing', async () => {
  const worker = loadServiceWorker({
    respond: () => { throw new Error('offline'); },
  });
  // No install, so the cache is empty and the network fails: respondWith must
  // still settle rather than rejecting.
  const call = worker.fetch('/assets/mobile/missing.js');
  await expect(call.response).resolves.toBeUndefined();
  await call.extended;
});

test('failed responses are never cached', async () => {
  const worker = loadServiceWorker({
    respond: () => new FakeResponse('not found', { status: 404 }),
  });
  const call = worker.fetch('/assets/mobile/gone.js');
  await call.response;
  await call.extended;

  for (const name of await worker.cacheStorage.keys()) {
    const stored = [...worker.cacheStorage.snapshot(name).keys()];
    expect(stored.some(url => url.endsWith('gone.js'))).toBe(false);
  }
});

test('opaque cross-origin responses are never cached', async () => {
  const worker = loadServiceWorker({
    respond: () => new FakeResponse('third-party', { type: 'opaque' }),
  });
  const call = worker.fetch('/assets/mobile/third-party.js');
  await call.response;
  await call.extended;

  for (const name of await worker.cacheStorage.keys()) {
    const stored = [...worker.cacheStorage.snapshot(name).keys()];
    expect(stored.some(url => url.endsWith('third-party.js'))).toBe(false);
  }
});

test('activate deletes caches from previous shell versions', async () => {
  const worker = loadServiceWorker({ respond: shellResponder() });
  await worker.install();
  const current = worker.cacheName();

  // A stale cache left by an earlier deploy.
  await worker.cacheStorage.open('redline-mobile-v1');
  expect(await worker.cacheStorage.keys()).toContain('redline-mobile-v1');

  await worker.activate();
  const remaining = await worker.cacheStorage.keys();
  expect(remaining).toContain(current);
  expect(remaining).not.toContain('redline-mobile-v1');
});

test('a clear-cache message drops the cached shell', async () => {
  // Sent when the dashboard detects an expired session, so a re-paired device
  // cannot render markup cached under the previous session.
  const worker = loadServiceWorker({ respond: shellResponder() });
  await worker.install();
  const current = worker.cacheName();
  expect(worker.cacheStorage.snapshot(current).size).toBeGreaterThan(0);

  await worker.message({ type: 'redline-clear-cache' });
  expect(await worker.cacheStorage.keys()).not.toContain(current);
});

test('unrelated messages leave the cache alone', async () => {
  const worker = loadServiceWorker({ respond: shellResponder() });
  await worker.install();
  const current = worker.cacheName();

  await worker.message({ type: 'something-else' });
  await worker.message(undefined);
  expect(await worker.cacheStorage.keys()).toContain(current);
});
