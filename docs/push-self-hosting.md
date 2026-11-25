# Self-hosting FCM push

Push is optional. A push is a content-free wake hint; the paired phone fetches
notification text from the desktop over its authenticated connection. The relay
accepts FCM HTTP v1 requests at `POST /v1/push` independently of session routing.

**One relay = one Firebase project = one app build.** An app built with another
project's Firebase configuration cannot receive this relay's messages. Use your
own Android package/application ID, Firebase project, and relay, not credentials
from someone else's app build.

## Firebase and Android build

1. Create a project in the Firebase console and enable the **Firebase Cloud
   Messaging API (HTTP v1)** in Google Cloud for that project.
2. Add an Android app using your own package name, matching the app build's
   `applicationId`. Download its `google-services.json`.
3. Keep that file **outside the repository**. Set `REDLINE_GOOGLE_SERVICES_JSON`
   to its absolute path when building the Android app, or use the gitignored
   `mobile/android/app/google-services.json`. Never commit either it or a
   service-account key. `bash scripts/check-no-firebase-secrets.sh` checks tracked
   files in CI, but is not a replacement for keeping credentials outside Git.
4. In Google Cloud IAM, create a dedicated service account and grant it the
   **Firebase Cloud Messaging API Admin** role
   (`roles/firebasecloudmessaging.admin`) for this project. Download a JSON key
   for that account to a secure location **outside the repository**. Restrict
   local permissions (for example, `chmod 600 /secure/path/fcm-service-account.json`).

The relay supports the FCM background APNs envelope for `platform: "ios"` too;
an iOS build additionally needs its own Firebase/APNs configuration. These
Android instructions do not provision APNs.

## Relay secrets and deployment

From `relay/`, install dependencies with `npm ci`. The self-hosted profile uses
the top-level `wrangler.toml` config. Choose your own Worker name/account before
deploying. Store the service-account key and Firebase project ID as secrets:

```sh
npx wrangler secret put FCM_SERVICE_ACCOUNT_JSON < /secure/path/fcm-service-account.json
npx wrangler secret put FCM_PROJECT_ID
```

At the second prompt, enter the Firebase **project ID**, not its display name,
project number, or Android package name.

Create a random desktop bearer and its SHA-256 digest without printing either:

```sh
umask 077
mkdir -p "$HOME/.config/redline"
openssl rand -hex 32 > "$HOME/.config/redline/push-auth-token"
tr -d '\r\n' < "$HOME/.config/redline/push-auth-token" | shasum -a 256 | awk '{print $1}' > "$HOME/.config/redline/push-auth-digest"
npx wrangler secret put PUSH_AUTH_TOKENS < "$HOME/.config/redline/push-auth-digest"
npm run deploy:self-hosted
```

`PUSH_AUTH_TOKENS` is a comma-separated list of **64-character SHA-256 hex
digests**, never plaintext bearers. The digest must cover the bearer without
its file's trailing newline. To rotate, temporarily set both old and new
digests separated by a comma, change the desktop token file and restart the
desktop, then remove the old digest. Keep the token file mode **0600**.

This shared-bearer model is for self-hosters and v1. A hosted relay would replace
it with entitlement-scoped authorization; session entitlements do not authorize
this endpoint. Anyone holding the push bearer can send wakes through this
project: distribute it only to trusted desktops.

All three secrets are required. Missing Firebase credentials/project or an
empty/invalid digest list returns `501 {"code":"push_not_configured"}`. Missing
or wrong bearer returns `401 {"code":"unauthorized"}`. No push-specific vars or
credentials belong in the checked-in Wrangler configuration.

For the existing hosted production Worker, consult the
[relay deployment notes](../relay/README.md#production-worker-identity). It is
named `redline-relay` and code updates must use
`npx wrangler deploy --env production --name redline-relay`, retaining production
vars/routes. Its secrets require the same `--env production --name redline-relay`
flags on each `wrangler secret put`; do not accidentally deploy the open
top-level profile to production. The normal `npm run deploy` entitlement-key
preflight remains in place.

## Desktop and test wake

Configure the desktop's YAML with the relay's HTTPS base URL and an absolute
path to the **plaintext token file**, not the digest:

```yaml
notifications:
  enabled: true
push:
  relay_url: https://your-relay.example
  auth_token_file: /home/you/.config/redline/push-auth-token
```

Restart the desktop after changing the token/configuration. Build/install your
Firebase-configured Android app, pair it with the desktop, allow notification
permission, and enable notifications so it registers its FCM token. Then run:

```sh
redline notification test --push
```

A successful send returns `202 {"ok":true}` and means FCM accepted the wake, not
that the phone displayed a notification. Dead tokens return `410 unregistered`
and the desktop deletes that registration. Google authentication failures return
`502 push_upstream_auth`; temporary network, quota, or Google server failures
return `503 push_upstream_unavailable`. Other upstream errors return
`502 push_upstream_error`. Fix credentials/project or check provider availability
without logging request bodies. The desktop backs off for one hour on an
unconfigured relay and does not retry transient send failures.

## Privacy and credential handling

The relay and Google see the device registration token, a delivery ID, a coarse
kind (`reset`, `firesale`, `job`, `test`), platform, and routing/timing metadata.
FCM receives only string data fields `type=wake`, `delivery_id`, and `kind`, with
high Android priority and a one-hour TTL; there is no `notification` block or
notification content. Push does not hide device metadata from Google.

Notification titles, messages, prompts, and other content remain on the desktop
and are fetched by the paired phone through the existing authenticated API
(Noise-encrypted end to end when using the session relay). The push bearer is
separate from the desktop API token and phone pairing credential. The relay
stores only its bearer digest list, plus the service-account secret needed to
mint OAuth tokens; OAuth access tokens are cached in Worker memory until five
minutes before expiry. It does not persist push payloads or log tokens, keys,
upstream bodies, or message content. Do not enable request-body logging outside
the Worker. Revoke/rotate the service-account key and bearer if exposed.
