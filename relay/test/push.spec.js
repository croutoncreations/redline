import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import worker from "../src/index.js";

const bearer = "throwaway-desktop-secret";
const deviceToken = "private-device-registration-token";
const accessToken = "private-google-access-token";
const tokenURL = "https://oauth2.googleapis.com/token";
let keys;
let serviceAccount;
let env;
let upstream;
let logs;
let sequence = 0;

function base64(bytes) {
  return btoa(String.fromCharCode(...new Uint8Array(bytes)));
}

async function digest(token) {
  const bytes = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(token));
  return [...new Uint8Array(bytes)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

function wake(overrides = {}) {
  return { provider: "fcm", platform: "android", token: deviceToken,
    hint: { delivery_id: 123, kind: "reset" }, ...overrides };
}

function send(body = wake(), options = {}) {
  const { authorization = `Bearer ${bearer}`, raw, ...rest } = options;
  const headers = { "content-type": "application/json" };
  if (authorization !== null) headers.authorization = authorization;
  return worker.fetch(new Request("https://relay.example/v1/push", {
    method: "POST", headers, body: raw ?? JSON.stringify(body), ...rest,
  }), env);
}

async function expectResult(response, status, body) {
  expect(response.status).toBe(status);
  expect(response.headers.get("content-type")).toContain("application/json");
  const text = await response.text();
  for (const sensitive of [bearer, deviceToken, accessToken, serviceAccount.private_key]) {
    expect(text.includes(sensitive)).toBe(false);
  }
  expect(JSON.parse(text)).toEqual(body);
}

function oauthOK() {
  return Response.json({ access_token: accessToken, expires_in: 3600, token_type: "Bearer" });
}

function googleError(status, error = {}) {
  return Response.json({ error: { message: `${deviceToken} ${bearer}`, ...error } }, { status });
}

beforeAll(async () => {
  keys = await crypto.subtle.generateKey({ name: "RSASSA-PKCS1-v1_5", modulusLength: 2048,
    publicExponent: new Uint8Array([1, 0, 1]), hash: "SHA-256" }, true, ["sign", "verify"]);
  const pkcs8 = await crypto.subtle.exportKey("pkcs8", keys.privateKey);
  serviceAccount = { client_email: "throwaway@example.invalid",
    private_key: `-----BEGIN PRIVATE KEY-----\n${base64(pkcs8)}\n-----END PRIVATE KEY-----\n` };
});

beforeEach(async () => {
  // A new project scopes the module cache independently for each test.
  env = { FCM_PROJECT_ID: `test-project-${++sequence}`,
    FCM_SERVICE_ACCOUNT_JSON: JSON.stringify(serviceAccount), PUSH_AUTH_TOKENS: await digest(bearer) };
  // Build a real Request from every fetch call so options the Workers runtime
  // rejects (e.g. redirect: "error") fail here as they do in production.
  upstream = vi.fn(async (url, init) => {
    new Request(url, init);
    return url === tokenURL ? oauthOK() : Response.json({ name: "sent" });
  });
  vi.stubGlobal("fetch", upstream);
  logs = ["log", "warn", "error"].map((method) => vi.spyOn(console, method));
});

afterEach(() => {
  for (const log of logs) expect(log).not.toHaveBeenCalled();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("POST /v1/push configuration and authorization", () => {
  it.each(["FCM_PROJECT_ID", "FCM_SERVICE_ACCOUNT_JSON", "PUSH_AUTH_TOKENS"])("is disabled without %s", async (key) => {
    delete env[key];
    await expectResult(await send(), 501, { code: "push_not_configured" });
    expect(upstream).not.toHaveBeenCalled();
  });

  it.each(["", " , , ", "not-a-sha256-digest"])("fails closed with an empty or invalid digest list %j", async (tokens) => {
    env.PUSH_AUTH_TOKENS = tokens;
    await expectResult(await send(), 501, { code: "push_not_configured" });
  });

  it.each([null, "Bearer wrong", "Basic nope", "Bearer", "Bearer wrong extra"])("rejects authorization %j", async (authorization) => {
    await expectResult(await send(wake(), { authorization }), 401, { code: "unauthorized" });
    expect(upstream).not.toHaveBeenCalled();
  });

  it("accepts a rotated token from a comma-separated digest list", async () => {
    env.PUSH_AUTH_TOKENS = `${await digest("old")}, ${env.PUSH_AUTH_TOKENS.toUpperCase()}`;
    await expectResult(await send(), 202, { ok: true });
  });

  it("only accepts POST", async () => {
    const res = await worker.fetch(new Request("https://relay.example/v1/push"), env);
    expect(res.status).toBe(405);
    expect(upstream).not.toHaveBeenCalled();
  });
});

describe("push body validation", () => {
  it("rejects unsupported providers", async () => {
    await expectResult(await send(wake({ provider: "apns" })), 400, { code: "unsupported_provider" });
  });

  it.each([
    null, [], "wake", {}, wake({ extra: true }), wake({ platform: "web" }),
    wake({ platform: null }), wake({ token: "" }), wake({ token: "a".repeat(4097) }),
    wake({ token: 123 }), wake({ hint: null }), wake({ hint: [] }), wake({ hint: {} }),
    ...[-1, 1.5, Number.MAX_SAFE_INTEGER + 1, "123"].map((delivery_id) => wake({ hint: { delivery_id, kind: "reset" } })),
    wake({ hint: { delivery_id: 0, kind: "unknown" } }),
  ])("rejects invalid body %#", async (body) => {
    expect((await send(body)).status).toBe(400);
    expect(upstream).not.toHaveBeenCalled();
  });

  it.each(["{", "", " ".repeat(8193), JSON.stringify(wake({ token: "é".repeat(4096) }))])("rejects malformed or oversized JSON %#", async (raw) => {
    await expectResult(await send(null, { raw }), 400, { code: "invalid_request" });
    expect(upstream).not.toHaveBeenCalled();
  });

  it("enforces the streaming size limit without trusting content-length", async () => {
    let chunks = 0;
    const body = new ReadableStream({ pull(controller) {
      chunks++;
      controller.enqueue(new Uint8Array(4096).fill(32));
      if (chunks === 4) controller.close();
    } });
    const res = await worker.fetch(new Request("https://relay.example/v1/push", {
      method: "POST", headers: { authorization: `Bearer ${bearer}`, "content-length": "1" }, body,
    }), env);
    await expectResult(res, 400, { code: "invalid_request" });
    expect(upstream).not.toHaveBeenCalled();
  });

  it.each(["reset", "firesale", "job", "test"])("accepts kind %s and boundary values", async (kind) => {
    await expectResult(await send(wake({ token: "x".repeat(4096), hint: { delivery_id: 0, kind } })), 202, { ok: true });
    await expectResult(await send(wake({ token: "x", hint: { delivery_id: Number.MAX_SAFE_INTEGER, kind } })), 202, { ok: true });
  });

  it("accepts exactly 8 KiB of JSON", async () => {
    const raw = JSON.stringify(wake());
    await expectResult(await send(null, { raw: raw + " ".repeat(8192 - raw.length) }), 202, { ok: true });
  });
});

describe("Google JWT, caching, and FCM request", () => {
  it("signs an RS256 JWT and sends exactly the data-only Android contract", async () => {
    await expectResult(await send(), 202, { ok: true });
    const [url, init] = upstream.mock.calls[0];
    expect(url).toBe(tokenURL);
    expect(init.method).toBe("POST");
    const form = new URLSearchParams(init.body);
    expect(form.get("grant_type")).toBe("urn:ietf:params:oauth:grant-type:jwt-bearer");
    const [header, payload, signature] = form.get("assertion").split(".");
    const decode = (s) => Uint8Array.from(atob(s.replace(/-/g, "+").replace(/_/g, "/")), (c) => c.charCodeAt(0));
    expect(JSON.parse(new TextDecoder().decode(decode(header)))).toEqual({ alg: "RS256", typ: "JWT" });
    const claims = JSON.parse(new TextDecoder().decode(decode(payload)));
    expect(claims).toEqual({ iss: serviceAccount.client_email,
      scope: "https://www.googleapis.com/auth/firebase.messaging", aud: tokenURL,
      iat: expect.any(Number), exp: expect.any(Number) });
    expect(claims.exp - claims.iat).toBe(3600);
    expect(Math.abs(claims.iat - Math.floor(Date.now() / 1000))).toBeLessThan(5);
    expect(await crypto.subtle.verify("RSASSA-PKCS1-v1_5", keys.publicKey, decode(signature),
      new TextEncoder().encode(`${header}.${payload}`))).toBe(true);
    const [fcmURL, fcm] = upstream.mock.calls[1];
    expect(fcmURL).toBe(`https://fcm.googleapis.com/v1/projects/${env.FCM_PROJECT_ID}/messages:send`);
    expect(fcm.method).toBe("POST");
    expect(new Headers(fcm.headers).get("authorization")).toBe(`Bearer ${accessToken}`);
    expect(JSON.parse(fcm.body)).toEqual({ message: { token: deviceToken,
      data: { type: "wake", delivery_id: "123", kind: "reset" },
      android: { priority: "high", ttl: "3600s" } } });
  });

  it("adds only background APNs configuration for iOS", async () => {
    await expectResult(await send(wake({ platform: "ios" })), 202, { ok: true });
    expect(JSON.parse(upstream.mock.calls[1][1].body)).toEqual({ message: { token: deviceToken,
      data: { type: "wake", delivery_id: "123", kind: "reset" },
      android: { priority: "high", ttl: "3600s" },
      apns: { headers: { "apns-priority": "5", "apns-push-type": "background" },
        payload: { aps: { "content-available": 1 } } } } });
  });

  it("caches one OAuth exchange for two sends, refreshing five minutes early", async () => {
    const now = Date.now();
    const clock = vi.spyOn(Date, "now").mockReturnValue(now);
    await expectResult(await send(), 202, { ok: true });
    clock.mockReturnValue(now + 3299 * 1000);
    await expectResult(await send(), 202, { ok: true });
    expect(upstream.mock.calls.filter(([url]) => url === tokenURL)).toHaveLength(1);
    clock.mockReturnValue(now + 3300 * 1000);
    await expectResult(await send(), 202, { ok: true });
    expect(upstream.mock.calls.filter(([url]) => url === tokenURL)).toHaveLength(2);
  });

  it("uses token_uri from the service account", async () => {
    const custom = "https://oauth2.googleapis.com/custom-token";
    env.FCM_SERVICE_ACCOUNT_JSON = JSON.stringify({ ...serviceAccount, token_uri: custom });
    upstream.mockImplementation(async (url) => url === custom ? oauthOK() : Response.json({ name: "sent" }));
    await expectResult(await send(), 202, { ok: true });
    expect(upstream.mock.calls[0][0]).toBe(custom);
    const assertion = new URLSearchParams(upstream.mock.calls[0][1].body).get("assertion");
    expect(JSON.parse(atob(assertion.split(".")[1].replace(/-/g, "+").replace(/_/g, "/"))).aud).toBe(custom);
  });

  it("does not reuse a cached token after credentials change", async () => {
    await send();
    env.FCM_SERVICE_ACCOUNT_JSON = JSON.stringify({ ...serviceAccount, client_email: "rotated@example.invalid" });
    await send();
    expect(upstream.mock.calls.filter(([url]) => url === tokenURL)).toHaveLength(2);
  });
});

describe("Google errors and privacy", () => {
  it.each([
    [404, {}, 410, "unregistered"],
    [400, { status: "UNREGISTERED" }, 410, "unregistered"],
    [400, { details: [{ errorCode: "UNREGISTERED" }] }, 410, "unregistered"],
    [400, { status: "INVALID_ARGUMENT", message: "The registration token is not valid" }, 410, "unregistered"],
    [400, { status: "INVALID_ARGUMENT", details: [{ fieldViolations: [{ field: "message.token", description: "Invalid value" }] }] }, 410, "unregistered"],
    [400, { status: "INVALID_ARGUMENT", message: "Invalid TTL" }, 502, "push_upstream_error"],
    [418, {}, 502, "push_upstream_error"],
    [302, {}, 502, "push_upstream_error"],
    [429, {}, 503, "push_upstream_unavailable"],
    [500, {}, 503, "push_upstream_unavailable"],
    [503, {}, 503, "push_upstream_unavailable"],
  ])("maps FCM %i %# without exposing upstream bodies", async (status, error, expected, code) => {
    upstream.mockImplementation(async (url, init) => {
      new Request(url, init);
      return url === tokenURL ? oauthOK() : googleError(status, error);
    });
    await expectResult(await send(), expected, { code });
  });

  it("never follows a redirect from Google", async () => {
    await send();
    for (const [, init] of upstream.mock.calls) expect(init.redirect).toBe("manual");
  });

  it.each([401, 403])("refreshes and retries FCM %i once", async (status) => {
    upstream.mockResolvedValueOnce(oauthOK()).mockResolvedValueOnce(googleError(status))
      .mockResolvedValueOnce(oauthOK()).mockResolvedValueOnce(Response.json({ name: "sent" }));
    await expectResult(await send(), 202, { ok: true });
    expect(upstream.mock.calls.map(([url]) => url === tokenURL ? "oauth" : "fcm")).toEqual(["oauth", "fcm", "oauth", "fcm"]);
  });

  it("replaces the rejected cached bearer on the retried send", async () => {
    await send();
    upstream.mockResolvedValueOnce(googleError(401))
      .mockResolvedValueOnce(Response.json({ access_token: "replacement-access-token", expires_in: 3600 }))
      .mockResolvedValueOnce(Response.json({ name: `${deviceToken} ${bearer}` }));
    await expectResult(await send(), 202, { ok: true });
    expect(new Headers(upstream.mock.calls[4][1].headers).get("authorization")).toBe("Bearer replacement-access-token");
    expect(upstream).toHaveBeenCalledTimes(5);
  });

  it.each([[404, 410, "unregistered"], [503, 503, "push_upstream_unavailable"]])("maps the FCM %i after an auth retry", async (status, expected, code) => {
    upstream.mockResolvedValueOnce(oauthOK()).mockResolvedValueOnce(googleError(401))
      .mockResolvedValueOnce(oauthOK()).mockResolvedValueOnce(googleError(status));
    await expectResult(await send(), expected, { code });
    expect(upstream).toHaveBeenCalledTimes(4);
  });

  it.each([401, 403])("returns auth failure after the second FCM %i and clears cache", async (status) => {
    upstream.mockImplementation(async (url) => url === tokenURL ? oauthOK() : googleError(status));
    await expectResult(await send(), 502, { code: "push_upstream_auth" });
    expect(upstream).toHaveBeenCalledTimes(4);
    upstream.mockImplementation(async (url) => url === tokenURL ? oauthOK() : Response.json({ name: "sent" }));
    await expectResult(await send(), 202, { ok: true });
    expect(upstream.mock.calls.filter(([url]) => url === tokenURL)).toHaveLength(3);
  });

  it.each([[400, 502, "push_upstream_auth"], [401, 502, "push_upstream_auth"],
    [403, 502, "push_upstream_auth"], [429, 503, "push_upstream_unavailable"],
    [500, 503, "push_upstream_unavailable"], [418, 502, "push_upstream_error"]])("maps OAuth %i without retry", async (status, expected, code) => {
    upstream.mockResolvedValue(googleError(status));
    await expectResult(await send(), expected, { code });
    expect(upstream).toHaveBeenCalledTimes(1);
  });

  it.each(["oauth", "fcm"])("sanitizes %s network failures and never logs", async (stage) => {
    upstream.mockImplementation(async (url) => {
      if (stage === "fcm" && url === tokenURL) return oauthOK();
      throw new Error(`${deviceToken} ${bearer}`);
    });
    await expectResult(await send(), 503, { code: "push_upstream_unavailable" });
  });

  it.each(["not-json", "missing-fields", "invalid-pem", "invalid-pkcs8"])("sanitizes bad service account %s", async (fixture) => {
    const accounts = { "not-json": "not-json", "missing-fields": "{}",
      "invalid-pem": JSON.stringify({ ...serviceAccount, private_key: "invalid" }),
      "invalid-pkcs8": JSON.stringify({ ...serviceAccount, private_key: "-----BEGIN PRIVATE KEY-----\nYWJj\n-----END PRIVATE KEY-----\n" }) };
    env.FCM_SERVICE_ACCOUNT_JSON = accounts[fixture];
    await expectResult(await send(), 502, { code: "push_upstream_auth" });
    expect(upstream).not.toHaveBeenCalled();
  });

  it.each([{}, { access_token: accessToken }, { access_token: accessToken, expires_in: "3600" },
    { access_token: accessToken, expires_in: -1 }])("sanitizes malformed OAuth success %#", async (body) => {
    upstream.mockResolvedValue(Response.json(body));
    await expectResult(await send(), 502, { code: "push_upstream_error" });
  });
});
