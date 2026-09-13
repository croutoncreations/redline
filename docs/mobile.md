# Mobile access setup

Redline has a native phone app and a phone-sized web dashboard at `/m`. A phone reaches
the desktop by one or both of two routes:

- **Direct over your tailnet.** Free, fastest, nothing leaves your network. Needs
  Tailscale on the Mac and the phone.
- **Through the Redline relay.** Works from any network, needs no Tailscale, and carries
  traffic inside an end-to-end encrypted session the relay cannot read. This is the paid
  route; you can also run your own relay.

Set up either, or both. With both, the app uses the tailnet when it can reach it and the
relay when it cannot.

Do not use Tailscale Funnel or any public tunnel: the shared Redline API credential is not
suitable for a public-internet service, and the relay exists so that nothing has to be.

## 1. Direct over the tailnet (optional)

### Trust the MagicDNS hostname

Find the Mac's fully qualified MagicDNS name with `tailscale status --json`, then add it
to the Redline configuration. Do not include a scheme or wildcard. A port is allowed, and
tells Redline which port a phone should pair on when Tailscale Serve is not on 443.

```yaml
api:
  trusted_hosts:
    - macbook-pro.example.ts.net:8443
```

Redline remains bound to loopback:

```sh
redline --config redline.yaml serve --listen 127.0.0.1:7436
```

### Enable tailnet-only HTTPS

Proxy the loopback service through Tailscale Serve:

```sh
tailscale serve --bg --https=8443 localhost:7436
tailscale serve status
```

Tailscale terminates HTTPS and forwards to Redline over loopback. Redline accepts the
forwarded HTTPS scheme only from a loopback peer, requires an exact trusted Host, and
continues to reject cross-origin requests.

## 2. The relay (optional)

The desktop includes hosted entitlement acquisition, protected caching, automatic
renewal, live relay refresh, and an authenticated local management API and CLI. The
Pair a Device configuration UI is a later milestone and does not exist yet. YAML is only
an optional bootstrap. Once `relay-state.json` exists, managed state wins and the service
does not rewrite the YAML.

```yaml
relay:
  enabled: true
  # url: https://relay.example.com  # setting a URL selects self-hosted mode
  # issuer_url: https://issuer.example.com/api  # hosted override only
```

With no `url`, bootstrap selects Redline's hosted relay and reports `needs_license` until
a license is present in Keychain. The service then exchanges it with the configured
HTTPS issuer and dials only with a validated, unexpired session-bound entitlement. A
valid cache is usable immediately while startup renewal runs. Issuer lifetime validation
allows bounded clock skew, but desktop dial authority ends at the signed raw `exp`, matching
the relay alarm. Issuer outages produce `renew_pending` only until that instant and
`unavailable` afterward; retry timers retain nonzero exponential backoff. Issuer 401, 402,
and 409 responses publish `invalid_key`, `lapsed`, and `no_seat`. If Keychain is locked,
denied, or unavailable, hosted relay reports `unavailable`. Cache-write failures are exposed
as sanitized `persistence_degraded` status but do not block expiry, issuer renewal, relay
signals, or license replacement; newer accepted authority supersedes older pending writes.
Every reconnect handshake reads the coordinator's current accepted token synchronously,
while a successful live refresh preserves the existing socket. All of these failures leave
the local API, scheduler, and database running. A custom `url` selects self-hosted mode,
which never reads a license or sends a token. `off` never dials.

The service creates `relay-state.json` beside `relay-identity.json` with mode `0600`, an
inter-process transaction lock, descriptor-based no-follow checks, and durable atomic
replacement. Its closed JSON shape is exactly `mode`, `url`, `issuer_url`, `label`, and
`session_id`; it contains no license or entitlement. The separate
`relay-entitlement.json` cache uses the same owner-only, no-follow, process-lock, and
atomic durability rules and contains schema version, credential fingerprint, `token`,
`exp`, `obtained_at`, `sid`, and `max_clients`. The independently locked
`relay-entitlement-revocation.json` has the exact closed schema
`{"schema_version":3,"revocations":{"<credential_fingerprint>":["<SHA-256 lowercase hex>"]}}`.
Each credential maps to sorted, unique hashes of exact revoked entitlement-token
bytes—never token plaintext or a reversible license. Startup loads the Keychain license,
then the cache, then the ledger immediately before publication. The cache credential's
entry rejects authority if and only if its exact token hash is listed; timestamps do not
affect revocation, so clock rollback and switching away from and back to a credential
cannot revive it. A security- and schema-validated candidate read lets a terminal decision
hash a durable future-dated cache without ever publishing that token. Credential
replacement synchronously enqueues all known startup, current, pending, and in-flight old
hashes before generation advance or cancellation. Every ledger write merges all same- and
different-fingerprint entries under the inter-process lock; completions, failures, and
bounded-shutdown retries are generation-independent. Credential entries and hashes are
never cleared or deleted. After issuer validation and relay acceptance, the controller
reloads the ledger before commit: a listed exact token is not cached or published, while a
distinct token is eligible without a clock comparison or ledger clearing.

