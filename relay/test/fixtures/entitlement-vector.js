/**
 * Fixed, explicitly test-only Ed25519 vector for the relay entitlement
 * contract.
 *
 * This keypair signs nothing real. It exists so `docs/relay-entitlement.md`
 * and the verifier in `src/index.js` cannot silently drift apart: both the
 * vitest environment (`vitest.config.js`) and the entitlement spec import
 * these constants, and the doc reproduces the same literal values with a
 * pointer back here. If the wire format changes, the vector token stops
 * verifying and the "documented test vector" test in entitlement.spec.js
 * fails immediately.
 *
 * Never replace these with real keys or tokens.
 */

// PKCS8-encoded Ed25519 private key. Test-only: generated for this fixture,
// used nowhere else, and never deployed.
export const TEST_ISSUER_PRIVATE_KEY_PKCS8_B64 =
  "MC4CAQAwBQYDK2VwBCIEIHvMpD0g16iN/YS6HjsejaiLRihmf/MVCJUTVHUwNhnC";

// Raw Ed25519 public key matching the private key above. Public keys are
// safe to commit: they can verify a signature and cannot create one.
export const TEST_ISSUER_PUBLIC_KEY_B64 =
  "4guLn8Qk+4hhJCYiLZeX+sb2bv4vLMqtG73sqlqxyrA=";

// The session id the vector token is bound to via its sid claim.
export const TEST_VECTOR_SESSION_ID = "entitlement-doc-vector-session01";

// sid = base64url(sha256(TEST_VECTOR_SESSION_ID)).
export const TEST_VECTOR_CLAIMS = Object.freeze({
  exp: 4102444800, // 2100-01-01T00:00:00Z: far enough out not to expire in CI
  sid: "Y_N8LLXYcrCg2Sf_1uhG_IodeMdkKjAinjZsp5IR740",
  max_clients: 5,
});

// `<base64 claims json>.<base64 Ed25519 signature over the claims bytes>`,
// signed by TEST_ISSUER_PRIVATE_KEY_PKCS8_B64 over
// JSON.stringify(TEST_VECTOR_CLAIMS) with no whitespace, in key order
// exp, sid, max_clients.
export const TEST_VECTOR_TOKEN =
  "eyJleHAiOjQxMDI0NDQ4MDAsInNpZCI6IllfTjhMTFhZY3JDZzJTZl8xdWhHX0lvZGVNZGtLakFpbmpac3A1SVI3NDAiLCJtYXhfY2xpZW50cyI6NX0=.2lcnhJKP8V1R/m7YLUeO23I3axGxwymK9Bm0441InvNA1dDJKCBfZVYKS/GMALPBq7aIWTjV64DMLPZE3IznDw==";
