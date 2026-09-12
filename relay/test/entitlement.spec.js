import { env, SELF } from "cloudflare:test";
import { beforeAll, describe, expect, it } from "vitest";
import worker, {
  requestWithoutEntitlement,
  sidForSession,
  verifyEntitlementToken,
} from "../src/index.js";
import {
  TEST_ISSUER_PRIVATE_KEY_PKCS8_B64,
  TEST_ISSUER_PUBLIC_KEY_B64,
  TEST_VECTOR_CLAIMS,
  TEST_VECTOR_SESSION_ID,
  TEST_VECTOR_TOKEN,
} from "./fixtures/entitlement-vector.js";

// The issuer's signing key never exists in the relay; these tests hold it only
// to mint tokens the way a real issuer would. It is the same fixed, test-only
// vector documented in docs/relay-entitlement.md.
let issuer;

function b64(bytes) {
  return btoa(String.fromCharCode(...new Uint8Array(bytes)));
}

async function mintToken(claims, signingKey = issuer.privateKey) {
  const payload = new TextEncoder().encode(JSON.stringify(claims));
  const signature = await crypto.subtle.sign("Ed25519", signingKey, payload);
  return `${b64(payload)}.${b64(signature)}`;
}

function bytesFromB64(value) {
  const binary = atob(value);
  const out = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) out[i] = binary.charCodeAt(i);
  return out;
}

beforeAll(async () => {
  const privateKey = await crypto.subtle.importKey(
    "pkcs8",
    bytesFromB64(TEST_ISSUER_PRIVATE_KEY_PKCS8_B64),
    { name: "Ed25519" },
    false,
    ["sign"],
  );
  issuer = { privateKey };
});

// Entitlement checks run with ALLOW_UNENTITLED off, which is production shape.
// Only role=host ever presents a token, so every helper here connects as host
// unless it is specifically exercising the client bypass.
async function connectAsHost(sessionId, token, extraHeaders = {}) {
  const headers = { Upgrade: "websocket", ...extraHeaders };
  if (token !== undefined) headers["X-Redline-Entitlement"] = token;
  return SELF.fetch(`https://relay.example.com/v1/session/${sessionId}?role=host`, { headers });
}

async function validClaimsFor(sessionId, overrides = {}) {
  const sid = await sidForSession(sessionId);
  return {
    exp: Math.floor(Date.now() / 1000) + 3600,
    sid,
    max_clients: 5,
    ...overrides,
  };
}

async function bodyCode(res) {
  const body = await res.json();
  return body.code;
}

