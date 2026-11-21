package com.croutoncreations.redline

import androidx.test.platform.app.InstrumentationRegistry
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Test

/**
 * Pairs the app with `redline demo serve` (synthetic data, no real account),
 * reached through `adb reverse tcp:7446 tcp:7446`, so the real activity can
 * be driven by UI automation. Runs only when asked for with
 * `-e pairDemo true`; never against a real Redline.
 */
class DemoPairingHelper {
    private val baseUrl = "http://127.0.0.1:7446"

    @Test
    fun pairWithDemo() {
        assumeTrue(InstrumentationRegistry.getArguments().getString("pairDemo") == "true")
        // The demo server runs without an API token and accepts any bearer,
        // so the stored value is a fixed placeholder, not a credential.
        val client = core.Core.newClient(baseUrl, "synthetic-demo")
        // Only the synthetic demo: its sources all say "demo source".
        val usage = runCatching { client.fetchUsage() }.getOrNull()
        assumeTrue("needs the demo on $baseUrl", usage != null && usage.contains("demo source"))
        val settings = RedlineSettings(InstrumentationRegistry.getInstrumentation().targetContext)
        // Never overwrite a real pairing unless explicitly told to.
        assumeTrue(
            "device is already paired; pass -e force true to replace it",
            !settings.isPaired || settings.baseUrl == baseUrl ||
                InstrumentationRegistry.getArguments().getString("force") == "true",
        )
        settings.update(baseUrl, "synthetic-demo")
        assertTrue(settings.isPaired)
        // update() uses apply(); give it time to reach disk before the
        // instrumentation process exits.
        Thread.sleep(1_500)
    }
}
