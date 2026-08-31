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

Scan the QR from a phone connected to the tailnet. The CLI authenticates to the running
Redline service and creates a random pairing credential that expires after ten minutes
and is consumed on first use. Redeeming it creates a Secure, HttpOnly, SameSite=Strict
session cookie and immediately redirects to a URL without the credential query parameter.

Until it is redeemed or expires, the QR can grant a browser the same API authority as the
Redline CLI. Keep it private. A reused or expired QR is rejected. To revoke an already
paired browser, stop Redline, replace the protected `api-token` file beside the
configuration, and pair devices again.

## Operational checks

- The dashboard URL must begin with `https://`; service workers and PWA installation do
  not work on a non-local HTTP origin.
- `redline candidates --provider <account>` previews the stored-snapshot decision and
  per-task reasons without collecting usage or writing scheduler history.
- `redline task dispatch <task-id>` resolves repository revisions server-side and still
  applies every budget, cooldown, concurrency, repository-change, and pressure-tier gate.
- A hard usage refresh is explicit. Candidate preview itself never contacts a provider.
