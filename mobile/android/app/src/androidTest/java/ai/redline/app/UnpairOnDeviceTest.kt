package ai.redline.app

import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

/**
 * Unpairing against the real Keystore-backed store.
 *
 * The JVM test proves the logic with a fake. This proves the encrypted store
 * actually forgets, which is the part that could fail on a device and nowhere
 * else: a credential that survives a clear is the whole point of the feature
 * not working.
 */
@RunWith(AndroidJUnit4::class)
class UnpairOnDeviceTest {

    private val context = InstrumentationRegistry.getInstrumentation().targetContext

    @Test
    fun clearingRemovesTheCredentialFromEncryptedStorage() {
        val settings = RedlineSettings(context)
        settings.update("https://macbook.example.ts.net", "durable-api-token")
        settings.updateRelay(
            "https://relay.example.com",
            "ZGVza3RvcC1rZXk=",
            "session-abcdefghij0123",
            "",
        )
        assertTrue("setup should leave the device paired", settings.isPaired)

        settings.clear()

        assertFalse("the device must no longer be paired", settings.isPaired)
        assertEquals("", settings.token)
        assertEquals("", settings.relayUrl)
        assertEquals("", settings.desktopKey)
        assertEquals("", settings.relaySession)
        assertFalse("a relayed session must not remain configured", settings.relayConfigured)

        // A second reader sees the same thing: the clear reached storage rather
        // than only this instance's cache.
        val reopened = RedlineSettings(context)
        assertFalse("the credential survived being reopened", reopened.isPaired)
        assertEquals("", reopened.relaySession)
    }

    @Test
    fun pairingAgainAfterClearingWorks() {
        val settings = RedlineSettings(context)
        settings.update("https://old.example.ts.net", "old-token")
        settings.updateRelay("https://old-relay.example.com", "b2xkLWtleQ==", "old-session-01234567", "")
        settings.clear()

        settings.update("https://new.example.ts.net", "new-token")

        assertTrue(settings.isPaired)
        assertEquals("new-token", settings.token)
        assertEquals("https://new.example.ts.net", settings.baseUrl)
        // The previous desktop's relay must not come back with the new
        // credential: it would point this phone at a session where its new
        // token means nothing.
        assertEquals("", settings.relayUrl)
        assertEquals("", settings.relaySession)

        settings.clear()
    }
}