A cache Save may return parent-directory-fsync uncertainty after its rename is already
visible. That status remains `persistence_degraded` in the running process, but it is not an
authorization failure: a later restart may use the observed record when it is structurally
valid, unexpired, fingerprint-bound, and its exact token hash is not revoked. No cross-file
commit receipt is required. `redline serve` permits one service/controller owner, while
store locks preserve monotonic hash-set merges across supported processes. Entitlements have a maximum lifetime of 14 days. The session id is
generated on first hosted or self-hosted use and persisted only in managed state. YAML
containing `session_id`, `entitlement_token`, or `license_key` is rejected.

Hosted licenses are generic-password items in macOS Keychain. The exact identifiers are
service `ai.redline.mac.relay-license`, account `hosted`. Only the local service reads the
item; the CLI calls authenticated service endpoints rather than accessing Keychain, and
future UI code must do the same. Linux builds expose the same `LicenseStore` interface for fake-backed tests but
do not provide plaintext file fallback.

A closed relay verifies an entitlement only on the desktop `host` connection. Phone
`client` connections present none: pairing URLs contain only `relay`, `key`, and `session`,
and clients are admitted only while the entitled host owns the session and while its
signed `max_clients` cap permits. Updated phones tolerate and discard the legacy fragment
instead of retaining it. The relay assigns each connected phone an opaque eight-byte
channel on the single desktop socket. The desktop keeps a separate serialized Noise state
and idle timeout per channel, so concurrent phones cannot share nonces or block one
another; closing or invalidating one channel leaves the others connected.

**Running your own relay.** Follow [Self-host the Redline relay](self-hosted-relay.md),
then run `redline relay setup --url https://your-relay.example` against the running
service (or use the YAML bootstrap before managed state exists). The committed self-host profile uses
`ALLOW_UNENTITLED=true`, while still requiring an attached desktop host and enforcing
`MAX_CLIENTS_DEFAULT` (five unless validly configured otherwise).

The desktop's relay identity (a Noise static keypair) is created on first use beside the
database. Every paired phone pins it; rotating it unpairs them all.

## 3. Pair the phone

The pairing code is composed by the Redline service from whatever is configured, so the
menu-bar app and the CLI always produce the same code.

**From the menu bar:** open **Pair a Device...** and scan. The window says which routes the
code offers, and turns into a confirmation when the phone has paired.

**From a terminal:**

```sh
redline --config redline.yaml pair --qr
```

The CLI asks the authenticated running service to compose the complete pairing URL from
resolved managed state; it never reconstructs relay fields from YAML. `--host` and
`--port` are sent as authenticated overrides and validated against the service's trusted
hosts. Otherwise the service uses the first trusted host. A relay route is included only
for `active`, `renew_pending`, or `self_hosted` entitlement state while the host socket is
actually connected. If it is connecting or unavailable, the response carries a structured
reason and a direct route remains usable; `--relay-only` fails clearly instead of emitting
a code that cannot work.

Relay lifecycle commands all use the same authenticated local API:

```sh
redline relay status
redline relay activate <license-key> --label "work mac"
redline relay devices
redline relay device deactivate <opaque-id>
redline relay setup --url https://your-relay.example
redline relay off
redline relay deactivate
```

`relay deactivate` removes the issuer activation marked as the current Mac and then turns
relay mode off. `relay off` only disables local relay use. CLI and API responses never
include the license key, entitlement token, issuer URL, or relay session id.

Pairing itself goes over whichever route the phone can reach. A phone with no Tailscale
pairs through the relay; nothing about pairing requires the tailnet.

The code is a one-time credential that expires after ten minutes and, until then, grants a
scanner the same API authority as the Redline CLI. Keep it private.

### The web dashboard instead of the app

The same code works in a phone browser over the tailnet: the scanner opens a public
Redline pairing page with the credential in the URL fragment, which is not sent in HTTP
requests or consumed by link previews. Tap **Pair this browser** to redeem it once, create
a Secure, HttpOnly, SameSite=Strict session cookie, and continue to `/m`. The web dashboard
is direct-only; the relay carries the native app.

The session cookie lasts 30 days and is renewed on every authenticated request. A browser
left unused past that window, or one whose cookie was cleared, shows a "Session expired"
prompt on `/m` and needs a fresh scan.

## Revoking access

Redline does not track individual devices, so revocation is all-or-nothing: rotating the
API token signs out every paired phone and browser and invalidates saved CLI credentials.

```bash
redline --config redline.yaml token rotate --yes
```

The command replaces the protected `api-token` file atomically and never prints the new
secret. A running service keeps serving the previous token until it reloads, so **restart
Redline** after rotating, then pair each device again.

## Operational checks

- The dashboard URL must begin with `https://`; service workers and PWA installation do
  not work on a non-local HTTP origin.
- The app's header says how it is connected: `live` over the tailnet, `relayed` through the
  relay, `offline` over numbers that are no longer current.
- On Android, `adb logcat -s RedlineRelay` shows every relay dial and its outcome,
  including the relay's own reason for refusing a session.
- `redline candidates --provider <account>` previews the stored-snapshot decision and
  per-task reasons without collecting usage or writing scheduler history.
- `redline task dispatch <task-id>` resolves repository revisions server-side and still
  applies every budget, cooldown, concurrency, repository-change, and pressure-tier gate.
- A hard usage refresh is explicit. Candidate preview itself never contacts a provider.
