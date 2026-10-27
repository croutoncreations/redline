const MAX_BODY_BYTES = 8 * 1024;
const TOP_LEVEL_FIELDS = new Set(["provider", "platform", "token", "hint"]);
const KINDS = new Set(["reset", "firesale", "job", "test"]);
const DEFAULT_TOKEN_URI = "https://oauth2.googleapis.com/token";
const SCOPE = "https://www.googleapis.com/auth/firebase.messaging";
let cachedToken;

// Only fixed, local codes escape this boundary; Google errors can echo secrets.
class PushError extends Error {
  constructor(status, code) {
    super(code);
    this.status = status;
    this.code = code;
  }
}

export async function handlePush(request, env) {
  if (request.method !== "POST") return new Response("method not allowed", { status: 405 });
  const digests = String(env.PUSH_AUTH_TOKENS || "").split(",").map((s) => s.trim()).filter(Boolean);
  if (!env.FCM_SERVICE_ACCOUNT_JSON || !env.FCM_PROJECT_ID || !digests.length ||
      digests.some((digest) => !/^[a-f0-9]{64}$/i.test(digest))) {
    return json(501, { code: "push_not_configured" });
  }

  try {
    if (!(await authorized(request, digests))) return json(401, { code: "unauthorized" });
    const body = await readBody(request);
    if (!isObject(body) || Object.keys(body).some((field) => !TOP_LEVEL_FIELDS.has(field))) {
      return json(400, { code: "invalid_request" });
    }
    if (body.provider !== "fcm") return json(400, { code: "unsupported_provider" });
    if (!["android", "ios"].includes(body.platform) || typeof body.token !== "string" ||
        body.token.length < 1 || body.token.length > 4096 || !isObject(body.hint) ||
        !Number.isSafeInteger(body.hint.delivery_id) || body.hint.delivery_id < 0 ||
        !KINDS.has(body.hint.kind)) {
      return json(400, { code: "invalid_request" });
    }

    const message = {
      token: body.token,
      data: { type: "wake", delivery_id: String(body.hint.delivery_id), kind: body.hint.kind },
      android: { priority: "high", ttl: "3600s" },
    };
    if (body.platform === "ios") {
      message.apns = {
        headers: { "apns-priority": "5", "apns-push-type": "background" },
        payload: { aps: { "content-available": 1 } },
      };
    }

    for (let attempt = 0; attempt < 2; attempt++) {
      const token = await accessToken(env);
      const response = await googleFetch(
        `https://fcm.googleapis.com/v1/projects/${encodeURIComponent(env.FCM_PROJECT_ID)}/messages:send`,
        { method: "POST", headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
          body: JSON.stringify({ message }) },
      );
      if (response.status === 200) return json(202, { ok: true });
      if (response.status === 401 || response.status === 403) {
        cachedToken = undefined;
        if (attempt === 0) continue;
        throw new PushError(502, "push_upstream_auth");
      }
      if (response.status === 429 || response.status >= 500) {
        throw new PushError(503, "push_upstream_unavailable");
      }
      if (response.status === 404 || await isUnregistered(response)) {
        return json(410, { code: "unregistered" });
      }
      throw new PushError(502, "push_upstream_error");
    }
  } catch (error) {
    if (error instanceof PushError) return json(error.status, { code: error.code });
    return json(502, { code: "push_upstream_error" });
  }
}

async function authorized(request, digests) {
  const bearer = request.headers.get("authorization")?.match(/^Bearer ([^\s]+)$/i)?.[1];
  if (!bearer) return false;
  const actual = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(bearer));
  let matched = 0;
  for (const digest of digests) {
    const expected = Uint8Array.from(digest.match(/../g), (byte) => parseInt(byte, 16));
    // Workers' native constant-time comparison; do not compare secret strings.
    matched |= Number(crypto.subtle.timingSafeEqual(actual, expected));
  }
  return matched !== 0;
}

