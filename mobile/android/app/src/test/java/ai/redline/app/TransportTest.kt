package ai.redline.app

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The phone prefers a direct connection and falls back to the relay.
 *
 * This matters beyond speed: direct is free and private, while the relay costs
 * money and involves a third party. Choosing it when it is not needed would be
 * both slower and more expensive.
 */
class TransportTest {

    @Test
    fun `direct is preferred when the desktop is reachable`() {
        val choice = chooseTransport(directReachable = true, relayConfigured = true)
        assertEquals(Transport.Direct, choice)
    }

    @Test
    fun `relay is used only when direct fails`() {
        val choice = chooseTransport(directReachable = false, relayConfigured = true)
        assertEquals(Transport.Relay, choice)
    }

    /**
     * With no relay configured there is nothing to fall back to, and saying
     * "unreachable" is honest. Silently reporting a relay problem the user has
     * not opted into would send them looking for the wrong fault.
     */
    @Test
    fun `no relay configured means unreachable rather than a relay error`() {
        val choice = chooseTransport(directReachable = false, relayConfigured = false)
        assertEquals(Transport.None, choice)
    }

    /**
     * A relayed frame costs a Cloudflare request and a slice of the free tier's
     * daily duration allowance. Five seconds is right on loopback; over a relay
     * it is wasteful, and the difference between 5s and 20s is roughly four
     * times the bill for data almost nobody is watching that closely.
     */
    @Test
    fun `live cadence backs off when relayed`() {
        val direct = liveCadenceSeconds(Transport.Direct)
        val relayed = liveCadenceSeconds(Transport.Relay)

        assertEquals(5, direct)
        assertTrue("relayed cadence should be slower than direct", relayed > direct)
        assertTrue("relayed cadence should stay responsive enough to feel live", relayed <= 30)
    }

    /**
     * An app left open on a desk is the expensive case: an always-connected
     * relayed session is most of the free tier's daily allowance by itself.
     */
    @Test
    fun `an idle relayed session is dropped`() {
        val tracker = RelayIdleTracker(idleMillis = 5 * 60 * 1000)

        assertFalse(tracker.expired(now = 0))
        assertFalse(tracker.expired(now = 4 * 60 * 1000))
        assertTrue(tracker.expired(now = 5 * 60 * 1000 + 1))

        tracker.sawTraffic(at = 4 * 60 * 1000)
        assertFalse("traffic should reset the idle clock", tracker.expired(now = 5 * 60 * 1000 + 1))
    }

    /**
     * A direct session has no such cost, so it is not dropped for being idle.
     * Applying the relay's guardrail to the free path would be a regression
     * for someone sitting at home on their own network.
     */
    @Test
    fun `a direct session is not dropped for being idle`() {
        assertFalse(shouldDropWhenIdle(Transport.Direct))
        assertTrue(shouldDropWhenIdle(Transport.Relay))
    }

    /**
     * Free-tier limits are hard stops, not overage billing, so a relay can
     * simply refuse. The app must say so rather than looking broken, and it
     * must not be confused with "your credential is wrong".
     */
    @Test
    fun `relay refusal states stay distinct from credential failures`() {
        assertEquals(RelayStatus.NeedsUpgrade, relayStatusFor(httpStatus = 402))
        assertEquals(RelayStatus.Unauthorized, relayStatusFor(httpStatus = 401))
        assertEquals(RelayStatus.Unauthorized, relayStatusFor(httpStatus = 403))
        assertEquals(RelayStatus.Busy, relayStatusFor(httpStatus = 409))
        assertEquals(RelayStatus.Unavailable, relayStatusFor(httpStatus = 500))
        assertEquals(RelayStatus.Unavailable, relayStatusFor(httpStatus = 503))
        assertEquals(RelayStatus.Connected, relayStatusFor(httpStatus = 101))
    }

    /** Each state needs wording a person can act on. */
    @Test
    fun `relay statuses explain what to do`() {
        assertTrue(relayStatusMessage(RelayStatus.NeedsUpgrade).contains("upgrade", ignoreCase = true))
        assertTrue(relayStatusMessage(RelayStatus.Busy).contains("another device", ignoreCase = true))

        // "Unavailable" must not blame the credential, and "unauthorized" must
        // not blame the relay: sending someone to re-pair over an outage, or to
        // wait out an outage that is really a bad token, both waste their time.
        val unavailable = relayStatusMessage(RelayStatus.Unavailable)
        assertFalse(unavailable.contains("pair", ignoreCase = true))
        assertTrue(relayStatusMessage(RelayStatus.Unauthorized).contains("pair", ignoreCase = true))
    }
}
