package ai.redline.app

import android.content.Context
import android.graphics.Bitmap
import android.graphics.Color
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.google.mlkit.vision.barcode.BarcodeScanner
import com.google.mlkit.vision.barcode.BarcodeScannerOptions
import com.google.mlkit.vision.barcode.BarcodeScanning
import com.google.mlkit.vision.barcode.common.Barcode
import com.google.mlkit.vision.common.InputImage
import kotlinx.serialization.builtins.serializer
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

/**
 * On-device checks for the parts of pairing that cannot run on the JVM: the
 * real QR decoder and the real Keystore.
 *
 * A unit test with a fake scanner would prove the plumbing but not that a QR
 * produced by `redline pair --qr` actually decodes, which is the part most
 * likely to be wrong.
 */
@RunWith(AndroidJUnit4::class)
class PairingOnDeviceTest {

    private val pairingUrl =
        "http://127.0.0.1:7436/pair#pairing_token=XgvBUexampletokenvalue1234567890abcdefgh"

    /**
     * Renders a QR using the same encoder the desktop uses, so the decoder is
     * tested against a real pairing code rather than a hand-built fixture that
     * merely resembles one.
     */
    private fun renderQr(contents: String, scale: Int = 12): Bitmap {
        val rows = redlineJson.decodeFromString(
            kotlinx.serialization.builtins.ListSerializer(String.serializer()),
            core.Core.encodePairingQR(contents),
        )
        val size = rows.size * scale
        val bitmap = Bitmap.createBitmap(size, size, Bitmap.Config.ARGB_8888)
        for (y in 0 until size) {
            for (x in 0 until size) {
                val dark = rows[y / scale][x / scale] == '1'
                bitmap.setPixel(x, y, if (dark) Color.BLACK else Color.WHITE)
            }
        }
        return bitmap
    }

    private fun scan(bitmap: Bitmap): String? {
        val scanner: BarcodeScanner = BarcodeScanning.getClient(
            BarcodeScannerOptions.Builder()
                .setBarcodeFormats(Barcode.FORMAT_QR_CODE)
                .build(),
        )
        var found: String? = null
        val latch = CountDownLatch(1)
        scanner.process(InputImage.fromBitmap(bitmap, 0))
            .addOnSuccessListener { codes ->
                found = codes.firstNotNullOfOrNull { it.rawValue }
                latch.countDown()
            }
            .addOnFailureListener { latch.countDown() }
        latch.await(20, TimeUnit.SECONDS)
        scanner.close()
        return found
    }

    /**
     * The end-to-end scan path: a rendered pairing QR must decode, and the Go
     * core must accept what comes out of the decoder.
     */
    @Test
    fun decodesAPairingQrAndParsesIt() {
        val decoded = scan(renderQr(pairingUrl))
        assertNotNull("the QR must decode", decoded)
        assertEquals(pairingUrl, decoded)

        // The decoded string goes straight into the core, exactly as the
        // camera path does.
        val parsed = core.Core.parsePairingURL(decoded!!)
        assertTrue("base url must survive", parsed.contains("127.0.0.1:7436"))
        assertTrue("token must survive", parsed.contains("XgvBUexampletokenvalue"))
    }

    /**
     * The credential must actually round-trip through Keystore-backed storage,
     * since an encryption failure would otherwise surface as a mysterious
     * unauthorized error much later.
     */
    @Test
    fun openingSecureSettingsDeletesCredentialsFromTheLegacyPlaintextStore() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        context.getSharedPreferences("redline", Context.MODE_PRIVATE).edit()
            .putString("token", "legacy-plaintext-token")
            .commit()

        RedlineSettings(context)

        assertEquals(
            null,
            context.getSharedPreferences("redline", Context.MODE_PRIVATE).getString("token", null),
        )
    }

    @Test
    fun relayOnlyPairingKeepsTheDirectEndpointAbsentAcrossRestart() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val settings = RedlineSettings(context)
        settings.clear()
        settings.updatePairing(
            PairingConfiguration(
                baseUrl = "",
                token = "relay-only-token",
                relayUrl = "https://relay.example.com",
                desktopKey = "desktop-key",
                relaySession = "relay-session-01234567",
                entitlementToken = "",
            ),
        )

        val reopened = RedlineSettings(context)
        assertEquals("", reopened.baseUrl)
        assertTrue("open relay details should be usable", reopened.relayConfigured)
        reopened.clear()
    }

    @Test
    fun storesAndReadsTheCredentialFromEncryptedStorage() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val settings = RedlineSettings(context)
        settings.clear()

        assertTrue("the real device must support encrypted storage", settings.usingEncryptedStorage)

        settings.update("https://macbook.example.ts.net", "a-durable-token")
        assertEquals("https://macbook.example.ts.net", settings.baseUrl)
        assertEquals("a-durable-token", settings.token)
        assertTrue(settings.isPaired)

        // A fresh instance must read the same values back, proving they were
        // persisted and decrypted rather than merely held in memory.
        val reopened = RedlineSettings(context)
        assertEquals("a-durable-token", reopened.token)

        reopened.clear()
        assertTrue("clearing must unpair", !RedlineSettings(context).isPaired)
    }
}
