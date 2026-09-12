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
renewal, and live relay refresh. Relay management API/CLI commands and the Pair a Device
configuration UI are later milestones and do not exist yet. YAML is only an optional
bootstrap. Once `relay-state.json` exists, managed state wins and the service does not
rewrite the YAML.

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
`relay-entitlement-revocation.json` contains exactly schema version, credential
fingerprint, and revocation time—never a token. On startup, a marker matching the loaded
Keychain license always overrides the cache, even if an older cache write completed later;
a marker for a replaced credential is ignored. Relay-accepted recovery is usable in memory,
but the marker is cleared only after the new cache is durable, and clear failure remains
restart-fail-closed with `persistence_degraded`. Entitlements have a maximum lifetime of 14
days. The session id is
generated on first hosted or self-hosted use and persisted only in managed state. YAML
containing `session_id`, `entitlement_token`, or `license_key` is rejected.

Hosted licenses are generic-password items in macOS Keychain. The exact identifiers are
service `ai.redline.mac.relay-license`, account `hosted`. Only the local service reads the
item; future CLI and UI code must call authenticated service endpoints rather than access
Keychain. Linux builds expose the same `LicenseStore` interface for fake-backed tests but
do not provide plaintext file fallback.

A closed relay verifies an entitlement only on the desktop `host` connection. Phone
`client` connections present none: pairing URLs contain only `relay`, `key`, and `session`,
and clients are admitted only while the entitled host owns the session and while its
signed `max_clients` cap permits. Updated phones tolerate and discard the legacy fragment
instead of retaining it.

**Running your own relay.** Follow [Self-host the Redline relay](self-hosted-relay.md),
then put its URL in the YAML block above. The committed self-host profile uses
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
hosts. Otherwise the service uses the first trusted host. With the relay enabled and no
tailnet host, or with `--relay-only`, it emits a code that pairs over the relay alone.

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
