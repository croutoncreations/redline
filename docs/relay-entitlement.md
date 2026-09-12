# Relay entitlement contract

This describes the contract the closed hosted relay (`ALLOW_UNENTITLED=false`)
enforces against a **host** connection at `wss://<relay>/v1/session/{session_id}?role=host`.
A **client** (phone) connection at `role=client` never presents an entitlement
and is not covered by any of this: it is admitted purely because an entitled
host is already attached to the same session. This document covers the
relay's entitlement boundary; transport metadata, traffic analysis, Worker
configuration, and issuer/payment security remain separate threat surfaces.

Self-hosted relays (`ALLOW_UNENTITLED=true`, the default in the committed
`wrangler.toml`) skip all of this and take their client cap from
`env.MAX_CLIENTS_DEFAULT` instead. See `docs/self-hosted-relay.md`.

## Token format

```
<base64 claims JSON>.<base64 Ed25519 signature over the claims bytes>
```

The signature covers the raw claims bytes exactly as transmitted (the base64
of the first segment, decoded), not a re-serialized or re-ordered form. The
relay verifies the signature **before** parsing the claims JSON, so an edited
claim set is rejected before anything — including the JSON parser — trusts
attacker-controlled bytes.

## Required claims

All three are required and all three are covered by the signature. There is
no partial-trust mode: a token missing any one of these, or with a claim of
the wrong type or out of range, is rejected the same way as a token with a
bad signature.

| claim         | type    | meaning                                                                 |
|---------------|---------|--------------------------------------------------------------------------|
| `exp`         | number  | Unix seconds. The token is invalid at or after this instant.             |
| `sid`         | string  | `base64url(sha256(session_id))`. Binds the token to one session.         |
| `max_clients` | integer | `1..25` inclusive. The issuer currently signs `5`.                       |

### `sid` derivation

```
sid = base64url( sha256( session_id ) )
```

`session_id` is the path segment from `/v1/session/{session_id}`, decoded
exactly as the relay receives it — the same value used to derive the Durable
Object name. The relay computes this itself from the path on every request;
it never trusts a caller-supplied session id independent of the URL. A
validly signed token whose `sid` does not match the connecting session's
derived `sid` is refused with `different_session` (see below) — it is not
merely "not entitled", since a real issuer did sign it, just for somewhere
else.

Standard base64url: `+`→`-`, `/`→`_`, no `=` padding.

### `max_clients` validation

Must satisfy `Number.isInteger(max_clients) && max_clients >= 1 && max_clients <= 25`.
`0`, negative numbers, non-integers (`5.5`), and values above `25` are all
rejected as `not_entitled`. The issuer currently signs `5`.

## Header

The header is the **only** accepted credential carrier:

```
X-Redline-Entitlement: <token>
```

An earlier version also accepted `?entitlement=<token>` in the query string
as a migration path for pre-header clients. That fallback has been removed:
credentials never belong in a URL, since edge/proxy access logs commonly
retain query strings outside the relay operator's control. A token presented
only as a query parameter is now rejected exactly as if no token had been
sent at all.

## Only `role=host` presents an entitlement

The relay runs the entitlement check only for `role=host` requests. A
`role=client` request skips the check entirely, regardless of whether it
carries an `X-Redline-Entitlement` header, a garbage value, or nothing — the
phone never holds an entitlement token, and admitting it is the Durable
Object's job (based on whether a host is attached and under its
`max_clients`), not this contract's.

## Internal claim boundary

Before forwarding a host request to the Durable Object, the relay:

1. Strips the `X-Redline-Entitlement` header and any `?entitlement=` query
   parameter (defensively, on every request, not only successful ones).
2. Strips any caller-supplied `X-Redline-Internal-Exp` and
   `X-Redline-Internal-Max-Clients` headers.
3. For a host request that just passed verification, injects trusted values
   under those same two header names, taken from the claims the relay itself
   verified — never from anything the caller sent.

The Durable Object (`session.js`) reads only those two internal headers and
never parses a token itself. This means a forged internal header can never
reach the Durable Object: it is removed in step 2 before step 3 has a chance
to overwrite it, so a caller racing the relay to set its own value first
gains nothing — the header is deleted, not merely conditionally trusted.

## Machine-readable error bodies

Every entitlement failure returns a JSON body of the form `{"code": "<code>"}`
so a client can branch on `code` rather than parsing prose. Status codes and
bodies:

| status | code               | meaning                                                                |
|--------|--------------------|--------------------------------------------------------------------------|
| 402    | `not_entitled`     | Missing token, malformed token, bad signature, expired, or an out-of-range/missing claim. |
| 402    | `different_session`| Validly signed token whose `sid` names a different session.           |
| 409    | `too_many_clients` | `role=client` when the session is already at `max_clients`. |
| 423    | `no_host`          | `role=client` when no host is attached to the session. |
| 503    | `session_unavailable` | `role=client` when persisted admission state is missing or invalid; admission fails closed. |

A duplicate host receives `409 {"code":"role_already_connected"}`.

