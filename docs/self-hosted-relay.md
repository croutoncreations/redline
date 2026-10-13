# Self-host the Redline relay

The committed `relay/wrangler.toml` is an open self-hosting profile. It does
not require an entitlement token or an issuer. An attached desktop host gates
phone admission, and up to five phones may use one session by default.

## Requirements

- A Cloudflare account with Workers enabled.
- Node.js and npm.
- The Workers Free plan is sufficient for a small personal relay and supports
  the SQLite-backed Durable Object used here. Cloudflare usage limits still
  apply; choose a paid Workers plan if your traffic exceeds the current Free
  plan limits.

From a Redline source checkout, run exactly:

```sh
cd relay
npm ci
npx wrangler login
npx wrangler deploy
```

The last command prints a URL ending in `*.workers.dev`. Copy the complete
`https://…workers.dev` URL into the current desktop YAML `relay.url` setting
(Phase 3 later moves this choice into Pair a Device). Do not use `npm run
deploy`: that script targets Redline's closed production environment and is
currently blocked until Phase 5 rotates the retired verifier key.

`MAX_CLIENTS_DEFAULT = "5"` in `wrangler.toml` controls how many phones can be
attached to one host. Valid values are integers from 1 through 25 inclusive.
A missing or invalid value falls back to the explicit five-client self-host
default; it never makes the cap unlimited. Keep `ALLOW_UNENTITLED = "true"`
and `ENTITLEMENT_PUBLIC_KEY = ""` for an open self-hosted deployment.

## Optional custom domain

A `workers.dev` address is enough. To use your own hostname, create the DNS
name in the same Cloudflare account and insert this top-level key **immediately
after `compatibility_date` and before the first `[[durable_objects.bindings]]`**:

```toml
routes = [
  { pattern = "relay.example.com", custom_domain = true }
]
```

Do not place it after `[vars]`: TOML would make it part of that table rather
than a top-level Wrangler route. Run `npx wrangler deploy` again, then use
`https://relay.example.com` in the current YAML relay configuration. Do not
edit or deploy `[env.production]`; that environment describes Redline's hosted
service rather than a self-host installation.

## Privacy and payload blindness

Cloudflare and the Worker can observe transport metadata needed to operate the
service: connecting IP addresses, the session id in the URL path, connection
and request timing, frame sizes, and normal HTTP/WebSocket metadata. Depending
on account logging settings, Cloudflare may retain that metadata.

The phone and Mac establish Noise encryption end to end before application
requests cross the relay. The relay only adds or removes an opaque random
8-byte channel prefix. It does not hold the Noise keys, inspect plaintext, log
frame contents, or persist frames, so neither this Worker nor Cloudflare can
read Redline request paths, headers, responses, or log payloads carried inside
the encrypted frames. Self-hosting changes who operates the transport; it does
not weaken that blind-payload guarantee.
