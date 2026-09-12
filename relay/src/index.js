/**
 * Redline relay request boundary. Payloads remain Noise-encrypted end to end;
 * this worker only authorizes hosts and selects a Durable Object by session.
 */

export { RelaySession } from "./session.js";

const SESSION_ID_PATTERN = /^[A-Za-z0-9_-]{16,128}$/;
const VALID_ROLES = new Set(["host", "client"]);
const ENTITLEMENT_HEADER = "X-Redline-Entitlement";
const INTERNAL_EXP_HEADER = "X-Redline-Internal-Exp";
const INTERNAL_MAX_CLIENTS_HEADER = "X-Redline-Internal-Max-Clients";
const NOT_ENTITLED = Object.freeze({ ok: false, status: 402, code: "not_entitled" });

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    if (url.pathname === "/health") {
      return new Response("ok", { headers: { "content-type": "text/plain; charset=utf-8" } });
    }

    const refreshMatch = url.pathname.match(/^\/v1\/session\/([^/]+)\/entitlement$/);
    const socketMatch = url.pathname.match(/^\/v1\/session\/([^/]+)$/);
    if (!refreshMatch && !socketMatch) return new Response("not found", { status: 404 });

    let sessionId;
    try {
      sessionId = decodeURIComponent((refreshMatch || socketMatch)[1]);
    } catch {
      return new Response("invalid session id", { status: 400 });
    }
    if (!SESSION_ID_PATTERN.test(sessionId)) {
      return new Response("invalid session id", { status: 400 });
    }

    if (refreshMatch) {
      if (request.method !== "POST") return new Response("method not allowed", { status: 405 });
      const result = await checkSignedEntitlement(env, request, sessionId);
      if (!result.ok) return jsonError(result.status, result.code);
      const id = env.SESSIONS.idFromName(sessionId);
      let forwarded = requestWithoutEntitlement(request);
      if (result.claims) forwarded = withInternalHostClaims(forwarded, result.claims);
      return env.SESSIONS.get(id).fetch(forwarded);
    }

    const role = url.searchParams.get("role");
    if (!VALID_ROLES.has(role)) return new Response("role must be host or client", { status: 400 });
    if (request.headers.get("Upgrade") !== "websocket") {
      return new Response("expected a websocket upgrade", { status: 426 });
    }

    let claims = null;
    if (role === "host") {
      const result = await checkEntitlement(env, request, sessionId);
      if (!result.ok) return jsonError(result.status, result.code);
      claims = result.claims;
    }

    const id = env.SESSIONS.idFromName(sessionId);
    let forwarded = requestWithoutEntitlement(request);
    if (claims) forwarded = withInternalHostClaims(forwarded, claims);
    return env.SESSIONS.get(id).fetch(forwarded);
  },
};

export function requestWithoutEntitlement(request) {
  const cleanURL = new URL(request.url);
  cleanURL.searchParams.delete("entitlement");
  const cleanHeaders = new Headers(request.headers);
  cleanHeaders.delete(ENTITLEMENT_HEADER);
  cleanHeaders.delete(INTERNAL_EXP_HEADER);
  cleanHeaders.delete(INTERNAL_MAX_CLIENTS_HEADER);
  const moved = new Request(cleanURL.toString(), request);
  return new Request(moved, { headers: cleanHeaders });
}

export function withInternalHostClaims(request, claims) {
  const headers = new Headers(request.headers);
  headers.set(INTERNAL_EXP_HEADER, String(claims.exp));
  headers.set(INTERNAL_MAX_CLIENTS_HEADER, String(claims.maxClients));
  return new Request(request, { headers });
}

async function checkEntitlement(env, request, sessionId) {
  const allowUnentitled = String(env.ALLOW_UNENTITLED).toLowerCase() === "true";
  if (allowUnentitled) return { ok: true, claims: null };
  return checkSignedEntitlement(env, request, sessionId);
}

async function checkSignedEntitlement(env, request, sessionId) {
  const publicKeyB64 = env.ENTITLEMENT_PUBLIC_KEY || "";
  const token = request.headers.get(ENTITLEMENT_HEADER);
  if (!publicKeyB64 || !token) return NOT_ENTITLED;
  return verifyEntitlementToken(token, publicKeyB64, sessionId);
}

/** Verify the signature before parsing or inspecting any signed claim byte. */
export async function verifyEntitlementToken(token, publicKeyB64, sessionId) {
  const parts = String(token).split(".");
  if (parts.length !== 2 || !parts[0] || !parts[1]) return NOT_ENTITLED;

  let claimsBytes;
  let signature;
  let publicKeyBytes;
  try {
    claimsBytes = base64ToBytes(parts[0]);
    signature = base64ToBytes(parts[1]);
    publicKeyBytes = base64ToBytes(publicKeyB64);
  } catch {
    return NOT_ENTITLED;
  }

  try {
    const key = await crypto.subtle.importKey(
      "raw",
      publicKeyBytes,
      { name: "Ed25519" },
      false,
      ["verify"],
    );
    if (!(await crypto.subtle.verify("Ed25519", key, signature, claimsBytes))) {
      return NOT_ENTITLED;
    }
  } catch {
    return NOT_ENTITLED;
  }

  let claims;
  try {
    claims = JSON.parse(new TextDecoder().decode(claimsBytes));
  } catch {
    return NOT_ENTITLED;
  }
  if (claims === null || typeof claims !== "object" || Array.isArray(claims)) {
    return NOT_ENTITLED;
  }
  if (typeof claims.exp !== "number" || !Number.isFinite(claims.exp) || claims.exp * 1000 <= Date.now()) {
    return NOT_ENTITLED;
  }
  if (typeof claims.sid !== "string" || claims.sid.length === 0) return NOT_ENTITLED;
  if (!Number.isInteger(claims.max_clients) || claims.max_clients < 1 || claims.max_clients > 25) {
    return NOT_ENTITLED;
  }

  if (claims.sid !== await sidForSession(sessionId)) {
    return { ok: false, status: 402, code: "different_session" };
  }
  return { ok: true, claims: { exp: claims.exp, maxClients: claims.max_clients } };
}

export async function sidForSession(sessionId) {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(sessionId));
  return base64UrlFromBytes(new Uint8Array(digest));
}

function base64UrlFromBytes(bytes) {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function base64ToBytes(value) {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
  return bytes;
}

function jsonError(status, code) {
  return new Response(JSON.stringify({ code }), {
    status,
    headers: { "content-type": "application/json" },
  });
}
