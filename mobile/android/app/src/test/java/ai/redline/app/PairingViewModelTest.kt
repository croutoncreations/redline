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
    private class FakeSettings : PairingStore {
        var baseUrl: String? = null
        var token: String? = null
        var relayUrl: String? = null
        var desktopKey: String? = null
        var relaySession: String? = null
        var entitlementToken: String? = null
        var pairingTransactions = 0
        fun update(baseUrl: String, token: String) {
            this.baseUrl = baseUrl
            this.token = token
        }

        fun updateRelay(
            relayUrl: String,
            desktopKey: String,
            relaySession: String,
            entitlementToken: String,
        ) {
            this.relayUrl = relayUrl
            this.desktopKey = desktopKey
            this.relaySession = relaySession
            this.entitlementToken = entitlementToken
        }

        override fun updatePairing(configuration: PairingConfiguration) {
            pairingTransactions += 1
            update(configuration.baseUrl, configuration.token)
            updateRelay(
                configuration.relayUrl,
                configuration.desktopKey,
                configuration.relaySession,
                configuration.entitlementToken,
            )
        }

        override fun clear() {
            baseUrl = null
            token = null
            relayUrl = null
            desktopKey = null
            relaySession = null
        }
    }

    private val validScan =
        "https://macbook.example.ts.net/pair#pairing_token=one-time-token"

    /**
     * After unpairing, the view model must stop claiming the device is paired.
     *
     * It holds its own success state from the last pairing, so without this the
     * app would clear the credential and immediately decide it was still paired
     * -- showing a dashboard it can no longer fetch.
     */
    @Test
    fun forgettingReturnsToTheUnpairedState() = runTest(dispatcher) {
        val settings = FakeSettings()
        val model = PairingViewModel(source(), settings, dispatcher)

        model.pair(validScan)
        dispatcher.scheduler.advanceUntilIdle()
        assertTrue(model.state.value.isPaired)

        model.forget()

        assertFalse("the model must not still report a pairing", model.state.value.isPaired)
        assertNull("a stale error would show on the pairing screen", model.state.value.error)
    }

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
                        "desktop_key":"ZGVza3RvcC1wdWJsaWMta2V5LWJhc2U2NA==","relay_session":"session-abcdefghij0123"}"""
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
        // Without the session id the phone knows where the relay is but not
        // which conversation on it is its own.
        assertEquals("session-abcdefghij0123", settings.relaySession)
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
        redeem: (PairingRequest) -> String = { "durable-api-token" },
    ) = object : PairingSource {
        override fun parsePairingUrl(raw: String): String = parse(raw)
        override fun redeem(request: PairingRequest): String = redeem(request)
    }

    /**
     * A relay-only code has no direct endpoint, so the redeem itself must go
     * over the relay. The source can only choose that route if it is handed
     * everything the QR carried -- the relay URL, the desktop key, the session
     * and the entitlement -- rather than a base URL that is empty.
     *
     * Pairing was the one request that could not fall back to the relay,
     * because it ran before any client with a fallback existed. That made a
     * tailnet a prerequisite for a product whose point is that it is optional.
     */
    @Test
    fun `stored relay-only endpoint remains empty instead of becoming localhost`() {
        assertEquals("", resolvedBaseUrl(hasStoredValue = true, storedValue = ""))
        assertEquals("http://127.0.0.1:7436", resolvedBaseUrl(hasStoredValue = false, storedValue = null))
    }

    @Test
    fun `open relay is configured without an entitlement`() {
        assertTrue(relayConfigurationComplete("https://relay.example", "key", "session"))
    }

    @Test
    fun redeemsARelayOnlyCodeWithTheRelayDetailsItCarried() = runTest(dispatcher) {
        val settings = FakeSettings()
        var seen: PairingRequest? = null
        val model = PairingViewModel(
            source(
                parse = {
                    """{"base_url":"","pairing_token":"one-time-token",""" +
                        """"relay_url":"https://relay.example","desktop_key":"key==",""" +
                        """"relay_session":"session","entitlement_token":"ent"}"""
                },
                redeem = { request -> seen = request; "durable-api-token" },
            ),
            settings,
            dispatcher,
        )

        model.pair("https://relay/pair#...")
        dispatcher.scheduler.advanceUntilIdle()

        val request = seen ?: error("redeem was never called")
        assertEquals("", request.baseUrl)
        assertEquals("https://relay.example", request.relayUrl)
        assertEquals("key==", request.desktopKey)
        assertEquals("session", request.relaySession)
        assertEquals("ent", request.entitlementToken)

        assertEquals("", settings.baseUrl)
        assertEquals("durable-api-token", settings.token)
        assertEquals("https://relay.example", settings.relayUrl)
        assertEquals(1, settings.pairingTransactions)
        assertTrue(model.state.value.isPaired)
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
            source(redeem = { redeemCount++; "durable-api-token" }),
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
            source(redeem = {
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
            source(redeem = { throw RuntimeException("nope") }),
            settings,
            dispatcher,
        )

        model.pair(validScan)
        dispatcher.scheduler.advanceUntilIdle()

        assertNull(settings.token)
        assertNull(settings.baseUrl)
    }
}
