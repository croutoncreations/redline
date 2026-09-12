# Redline mobile + relay: distribution and licensing plan

Current-state record for the `jf-mobile-app` branch and the remaining plan for shipping it. Phase 1 is implemented; later-phase descriptions below remain design, not released UI.

## 1. Where the branch stands after Phase 1

The relay boundary now provides:

- Ed25519 tokens with required signed `{exp, sid, max_clients}` claims, signature-before-parse verification, session binding, header-only transport, and trusted claim forwarding into the Durable Object.
- Host-only entitlement checks. Clients are admitted only behind the owning host generation, capped by stored `maxClients`, and multiplexed with random eight-byte channels. Stored expiry alarms terminate due sessions; refresh advances claims without replacing sockets.
- An open self-host default and a closed production environment in `wrangler.toml`, plus public self-hosting and entitlement contracts. Production deployment is intentionally blocked while the retired verifier key remains configured; Phase 5 supplies the new public key while its private half exists only in the issuer Worker secret.
- A desktop host-wire read boundary of 1 MiB payload plus the eight-byte channel. Desktop channel demultiplexing is deliberately not part of Phase 1.

Gaps that matter for shipping, in phase order:

1. **Desktop entitlement lifecycle (Phase 2).** Managed relay state, Keychain license storage, issuer calls/cache/renewal, relay refresh handling, per-channel Noise handlers, structured relay state, local APIs, and CLI commands are not implemented.
2. **Remove legacy phone token plumbing (Phase 2/4).** The relay no longer requires or trusts a client token, but the existing pairing/mobile APIs still carry the legacy field. Phase 2 removes it from pairing; Phase 4 deletes stored phone values and adds structured client errors.
3. **Desktop setup and status UI (Phase 3).** Pair a Device does not yet configure hosted/self-hosted mode or activate a license, and the menu bar has no renewal/lapse state.
4. **Android release (Phase 4).** Final application id, SDK 36 target, upload signing, Play-only AAB workflow, privacy verification, and listing work remain.
5. **Issuer and key rotation (Phase 5).** Checkout, seat activation, token issuance, recovery, portal, webhook ordering, and the new production signing key live in the separate private issuer repository. No production relay deployment is usable until that key exists and the guarded retired public key is replaced.
6. **Launch operations/docs (Phase 6).** Rate limits, paid capacity, monitoring, threat-model/privacy publication, README/CHANGELOG launch copy, and final coordinated cutover remain.

Not reviewed here: usage-meter/pace/reserve work, which is unrelated to mobile distribution.

## 2. Sharing the app: open source and Play Store

Short answer: yes to both, and they do not conflict. The repo is already public under Apache-2.0 with signed macOS releases, and the app lives in the same repo, so it is open source the moment the branch merges. Apache-2.0 apps on Play are routine. What you need is release engineering and store compliance, not a licensing decision.

**Decide before the first upload (irreversible):**