Close code `1008` with reason `entitlement expired` is sent to the host and
every attached client when a Durable Object alarm fires at the stored `exp`.
Host disconnect instead closes all clients with `1000 peer disconnected` and
clears the stored claims and alarm.

`402` rather than `401` for every entitlement failure: nothing is wrong with
the caller's identity — there is no identity here to be wrong about — the
caller simply is not entitled to relay this session.

## Desktop cache and revocation schema

The desktop cache is a versioned JSON record stored in `relay-entitlement.json`
with owner-only permissions and durable atomic replacement. Schema version 3
extends the original five authority fields (`token`, `exp`, `obtained_at`,
`sid`, `max_clients`) with `schema_version` and `credential_fingerprint`.
`credential_fingerprint` is SHA-256 over the high-entropy license credential;
it binds authority to one credential replacement without storing a reversible
key. This is an intentional deviation from the original five-field schema.
Legacy/unversioned records, fingerprint mismatches, unknown fields, omitted
required fields, explicit `null`, and duplicate member names all fail closed for
authority. Under the cache lock, Save recognizes only an exact authority shape
that the schema-v2 serializer could have produced as replaceable legacy.
Malformed schema-v2 and unknown future-schema records remain fail-closed and
nonreplaceable. The cache has one authority-record variant; revocation records
are not written into it.

Terminal `invalid_key`, `lapsed`, and `no_seat` decisions instead write
`relay-entitlement-revocation.json` beside the cache. Its exact closed schema is
`{"schema_version":2,"credential_fingerprint":"…","revoked_token_hashes":["<64 lowercase hex characters>"]}`.
Each entry is SHA-256 over the exact entitlement-token bytes. Entries are
sorted, unique, and contain neither token plaintext nor a reversible license
credential. Unknown, omitted, duplicate, and explicit-null JSON members fail
closed. The file has the same 0600, owner, no-follow, atomic replacement,
inter-process locking, and parent-directory fsync guarantees as the cache, but
uses its own path and lock. Same-fingerprint writes merge under that lock, so
concurrent writers cannot drop hashes. Entries never delete. Growth is expected
to stay small because normal authority renews around half of the maximum
fourteen-day lifetime; the list is intentionally unbounded rather than risk
dropping an unexpired revoked authority during unusually frequent accepted
refreshes.

Startup loads the Keychain credential, then the credential-bound cache, and
finally the marker immediately before publication. A matching marker rejects a
cache if and only if SHA-256 of its exact token bytes is present. This is
independent of `obtained_at`, so clock rollback and a future-dated cache cannot
revive terminally denied authority. A revocation-only cache candidate read still
requires the closed schema, fingerprint, owner, mode, and no-follow checks, but
bypasses wall-clock publication checks; its token is never published and is
used only to ensure terminal handling hashes durable authority. Loading the
marker after potentially blocking cache I/O also catches a terminal write while
the cache load was blocked. A marker for a replaced credential does not affect
the new fingerprint.

Runtime authority is revoked immediately. Terminal handling records every known
current, durable, pending, or in-flight token hash before the bounded,
parent-cancellation-independent shutdown barrier completes. The marker writer
has an independent path, lock, and worker, so an old cache Save may become
visible later but its exact token remains denied at restart. A newly
relay-accepted token with a distinct hash is immediately eligible and may be
cached without clearing the marker.

A cache Save can report parent-directory-fsync uncertainty after atomic rename.
That is durability uncertainty, not authorization failure: if a later restart
observes the file and it remains structurally valid, unexpired, fingerprint
bound, and absent from the hash marker, it is safe to use because the relay had
already accepted that exact token. `persistence_degraded` remains visible while
the running process cannot confirm durability. Marker failures likewise retain
that status. No cross-file commit receipt is required or implied.

`redline serve` claims the listening socket before constructing the controller,
so controller marker writes have one service-process owner. Store process and
file locks still preserve monotonic same-credential hash-set merges across
supported processes. There is no external marker-writer API, so hostile
mutation after startup is outside this ownership model and does not require a
watcher.

## Implemented relay refresh endpoint

`POST /v1/session/{session_id}/entitlement` is implemented by this relay. It
requires `X-Redline-Entitlement: <token>`, applies the same signature, claim,
and `sid` checks as host admission, and returns `204` after replacing the live
session's stored `{exp, maxClients}` and rescheduling its alarm. The host and
all clients remain attached and in-flight traffic is not interrupted. With no
attached host it returns `423 {"code":"no_host"}`. The boundary strips the
credential, query fallback, and forged internal headers before forwarding.

## Public issuer HTTP API contract

The issuer is a separate service planned for the private `redline-issuer`
repository; it is **not implemented here**. The desktop client for this contract
is implemented, while the relay never calls it and never looks anything up. The issuer API base is
`https://redline.croutoncreations.com/api`; paths below are relative to it.

Entitlement creation puts the license key in JSON because this is also the
Mac activation operation:

```http
POST /v1/entitlement
Content-Type: application/json

{"license_key":"rl_live_…","sid":"<base64url sha256>","label":"Studio Mac"}
```

