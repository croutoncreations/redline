package ai.redline.app

import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Test
import org.junit.runner.RunWith

/**
 * Pairs against a real Redline service, end to end.
 *
 * This covers the half the camera cannot be automated for: taking the string a
 * scanner produced, redeeming it, and leaving the app holding a credential that
 * actually works. The emulator reaches the desktop through
 * `adb reverse tcp:7436 tcp:7436`; without it the test skips rather than fails,
 * since a missing desktop is an absent fixture and not a defect.
 */
@RunWith(AndroidJUnit4::class)
class PairingEndToEndTest {

    private val baseUrl = "http://127.0.0.1:7436"

    private fun serviceReachable(): Boolean =
        runCatching { core.Core.newClient(baseUrl, "").fetchUsage() }
            .fold(
                onSuccess = { true },
                // Unauthorized proves the service answered, which is all this
                // needs to know.
                onFailure = { error -> core.Core.isUnauthorized(error as? Exception ?: return false) },
            )

    @Test
    fun scannedCodeBecomesAWorkingCredential() {
        assumeTrue("needs a Redline desktop on 127.0.0.1:7436", serviceReachable())

        // Mint a pairing token the way `redline pair` does. This needs the real
        // API token, which the harness supplies through the instrumentation
        // arguments so it never appears in the repository.
        val apiToken = InstrumentationRegistry.getArguments().getString("redlineToken")
        assumeTrue("needs -Pandroid.testInstrumentationRunnerArguments.redlineToken", apiToken != null)

        val pairingToken = mintPairingToken(apiToken!!)
        val scanned = "$baseUrl/pair#pairing_token=$pairingToken"

        val settings = RedlineSettings(
            InstrumentationRegistry.getInstrumentation().targetContext,
        )
        settings.clear()

        // Exactly what the camera callback does with a decoded string.
        val request = redlineJson.decodeFromString(
            PairingRequest.serializer(),
            core.Core.parsePairingURL(scanned),
        )
        val credential = core.Core.redeemPairing(request.baseUrl, request.pairingToken)
        settings.update(request.baseUrl, credential)

        assertTrue("the app must now be paired", settings.isPaired)

        // The credential must work for ordinary requests, which is the only
        // definition of pairing that matters.
        val usage = core.Core.newClient(settings.baseUrl, settings.token).fetchUsage()
        assertTrue("a paired client must fetch usage", usage.contains("providers"))

        // The token is single use, so replaying it must fail.
        val replay = runCatching { core.Core.redeemPairing(request.baseUrl, request.pairingToken) }
        assertTrue("a used pairing token must not work twice", replay.isFailure)

        // The credential is deliberately left in place: this runs against a
        // developer's own desktop, and leaving the emulator paired is what
        // makes the app usable for manual inspection straight afterwards.
    }

    /**
     * Mints a pairing token through the Go core rather than HttpURLConnection.
     *
     * Android blocks cleartext HTTP for the platform HTTP stacks, so a Kotlin
     * client cannot reach a local Redline over plain HTTP; the core can,
     * because Go's net/http opens sockets directly and is not subject to
     * NetworkSecurityPolicy. That asymmetry is easy to trip over, so the test
     * uses the same transport the app does.
     */
    private fun mintPairingToken(apiToken: String): String =
        core.Core.createPairingToken(baseUrl, apiToken)
}
