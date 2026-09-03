import { SELF } from "cloudflare:test";
import { beforeAll, describe, expect, it } from "vitest";

// The issuer's signing key never exists in the relay; these tests hold it only
// to mint tokens the way a real issuer would.
let issuer;
let issuerPublicKeyB64;

function b64(bytes) {
  return btoa(String.fromCharCode(...new Uint8Array(bytes)));
}

async function mintToken(claims, signingKey = issuer.privateKey) {
  const payload = new TextEncoder().encode(JSON.stringify(claims));
  const signature = await crypto.subtle.sign("Ed25519", signingKey, payload);
  return `${b64(payload)}.${b64(signature)}`;
}

beforeAll(async () => {
  issuer = await crypto.subtle.generateKey("Ed25519", true, ["sign", "verify"]);
  const raw = await crypto.subtle.exportKey("raw", issuer.publicKey);
  issuerPublicKeyB64 = b64(raw);
});

// Entitlement checks run with ALLOW_UNENTITLED off, which is production shape.
async function connectWithToken(sessionId, token) {
  const query = token === undefined ? "" : `&entitlement=${encodeURIComponent(token)}`;
  return SELF.fetch(`https://relay.example.com/v1/session/${sessionId}?role=client${query}`, {
    headers: {
      Upgrade: "websocket",
      // The test harness injects production-shaped config per request so one
      // deployment can be exercised both ways.
      "x-test-entitlement-key": issuerPublicKeyB64,
      "x-test-require-entitlement": "true",
    },
  });
}

describe("entitlements", () => {
  it("accepts a validly signed, unexpired token", async () => {
    const token = await mintToken({ exp: Math.floor(Date.now() / 1000) + 3600 });
    const res = await connectWithToken("ent-ok-relaytestpadding", token);
    expect(res.status).toBe(101);
  });

  it("refuses a session with no token when entitlements are required", async () => {
    const res = await connectWithToken("ent-missing-relaytestpadding", undefined);
    expect(res.status).toBe(402);
  });

  it("refuses an expired token", async () => {
    const token = await mintToken({ exp: Math.floor(Date.now() / 1000) - 60 });
    const res = await connectWithToken("ent-expired-relaytestpadding", token);
    expect(res.status).toBe(402);
  });

  it("refuses a token signed by someone else", async () => {
    const impostor = await crypto.subtle.generateKey("Ed25519", true, ["sign", "verify"]);
    const token = await mintToken(
      { exp: Math.floor(Date.now() / 1000) + 3600 },
      impostor.privateKey,
    );
    const res = await connectWithToken("ent-impostor-relaytestpadding", token);
    expect(res.status).toBe(402);
  });

  it("refuses a token whose claims were edited after signing", async () => {
    const token = await mintToken({ exp: Math.floor(Date.now() / 1000) - 60 });
    const [, signature] = token.split(".");
    const forgedClaims = b64(
      new TextEncoder().encode(JSON.stringify({ exp: Math.floor(Date.now() / 1000) + 99999 })),
    );
    const res = await connectWithToken("ent-forged-relaytestpadding", `${forgedClaims}.${signature}`);
    expect(res.status).toBe(402);
  });

  it("refuses structurally broken tokens without crashing", async () => {
    for (const bad of ["", ".", "a.b.c", "notbase64!!.notbase64!!", "onlyonepart"]) {
      const res = await connectWithToken("ent-junk-relaytestpadding", bad);
      expect(res.status).toBe(402);
    }
  });

  // The paywall must not become a way to identify traffic. The relay learns
  // "this token is valid and unexpired" and nothing else, so the token carries
  // no account id and the relay performs no lookup.
  it("does not require any identity claim to authorise a session", async () => {
    const token = await mintToken({ exp: Math.floor(Date.now() / 1000) + 3600 });
    const res = await connectWithToken("ent-anon-relaytestpadding", token);
    expect(res.status).toBe(101);
  });
});
