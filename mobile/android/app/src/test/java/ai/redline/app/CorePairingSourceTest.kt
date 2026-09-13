package ai.redline.app

import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * The one piece of a relay envelope the redeem log is allowed to show.
 *
 * The log used to print the first forty characters of the envelope, and the
 * credential in Set-Cookie stayed out of it only because another header
 * sorted first. Now only the status is read out, and this pins that the
 * reader cannot be made to yield anything else.
 */
class CorePairingSourceTest {

    @Test
    fun `reads the status Go's json Marshal writes`() {
        assertEquals("204", statusOf("""{"status":204,"header":{"Set-Cookie":["redline_api_session=SECRET"]}}"""))
        assertEquals("401", statusOf("""{"status":401,"body":"eyJ..."}"""))
    }

    @Test
    fun `yields a placeholder, never a fragment of the envelope, when there is no status`() {
        for (envelope in listOf("", "garbage", """{"header":{"Set-Cookie":["redline_api_session=SECRET"]}}""")) {
            assertEquals(envelope, "?", statusOf(envelope))
        }
    }

    @Test
    fun `takes exactly three digits and nothing that follows`() {
        assertEquals("204", statusOf("""{"status":2044,"x":1}"""))
        assertEquals("?", statusOf("""{"status":"204"}"""))
    }
}
