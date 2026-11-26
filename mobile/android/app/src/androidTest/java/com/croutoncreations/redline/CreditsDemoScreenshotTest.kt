package com.croutoncreations.redline

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.hasScrollToNodeAction
import androidx.compose.ui.unit.dp
import androidx.test.platform.app.InstrumentationRegistry
import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Rule
import org.junit.Test

/**
 * Renders an OpenAI API credits account from the real Go core against
 * `redline demo serve --scenario api-credits` (synthetic data, no keys),
 * reached through `adb reverse tcp:7446 tcp:7446`. Skips when the demo is not
 * running. The no-promo and signed-out states use synthetic fixtures.
 */
class CreditsDemoScreenshotTest {

    @get:Rule val compose = createAndroidComposeRule<ComponentActivity>()

    private val baseUrl = "http://127.0.0.1:7446"
    // The demo server has no API token; any value is accepted. Not a secret.
    private val client by lazy { core.Core.newClient(baseUrl, "synthetic-demo") }

    private fun capture(name: String) {
        val bitmap = compose.onRoot().captureToImage().asAndroidBitmap()
        val directory = InstrumentationRegistry.getInstrumentation().targetContext.getExternalFilesDir("screenshots")!!
        directory.mkdirs()
        File(directory, "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private fun shows(text: String) = compose.onAllNodesWithText(text, substring = true).fetchSemanticsNodes().isNotEmpty()

    @Test
    fun capturesTheCreditsAccountFromTheDemo() {
        // Skip only when the demo is unreachable; a response that no longer
        // decodes is a failure.
        val raw = runCatching { client.fetchUsage() }.getOrNull()
        assumeTrue("needs redline demo serve --scenario api-credits on $baseUrl", raw != null)
        val view = redlineJson.decodeFromString(UsageView.serializer(), raw!!)
        val account = view.providers.single { it.provider == "openai-api-credits" }
        assertEquals("\$62.40 left", account.credits?.headline)

        // The credits card first, then the rest of the screen below it.
        val ordered = view.copy(providers = listOf(account) + view.providers.filter { it !== account })
        compose.setContent { UsageScreen(state = UsageUiState(view = ordered, live = LiveState.LIVE), onRetry = {}) }
        compose.waitForIdle()
        assertTrue(shows("OpenAI API credits"))
        assertTrue(shows("\$62.40 left"))
                capture("android-credits-usage")

        // The detail: rule line, paid credit and grants.
        compose.onNode(hasScrollToNodeAction()).performScrollToNode(hasText("GRANTS"))
        compose.waitForIdle()
        assertTrue(shows("ChatGPT subscription grant"))
        assertTrue(shows("shown only, never scheduled"))
        capture("android-credits-detail")
    }

    private fun single(provider: ProviderUsage, name: String) {
        compose.setContent {
            Column(Modifier.fillMaxSize().background(Background).padding(vertical = 16.dp)) {
                UsageScreen(state = UsageUiState(view = UsageView(providers = listOf(provider)), live = LiveState.LIVE), onRetry = {})
            }
        }
        compose.waitForIdle()
        capture(name)
    }

    @Test
    fun capturesNoPromo() {
        val noPromo = ProviderUsage(
            id = "api-credits", provider = "openai-api-credits", label = "Linked org", accountStatus = "ok",
            sourceLabel = "apicredits source · 0/1 active · sampled just now",
            credits = CreditsView(
                headline = "\$0.00 promo", detail = "No promotional credit", promoLabel = "\$0.00 of \$0.00 left",
                ruleLine = "Jobs start above \$5.00 · Firesale: runs within 3 days of expiry",
                paidLabel = "\$7.25 · shown only, never scheduled", noPromo = true,
                grants = listOf(CreditGrantView("Auto recharge", "\$7.25 of \$10.00", "2027-10-15T19:00:00Z")),
            ),
            scheduling = Scheduling(decision = "WAIT", reason = "no promotional API credit is available"),
        )
        single(noPromo, "android-credits-no-promo")
        assertTrue(shows("\$0.00 promo"))
        assertTrue(!shows("not reported"))
    }

    @Test
    fun capturesSignedOut() {
        val signedOut = ProviderUsage(
            id = "api-credits", provider = "openai-api-credits", label = "Linked org", accountStatus = "signed_out",
            stale = true, staleReason = "Session key expired · re-paste it on the computer running Redline",
            credits = CreditsView(
                headline = "Session key needed",
                detail = "Session key expired · re-paste it on the computer running Redline", signedOut = true,
            ),
        )
        single(signedOut, "android-credits-signed-out")
        assertTrue(shows("Session key needed"))
        assertTrue(shows("re-paste it on the computer running Redline"))
    }
}
