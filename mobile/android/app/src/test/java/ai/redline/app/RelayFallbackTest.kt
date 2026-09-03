package ai.redline.app

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Choosing between the direct route and the relay.
 *
 * Kept as a pure decision so it can be tested on the JVM: the transports
 * themselves need a device and a network.
 */
class RelayFallbackTest {

    private fun plan(
        directWorks: Boolean,
        relayReady: Boolean,
        lastKnownTransport: Transport = Transport.Direct,
    ) = planTransport(
        directReachable = directWorks,
        relayConfigured = relayReady,
        previous = lastKnownTransport,
    )

    @Test
    fun `direct is used whenever it works`() {
        assertEquals(Transport.Direct, plan(directWorks = true, relayReady = true).transport)
        assertEquals(Transport.Direct, plan(directWorks = true, relayReady = false).transport)
    }

    @Test
    fun `relay is used when direct fails and one is paired`() {
        val decision = plan(directWorks = false, relayReady = true)
        assertEquals(Transport.Relay, decision.transport)
        assertTrue(decision.shouldTryRelay)
    }

    /**
     * With no relay paired there is nothing to fall back to, and the honest
     * answer is that the desktop is unreachable. Reporting a relay problem to
     * someone who never set one up sends them to look in the wrong place.
     */
    @Test
    fun `without a relay the failure stays a plain unreachable`() {
        val decision = plan(directWorks = false, relayReady = false)
        assertEquals(Transport.None, decision.transport)
        assertFalse(decision.shouldTryRelay)
    }

    /**
     * Direct is retried every time rather than sticking to the relay once it
     * has been used. Walking back through the front door should stop costing
     * money without needing the app restarted.
     */
    @Test
    fun `coming home returns to the direct route`() {
        val decision = plan(
            directWorks = true,
            relayReady = true,
            lastKnownTransport = Transport.Relay,
        )
        assertEquals(Transport.Direct, decision.transport)
    }

    /** The cadence and idle rules follow the chosen transport. */
    @Test
    fun `a relayed plan carries the cheaper cadence`() {
        val relayed = plan(directWorks = false, relayReady = true)
        val direct = plan(directWorks = true, relayReady = true)

        assertEquals(20, relayed.liveCadenceSeconds)
        assertEquals(5, direct.liveCadenceSeconds)
        assertTrue(relayed.dropWhenIdle)
        assertFalse(direct.dropWhenIdle)
    }
}
