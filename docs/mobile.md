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

Phase 1 implements the relay protocol boundary; relay account setup and renewal UI are
Phase 2/3 work and do not exist yet. For current development builds, configure the desktop
in YAML. The URL must be `https://`; `session_id` is a long random value persisted across
restarts.

```yaml
relay:
  enabled: true
  url: https://relay.example.com
  session_id: "<a long random string>"
  # entitlement_token: "<host token>" # closed hosted relay only
```

A closed relay verifies the token only on the desktop `host` connection. Phone `client`
connections present no entitlement to the relay: they are admitted only while that
entitled host owns the session and while its signed `max_clients` cap permits. Current
pairing code still has a legacy entitlement field in its Phase 0 API; the Phase 1 relay
ignores it for clients. Phase 2 removes it from emitted QR payloads, and Phase 4 removes
it from phone storage. Until then, legacy phone builds may still carry and transmit the
ignored entitlement. Do not read this as documentation for a finished purchase,
activation, renewal, or Pair a Device configuration flow.

**Running your own relay.** Follow [Self-host the Redline relay](self-hosted-relay.md),
then put its URL in the YAML block above and omit `entitlement_token`. The committed
self-host profile uses `ALLOW_UNENTITLED=true`, while still requiring an attached desktop
host and enforcing `MAX_CLIENTS_DEFAULT` (five unless validly configured otherwise).

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

The CLI picks the direct endpoint from `--host` if given, else a detected tailnet name that
is trusted, else the first trusted host. With the relay enabled and no tailnet host, or
with `--relay-only`, it emits a code that pairs over the relay alone.

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
