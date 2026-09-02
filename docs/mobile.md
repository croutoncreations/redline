# Mobile dashboard setup

Redline's phone-sized dashboard is served at `/m`. Remote access is deliberately
limited to devices on the same Tailscale network and uses Tailscale Serve for HTTPS.
Do not use Tailscale Funnel; the shared Redline API credential is not suitable for a
public-internet service.

## 1. Trust the MagicDNS hostname

Find the Mac's fully qualified MagicDNS name with `tailscale status --json`, then add
that exact hostname to the Redline configuration. Do not include a scheme, port, or
wildcard.

```yaml
api:
  trusted_hosts:
    - macbook-pro.example.ts.net
```

Redline remains bound to loopback:

```sh
redline --config redline.yaml serve --listen 127.0.0.1:7436
```

## 2. Enable tailnet-only HTTPS

Proxy the loopback service through Tailscale Serve:

```sh
tailscale serve --bg localhost:7436
tailscale serve status
```

If HTTPS port 443 is already serving another local tool, use a separate HTTPS port
without replacing that configuration:

```sh
tailscale serve --bg --https=8443 localhost:7436
```

Tailscale terminates HTTPS and forwards to Redline over loopback. Redline accepts the
forwarded HTTPS scheme only from a loopback peer, requires an exact trusted Host, and
continues to reject cross-origin requests.

## 3. Pair the phone

```sh
redline --config redline.yaml pair --qr
```

If automatic hostname discovery is unavailable, provide the already-trusted hostname:

```sh
redline --config redline.yaml pair --qr --host macbook-pro.example.ts.net
```

For a non-default Tailscale Serve HTTPS port, include the same port when pairing:

```sh
redline --config redline.yaml pair --qr --host macbook-pro.example.ts.net --port 8443
```

Scan the QR from a phone connected to the tailnet. The CLI authenticates to the running
Redline service and creates a random pairing credential that expires after ten minutes.
The scanner opens a public Redline pairing page with the credential in the URL fragment,
which is not sent in HTTP requests or consumed by link previews. Tap **Pair this browser**
to redeem it once, create a Secure, HttpOnly, SameSite=Strict session cookie, and continue
to `/m`.

The session cookie lasts 30 days and is renewed on every authenticated request, so a
browser that opens the dashboard at least once within any 30-day span stays paired without
rescanning. A device left unused past that window, or one whose cookie was cleared, shows
a "Session expired" prompt on `/m` and needs a fresh `pair --qr` scan.

Until it is redeemed or expires, the QR can grant a browser the same API authority as the
Redline CLI. Keep it private.

## Revoking access

Redline does not track individual devices, so revocation is all-or-nothing: rotating the
API token signs out every paired browser and invalidates saved CLI credentials. Because
sessions renew on use, this is the only way to end an active session before its window
lapses.

```bash
redline --config redline.yaml token rotate --yes
```

The command replaces the protected `api-token` file atomically and never prints the new
secret. A running service keeps serving the previous token until it reloads, so **restart
Redline** after rotating, then re-run `pair --qr` for each device you still want paired.

## Operational checks

- The dashboard URL must begin with `https://`; service workers and PWA installation do
  not work on a non-local HTTP origin.
- `redline candidates --provider <account>` previews the stored-snapshot decision and
  per-task reasons without collecting usage or writing scheduler history.
- `redline task dispatch <task-id>` resolves repository revisions server-side and still
  applies every budget, cooldown, concurrency, repository-change, and pressure-tier gate.
- A hard usage refresh is explicit. Candidate preview itself never contacts a provider.