- **Application ID.** It is `ai.redline.app`. The ID can never change after publishing, and Play convention is a reverse domain you control. If you do not own `redline.ai`, use `com.croutoncreations.redline`. Also check that a "Redline" listing does not collide with an existing trademarked app name; a subtitle like "Redline for Crouton Creations" or "Redline Dispatcher" may be needed.
- **Developer account.** If you do not already have a Play Console account, personal accounts created after Nov 2023 must run a closed test with at least 12 testers for 14 continuous days before production access; organization accounts are exempt. If Crouton Creations has a D-U-N-S number, an org account skips the wait ([Play Console help](https://support.google.com/googleplay/android-developer/answer/11926878?hl=en)).

**Build changes (a short PR on top of the branch):**

- `targetSdk` must be 36 for new apps after Aug 31 2026; 34 will be rejected outright. Bump and re-test the camera permission flow.
- Add `signingConfigs.release` read from environment variables (`REDLINE_ANDROID_KEYSTORE_B64`, keystore password, alias, and key password), never a committed keystore. Enroll in Play App Signing and keep the upload key distinct from Google's app-signing key.
- Derive `versionCode` from CI (run number or tag) and `versionName` from the git tag, matching how the macOS release is versioned.
- Turn on R8 (`isMinifyEnabled = true`) only after adding keep rules for the gomobile-generated `redlinecore` classes; or ship the first release unminified and do it later. The AAR is ~19 MB, so App Bundle + per-ABI splits matter more than R8.
- A release workflow: on tag, build the Go core, assemble a signed AAB, and upload it to the Play internal track. Pin every workflow action to a full reviewed commit SHA and grant minimal permissions. Android binaries initially ship only through Google Play; the Apache-2.0 source remains public, but no GitHub APK or cross-channel update path is required.

**Store compliance:**

- Privacy policy URL is mandatory (camera permission alone triggers it). Host at `redline.croutoncreations.com/privacy`. The honest version is a strong pitch: the app collects nothing, the relay forwards ciphertext it cannot read, camera is used only to scan a pairing code and no frames are stored.
- Data Safety form: verify the answers against Google's current definitions before submission, including the Play Services ML Kit barcode dependency and hosted relay IP/timing metadata. Camera frames stay on-device and are used only for pairing, but do not prescribe “no data collected or shared” until the complete dependency behavior and ephemeral-processing rules have been checked.
- Listing assets: 4–8 phone screenshots, a 1024×500 feature graphic, 512 icon, short and long descriptions. The existing demo fixtures can generate clean screenshots.
- Payments policy: keep every purchase and every "buy" link out of the Android app. The relay subscription is a desktop-side license (section 3), and the phone only ever consumes it. That avoids Google Play Billing entirely; the app is a client for a service the user configured elsewhere, like an email client. If the app ever shows "subscription lapsed", say it and point at the Mac, not at a URL.

**Later, optional:** Reconsider F-Droid or signed GitHub APKs only if demand appears. Reproducible gomobile builds and signing-channel compatibility make them unnecessary for the initial Play-only launch.

## 3. Issuers, hosted vs self-hosted

### The shape of the product

Two ways to use the relay, one codebase:

- **Hosted relay** at `redline-relay.croutoncreations.com`: $10/year initially, annual only. Requires an entitlement token, minted by an issuer you run.
- **Self-hosted relay**: `wrangler deploy` your own, run with `ALLOW_UNENTITLED=true`, no issuer needed. Free forever, fully documented, and it is the credibility that makes the paid one acceptable to this audience.

### What the issuer is

A very small service with three jobs: take payment, mint short-lived tokens for valid licenses, and refuse when the subscription lapses. Everything about *who* paid lives here and nowhere else.

Recommended flow:

1. User buys on a landing page (`redline.croutoncreations.com/relay`). After authoritative payment success, the success page shows a license key (`rl_live_...`) and directs them to paste it into Pair a Device or run `redline relay activate <key>`; it never asks them to store the key in YAML.
2. Desktop config becomes:

   ```yaml
   relay:
     enabled: true
     # url: https://relay.example.com  # set only when self-hosting
   ```

   The Pair a Device flow writes non-secret managed state atomically to a 0600 `relay-state.json`; YAML is bootstrap configuration only. The hosted license key is stored in macOS Keychain and never in YAML or a state file. Managed state takes precedence once present. Hosted mode uses the default hosted relay and issuer; custom URL means self-hosted with no token; hosted mode with no Keychain key reports `needs_license`. Generate `session_id` with `relay.NewSessionID()` and persist it in managed state.
3. Desktop calls `POST /v1/entitlement {license_key, sid}` on the issuer, gets a signed token with required `{exp, sid, max_clients}` and a 14-day lifetime, caches only the token beside the identity file, and renews at half-life and on any 402. Successful renewal calls the relay's authenticated host entitlement-refresh endpoint to advance the stored expiry alarm without replacing the socket or disconnecting phones. Menu bar shows "Relayed · renews Sep 18", "Lapsed · manage subscription", "Self-hosted", or "Off". The CLI gets `redline relay status` and `redline relay activate <key>`.
4. The relay admits the phone's `client` leg because an entitled `host` is attached (section 1, item 2). The phone never holds a token.
5. Lapse: issuer refuses renewal → desktop keeps serving over the tailnet, relay goes dark within a week, menu bar says why. No revocation list anywhere, exactly as the relay's comments intend. Rotating `ENTITLEMENT_PUBLIC_KEY` remains the break-glass.

### Payments: Stripe Managed Payments

Stripe Managed Payments is the selected merchant-of-record path for the $10/year product. Create a new Stripe account dedicated to Redline, then confirm that the account is eligible and that adjustable Checkout quantity and subscription quantity management work before depending on them. Use one annual per-seat Price with Checkout quantity equal to seats. If Managed Payments is unavailable or lacks those capabilities, the decided fallback is the same Stripe integration with Stripe Tax—not a different license provider.

### Separate repo?

Yes, a separate `redline-issuer` repo, but for operational reasons rather than secrecy: the only actual secret is the private key, and that lives in a Worker secret either way. Keeping the issuer separate keeps payment-provider glue, pricing experiments, and abuse handling out of a repo you want people to read and contribute to, and keeps its deploy cadence independent. Start private; you can open it later if the "the issuer learns nothing about your traffic" claim would benefit from being inspectable.

What must stay in the public repo is the **contract**: a `docs/relay-entitlement.md` that specifies the token format, required signed claims (`exp`, `sid`, `max_clients`), trusted Worker-to-Durable-Object claim handoff, structured error codes, and issuer HTTP API, plus a shared test vector consumed by both relay and issuer tests. Closed mode rejects any token missing `sid`; there is no legacy `{exp}` compatibility path because there are no users yet.

### Making self-hosting clean

- Split `wrangler.toml`: commit a version with the route block commented out and empty `ENTITLEMENT_PUBLIC_KEY`, and put your production values in `wrangler.production.toml` or a `[env.production]` section that only your deploy uses. Self-host becomes: `npm ci`, set `ALLOW_UNENTITLED=true`, `wrangler deploy`, paste the URL into config.
- Add a `docs/self-hosted-relay.md` with exactly those steps, the Workers plan needed, and what Cloudflare logs (IPs, session ids) so people can make their own call.
- Keep the relay's public tests as the proof that a hostile operator still learns nothing; link to them from the doc.

## 4. What else is missing

- **Abuse and cost controls on the hosted relay.** There are no limits today. Add a Cloudflare rate-limiting rule on `/v1/session/*` for 402s per IP and for new-session churn, and move the worker to the Workers Paid plan ($5/mo) before any paying customer depends on it; the free plan's daily request cap is a single-point outage. Hibernation keeps idle sockets free, so cost per subscriber should be cents.
- **Environments and monitoring.** A staging relay, an uptime check on `/health`, and alerting on 5xx rates. Right now a broken deploy is discovered by a phone showing "offline".
- **Protocol versioning.** Add a version field to the pairing fragment and the Noise handshake prologue so an old phone against a new desktop (or vice versa) gets a "please update" instead of a confusing failure. Once the app is in the store you no longer control both ends. Use structured relay error bodies rather than matching English text.
- **Desktop UX for relay state.** Beyond the pairing window: a menu-bar line for relay status and lapse, and a "Manage subscription" item that opens the payment portal. Today a 402 is invisible on the Mac.
- **Recovery story.** Losing `relay-identity.json` unpairs every phone and creates a new seat identity. Add issuer and local APIs to list activations and deactivate any activation by opaque id, so a lost Mac cannot permanently consume a seat. Document that identity rotation requires re-pairing phones.
- **Threat model page.** A short public write-up of what the relay can and cannot see (ciphertext, session ids, IPs, timing), what the issuer knows (email, payment status, hashed session id), and what the phone stores. It doubles as the privacy policy's substance and pre-empts the Hacker News thread.
- **README and getting-started.** A "Phone app" section with the two routes, a Play badge, source/build instructions, and a pointer to self-hosting.
- **iOS.** `scripts/build-mobile-core.sh` already targets gomobile, so an XCFramework is a small step, but App Store review, TestFlight, and the same no-payments-in-app posture are a separate project. Ship Android first and let demand decide.
- **Support and refunds.** With a merchant of record, refunds and receipts are handled for you; decide on a support email and put it in the listing and the landing page.

## 5. Suggested order

1. Rebase and land the mobile branch baseline without deploying a new production entitlement contract.
2. Build the final relay contract: host-gated admission, mandatory `sid`, trusted claim handoff, multiplexing, self-host defaults, and public contract tests.
3. Build desktop managed state, Keychain license storage, issuer client/renewal, activation management, status UI, and CLI against a test issuer.
4. Create the dedicated Stripe account, confirm Managed Payments capabilities, and deploy the issuer, landing page, checkout, recovery, and activation APIs.
5. Build the Play-only Android release: final application ID, target SDK 36, upload signing, privacy/Data Safety verification, listing assets, and internal-track workflow.
6. Perform one coordinated production cutover: issue the new signing key, deploy issuer, desktop, relay verifier, and Android release in an order that never leaves the hosted relay expecting tokens no deployed desktop can obtain.
7. Enable paid Workers capacity, rate limits, staging/health monitoring, threat-model documentation, and launch materials before accepting paid customers.

## Appendix A. Decisions so far (running log)

- Licensing is per Mac seat; phones are free clients. One license key carries a `seats` count chosen at checkout (quantity); the issuer registers up to `seats` distinct `sid`s against the key and refuses the next with 409. Seats can be changed on the subscription (provider handles proration). Self-service deactivation of a seat via the provider portal or `redline relay deactivate`.
- Host-gated admission and client multiplexing ship together, in the same release as the Play launch.
- Payments: Stripe Managed Payments on a new Redline-dedicated Stripe account under the existing Crouton Creations login; fallback is the same integration with Stripe Tax if Managed Payments is unavailable or does not support adjustable seat quantity. Price is $10/year initially, annual only.
- Product pages, issuer API, privacy policy, and checkout live at redline.croutoncreations.com.
- Multiple phones per Mac must work. Cap enforced by the relay from a `max_clients` claim in the token (issuer sets it; self-hosted relays ignore it).
- Phone needs no entitlement token: relay admits `client` when an entitled `host` is attached. Remove `entitlement` from the QR.
- Application ID: `com.croutoncreations.redline` (rename `namespace` and Kotlin package to match). Android binaries ship only through Google Play initially; source remains public, and cross-channel updates are not required.
- Pair a Device becomes the entry point for relay setup when no relay is configured; goes straight to the QR when one is.
- Hosted license keys live in macOS Keychain. Mutable non-secret relay configuration lives in an atomically replaced 0600 managed state file; user YAML is never rewritten by the UI.
- Hosted entitlement tokens require signed `exp`, `sid`, and `max_clients` claims and live for 14 days. There is no legacy-token migration path.
- Issuer and local APIs list activations and deactivate any Mac by opaque activation id, including a lost device.
- Customer Portal URLs are created fresh when requested, never cached with an entitlement.

## Appendix B. `relay/wrangler.toml` Phase 1 result

1. The committed default is the open self-host profile with no route, an empty key, and `MAX_CLIENTS_DEFAULT = "5"`.
2. `[env.production]` repeats the route, closed vars, Durable Object binding, and migration because environment sections do not inherit bindings.
3. The configured production verifier is explicitly retired. `npm run deploy` runs a preflight that rejects that exact key and malformed/empty replacements before invoking `wrangler deploy --env production`. Phase 5 must install the new public key; private material stays in the issuer Worker secret.
4. `npm run deploy:self-hosted` remains plain `wrangler deploy`. The self-host guide documents setup, unambiguous top-level custom-domain insertion, plan limits, and observable metadata.
5. Tests assert the production profile is closed and prove the retired-key guard distinguishes a different validly shaped key. Direct Wrangler dry-runs remain available for structural validation.

## Appendix C. Relay protocol Phase 1 result

- Host-gated client admission; new status for "no host attached" (423) distinct from 402.
- Persist host claims with a random ownership generation; a due `exp` alarm closes only that generation, so stale callbacks and refreshed alarms cannot tear down replacements.
- Client multiplexing: relay assigns each `client` socket a random channel id; client→host frames are prefixed with it, host→client frames carry it and the relay strips it and routes. Phone wire format unchanged. Enforce `max_clients` per session by counting client sockets.
- Required `sid` claim: the closed relay compares `base64url(sha256(session_id))` to the signed claim and rejects a missing or mismatched value.
- Remove `entitlement` query-param fallback.

## Appendix D. Desktop changes (tracked)

- `internal/relay/desktop.go`: one `SessionHandler` per channel (map keyed by channel id, idle-evicted) instead of a single handler that restarts its handshake on every new phone.
- `internal/relay/dial.go`: strip/attach channel prefix; handle 402 (surface entitlement state) and 423.
- State: atomically managed `relay-state.json` for non-secrets; macOS Keychain for the license key; `url` defaults to hosted; `session_id` auto-generated with `relay.NewSessionID()`. YAML is bootstrap-only and the UI never rewrites it.
- Issuer client: `POST /v1/entitlement {license_key, sid}`; cache the 14-day token beside `relay-identity.json`; renew at half-life and on 402, then call the authenticated relay entitlement-refresh endpoint to advance the alarm without disconnecting clients.
- `internal/pairing`: stop emitting `entitlement` in the QR fragment.
- CLI: `redline relay status | activate <key> | devices | device deactivate <id> | setup --url <url> | off`.
- macOS: Pair a Device wizard (see Appendix A); menu-bar relay status line; "Manage subscription" opens the provider portal.

## Appendix E. Android changes (tracked)

- `applicationId`/`namespace`/package → `com.croutoncreations.redline`.
- `targetSdk` 36; `signingConfigs.release` from env; CI-derived `versionCode`; an App Bundle so Play performs per-ABI delivery.
- Stop reading/storing `entitlement` from the pairing fragment (keep tolerant parsing for old QRs); map 423 to "Mac is offline".
- Release workflow: signed AAB to the Play internal track on tag; no GitHub APK initially.
- Privacy policy URL, Data Safety answers, listing assets.

## Appendix F. Issuer (separate repo, tracked)

- Cloudflare Worker; Ed25519 private key in a Worker secret; KV or D1 keyed by a one-way hash of the license key with `{seats, status, activations[]}` as the value.
- `POST /v1/entitlement {license_key, sid, label?}` → registers the sid with an opaque activation id if under `seats` (idempotent for a known sid), returns a 14-day token with required `{exp, sid, max_clients}`; 402 when lapsed, 409 when seats are exhausted.
- `GET /v1/activations`, `DELETE /v1/activations/{id}`, and `POST /v1/portal` authenticate with the license key; the portal endpoint creates a fresh URL.
- Stripe Managed Payments: one $10/year per-seat Price, Checkout in subscription mode with adjustable quantity = seats; provision only after authoritative payment success; webhook updates tolerate duplicate and out-of-order delivery; Customer Portal sessions are minted on demand. Store only a non-secret license record id in Stripe metadata and replace lost keys rather than retaining recoverable plaintext.
- Landing page and checkout success page use a 24-hour encrypted one-time delivery record to show the newly generated key after authoritative payment success; the permanent license record retains only its hash. Recovery rotates rather than retrieves a key.
- Shared test vector with the public repo's `docs/relay-entitlement.md`.
