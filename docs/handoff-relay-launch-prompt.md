# Handoff: Redline relay licensing, multi-phone relay, and Android release

You are working in the Redline repository (`github.com/croutoncreations/redline`, Apache-2.0, public). Start from the `jf-mobile-app` branch (worktree at `.worktrees/jf-mobile-app`), which adds a native Android app (`mobile/android`, Compose), a Go core compiled through gomobile (`mobile/core`), a Cloudflare Worker relay (`relay/`), the desktop relay dialer (`internal/relay`), unified pairing (`internal/pairing`), and a macOS Pair a Device window. Read `docs/mobile.md`, `docs/mobile-distribution-plan.md`, `relay/src/index.js`, `relay/src/session.js`, `internal/relay/*.go`, `internal/pairing/pairing.go`, `internal/config/config.go`, and `mobile/core/relayclient.go` before changing anything.

Work in phases, in the order below. Each phase ends with green CI (`.github/workflows/ci.yml` runs Go, relay vitest, dashboard Playwright, Android, and macOS jobs) and a commit history in the repository's existing style: small commits, lowercase subjects, an optional `area:` prefix, and a subject that states the invariant rather than the file touched. Every behavioural change ships with a test in the same commit. Do not create the issuer in this repo; Phase 5 describes it for a separate private repository, and only its contract lives here.

## Phase 0: Rebase and baseline

