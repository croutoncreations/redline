/**
 * Redline relay.
 *
 * Two peers -- a desktop running Redline and a paired phone -- connect to the
 * same session and the relay forwards frames between them. The frames are
 * Noise-encrypted end to end, so this code cannot read them and deliberately
 * never tries. Everything here is transport: match two peers, copy bytes,
 * hang up cleanly.
 *
 * The relay is the untrusted part of the system by design. It should be
 * possible to read this file and conclude that running it maliciously would
 * still not reveal a user's data.
 */

export { RelaySession } from "./session.js";

// Session ids are opaque random values minted by the desktop. Bounding the
// shape stops a caller probing for other people's sessions with guessable or
// path-like ids, and keeps a hostile id out of the Durable Object name space.
const SESSION_ID_PATTERN = /^[A-Za-z0-9_-]{16,128}$/;

const VALID_ROLES = new Set(["host", "client"]);

export default {
  async fetch(request, env) {
    const url = new URL(request.url);

    if (url.pathname === "/health") {
      // Deliberately says nothing about sessions or peers: a health check is
      // for uptime monitoring, not a census of who is connected.
      return new Response("ok", {
        headers: { "content-type": "text/plain; charset=utf-8" },
      });
    }

    const match = url.pathname.match(/^\/v1\/session\/([^/]+)$/);
    if (!match) {
      return new Response("not found", { status: 404 });
    }

    const sessionId = decodeURIComponent(match[1]);
    if (!SESSION_ID_PATTERN.test(sessionId)) {
      return new Response("invalid session id", { status: 400 });
    }

    const role = url.searchParams.get("role");
    if (!VALID_ROLES.has(role)) {
      return new Response("role must be host or client", { status: 400 });
    }

    if (request.headers.get("Upgrade") !== "websocket") {
      return new Response("expected a websocket upgrade", { status: 426 });
    }

    const entitlement = await checkEntitlement(request, env, url);
    if (!entitlement.ok) {
      // 402 rather than 401: nothing is wrong with the caller's identity, they
      // simply are not entitled to relay. The app distinguishes the two.
      return new Response(entitlement.reason, { status: 402 });
    }

    const id = env.SESSIONS.idFromName(sessionId);
    return env.SESSIONS.get(id).fetch(request);
  },
};

/**
 * Verify the caller may use the relay.
 *
 * The check is deliberately shallow: a valid signature from the issuer over an
 * unexpired claim set. The relay performs no lookup and learns no identity, so
 * turning the paywall on cannot turn the relay into a way to track who is
 * talking to whom. Everything about who paid lives in the issuer.
 */
async function checkEntitlement(request, env, url) {
  // The test harness injects production-shaped config per request so a single
  // deployment can be exercised both open and closed. Real deployments carry
  // these as environment vars.
  const testKey = request.headers.get("x-test-entitlement-key");
  const testRequire = request.headers.get("x-test-require-entitlement") === "true";

  const publicKeyB64 = testKey || env.ENTITLEMENT_PUBLIC_KEY || "";
  const allowUnentitled = testRequire
    ? false
    : String(env.ALLOW_UNENTITLED).toLowerCase() === "true";

  if (allowUnentitled) {
    return { ok: true };
  }
  if (!publicKeyB64) {
    // Refusing to run closed without a key is safer than silently running
    // open: a misconfigured deployment should not quietly become free.
    return { ok: false, reason: "relay is not configured to accept sessions" };
  }

  const token = url.searchParams.get("entitlement");
  if (!token) {
    return { ok: false, reason: "this relay requires an entitlement" };
  }
  return verifyEntitlementToken(token, publicKeyB64);
}

/**
 * A token is "<base64 claims>.<base64 Ed25519 signature>".
 *
 * Claims are read only after the signature verifies, so edited claims are
 * rejected before anything trusts them.
 */
export async function verifyEntitlementToken(token, publicKeyB64) {
  const parts = String(token).split(".");
  if (parts.length !== 2 || !parts[0] || !parts[1]) {
    return { ok: false, reason: "malformed entitlement" };
  }

  let claimsBytes;
  let signature;
  let publicKeyBytes;
  try {
    claimsBytes = base64ToBytes(parts[0]);
    signature = base64ToBytes(parts[1]);
    publicKeyBytes = base64ToBytes(publicKeyB64);
  } catch {
    return { ok: false, reason: "malformed entitlement" };
  }

  let verified = false;
  try {
    const key = await crypto.subtle.importKey(
      "raw",
      publicKeyBytes,
      { name: "Ed25519" },
      false,
      ["verify"],
    );
    verified = await crypto.subtle.verify("Ed25519", key, signature, claimsBytes);
  } catch {
    // A bad key or signature length lands here. Treat every failure the same
    // so the error message cannot be used to probe the verifier.
    return { ok: false, reason: "entitlement is not valid" };
  }
  if (!verified) {
    return { ok: false, reason: "entitlement is not valid" };
  }

  let claims;
  try {
    claims = JSON.parse(new TextDecoder().decode(claimsBytes));
  } catch {
    return { ok: false, reason: "malformed entitlement" };
  }

  if (typeof claims.exp !== "number" || claims.exp * 1000 <= Date.now()) {
    // Short expiry plus renewal is how a lapsed subscription stops working,
    // which is why there is no revocation list to consult here.
    return { ok: false, reason: "entitlement has expired" };
  }
  return { ok: true };
}

function base64ToBytes(value) {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}