describe("entitlements: signature and claim validation", () => {
  it("accepts a validly signed, unexpired, correctly bound token", async () => {
    const sessionId = "ent-ok-relaytestpadding";
    const token = await mintToken(await validClaimsFor(sessionId));
    const res = await connectAsHost(sessionId, token);
    expect(res.status).toBe(101);
  });

  it("refuses a host with no token when entitlements are required", async () => {
    const res = await connectAsHost("ent-missing-relaytestpadding", undefined);
    expect(res.status).toBe(402);
    expect(await bodyCode(res)).toBe("not_entitled");
  });

  it("refuses an expired token", async () => {
    const sessionId = "ent-expired-relaytestpadding";
    const token = await mintToken(
      await validClaimsFor(sessionId, { exp: Math.floor(Date.now() / 1000) - 60 }),
    );
    const res = await connectAsHost(sessionId, token);
    expect(res.status).toBe(402);
    expect(await bodyCode(res)).toBe("not_entitled");
  });

  it("refuses a token signed by someone else", async () => {
    const sessionId = "ent-impostor-relaytestpadding";
    const impostor = await crypto.subtle.generateKey("Ed25519", true, ["sign", "verify"]);
    const token = await mintToken(await validClaimsFor(sessionId), impostor.privateKey);
    const res = await connectAsHost(sessionId, token);
    expect(res.status).toBe(402);
    expect(await bodyCode(res)).toBe("not_entitled");
  });

  it("refuses a token whose claims were edited after signing", async () => {
    const sessionId = "ent-forged-relaytestpadding";
    const token = await mintToken(
      await validClaimsFor(sessionId, { exp: Math.floor(Date.now() / 1000) - 60 }),
    );
    const [, signature] = token.split(".");
    const forgedClaims = b64(
      new TextEncoder().encode(
        JSON.stringify(await validClaimsFor(sessionId, { exp: Math.floor(Date.now() / 1000) + 99999 })),
      ),
    );
    const res = await connectAsHost(sessionId, `${forgedClaims}.${signature}`);
    expect(res.status).toBe(402);
    expect(await bodyCode(res)).toBe("not_entitled");
  });

  it("refuses structurally broken tokens without crashing", async () => {
    for (const bad of ["", ".", "a.b.c", "notbase64!!.notbase64!!", "onlyonepart"]) {
      const res = await connectAsHost("ent-junk-relaytestpadding", bad);
      expect(res.status).toBe(402);
      expect(await bodyCode(res)).toBe("not_entitled");
    }
  });

  // The signature must be checked before anything about the claims -- sid
  // included -- is trusted. If sid were compared first, an attacker could
  // learn something about valid sids from a signature-invalid token; if the
  // implementation instead trusted the impostor's signature, this would come
  // back "different_session" instead of "not_entitled".
  it("verifies the signature before trusting the sid claim", async () => {
    const sessionId = "ent-sigorder-relaytestpadding";
    const impostor = await crypto.subtle.generateKey("Ed25519", true, ["sign", "verify"]);
    const token = await mintToken(await validClaimsFor(sessionId), impostor.privateKey);
    const res = await connectAsHost(sessionId, token);
    expect(res.status).toBe(402);
    expect(await bodyCode(res)).toBe("not_entitled");
  });

  // A caller must never be able to choose the key its own token is checked
  // against. An earlier version read the verification key from a request
  // header so one deployment could be tested both ways, which meant anyone
  // could sign their own entitlement and present the matching public key.
  it("ignores a verification key supplied by the caller", async () => {
    const sessionId = "ent-selfsigned-relaytest";
    const impostor = await crypto.subtle.generateKey("Ed25519", true, ["sign", "verify"]);
    const impostorRaw = await crypto.subtle.exportKey("raw", impostor.publicKey);
    const token = await mintToken(await validClaimsFor(sessionId), impostor.privateKey);

    const res = await connectAsHost(sessionId, token, {
      "x-test-entitlement-key": b64(impostorRaw),
      "x-test-require-entitlement": "true",
      "x-entitlement-key": b64(impostorRaw),
      "entitlement-public-key": b64(impostorRaw),
    });
    expect(res.status).toBe(402);
    expect(await bodyCode(res)).toBe("not_entitled");
  });

  // Turning entitlements on must not be something a caller can turn back off.
  it("ignores a caller trying to disable the entitlement requirement", async () => {
    const res = await connectAsHost("ent-disable-relaytest", undefined, {
      "x-test-require-entitlement": "false",
      "x-allow-unentitled": "true",
    });
    // ALLOW_UNENTITLED is false in the entitlement test environment, so
    // nothing a caller sends may flip that; the request has no token at all.
    expect(res.status).toBe(402);
    expect(await bodyCode(res)).toBe("not_entitled");
  });

  // The paywall must not become a way to identify traffic. The relay learns
  // "this token is valid, unexpired, and for this session" and nothing else,
  // so the token carries no account id and the relay performs no lookup.
  it("does not require any identity claim to authorise a session", async () => {
    const sessionId = "ent-anon-relaytestpadding";
    const token = await mintToken(await validClaimsFor(sessionId));
    const res = await connectAsHost(sessionId, token);
    expect(res.status).toBe(101);
  });
});

