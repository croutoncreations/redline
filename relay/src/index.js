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

// Header used to carry the entitlement token from the caller.
const ENTITLEMENT_HEADER = "X-Redline-Entitlement";

// Internal-only claim headers. index.js sets these itself, after
// independently verifying an entitlement token; nothing a caller sends may
// reach the Durable Object under these names. If a caller could set them
// directly, it could dictate its own expiry and client cap and the signature
// check above would be theatre.
const INTERNAL_EXP_HEADER = "X-Redline-Internal-Exp";
const INTERNAL_MAX_CLIENTS_HEADER = "X-Redline-Internal-Max-Clients";

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

    // Only a host ever presents an entitlement. The phone never holds one --
    // it is admitted because an entitled host is already attached to the
    // same session, not because it can prove anything about itself -- so a
    // client request skips the check entirely and is forwarded unexamined.
    let claims = null;
    if (role === "host") {
      const result = await checkEntitlement(env, request, sessionId);
      if (!result.ok) {
        return jsonError(result.status, result.code);
      }
      claims = result.claims;
    }

    const id = env.SESSIONS.idFromName(sessionId);
    let forwarded = requestWithoutEntitlement(request);
    if (claims) {
      forwarded = withInternalHostClaims(forwarded, claims);
    }
    return env.SESSIONS.get(id).fetch(forwarded);
  },
};

/**
 * Remove every accepted credential representation, and any caller-supplied
 * internal claim header, at the authorization boundary. The session object
 * pairs opaque sockets and trusts only what index.js injects below it; it
 * must never see a credential or a forged internal claim.
 */
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

/**
 * Inject the claims index.js just verified, under headers only index.js ever
 * writes. Call only after requestWithoutEntitlement has removed whatever the
 * caller sent under the same names, or this would merely be trusting the
 * caller's forgery instead of overwriting it.
 */
export function withInternalHostClaims(request, claims) {
  const headers = new Headers(request.headers);
  headers.set(INTERNAL_EXP_HEADER, String(claims.exp));
  headers.set(INTERNAL_MAX_CLIENTS_HEADER, String(claims.maxClients));
  return new Request(request, { headers });
}

/**
 * Verify the host may use the relay for this specific session.
 *
 * The check is deliberately shallow: a valid signature from the issuer over
 * an unexpired, session-bound claim set. The relay performs no lookup and
 * learns no identity beyond "this session", so turning the paywall on cannot
 * turn the relay into a way to track who is talking to whom. Everything
 * about who paid lives in the issuer.
 *
 * Only role=host ever calls this. A client is admitted because an entitled
 * host is already attached to the same session, not because it can present
 * anything of its own; the phone never holds an entitlement token.
 */
async function checkEntitlement(env, request, sessionId) {
  // Configuration comes only from the environment. An earlier version let a
  // request header supply the verification key so one deployment could be
  // exercised both open and closed, which meant a caller could sign its own
  // entitlement and hand over the matching public key -- the relay would then
  // dutifully verify the token against the attacker's key and let them in.
  // Nothing a caller sends may influence how that caller is authorised.
  const publicKeyB64 = env.ENTITLEMENT_PUBLIC_KEY || "";
  const allowUnentitled = String(env.ALLOW_UNENTITLED).toLowerCase() === "true";

  if (allowUnentitled) {
    // Open self-hosted mode has no token and takes its client cap from the
    // deployment default; session.js reads that default directly, not from
    // here, so there are no claims to hand back.
    return { ok: true, claims: null };
  }
  if (!publicKeyB64) {
    // Refusing to run closed without a key is safer than silently running
    // open: a misconfigured deployment should not quietly become free.
    return { ok: false, status: 402, code: "not_entitled" };
  }

  // The header is the only accepted credential carrier. An earlier version
  // also accepted `?entitlement=`, which put a bearer token in a URL that
  // edge/proxy access logs commonly retain outside this worker's control;
  // there are no clients left that need the fallback, so it is gone.
  const token = request.headers.get(ENTITLEMENT_HEADER);
  if (!token) {
    return { ok: false, status: 402, code: "not_entitled" };
  }
  return verifyEntitlementToken(token, publicKeyB64, sessionId);
}

/**
 * A token is "<base64 claims>.<base64 Ed25519 signature>".
 *
 * The signature is checked before the claims bytes are ever parsed, so an
 * edited claim set is rejected before anything -- including the JSON parser
 * -- trusts attacker-controlled bytes.
 */
export async function verifyEntitlementToken(token, publicKeyB64, sessionId) {
  const parts = String(token).split(".");
  if (parts.length !== 2 || !parts[0] || !parts[1]) {
    return { ok: false, status: 402, code: "not_entitled" };
  }

  let claimsBytes;
  let signature;
  let publicKeyBytes;
  try {
    claimsBytes = base64ToBytes(parts[0]);
    signature = base64ToBytes(parts[1]);
    publicKeyBytes = base64ToBytes(publicKeyB64);
  } catch {
    return { ok: false, status: 402, code: "not_entitled" };
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
    // so the error code cannot be used to probe the verifier.
    return { ok: false, status: 402, code: "not_entitled" };
  }
  if (!verified) {
    return { ok: false, status: 402, code: "not_entitled" };
  }

  // Only now, after the signature has been checked against the bytes exactly
  // as received, is it safe to parse and trust the claims.
  let claims;
  try {
    claims = JSON.parse(new TextDecoder().decode(claimsBytes));
  } catch {
    return { ok: false, status: 402, code: "not_entitled" };
  }

  if (typeof claims.exp !== "number" || claims.exp * 1000 <= Date.now()) {
    // Short expiry plus renewal is how a lapsed subscription stops working,
    // which is why there is no revocation list to consult here.
    return { ok: false, status: 402, code: "not_entitled" };
  }

  if (!Number.isInteger(claims.max_clients) || claims.max_clients < 1 || claims.max_clients > 25) {
    return { ok: false, status: 402, code: "not_entitled" };
  }

  if (typeof claims.sid !== "string" || claims.sid.length === 0) {
    return { ok: false, status: 402, code: "not_entitled" };
  }

  const expectedSid = await sidForSession(sessionId);
  if (claims.sid !== expectedSid) {
    // A structurally valid, validly signed token for a *different* session is
    // not "not entitled" -- it is entitled to the wrong thing, which is worth
    // a distinct code so a client can tell the two failure modes apart.
    return { ok: false, status: 402, code: "different_session" };
  }

  return {
    ok: true,
    claims: { exp: claims.exp, maxClients: claims.max_clients },
  };
}

/**
 * `sid` binds a token to one session: base64url(sha256(sessionId)). Deriving
 * it from the path rather than trusting a caller-supplied session identifier
 * is what makes claims.sid meaningful to check at all.
 */
export async function sidForSession(sessionId) {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(sessionId));
  return base64UrlFromBytes(new Uint8Array(digest));
}

function base64UrlFromBytes(bytes) {
  let binary = "";
  for (let i = 0; i < bytes.length; i += 1) {
    binary += String.fromCharCode(bytes[i]);
  }
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function base64ToBytes(value) {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

/**
 * Every entitlement failure returns a machine-readable body so a phone or
 * desktop client can branch on `code` instead of parsing prose. See
 * docs/relay-entitlement.md for the fixed set of codes.
 */
function jsonError(status, code) {
  return new Response(JSON.stringify({ code }), {
    status,
    headers: { "content-type": "application/json" },
  });
}
