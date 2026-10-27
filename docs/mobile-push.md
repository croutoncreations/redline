# Android push notifications

Push is optional. Builds without Firebase client configuration run normally and
hide notification settings. For relay/FCM setup, see
[Push self-hosting](push-self-hosting.md). Never use a Firebase **service-account
key** as Android client configuration.

## Behaviour

After pairing, Redline offers a one-line permission rationale on Android 13+
before requesting notification permission. In the Usage overflow menu, open
**Settings → Notifications** to enable or disable alerts. If Android has already
denied permission permanently, allow it in Android's app notification settings.
Disabling unregisters the phone and stops local alerts; a failed removal retries
on app start. Unpairing attempts removal before forgetting the credential. If
the desktop is unreachable, revoke that device on the desktop as well.

Registration refreshes after pairing, on token changes, and on app start if the
token changed or the last registration is at least seven days old. The first
registration records the newest delivery ID without showing history. The cursor
is held in plain private preferences; credentials stay in encrypted storage.

FCM carries only a high-priority data wake, not notification content. Redline
fetches deliveries from the desktop, direct first and then through the existing
encrypted relay fallback. Each new delivery produces an alert; duplicate wakes
do not replay seen IDs. Full pages are drained while the nine-second service
budget allows. A failed or timed-out fetch leaves unseen IDs for a later wake.
Time-boxed core calls allow three seconds for direct access and eight seconds
overall, leaving time for relay fallback. A late native result cannot post alerts
or advance the cursor.

Channels are **Resets** (high importance, `reset.*`), **Firesale** (high,
`firesale.*`), and **Jobs** (default, everything else). Test deliveries keep their
event's channel and have a `Test` title prefix.

Every alert has **Open**. An active `reset.announced` of kind `PROMISED` or
`SCHEDULED` has **Start firesale**; retracted/completed announcements do not.
`firesale.started` has **Stop firesale**. Actions run off the main thread and
update the alert with success or failure. If an action times out, open Redline
to check its result: an already-running native request may still complete.

## Build and test (no device needed)

With a Go toolchain, gomobile, Android SDK/NDK, and JDK 17 installed:

```sh
go test ./mobile/core/...
./scripts/build-mobile-core.sh android
mkdir -p mobile/android/app/libs
cp mobile/build/redlinecore.aar mobile/android/app/libs/
cd mobile/android
# Set ANDROID_HOME to your SDK path if it is not already configured.
env -u REDLINE_GOOGLE_SERVICES_JSON ./gradlew assembleDebug testDebugUnitTest
```

The last command builds with `BuildConfig.PUSH_ENABLED=false` provided there is
no `app/google-services.json`. To build with push:

```sh
REDLINE_GOOGLE_SERVICES_JSON="/absolute/path/to/google-services.json" \
  ./gradlew assembleDebug testDebugUnitTest
```

The Google Services plugin reads that external client file in place. Alternatively,
an existing `app/google-services.json` works and is gitignored. No client config
needs to be copied into the checkout. Keep client config, generated resources,
APKs and service-account secrets out of commits. Output (both modes, overwritten
by the last build): `mobile/android/app/build/outputs/apk/debug/app-debug.apk`.

Device checks are manual: pair and grant/deny permission, send a test wake,
verify channels/actions, duplicate wakes, direct/relay access, toggling and
unpairing. JVM tests cover notification policy, cursor first-run/advance rules,
registration cadence, and cancellation of late native results.
