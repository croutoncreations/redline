package ai.redline.app

import androidx.test.ext.junit.runners.AndroidJUnit4
import core.Core
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Test
import org.junit.runner.RunWith

/**
 * Drives the relay client on a real device.
 *
 * The JVM tests prove the logic and the integration test proves the Go pieces
 * fit together, but neither runs the Noise handshake through the gomobile
 * boundary on an actual phone. Cryptography that works on a laptop and fails on
 * a device is exactly the class of bug this catches.
 *
 * Needs a relay and a desktop reachable from the device:
 *
 *   wrangler dev --port 8788 --local --var ALLOW_UNENTITLED:true
 *   adb reverse tcp:8788 tcp:8788
 */
@RunWith(AndroidJUnit4::class)
class RelayOnDeviceTest {

    private val relayUrl = "ws://127.0.0.1:8788"

    @Test
    fun rejectsAMalformedRelayConfiguration() {
        // Runs without any relay present: these must fail before dialling.
        val cases = listOf(
            Triple("", "session-abcdefghij0123", "key"),
            Triple("http://relay.example.com", "session-abcdefghij0123", "key"),
            Triple(relayUrl, "short", "key"),
        )
        for ((url, session, key) in cases) {
            val failed = runCatching { Core.dialRelay(url, session, key, "") }.isFailure
            assertTrue("dialRelay accepted $url / $session", failed)
        }
    }

    /**
     * The full handshake, on device, against a real relay. Skipped when no
     * relay is reachable so the suite stays runnable on its own.
     */
    @Test
    fun completesAHandshakeThroughARealRelay() {
        val desktopKey = System.getProperty("redline.desktopKey")
        val session = System.getProperty("redline.relaySession")
        assumeTrue(
            "needs -Pandroid.testInstrumentationRunnerArguments for a live relay",
            desktopKey != null && session != null,
        )

        val client = Core.dialRelay(relayUrl, session, desktopKey, "")
        client.setAuthToken("desktop-api-token")
        try {
            val body = client.request("GET", "/v1/dashboard", "")
            assertTrue("unexpected response: $body", body.contains("ok"))
        } finally {
            client.close()
        }
    }
}