`label` is optional. Success is
`200 {"token":"<signed token>","exp":<unix seconds>,"max_clients":5,"seats":<integer>,"seats_used":<integer>}`.
An unknown key returns `401`; a lapsed subscription returns `402`; exhausted
seats return `409 {"activations":[{"label":"Studio Mac","first_seen":"<RFC3339 timestamp>"}]}`.

The remaining calls authenticate with exactly
`Authorization: Bearer <license_key>`:

- `GET /v1/activations` returns
  `200 {"activations":[{"id":"<opaque id>","label":"Studio Mac","first_seen":"<RFC3339 timestamp>","current":true}]}`.
- `DELETE /v1/activations/{id}` removes that activation and returns `204`.
- `POST /v1/portal` has no request body and returns
  `200 {"url":"https://<short-lived-customer-portal-url>"}`. A portal URL is
  created for each request and must never be cached in an entitlement response.

The issuer signs `{exp, sid, max_clients}`, sets `exp` to issuance time plus 14
days, and currently sets `max_clients` to `5`. Desktop clients sample receipt time
after the complete issuer response body has been handled (independently of caller
or Keychain timing) and reject responses whose token claims disagree with the
surrounding response, exceed the 14-day lifetime plus bounded validation skew, or
carry out-of-range seat/client counts. That skew applies only to issuer-response
coherence: cached and in-memory dial authority ends at signed `exp`, exactly when
the relay alarm expires it. Each accepted desktop authority owns an independent,
generation-bound expiry watcher rather than sharing the serialized renewal or
persistence loop, and the live dialer token supplier also checks raw `exp` so a
blocked Keychain, issuer, refresh, or cache operation cannot extend authority.

### License recovery contract

Recovery is rotation, never retrieval of stored plaintext:

```http
POST /v1/license/recover
Content-Type: application/json

{"email":"customer@example.com"}
```

The endpoint always returns `200` with the same response shape, whether or not
the normalized email belongs to a license, so it cannot be used for account
enumeration. It is rate-limited independently by source IP and normalized
email, with a cooldown that prevents both brute force and email flooding.

For an eligible account, the issuer revokes the lost license-key hash, creates
a random replacement key, and delivers it through the same authenticated
email path used for paid activation. The permanent license record stores only
the replacement key's one-way hash. Plaintext exists only in a separate
encrypted, one-time delivery record with a 24-hour TTL; successful display or
email delivery consumes that record, and expiry deletes it. Recovery preserves
the subscription and its seat count but invalidates the old key for every
issuer API immediately. Concurrent retries must resolve idempotently to one
active replacement, not mint several valid keys. Responses and logs never
contain the old key, the new key, or whether the email matched.

Managed Payments customer deletion is stronger than recovery: it revokes the
license and deletes or anonymizes email and device labels, retaining only the
minimal non-identifying accounting/security records required.

The complete future payment, activation, webhook-ordering, recovery, and
key-rotation contract is in `docs/handoff-relay-launch-prompt.md` Phase 5.

## Test vector

The following is a **fixed, explicitly test-only** Ed25519 vector. It signs
nothing real, is committed on purpose, and exists only so this document and
the verifier in `relay/src/index.js` cannot silently drift apart:
`relay/test/entitlement.spec.js` imports these exact constants from
`relay/test/fixtures/entitlement-vector.js` and asserts the token verifies to
the claims below. If the wire format ever changes, that test fails and this
document must change in the same commit.

**Never reuse this keypair or token for anything real.**

PKCS#8 private key (base64 DER containing the fixed Ed25519 seed; never deployed anywhere):

```
MC4CAQAwBQYDK2VwBCIEIHvMpD0g16iN/YS6HjsejaiLRihmf/MVCJUTVHUwNhnC
```

Matching raw public key (base64, 32 bytes) — this is the value that would go
in `ENTITLEMENT_PUBLIC_KEY` for a deployment using this test key:

```
4guLn8Qk+4hhJCYiLZeX+sb2bv4vLMqtG73sqlqxyrA=
```

Session id the token is bound to:

```
entitlement-doc-vector-session01
```

Resulting `sid` (`base64url(sha256(session_id))`):

```
Y_N8LLXYcrCg2Sf_1uhG_IodeMdkKjAinjZsp5IR740
```

Claim set (JSON, no whitespace, key order `exp, sid, max_clients` as
serialized before signing):

```json
{"exp":4102444800,"sid":"Y_N8LLXYcrCg2Sf_1uhG_IodeMdkKjAinjZsp5IR740","max_clients":5}
```

Resulting token:

```
eyJleHAiOjQxMDI0NDQ4MDAsInNpZCI6IllfTjhMTFhZY3JDZzJTZl8xdWhHX0lvZGVNZGtLakFpbmpac3A1SVI3NDAiLCJtYXhfY2xpZW50cyI6NX0=.2lcnhJKP8V1R/m7YLUeO23I3axGxwymK9Bm0441InvNA1dDJKCBfZVYKS/GMALPBq7aIWTjV64DMLPZE3IznDw==
```

`exp` is `4102444800` (2100-01-01T00:00:00Z), chosen so the vector never
expires in CI.
