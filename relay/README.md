# Redline relay

The default `wrangler.toml` profile is for self-hosting. It allows unentitled
sessions; the hosted production profile requires signed entitlements. Push is
independently disabled until all three push secrets are set. See
[push self-hosting](../docs/push-self-hosting.md) for Firebase and desktop setup.

```sh
npm ci
npm test
npm run deploy:self-hosted
```

## Production Worker identity

The existing production Worker is named **`redline-relay`**, not
`redline-relay-production`. For an explicitly authorized code-only update to that
existing Worker, use:

```sh
npx wrangler deploy --env production --name redline-relay
```

Both flags matter: `--env production` preserves the production vars, Durable
Object bindings, and custom-domain routes. Using only `--name redline-relay`
would select the open top-level self-hosting profile instead. The production
command does not rotate entitlement keys or enable unentitled access.

Offline-safe bundle verification (no deployment):

```sh
npx wrangler deploy --dry-run --env production --name redline-relay --outdir /tmp/relay-dry
```

`npm run deploy` remains unchanged: it runs the production entitlement preflight
and refuses the retired verifier key pending the coordinated issuer cutover.
The explicit Wrangler command above does not run that preflight; it is not a
substitute for the coordinated key-rotation/cutover procedure. Do not deploy
production without operator authorization. Set production push secrets with
`--env production --name redline-relay` too; secrets are environment-specific.