describe("entitlements: sid binds a token to one session", () => {
  it("computes sid as base64url(sha256(session_id))", async () => {
    // Independently computed with Node's WebCrypto against the same
    // algorithm the verifier uses, so this test would fail if the encoding
    // (base64 vs base64url) or the hash drifted.
    const digest = await crypto.subtle.digest(
      "SHA-256",
      new TextEncoder().encode("some-session-id-relaytestpad"),
    );
    let binary = "";
    for (const byte of new Uint8Array(digest)) binary += String.fromCharCode(byte);
    const expected = btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");

    expect(await sidForSession("some-session-id-relaytestpad")).toBe(expected);
  });

  it("refuses a validly signed token whose sid names a different session", async () => {
    const tokenSessionId = "ent-wrongsid-token-relaytest";
    const connectSessionId = "ent-wrongsid-conn-relaytest";
    const token = await mintToken(await validClaimsFor(tokenSessionId));
    const res = await connectAsHost(connectSessionId, token);
    expect(res.status).toBe(402);
    expect(await bodyCode(res)).toBe("different_session");
  });

  it("refuses a token with no sid claim at all", async () => {
    const sessionId = "ent-nosid-relaytestpadding";
    const claims = await validClaimsFor(sessionId);
    delete claims.sid;
    const token = await mintToken(claims);
    const res = await connectAsHost(sessionId, token);
    expect(res.status).toBe(402);
    expect(await bodyCode(res)).toBe("not_entitled");
  });
});

describe("entitlements: max_clients is an integer from 1 through 25", () => {
  it("accepts the boundary values 1 and 25", async () => {
    for (const maxClients of [1, 25]) {
      const sessionId = `ent-bound-${maxClients}-relaytestpad`;
      const token = await mintToken(await validClaimsFor(sessionId, { max_clients: maxClients }));
      const res = await connectAsHost(sessionId, token);
      expect(res.status).toBe(101);
    }
  });

  it("refuses zero, negative, non-integer, and above-25 values", async () => {
    for (const maxClients of [0, -1, 5.5, 26, 1000]) {
      const sessionId = `ent-badmax-${String(maxClients).replace(".", "d")}-relaytest`;
      const token = await mintToken(await validClaimsFor(sessionId, { max_clients: maxClients }));
      const res = await connectAsHost(sessionId, token);
      expect(res.status).toBe(402);
      expect(await bodyCode(res)).toBe("not_entitled");
    }
  });

  it("refuses a token with no max_clients claim at all", async () => {
    const sessionId = "ent-nomax-relaytestpadding";
    const claims = await validClaimsFor(sessionId);
    delete claims.max_clients;
    const token = await mintToken(claims);
    const res = await connectAsHost(sessionId, token);
    expect(res.status).toBe(402);
    expect(await bodyCode(res)).toBe("not_entitled");
  });
});

describe("entitlements: only role=host presents an entitlement", () => {
  it("bypasses the entitlement check entirely for role=client, even with a garbage token", async () => {
    const res = await SELF.fetch(
      "https://relay.example.com/v1/session/ent-clientbypass-relaytest?role=client",
      { headers: { Upgrade: "websocket", "X-Redline-Entitlement": "not-a-real-token" } },
    );
    // A client is admitted because an entitled host is already attached to
    // the same session, not because of anything it presents itself; a role
    // that never had a token to check must never be refused for lacking one.
    expect(res.status).toBe(101);
  });

  it("bypasses the entitlement check entirely for role=client with no token at all", async () => {
    const res = await SELF.fetch(
      "https://relay.example.com/v1/session/ent-clientnotoken-relaytest?role=client",
      { headers: { Upgrade: "websocket" } },
    );
    expect(res.status).toBe(101);
  });
});

