// Loads the real internal/api/dashboard/sw.js into a simulated
// ServiceWorkerGlobalScope so its behaviour can be asserted directly.
//
// Playwright drives a page, not a service worker, so the SW had no behavioural
// coverage at all — every service-worker regression in this area shipped
// invisibly. This harness implements just enough of the Cache, Request,
// Response and fetch APIs to exercise the real source file (never a copy) and,
// crucially, to control timing: a test can hold a cache write open and assert
// the response was already delivered.
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const workerPath = path.join(__dirname, '..', '..', 'internal', 'api', 'dashboard', 'sw.js');

const ORIGIN = 'https://redline.test';

function urlOf(request) {
  const raw = typeof request === 'string' ? request : request.url;
  return new URL(raw, ORIGIN).toString();
}

class FakeResponse {
  constructor(body, { status = 200, type = 'basic' } = {}) {
    this.body = body;
    this.status = status;
    this.type = type;
  }
  get ok() {
    return this.status >= 200 && this.status < 300;
  }
  clone() {
    return new FakeResponse(this.body, { status: this.status, type: this.type });
  }
  text() {
    return Promise.resolve(this.body);
  }
}

class FakeRequest {
  constructor(url, { method = 'GET' } = {}) {
    this.url = urlOf(url);
    this.method = method;
  }
}

class FakeCache {
  constructor(entries, hooks) {
    this.entries = entries;
    this.hooks = hooks;
  }
  match(request) {
    return Promise.resolve(this.entries.get(urlOf(request)));
  }
  put(request, response) {
    const write = () => {
      this.entries.set(urlOf(request), response);
    };
    if (this.hooks.beforePut) {
      return this.hooks.beforePut(urlOf(request)).then(write);
    }
    write();
    return Promise.resolve();
  }
  addAll(urls) {
    return Promise.all(urls.map(url => this.hooks.fetch(new FakeRequest(url)).then(response => {
      if (!response || !response.ok) throw new Error(`addAll failed for ${url}`);
      return this.put(new FakeRequest(url), response);
    })));
  }
}

class FakeCacheStorage {
  constructor(hooks) {
    this.caches = new Map();
    this.hooks = hooks;
  }
  open(name) {
    if (!this.caches.has(name)) this.caches.set(name, new Map());
    return Promise.resolve(new FakeCache(this.caches.get(name), this.hooks));
  }
  match(request) {
    for (const entries of this.caches.values()) {
      const hit = entries.get(urlOf(request));
      if (hit) return Promise.resolve(hit);
    }
    return Promise.resolve(undefined);
  }
  keys() {
    return Promise.resolve([...this.caches.keys()]);
  }
  delete(name) {
    return Promise.resolve(this.caches.delete(name));
  }
  // Test helper: what is actually stored, by cache name.
  snapshot(name) {
    const entries = this.caches.get(name);
    return entries ? new Map(entries) : new Map();
  }
}

// loadServiceWorker evaluates the real sw.js and returns handles for driving it.
//
// options.respond(request) -> FakeResponse | Promise<FakeResponse>; throw or
// reject to simulate an offline network.
// options.beforePut(url) -> Promise resolved when the cache write may complete.
function loadServiceWorker(options = {}) {
  const fetchCalls = [];
  const hooks = {};

  const fetchImpl = request => {
    fetchCalls.push({ url: urlOf(request), method: request.method || 'GET' });
    try {
      return Promise.resolve(options.respond(request));
    } catch (error) {
      return Promise.reject(error);
    }
  };
  hooks.fetch = fetchImpl;
  if (options.beforePut) hooks.beforePut = options.beforePut;

  const cacheStorage = new FakeCacheStorage(hooks);
  const listeners = new Map();
  const self = {
    addEventListener: (name, listener) => {
      if (!listeners.has(name)) listeners.set(name, []);
      listeners.get(name).push(listener);
    },
    skipWaiting: () => Promise.resolve(),
    clients: { claim: () => Promise.resolve() },
  };

  const context = vm.createContext({
    self,
    caches: cacheStorage,
    fetch: fetchImpl,
    URL,
    Promise,
    console,
  });
  vm.runInContext(fs.readFileSync(workerPath, 'utf8'), context, { filename: 'sw.js' });

  // dispatch runs every listener for an event and resolves once the extending
  // promises passed to waitUntil settle, mirroring the browser contract that a
  // worker stays alive until they complete.
  function dispatch(name, event) {
    const extended = [];
    const responses = [];
    const enriched = Object.assign({
      waitUntil: promise => extended.push(Promise.resolve(promise)),
      respondWith: promise => responses.push(Promise.resolve(promise)),
    }, event);
    for (const listener of listeners.get(name) || []) listener(enriched);
    return {
      // Settles when the handler's background work finishes.
      extended: Promise.all(extended),
      // The response the browser would deliver to the page.
      response: responses.length ? responses[0] : undefined,
    };
  }

  return {
    cacheStorage,
    fetchCalls,
    listeners,
    install: () => dispatch('install', {}).extended,
    activate: () => dispatch('activate', {}).extended,
    fetch: (url, init) => dispatch('fetch', { request: new FakeRequest(url, init) }),
    message: data => dispatch('message', { data }).extended,
    cacheName: () => {
      const names = [...cacheStorage.caches.keys()];
      return names.length ? names[0] : undefined;
    },
  };
}

module.exports = { loadServiceWorker, FakeResponse, FakeRequest, ORIGIN };