Before any other change, rebase `jf-mobile-app` onto current `origin/main` (or merge `main` into it if the history is shared with others; prefer rebase since the branch is Jon's). Resolve conflicts preserving the mobile branch's behaviour, rebuild the gomobile AAR, and get CI fully green on the rebased branch. Commit nothing else in this phase. If the rebase surfaces behavioural conflicts you cannot resolve with confidence, stop and report them rather than guessing.

## Decisions already made (do not relitigate)

- The relay stays blind: it forwards Noise-encrypted frames it cannot read. Nothing below adds any inspection of payloads.
- Licensing is per Mac seat. One license key carries a `seats` count chosen at checkout; the issuer registers up to `seats` distinct Macs. Phones are free and uncounted at the license level, but a relay session admits at most `max_clients` phones at once (a token claim, default 5).
- The phone never holds an entitlement token. The relay admits a `client` only when an entitled `host` is attached to the same session. `entitlement` leaves the pairing QR.
- Multiple phones per Mac must work simultaneously. The relay multiplexes clients onto the single host socket with a channel prefix; the phone's wire format does not change.
- Self-hosting is a first-class path: `wrangler deploy` of the committed config yields an open relay (`ALLOW_UNENTITLED=true`); the hosted relay's closed configuration lives under `[env.production]`.
- Hosted relay and self-hosted relay are chosen in the Pair a Device flow on the Mac; the Android app contains no purchase UI and no purchase links.
- Android application ID becomes `com.croutoncreations.redline`.
- Everything ships together in one production cutover. There are no existing users, so do not build a dual-key, legacy-token, or query-token migration path.
- Pricing: $10/year initially, annual only. Use one annual per-seat Stripe Price and set Checkout quantity to the number of seats. Do not hardcode the amount in application code; read it from the Price.
- Stripe Managed Payments is the selected payment provider. A new Stripe account dedicated to Redline must be created and its Managed Payments eligibility, adjustable Checkout quantity, and subscription quantity management verified before implementation depends on them.
- Every key and secret is rotated as part of this work: a new production Ed25519 issuer keypair (public half into `[env.production]`, private half only ever into the issuer's Worker secret; the current hand-made deployment material is retired), a fresh Android upload keystore, a new Play service account, and new Stripe restricted API keys scoped to what the issuer needs. Nothing pre-existing is reused. Because this is one cutover with no users, deploy the issuer and desktop build that can obtain the new token before switching the production relay to the new public key; do not deploy an unusable verifier between phases.
- Android binaries are distributed through Google Play only. The source remains public under Apache-2.0, but no GitHub APK or other custom Android binary channel is required. Cross-channel app updates are therefore not a requirement.

## Phase 1: Relay (`relay/`)

### 1.1 Entitlement claims and `sid` binding

Claims become `{ "exp": <unix seconds>, "sid": "<base64url sha256 of session_id>", "max_clients": <int> }`. On the closed hosted relay all three claims are required and covered by the Ed25519 signature. The relay verifies the signature before parsing claims, computes `base64url(sha256(sessionId))` from the path, and refuses with 402 `entitlement is for a different session` when it differs. Validate `max_clients` as an integer from 1 through 25. The issuer currently signs 5. Open self-hosted mode has no token; it takes its client cap from `env.MAX_CLIENTS_DEFAULT`.

`checkEntitlement` returns the verified claims, not only a Boolean. Before forwarding a host request to the Durable Object, `index.js` removes any caller-supplied internal claim headers and injects trusted internal values for `exp` and `maxClients`. It removes the entitlement header and query parameter at the same boundary. `session.js` reads only those overwritten internal values and never parses the token. Tests prove that forged internal headers are ignored and that neither credential carrier reaches the Durable Object.

Add `POST /v1/session/{session_id}/entitlement` as an authenticated host control endpoint. It verifies the new entitlement and `sid` in `index.js`, forwards only trusted claims, and asks the Durable Object to replace `{exp, maxClients}` and reschedule its alarm only when a host socket is currently attached. It does not replace the host socket or disturb clients. Return 423 when no host is attached.

Remove the `?entitlement=` query-string fallback in `checkEntitlement` and its tests; the header is the only accepted carrier. Keep `requestWithoutEntitlement` stripping both, defensively.

### 1.2 Host-gated client admission

In `index.js`, run `checkEntitlement` only for `role=host`. For `role=client`, skip the token check entirely and forward to the Durable Object. In `session.js`:

- On host connect: persist `{ exp, maxClients }` to `ctx.storage` and set a Durable Object alarm at `exp`. In `alarm()`, close the host and every client with code `1008` and reason `entitlement expired`, then clear storage. A new host connect when no host is attached replaces stale stored values and reschedules the alarm; a duplicate attached host still gets 409. In open self-hosted mode, store `maxClients = MAX_CLIENTS_DEFAULT`, store no `exp`, and set no alarm. On host disconnect, close every client, clear entitlement state, and cancel the alarm.
- On client connect: if no host socket is attached, respond `423` with body `no host attached`. If the number of attached clients is already `maxClients`, respond `409` with body `too many clients`. Otherwise accept.
- Keep the existing `409 that role is already connected` for a duplicate host.

### 1.3 Client multiplexing

Each accepted client socket gets a random 8-byte channel id, retrying on the unlikely event that the tag already exists, and stores it as a tag (`client:<hex>`) so it survives hibernation. Frames flow as:

- client → relay → host: relay prepends the 8-byte channel id to the client's frame and sends the result to the host as a binary message.
- host → relay → client: host frames are `channel(8) || payload`; the relay strips the prefix and sends `payload` to the client that owns that channel. Unknown channel: drop silently.
- client close or error: relay sends the host a frame of exactly 8 bytes (channel id, empty payload) meaning "channel closed", so the host can discard that phone's Noise state. Host close: close every client with `1000 peer disconnected`, as today.

The relay never buffers, as today. Define the frame limit as a payload limit plus the 8-byte channel prefix, and update both desktop and relay WebSocket read limits so a previously valid maximum-size encrypted frame remains valid after multiplexing. Update `relay.spec.js` for: two clients talking to one host without crosstalk, channel-id collision retry, boundary-size frames, channel-closed notification, `max_clients` enforcement, 423 without a host, 1008 at alarm time, and a test-only `debugStorageDump` assertion that storage holds only `exp` and `maxClients`.

### 1.4 `wrangler.toml`

Restructure so the committed default is the self-host profile:

- Top level: no `[[routes]]` (leave it commented with a note on adding a custom domain), `ALLOW_UNENTITLED = "true"`, `ENTITLEMENT_PUBLIC_KEY = ""`, `MAX_CLIENTS_DEFAULT = "5"`, the `SESSIONS` binding, and the migration.
- `[env.production]`: the `redline-relay.croutoncreations.com` custom-domain route, `ALLOW_UNENTITLED = "false"`, `MAX_CLIENTS_DEFAULT = "5"`, and the DO binding and migrations repeated (env sections do not inherit bindings). Until Phase 5 creates the new issuer key, keep the old public key explicitly marked retired/temporary; do not generate its replacement in this repository.
- `package.json`: `deploy` targets `wrangler deploy --env production` only after a tested preflight rejects the exact retired public key. Production deployment remains **BLOCKED** until Phase 5 supplies the new public key. `deploy:self-hosted` remains plain `wrangler deploy`; direct production `--dry-run` is allowed for structural validation.
- Add vitest coverage that parses `wrangler.toml`, asserts production is closed, and proves the deploy preflight rejects the exact retired verifier key but accepts a different validly shaped public key. This guards both against shipping open and against accidentally deploying the retired verifier.
- Rewrite the header comment: this file is the self-hoster's starting point; production lives in the env section; keep the note that the public key is public by construction.

### 1.5 Public contract documents

- `docs/relay-entitlement.md`: token format, required claims, header name, the `sid` derivation, and machine-readable error bodies (`{"code":"not_entitled"}`, `different_session`, `too_many_clients`, `no_host`) for 402/409/423 plus close 1008 `entitlement expired`. Document the issuer HTTP API from Phase 5. Include a test vector: a fixed Ed25519 seed (test-only, say so), one claim set, and the resulting token string. Consume that vector from `relay/test/entitlement.spec.js` so the doc and the verifier cannot drift.
- `docs/self-hosted-relay.md`: `npm ci`, `npx wrangler login`, `npx wrangler deploy`, copy the `*.workers.dev` URL, optional custom domain, the Workers plan needed, what Cloudflare logs (IPs, session ids, timing) and what it cannot see.

## Phase 2: Desktop service and CLI (`internal/`)

### 2.1 Configuration (`internal/config`)

Do not rewrite the user's YAML from the Pair a Device UI. Add an atomically replaced `relay-state.json` beside `relay-identity.json`, mode 0600, containing only non-secret managed state: `{ mode, url, issuer_url, label, session_id }`. Store the hosted `license_key` as a generic-password item in macOS Keychain, owned by the signed Redline app/service; the local service is the only component that reads it. CLI activation calls the authenticated local service rather than reading Keychain directly. The entitlement cache remains a separate 0600 file and never contains the license key.

YAML remains an optional bootstrap/operator override:

```yaml
relay:
  enabled: true
  # url: https://relay.example.com  # self-hosted
  # issuer_url: https://...          # custom issuer
```

Resolution rules, each with a test: managed state takes precedence after the Pair a Device wizard has written it; otherwise YAML bootstraps it. Hosted mode uses `https://redline-relay.croutoncreations.com` and the default issuer; self-hosted mode requires a custom URL and sends no token; hosted mode with no Keychain license does not dial and reports `needs_license`; off never dials. Generate `session_id` with `relay.NewSessionID()` on first use and persist it only in managed state. Remove the user-facing `session_id`, `license_key`, and static `entitlement_token` YAML paths rather than carrying legacy formats—there are no users to migrate. Document the exact Keychain service/account names and test that logs, API responses, diagnostics, and state files never contain the license key.

### 2.2 Issuer client and renewal (`internal/relay/entitlement.go`, new)

- `sid = base64url(sha256(session_id))`.
- `POST {issuer_url}/v1/entitlement` (default issuer_url `https://redline.croutoncreations.com/api`) with JSON `{ "license_key", "sid", "label" }` where `label` is the user's chosen device label (default: empty; never send hostname). Responses: `200 { token, exp, max_clients, seats, seats_used }`; `401` unknown key; `402` subscription lapsed; `409 { activations: [{ label, first_seen }] }` seats exhausted. Redact the key and the token from every log line by extending `redactToken`.
- Cache to `relay-entitlement.json` beside the identity file, mode 0600: `{ token, exp, obtained_at, sid, max_clients }`. Tokens live for 14 days.
- Renewal schedule: on service start, at half of the token's remaining lifetime, immediately on a relay `402`, and immediately on a relay close with code `1008`. Add jitter of up to 10% to timer-driven renewals. On failure, back off exponentially from 1 minute to a cap of 6 hours; a failed renewal never discards an unexpired cached token. After successful renewal, call the relay's authenticated `POST /v1/session/{session_id}/entitlement` control endpoint so the Durable Object advances its claims and alarm without replacing the host socket or disconnecting phones. If refresh returns 423, reconnect normally. Test that renewal advances the alarm while an in-flight client request completes uninterrupted.
- State machine, exposed by the service: `off`, `self_hosted`, `needs_license`, `active { renews_at }`, `renew_pending { expires_at }` (issuer unreachable, cached token still valid), `unavailable { since }` (token expired and issuer unreachable), `lapsed` (issuer said 402), `no_seat { activations }` (issuer said 409), `invalid_key`. In `lapsed`, `no_seat`, and `invalid_key`, the dialer does not connect; retry the issuer every 6 hours and immediately when the key changes.
- `GET {issuer_url}/v1/activations` with the license key in the Authorization header returns `{ activations: [{ id, label, first_seen, current }] }`.
- `DELETE {issuer_url}/v1/activations/{id}` with the license key in the Authorization header removes any selected activation, including a lost Mac. The current Mac's `redline relay deactivate` uses its activation id.
- `POST {issuer_url}/v1/portal` with the license key in the Authorization header creates and returns a fresh short-lived Customer Portal URL; never cache a portal URL in an entitlement response.

### 2.3 Dialer and session handling (`internal/relay/dial.go`, `desktop.go`)

- Host frames are `channel(8) || payload`. Replace the single `SessionHandler` with a map keyed by channel id, creating a handler on the first frame for a new channel and deleting it on the 8-byte channel-closed frame or after the existing idle timeout. Each handler keeps its own Noise state; a handshake on one channel never touches another.
- Treat `402` from the relay as an entitlement signal (trigger renewal, set state), `423` cannot occur for the host, `409 that role is already connected` as today, and close code `1008` as "renew then reconnect". Never log the token.
- Integration tests: two simulated phones through the relay test double against one desktop, concurrent requests, one phone disconnecting while the other continues; token expiry mid-session leading to renewal and reconnect.

### 2.4 Pairing (`internal/pairing`)

Stop emitting `entitlement` in the QR fragment. Keep `relay`, `key`, `session`. Update `url_test.go` and `compose_test.go`. Refuse to compose a relay route when the relay state is anything other than `active`, `renew_pending`, or `self_hosted`; the composer returns the reason so the UI can show it.

### 2.5 Service API and CLI

Add to the local API under the existing `/v1` namespace and with the same authentication as existing endpoints: `GET /v1/relay/status` (state plus `mode`, `url`, `seats`, `seats_used`, `max_clients`, and connection state), `POST /v1/relay/configure` (`{ mode: "hosted"|"self_hosted"|"off", url?, license_key?, label? }`, writes non-secret managed state atomically, writes the key to Keychain, performs the first issuer call synchronously, and never echoes the key), `GET /v1/relay/devices`, `DELETE /v1/relay/devices/{id}`, `POST /v1/relay/portal`, and `POST /v1/relay/deactivate`. The pairing composer may proceed only when entitlement state is usable and the host relay socket is connected; otherwise it returns a specific connecting or unavailable reason.

CLI: `redline relay status`, `redline relay activate <key> [--label ...]`, `redline relay devices`, `redline relay device deactivate <id>`, `redline relay setup --url <url>`, `redline relay off`, and `redline relay deactivate`. `redline pair --qr` prints a one-line relay status before the code.

## Phase 3: macOS app (`macos/`)

- Pair a Device becomes a two-step flow. Step one appears only when no managed relay choice exists: "How should your phone reach this Mac?" with three choices: over your tailnet only (enabled when a trusted host exists, otherwise explains what is missing), Redline's relay (a "Buy a license" button opening `https://redline.croutoncreations.com/relay`, a license key field, an optional device label, and a Continue that calls `/v1/relay/configure` and shows the returned state inline, including the activations list on `no_seat`), and my own relay (URL field, a "how to run one" link to `docs/self-hosted-relay.md`). Any choice, including tailnet only, writes managed state so the step does not reappear. Step two is the existing QR view with a "Change…" link that reopens step one.
- Menu bar: a relay status line rendered from `/v1/relay/status` (`Relayed · renews Sep 18`, `Relay renew pending · expires Sep 20`, `Relay unavailable · could not renew`, `Relay subscription lapsed`, `Relay: no seat available`, `Relay: self-hosted`, `Relay off`) and a "Manage subscription…" item that calls `/v1/relay/portal` to create and open a fresh Customer Portal URL.
- Tests in `RedlineKitTests` for state → string mapping and for the pairing composer's refusal reasons surfacing in the window.

## Phase 4: Android (`mobile/`)

- Rename `applicationId`, `namespace`, and the Kotlin package from `ai.redline.app` to `com.croutoncreations.redline`; update tests, the manifest, and `scripts/build-mobile-core.sh` if it references the package.
- `mobile/core/pairing.go`: stop requiring or storing `entitlement`; parse it tolerantly from old QRs and discard it. `mobile/core/relayclient.go`: map machine-readable HTTP `423 no_host` to a new `ErrHostOffline` ("your Mac is not connected to the relay") and `409 too_many_clients` to `ErrTooManyPhones`. Map a mid-session WebSocket close `1008 entitlement expired` to a distinct error that tells the user to check the relay subscription on the Mac. Remove the entitlement argument from `DialRelay` and from `CoreClientHolder.kt`; drop `KEY_ENTITLEMENT` from `RedlineSettings.kt` with a one-time migration that deletes the stored value. The app never shows a purchase link.
- Build: `targetSdk = 36`, `compileSdk` to match; `signingConfigs.release` fed from `REDLINE_ANDROID_KEYSTORE_B64`, `REDLINE_ANDROID_KEYSTORE_PASSWORD`, `REDLINE_ANDROID_KEY_ALIAS`, `REDLINE_ANDROID_KEY_PASSWORD` (skip signing config when unset so local debug builds still work); `versionCode` from `REDLINE_ANDROID_VERSION_CODE` (default 1) and `versionName` from `REDLINE_VERSION` (default `0.1.0-dev`); produce an App Bundle so Play delivers only the required ABI from the ~19 MB gomobile AAR. Leave R8 off for this release and add a TODO with the keep-rule requirement for `redlinecore`.
- `.github/workflows/android-release.yml`: on tag `mobile-v*`, build the Go core, assemble and sign an AAB, and upload it to the Play internal track. Pin every action, including the Play upload action, to a reviewed full commit SHA; grant minimal workflow permissions. Do not publish a GitHub APK: Android binaries initially ship only through Play, while the source remains public. Enroll in Play App Signing and treat the upload key and Google's app-signing key as distinct; cross-channel updates are not required.
- `docs/android-release.md`: one-time Play Console setup (App Signing enrollment, service account, privacy policy URL, and per-release steps). Treat Data Safety answers as a verification task, not a predetermined claim: assess the Play Services ML Kit barcode dependency and hosted relay IP/timing metadata under Google's current collection and ephemeral-processing definitions. Camera frames remain on-device and are used only to scan a pairing code.

## Phase 5: Issuer (separate private repo `redline-issuer`; this repo only carries the contract)

Hosted at `https://redline.croutoncreations.com` (issuer API under `/api/v1/...`, so the default `issuer_url` in Phase 2.1 is `https://redline.croutoncreations.com/api`; landing page, privacy policy, and success page on the same host). Cloudflare Worker with the Ed25519 private key in a Worker secret and a KV or D1 store keyed by a one-way hash of the license key: `{ seats, status, activations: [{ id, sid, label, first_seen }] }`. Never store license keys in plaintext logs or analytics.

- `POST /v1/entitlement { license_key, sid, label? }` → if the key is unknown `401`; if the subscription is not active `402`; if `sid` is already registered, refresh it; else if `len(activations) < seats` register it with an opaque activation `id`; else `409 { activations }`. On success sign `{ exp: now + 14d, sid, max_clients: 5 }` and return `{ token, exp, max_clients, seats, seats_used }`.
- `GET /v1/activations` and `DELETE /v1/activations/{id}` authenticate with the license key in the Authorization header, allowing a user to inspect and deactivate any Mac, including one that was lost.
- `POST /v1/portal` authenticates with the license key and creates a fresh Customer Portal session on demand.
- Payments: **Stripe with Managed Payments enabled** (Stripe is merchant of record; Stripe handles tax, disputes, and transaction support). Use a **new Stripe account dedicated to Redline** under the existing Crouton Creations login, with statement descriptor `REDLINE` (customers see `LINK.COM* REDLINE`; Managed Payments receipts and transaction emails come from Link, not from the account's email settings). Managed Payments subscriptions can only be created through Checkout or Payment Links, custom checkout domains are unsupported, and Stripe may delete a customer's `Customer`/`Subscription` objects on a data-deletion request, so the license record is the source of truth and must survive those objects disappearing (handle `customer.subscription.deleted` and `customer.deleted` as lapsed). Keep the account's support email accurate: Stripe escalates transaction support to it and may refund without approval after 48 hours of silence. One Product ("Redline relay seat") with a yearly recurring Price; Checkout Sessions in `subscription` mode with `adjustable_quantity` enabled so seats are the line-item quantity. Confirm during setup that the account is eligible for Managed Payments and that adjustable quantity and the Customer Portal quantity update both work under it; if either does not, the fallback is the same integration with Stripe Tax enabled instead of Managed Payments, with no change to the issuer's data model.
- Webhooks verify signatures and persist `event.id` idempotency records, but do not trust delivery order. For every subscription-affecting event, fetch or otherwise compare the authoritative current Stripe subscription before updating license state so an older event cannot overwrite a newer state. `checkout.session.completed` records the checkout but activates only when `payment_status` is paid; handle `checkout.session.async_payment_succeeded` for delayed methods. Mint a license key `rl_live_<random>` only after authoritative payment success, store `{ stripe_customer_id, stripe_subscription_id, seats: quantity, status: active, activations: [] }`, and attach only a non-secret license record id—not the full key—to Stripe metadata. `customer.subscription.updated` syncs seats and status (`active`/`trialing` → active, `past_due` → active with a grace flag, anything else → lapsed); `customer.subscription.deleted` → lapsed; `invoice.payment_failed` → no immediate state change because Stripe dunning drives status. When seats shrink below active registrations, keep existing activations but refuse new ones until the count fits.
- Create Customer Portal sessions only through `POST /v1/portal` at click time; never cache their short-lived URLs. Enable quantity changes and cancellation in the portal configuration.
- Key recovery: a rate-limited `POST /v1/license/recover { email }` that always responds 200. Revoke the lost key and issue a replacement rather than retaining recoverable plaintext. Rate-limit by both IP and normalized email with a cooldown so the endpoint cannot be used for enumeration or email flooding.
- Landing page at `redline.croutoncreations.com/relay` with a Buy button that creates the Checkout Session. After authoritative payment success, generate the key once, store only its permanent one-way hash on the license record, and create a separate encrypted one-time delivery record keyed by Checkout session id with a 24-hour TTL. The success page (`/relay/welcome?session_id=...`) consumes that record to show the key once, plus `redline relay activate <key>` and Pair a Device instructions. Send the welcome email from the same authoritative paid activation path, then delete the delivery record after successful display/email or expiry. A webhook/success-page race must resolve to one idempotent license and one key. Recovery revokes the old hash, creates a replacement key and delivery, and sends it through the same authenticated email path.
- Managed Payments customer-deletion handling revokes the license and deletes or anonymizes email and device labels while retaining only the minimal non-identifying records required for accounting/security. It must not silently preserve personal data after Stripe deletes its copies.
- Tests consume the same test vector as `docs/relay-entitlement.md`.

## Phase 6: Docs and README

Update `docs/mobile.md` (the relay section now describes the Pair a Device choice, hosted vs self-hosted, and no longer mentions `entitlement_token` or a manual `session_id`), `config.example.yaml`, `README.md` (a "Phone app" section with both routes, a Play link, source/build instructions, and a self-hosting pointer), and `CHANGELOG.md`. Add a short `docs/relay-threat-model.md`: what the relay can see (session ids, IPs, timing, frame sizes), what the issuer knows (email via the payment provider, license status, sid, label), what the phone stores, and what none of them can see.

## Acceptance checklist

- A fresh self-hoster can run the three commands in `docs/self-hosted-relay.md`, paste the URL into Pair a Device, and pair two phones that work at the same time, with no token anywhere.
- A hosted user can paste a license key in Pair a Device, see `active`, pair, and keep working across a token expiry without touching anything; after the issuer returns 402 the menu bar says lapsed within one renewal cycle and the tailnet route keeps working.
- A second Mac with the same single-seat key gets a clear `no seat available` message listing the first Mac's label; the devices API/UI can deactivate that first activation by id even if the original Mac is lost.
- `wrangler deploy --env production` is closed; the vitest guard fails if anyone flips it.
- The Android debug build runs on-device against both routes, and the release workflow produces a signed AAB from a tag and uploads it to the Play internal track. No GitHub APK is required.
- No log line in any component contains a license key or an entitlement token.