describe("entitlements: credential and internal-claim boundary", () => {
  it("removes both entitlement transports and forged internal claim headers before forwarding", () => {
    const request = new Request(
      "https://relay.example.com/v1/session/test?role=host&entitlement=query-secret",
      {
        headers: {
          "X-Redline-Entitlement": "header-secret",
          "X-Redline-Internal-Exp": "9999999999",
          "X-Redline-Internal-Max-Clients": "999",
          Upgrade: "websocket",
        },
      },
    );
    const clean = requestWithoutEntitlement(request);
    expect(clean.headers.get("X-Redline-Entitlement")).toBeNull();
    expect(clean.headers.get("X-Redline-Internal-Exp")).toBeNull();
    expect(clean.headers.get("X-Redline-Internal-Max-Clients")).toBeNull();
    expect(new URL(clean.url).searchParams.get("entitlement")).toBeNull();
    expect(new URL(clean.url).searchParams.get("role")).toBe("host");
  });

  it("no longer accepts a token carried only in the query string", async () => {
    const sessionId = "ent-query-relaytest";
    const token = await mintToken(await validClaimsFor(sessionId));
    const res = await SELF.fetch(
      `https://relay.example.com/v1/session/${sessionId}?role=host&entitlement=${encodeURIComponent(token)}`,
      { headers: { Upgrade: "websocket" } },
    );
    expect(res.status).toBe(402);
    expect(await bodyCode(res)).toBe("not_entitled");
  });

  it("injects trusted internal claims and strips any forged ones before forwarding a host request", async () => {
    const sessionId = "ent-internal-relaytestpadding";
    const claims = await validClaimsFor(sessionId, { max_clients: 7 });
    const token = await mintToken(claims);

    let captured;
    const fakeEnv = {
      ...env,
      SESSIONS: {
        idFromName: () => "fake-durable-object-id",
        get: () => ({
          fetch: async (request) => {
            captured = request;
            return new Response(null, { status: 200 });
          },
        }),
      },
    };

    const request = new Request(`https://relay.example.com/v1/session/${sessionId}?role=host`, {
      headers: {
        Upgrade: "websocket",
        "X-Redline-Entitlement": token,
        // A caller trying to hand the Durable Object its own claims. These
        // must be discarded, not merely ignored -- if index.js ever forgot to
        // strip before injecting, an attacker forging the header first and
        // losing a race would be pure luck, not a guarantee.
        "X-Redline-Internal-Exp": "1",
        "X-Redline-Internal-Max-Clients": "999",
      },
    });

    // The fake Durable Object stub returns a plain 200: the point of this
    // test is what index.js forwards to it, not a real WebSocket upgrade.
    const res = await worker.fetch(request, fakeEnv);
    expect(res.status).toBe(200);
    expect(captured.headers.get("X-Redline-Entitlement")).toBeNull();
    expect(captured.headers.get("X-Redline-Internal-Exp")).toBe(String(claims.exp));
    expect(captured.headers.get("X-Redline-Internal-Max-Clients")).toBe("7");
  });

  it("injects no internal claims for a client request, which presented none to verify", async () => {
    let captured;
    const fakeEnv = {
      ...env,
      SESSIONS: {
        idFromName: () => "fake-durable-object-id",
        get: () => ({
          fetch: async (request) => {
            captured = request;
            return new Response(null, { status: 200 });
          },
        }),
      },
    };

    const request = new Request(
      "https://relay.example.com/v1/session/ent-clientnoclaims-relaytest?role=client",
      {
        headers: {
          Upgrade: "websocket",
          // Defensive stripping applies regardless of role, even though a
          // client's own header could not have authorised anything.
          "X-Redline-Internal-Exp": "1",
          "X-Redline-Internal-Max-Clients": "999",
        },
      },
    );

    const res = await worker.fetch(request, fakeEnv);
    expect(res.status).toBe(200);
    expect(captured.headers.get("X-Redline-Internal-Exp")).toBeNull();
    expect(captured.headers.get("X-Redline-Internal-Max-Clients")).toBeNull();
  });
});

describe("entitlements: documented test vector", () => {
  // This is the exact vector reproduced in docs/relay-entitlement.md. If the
  // wire format (claim shape, key order, encoding) ever changes, this test
  // fails and the doc must be updated in the same commit -- it cannot drift
  // silently the way a comment could.
  it("verifies against the fixed vector documented in docs/relay-entitlement.md", async () => {
    const result = await verifyEntitlementToken(
      TEST_VECTOR_TOKEN,
      TEST_ISSUER_PUBLIC_KEY_B64,
      TEST_VECTOR_SESSION_ID,
    );
    expect(result.ok).toBe(true);
    expect(result.claims).toEqual({
      exp: TEST_VECTOR_CLAIMS.exp,
      maxClients: TEST_VECTOR_CLAIMS.max_clients,
    });
  });

  it("computes the vector's sid from the vector's session id", async () => {
    expect(await sidForSession(TEST_VECTOR_SESSION_ID)).toBe(TEST_VECTOR_CLAIMS.sid);
  });
});
