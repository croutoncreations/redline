package ai.redline.app

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.setMain
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class PairingViewModelTest {

    private val dispatcher = StandardTestDispatcher()

    @Before fun setUp() = Dispatchers.setMain(dispatcher)

    @After fun tearDown() = Dispatchers.resetMain()

    /** Records what was stored, standing in for the Keystore-backed settings. */
    private class FakeSettings : RedlineSettingsWriter {
        var baseUrl: String? = null
        var token: String? = null
        var relayUrl: String? = null
        var desktopKey: String? = null
        override fun update(baseUrl: String, token: String) {
            this.baseUrl = baseUrl
            this.token = token
        }

        override fun updateRelay(relayUrl: String, desktopKey: String) {
            this.relayUrl = relayUrl
            this.desktopKey = desktopKey
        }
    }

    private val validScan =
        "https://macbook.example.ts.net/pair#pairing_token=one-time-token"

    /**
     * A desktop that publishes a relay must have those details stored, or the
     * fallback silently never happens and the phone simply fails away from
     * home with no clue why.
     */
    @Test
    fun storesRelayDetailsWhenTheDesktopPublishesThem() = runTest(dispatcher) {
        val settings = FakeSettings()
        val model = PairingViewModel(
            source = source(
                parse = {
                    """{"base_url":"https://macbook.example.ts.net",
                        "pairing_token":"one-time-token",
                        "relay_url":"https://relay.example.com",
                        "desktop_key":"ZGVza3RvcC1wdWJsaWMta2V5LWJhc2U2NA=="}"""
                        .trimIndent().replace("\n", "").replace("  ", "")
                },
            ),
            settings = settings,
            ioDispatcher = dispatcher,
        )

        model.pair(validScan)
        dispatcher.scheduler.advanceUntilIdle()

        assertEquals("https://relay.example.com", settings.relayUrl)
        assertEquals("ZGVza3RvcC1wdWJsaWMta2V5LWJhc2U2NA==", settings.desktopKey)
    }

    /**
     * An older desktop publishes neither. Pairing must still succeed; only the
     * relay is unavailable.
     */
    @Test
    fun pairsWithoutRelayDetails() = runTest(dispatcher) {
        val settings = FakeSettings()
        val model = PairingViewModel(
            source = source(),
            settings = settings,
            ioDispatcher = dispatcher,
        )

        model.pair(validScan)
        dispatcher.scheduler.advanceUntilIdle()

        assertEquals("durable-api-token", settings.token)
        assertEquals("", settings.relayUrl ?: "")
        assertEquals("", settings.desktopKey ?: "")
    }

    private fun source(
        parse: (String) -> String = {
            """{"base_url":"https://macbook.example.ts.net","pairing_token":"one-time-token"}"""
        },
        redeem: (String, String) -> String = { _, _ -> "durable-api-token" },
    ) = object : PairingSource {
        override fun parsePairingUrl(raw: String): String = parse(raw)
        override fun redeem(baseUrl: String, pairingToken: String): String =
            redeem(baseUrl, pairingToken)
    }

    @Test
    fun storesTheCredentialAfterASuccessfulPairing() = runTest(dispatcher) {
        val settings = FakeSettings()
        val model = PairingViewModel(source(), settings, dispatcher)

        model.pair(validScan)
        dispatcher.scheduler.advanceUntilIdle()

        assertEquals("https://macbook.example.ts.net", settings.baseUrl)
        assertEquals("durable-api-token", settings.token)
        assertTrue(model.state.value.isPaired)
        assertNull(model.state.value.error)
    }

    /**
     * The camera reports the same code many times a second. A pairing token is
     * single use, so redeeming on every frame would fail on all but the first
     * and show an error over a pairing that actually worked.
     */
    @Test
    fun redeemsOnlyOnceWhenTheCameraRepeatsAScan() = runTest(dispatcher) {
        var redeemCount = 0
        val model = PairingViewModel(
            source(redeem = { _, _ -> redeemCount++; "durable-api-token" }),
            FakeSettings(),
            dispatcher,
        )

        model.pair(validScan)
        model.pair(validScan)
        model.pair(validScan)
        dispatcher.scheduler.advanceUntilIdle()

        assertEquals("a single-use token must be redeemed once", 1, redeemCount)
    }

    /** A QR from another app should say so, not fail cryptically. */
    @Test
    fun reportsAForeignQRCode() = runTest(dispatcher) {
        val model = PairingViewModel(
            source(parse = { throw RuntimeException("this does not look like a Redline pairing code") }),
            FakeSettings(),
            dispatcher,
        )

        model.pair("https://example.com/something-else")
        dispatcher.scheduler.advanceUntilIdle()

        assertFalse(model.state.value.isPaired)
        assertEquals(
            "this does not look like a Redline pairing code",
            model.state.value.error,
        )
    }

    /**
     * An expired code needs a new QR, so the service's explanation must reach
     * the user rather than a generic failure.
     */
    @Test
    fun surfacesTheServiceExplanationForAnExpiredCode() = runTest(dispatcher) {
        val model = PairingViewModel(
            source(redeem = { _, _ ->
                throw RuntimeException("invalid or expired Redline pairing token")
            }),
            FakeSettings(),
            dispatcher,
        )

        model.pair(validScan)
        dispatcher.scheduler.advanceUntilIdle()

        assertEquals(
            "invalid or expired Redline pairing token",
            model.state.value.error,
        )
    }

    /** Nothing is stored when pairing fails, so the app does not look paired. */
    @Test
    fun storesNothingWhenPairingFails() = runTest(dispatcher) {
        val settings = FakeSettings()
        val model = PairingViewModel(
            source(redeem = { _, _ -> throw RuntimeException("nope") }),
            settings,
            dispatcher,
        )

        model.pair(validScan)
        dispatcher.scheduler.advanceUntilIdle()

        assertNull(settings.token)
        assertNull(settings.baseUrl)
    }
}
