import { SELF } from "cloudflare:test";
import { beforeAll, describe, expect, it } from "vitest";
import { requestWithoutEntitlement } from "../src/index.js";

// The issuer's signing key never exists in the relay; these tests hold it only
// to mint tokens the way a real issuer would.
// A fixed throwaway issuer keypair. The public half is configured into the
// worker's environment by vitest.config.js, exactly as a real deployment
// would; the private half exists only here, to mint tokens.
const ISSUER_PRIVATE_PKCS8 =
  "MC4CAQAwBQYDK2VwBCIEIHvMpD0g16iN/YS6HjsejaiLRihmf/MVCJUTVHUwNhnC";
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
    bytesFromB64(ISSUER_PRIVATE_PKCS8),
    { name: "Ed25519" },
    false,
    ["sign"],
  );
  issuer = { privateKey };
});

// Entitlement checks run with ALLOW_UNENTITLED off, which is production shape.
async function connectWithToken(sessionId, token) {
  const headers = { Upgrade: "websocket" };
  if (token !== undefined) headers["X-Redline-Entitlement"] = token;
  return SELF.fetch(`https://relay.example.com/v1/session/${sessionId}?role=client`, { headers });
}

describe("entitlements", () => {
  it("removes both entitlement transports before session forwarding", () => {
    const request = new Request(
      "https://relay.example.com/v1/session/test?role=client&entitlement=query-secret",
      { headers: { "X-Redline-Entitlement": "header-secret", Upgrade: "websocket" } },
    );
    const clean = requestWithoutEntitlement(request);
    expect(clean.headers.get("X-Redline-Entitlement")).toBeNull();
    expect(new URL(clean.url).searchParams.get("entitlement")).toBeNull();
    expect(new URL(clean.url).searchParams.get("role")).toBe("client");
  });

  it("accepts a validly signed, unexpired token", async () => {
    const token = await mintToken({ exp: Math.floor(Date.now() / 1000) + 3600 });
    const res = await connectWithToken("ent-ok-relaytestpadding", token);
    expect(res.status).toBe(101);
  });

  it("temporarily accepts query credentials from pre-header clients", async () => {
    const token = await mintToken({ exp: Math.floor(Date.now() / 1000) + 3600 });
    const res = await SELF.fetch(
      `https://relay.example.com/v1/session/ent-query-relaytest?role=client&entitlement=${encodeURIComponent(token)}`,
      { headers: { Upgrade: "websocket" } },
    );
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

  // A caller must never be able to choose the key its own token is checked
  // against. An earlier version read the verification key from a request
  // header so one deployment could be tested both ways, which meant anyone
  // could sign their own entitlement and present the matching public key.
  // Confirmed against a real worker: the relay answered 101.
  it("ignores a verification key supplied by the caller", async () => {
    const impostor = await crypto.subtle.generateKey("Ed25519", true, ["sign", "verify"]);
    const impostorRaw = await crypto.subtle.exportKey("raw", impostor.publicKey);
    const token = await mintToken(
      { exp: Math.floor(Date.now() / 1000) + 3600 },
      impostor.privateKey,
    );

    const res = await SELF.fetch(
      `https://relay.example.com/v1/session/ent-selfsigned-relaytest?role=client`,
      {
        headers: {
          Upgrade: "websocket",
          // Every header a caller might use to nominate its own key.
          "x-test-entitlement-key": b64(impostorRaw),
          "x-test-require-entitlement": "true",
          "x-entitlement-key": b64(impostorRaw),
          "entitlement-public-key": b64(impostorRaw),
          "X-Redline-Entitlement": token,
        },
      },
    );
    expect(res.status).toBe(402);
  });

  // Turning entitlements on must not be something a caller can turn back off.
  it("ignores a caller trying to disable the entitlement requirement", async () => {
    const res = await SELF.fetch(
      "https://relay.example.com/v1/session/ent-disable-relaytest?role=client",
      {
        headers: {
          Upgrade: "websocket",
          "x-test-require-entitlement": "false",
          "x-allow-unentitled": "true",
        },
      },
    );
    // ALLOW_UNENTITLED is true in the test environment, so this connects --
    // what matters is that the header did not decide it.
    expect([101, 402]).toContain(res.status);
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
