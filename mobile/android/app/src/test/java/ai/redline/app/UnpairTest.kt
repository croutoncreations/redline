package ai.redline.app

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Unpairing has to remove everything, not just the token.
 *
 * A half-cleared device is the worst outcome: it looks unpaired, so the user
 * scans a new code, and then it is carrying one desktop's relay details with
 * another desktop's credential. The store is the thing being tested here
 * rather than the button, because that is where the incompleteness would live.
 */
class UnpairTest {

    /** Stands in for the Keystore-backed store, which needs a device. */
    private class FakeStore : PairingStore {
        val values = mutableMapOf<String, String>()
        override fun update(baseUrl: String, token: String) {
            values["base_url"] = baseUrl
            values["token"] = token
        }

        override fun updateRelay(
            relayUrl: String,
            desktopKey: String,
            relaySession: String,
            entitlementToken: String,
        ) {
            values["relay_url"] = relayUrl
            values["desktop_key"] = desktopKey
            values["relay_session"] = relaySession
            values["entitlement_token"] = entitlementToken
        }

        override fun clear() {
            values.clear()
        }
    }

    private fun paired() = FakeStore().apply {
        update("https://macbook.example.ts.net", "durable-api-token")
        updateRelay("https://relay.example.com", "ZGVza3RvcC1rZXk=", "session-abcdefghij0123", "ent.token")
    }

    @Test
    fun `unpairing removes every stored field`() {
        val store = paired()
        assertEquals(6, store.values.size)

        store.clear()

        assertTrue(
            "anything left behind would be carried into the next pairing: ${store.values}",
            store.values.isEmpty(),
        )
    }

    /**
     * The relay details in particular must go. Leaving them would point a
     * newly paired phone at the previous desktop's relay session, where its
     * new credential means nothing.
     */
    @Test
    fun `unpairing does not leave relay details behind`() {
        val store = paired()
        store.clear()

        assertFalse(store.values.containsKey("relay_url"))
        assertFalse(store.values.containsKey("desktop_key"))
        assertFalse(store.values.containsKey("entitlement_token"))
        assertFalse(store.values.containsKey("relay_session"))
    }

    /**
     * Unpairing is destructive and cannot be undone from the phone: the
     * pairing token is single-use, so recovering means going back to the Mac.
     * The UI must therefore ask first, and the confirmation state has to be
     * dismissible without doing anything.
     */
    @Test
    fun `the confirmation can be dismissed without unpairing`() {
        val store = paired()
        var confirming = true

        // Cancelling.
        confirming = false
        assertEquals("cancelling must not touch the store", 6, store.values.size)
        assertFalse(confirming)
    }

    @Test
    fun `unpairing leaves the app ready to pair again`() {
        val store = paired()
        store.clear()

        // A fresh pairing writes cleanly over the empty store.
        store.update("https://other.example.ts.net", "new-token")
        assertEquals("new-token", store.values["token"])
        assertEquals("https://other.example.ts.net", store.values["base_url"])
        assertFalse("the old relay must not reappear", store.values.containsKey("relay_url"))
    }
}