async function readBody(request) {
  try {
    const reader = request.body?.getReader();
    if (!reader) throw new Error();
    const chunks = [];
    let length = 0;
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      length += value.byteLength;
      if (length > MAX_BODY_BYTES) {
        await reader.cancel();
        throw new Error();
      }
      chunks.push(value);
    }
    const bytes = new Uint8Array(length);
    let offset = 0;
    for (const chunk of chunks) {
      bytes.set(chunk, offset);
      offset += chunk.byteLength;
    }
    return JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes));
  } catch {
    throw new PushError(400, "invalid_request");
  }
}

async function accessToken(env) {
  const credentials = env.FCM_SERVICE_ACCOUNT_JSON;
  const project = env.FCM_PROJECT_ID;
  if (cachedToken?.credentials === credentials && cachedToken.project === project &&
      Date.now() < cachedToken.expiresAt - 300_000) {
    return cachedToken.token;
  }

  let assertion;
  let tokenURI;
  try {
    const account = JSON.parse(credentials);
    if (typeof account.client_email !== "string" || !account.client_email ||
        typeof account.private_key !== "string") throw new Error();
    tokenURI = account.token_uri || DEFAULT_TOKEN_URI;
    const pem = account.private_key.match(/^-----BEGIN PRIVATE KEY-----\s+([A-Za-z0-9+/=\s]+)-----END PRIVATE KEY-----\s*$/);
    if (!pem) throw new Error();
    const der = Uint8Array.from(atob(pem[1].replace(/\s/g, "")), (c) => c.charCodeAt(0));
    const key = await crypto.subtle.importKey("pkcs8", der,
      { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256" }, false, ["sign"]);
    const now = Math.floor(Date.now() / 1000);
    const input = `${base64url(new TextEncoder().encode(JSON.stringify({ alg: "RS256", typ: "JWT" })))}.${
      base64url(new TextEncoder().encode(JSON.stringify({
        iss: account.client_email, scope: SCOPE, aud: tokenURI, iat: now, exp: now + 3600,
      })))}`;
    const signature = await crypto.subtle.sign("RSASSA-PKCS1-v1_5", key, new TextEncoder().encode(input));
    assertion = `${input}.${base64url(new Uint8Array(signature))}`;
  } catch {
    throw new PushError(502, "push_upstream_auth");
  }

  const issuedAt = Date.now();
  const response = await googleFetch(tokenURI, {
    method: "POST", headers: { "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ grant_type: "urn:ietf:params:oauth:grant-type:jwt-bearer", assertion }).toString(),
  });
  if ([400, 401, 403].includes(response.status)) throw new PushError(502, "push_upstream_auth");
  if (response.status === 429 || response.status >= 500) throw new PushError(503, "push_upstream_unavailable");
  if (response.status !== 200) throw new PushError(502, "push_upstream_error");
  let body;
  try {
    body = await response.json();
    if (!body || typeof body.access_token !== "string" || !body.access_token ||
        !Number.isFinite(body.expires_in) || body.expires_in <= 0) throw new Error();
  } catch {
    throw new PushError(502, "push_upstream_error");
  }
  cachedToken = { credentials, project, token: body.access_token, expiresAt: issuedAt + body.expires_in * 1000 };
  return cachedToken.token;
}

async function googleFetch(url, init) {
  try {
    return await fetch(url, { ...init, redirect: "error" });
  } catch {
    throw new PushError(503, "push_upstream_unavailable");
  }
}

async function isUnregistered(response) {
  try {
    const { error } = await response.json();
    if (error?.status === "UNREGISTERED" || error?.details?.some((detail) => detail.errorCode === "UNREGISTERED")) {
      return true;
    }
    if (response.status !== 400 || error?.status !== "INVALID_ARGUMENT") return false;
    return /registration[ _-]*token/i.test(error.message || "") ||
      Boolean(error.details?.some((detail) => detail.fieldViolations?.some((violation) =>
        violation.field === "message.token" || /registration[ _-]*token/i.test(violation.description || ""))));
  } catch {
    return false;
  }
}

function base64url(bytes) {
  return btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function isObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function json(status, body) {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}
