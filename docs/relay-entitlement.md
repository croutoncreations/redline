# Relay entitlement contract

This describes the contract the closed hosted relay (`ALLOW_UNENTITLED=false`)
enforces against a **host** connection at `wss://<relay>/v1/session/{session_id}?role=host`.
A **client** (phone) connection at `role=client` never presents an entitlement
and is not covered by any of this: it is admitted purely because an entitled
host is already attached to the same session. The relay's zero-knowledge
design means this document is also the relay's entire threat surface for
paywall bypass; nothing described here is enforced anywhere else.

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
| 409    | `too_many_clients` | (Phase 1.2/1.3) `role=client` when the session is already at `max_clients`. |
| 423    | `no_host`          | (Phase 1.2/1.3) `role=client` when no host is attached to the session. |

Close code `1008` with reason `entitlement expired` is sent to the host and
every attached client when a Durable Object alarm fires at the stored `exp`
(Phase 1.2/1.3; not yet implemented as of this contract's introduction).

`402` rather than `401` for every entitlement failure: nothing is wrong with
the caller's identity — there is no identity here to be wrong about — the
caller simply is not entitled to relay this session.

## Issuer HTTP API

The issuer is a separate service (`redline-issuer`, a private repository;
see `docs/handoff-relay-launch-prompt.md` Phase 5 for the full contract). The
relay never calls the issuer and never looks anything up; it only verifies
signatures against `env.ENTITLEMENT_PUBLIC_KEY`. The issuer's role, for
context:

- `POST /v1/entitlement { license_key, sid, label? }` → `200 { token, exp, max_clients, seats, seats_used }`,
  `401` unknown key, `402` subscription lapsed, `409 { activations }` seats
  exhausted.
- `GET /v1/activations`, `DELETE /v1/activations/{id}` — license key in the
  `Authorization` header.
- `POST /v1/portal` — creates a fresh, short-lived Customer Portal URL;
  never cached.

The relay's authenticated host control endpoint,
`POST /v1/session/{session_id}/entitlement`, lets a renewed token update a
live session's stored `{exp, maxClients}` and reschedule its alarm without
disturbing the host socket or any attached clients. It uses the exact same
verification and `sid`-binding rules as the initial `role=host` connection
described above. (Phase 1.2/1.3; not yet implemented as of this contract's
introduction.)

## Test vector

The following is a **fixed, explicitly test-only** Ed25519 vector. It signs
nothing real, is committed on purpose, and exists only so this document and
the verifier in `relay/src/index.js` cannot silently drift apart:
`relay/test/entitlement.spec.js` imports these exact constants from
`relay/test/fixtures/entitlement-vector.js` and asserts the token verifies to
the claims below. If the wire format ever changes, that test fails and this
document must change in the same commit.

**Never reuse this keypair or token for anything real.**

Ed25519 seed / PKCS8 private key (base64, never deployed anywhere):

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
