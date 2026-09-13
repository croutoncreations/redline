# Android release

Redline's Android app is distributed through Google Play only. The source remains public
under Apache-2.0; there is no GitHub APK and no other binary channel to keep in sync.

## One-time Play Console setup

Do these once, before the first upload.

1. **Create the app in Play Console** with application id `com.croutoncreations.redline`.
   This id can never change after publishing.
2. **Enroll in Play App Signing.** Upload the initial release through the Play Console UI
   (not this repository's workflow) so Google generates its own app-signing key. From then
   on, every automated upload uses a separate *upload key* (below); Google re-signs the
   bundle with its own key before distributing it. The two keys are never the same, and
   losing the upload key is recoverable (request a reset from Google); losing control of the
   app-signing key before enrolling is not.
3. **Generate the upload keystore** locally, once:

   ```sh
   keytool -genkeypair -v -keystore redline-upload.jks -alias redline-upload \
     -keyalg RSA -keysize 2048 -validity 10000
   ```

   Store the resulting `.jks` file and its passwords in a password manager, not in the
   repository. Base64-encode it for the CI secret:

   ```sh
   base64 -i redline-upload.jks | tr -d '\n' > redline-upload.b64
   ```

4. **Create a Google Play Android Developer API service account** (Play Console → Setup →
   API access → Create new service account), grant it Release Manager access to this app,
   and download its JSON key.
5. **Add repository secrets** (Settings → Secrets and variables → Actions → New repository
   secret, or an `android-release` environment's secrets if you want a manual-approval gate
   in front of every upload):
   - `REDLINE_ANDROID_KEYSTORE_B64` — the base64 file from step 3.
   - `REDLINE_ANDROID_KEYSTORE_PASSWORD`, `REDLINE_ANDROID_KEY_ALIAS`,
     `REDLINE_ANDROID_KEY_PASSWORD` — from the same keystore.
   - `PLAY_SERVICE_ACCOUNT_JSON` — the full JSON key from step 4, pasted as plain text.
6. **Publish a privacy policy** at a stable URL (e.g. `redline.croutoncreations.com/privacy`)
   and enter it in Play Console → App content → Privacy policy. Camera permission alone
   makes this mandatory.
7. **Complete the Data Safety form.** Do not assume "no data collected" — verify each answer
   against Google's current definitions:
   - The bundled `play-services-mlkit-barcode-scanning` dependency decodes the pairing QR
     entirely on-device; no frame or decoded value leaves the phone through it. Confirm this
     against the dependency's current privacy documentation before answering, since Google's
     collection/sharing definitions and the library's behavior can both change between
     releases.
   - When the hosted relay route is used, the phone's IP address and connection timing are
     visible to Cloudflare as the relay operator, the same way they would be to any network
     intermediary. This is "app functionality" data under Play's definitions, not personal
     data the app itself collects — but confirm the current form's category boundaries
     rather than assuming last year's answer still applies.
   - The app collects no account, contact, financial, or health data of any kind. It sends
     no credential to the relay: the desktop is the only side of the connection with a
     credential to present at all (`docs/relay-entitlement.md`).
8. **Prepare listing assets**: 4–8 phone screenshots, a 1024×500 feature graphic, a 512×512
   icon, and short/long descriptions. `tests/dashboard/mobile-screenshots.spec.js` and the
   on-device Compose screenshot tests under `mobile/android/app/src/androidTest` can produce
   clean source images.
9. **Run a closed test first if required.** A personal Play Console account created after
   November 2023 must run a closed test with at least 12 testers for 14 continuous days
   before it can publish to production; an organization account with a D-U-N-S number skips
   this. The internal track this repository's workflow uploads to is unaffected either way —
   internal testing has no such wait — but plan the schedule for the first production
   promotion accordingly.

## Per-release steps

Every release after the one-time setup is a single tag push:

```sh
git tag mobile-v0.2.0
git push origin mobile-v0.2.0
```

This triggers `.github/workflows/android-release.yml`, which:

1. Builds the Go core for Android with the pinned `gomobile` toolchain (the same build every
   `ci.yml` PR already exercises, run fresh here rather than reused, since this is a separate
   workflow).
2. Derives `versionName` from the tag (`0.2.0`) and `versionCode` from the GitHub Actions run
   id, which is unique and monotonically assigned across the whole repository — the property
   the Play internal track actually requires, and one a per-tag counter would not have across
   a rerun of the same tag.
3. Runs the same guards and unit tests `ci.yml`'s `android` job runs
   (`checkNoTestOnlyDeclarations`, `checkCoreFreshness`, `testDebugUnitTest`) before
   assembling anything, so a broken build never reaches signing.
4. Assembles and signs a release App Bundle (`bundleRelease`) using the upload keystore from
   the repository secrets.
5. Uploads the signed `.aab` to the Play **internal** track with `r0adkll/upload-google-play`,
   pinned to a reviewed commit SHA.

Promoting from internal to a wider track (closed testing, open testing, or production) is a
manual step in Play Console — this repository does not automate it, so a release always gets
a human look before it reaches real users beyond the internal testing list.

## What is deliberately not automated

- **R8/minification** is off (`isMinifyEnabled = false`). The gomobile-generated
  `redlinecore` classes need keep rules before R8 is safe to enable, and the ~19 MB AAR is
  already handled by the App Bundle's per-ABI delivery without it. Enabling R8 is future
  work, not a gap in this release process.
- **GitHub Releases / APK artifacts.** Android binaries are Play-only by design
  (`docs/handoff-relay-launch-prompt.md`); the workflow above produces no artifact outside
  the Play Console upload.
- **Production promotion.** Moving a build from internal to production is a Play Console
  action, not a CI step, so every wider release is a deliberate choice rather than a side
  effect of tagging.
