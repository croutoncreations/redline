package ai.redline.app

import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.test.platform.app.InstrumentationRegistry
import org.junit.Rule
import org.junit.Test

/**
 * Renders the capacity screen in the state the user reported: fresh data, a
 * good weekly allowance, and a five hour window the provider could not report.
 *
 * The unit tests prove the payload decodes correctly; this proves the screen
 * actually draws a row saying so, which is the part that was wrong.
 */
class UnknownWindowScreenTest {

    @get:Rule val compose = createComposeRule()

    private val providerWithUnreadableWindow = ProviderUsage(
        id = "claude-main",
        provider = "claude",
        sourceLabel = "openusage source · 1/1 active · sampled 4m ago",
        session = null,
        sessionUnknown = true,
        weekly = Window(remainingPercent = 54, resetsInSeconds = 93_600),
    )

    /**
     * Codex as it really is: no general five hour window, a spent weekly, both
     * Spark pools under their own names, and the count of banked resets that
     * could refill the weekly.
     *
     * The resets row is the one that went missing in practice, because the Go
     * core was edited without rebuilding the native library the app bundles.
     * A test that renders the composable would not have caught that either --
     * only the on-device path exercises the real AAR.
     */
    @Test
    fun showsSparkPoolsAndBankedResets() {
        val codex = ProviderUsage(
            id = "codex-main",
            provider = "codex",
            sourceLabel = "openusage source · 0/1 active · sampled 1m ago",
            session = null,
            bankedResets = 0,
            weekly = Window(remainingPercent = 0, resetsInSeconds = 275_000),
            pools = listOf(
                Pool(key = "model:spark:short", label = "Spark", remainingPercent = 98, resetsInSeconds = 12_780),
                Pool(key = "model:spark:weekly", label = "Spark Weekly", remainingPercent = 99, resetsInSeconds = 599_000),
            ),
        )

        compose.setContent {
            UsageScreen(
                state = UsageUiState(
                    view = UsageView(providers = listOf(codex)),
                    live = LiveState.LIVE,
                ),
                onRetry = {},
            )
        }

        compose.onNodeWithText("Spark").assertIsDisplayed()
        compose.onNodeWithText("Spark Weekly").assertIsDisplayed()
        compose.onNodeWithText("Banked quota resets").assertIsDisplayed()
        compose.onNodeWithText("0 available").assertIsDisplayed()

        if (InstrumentationRegistry.getArguments().getString("holdForScreenshot") != null) {
            Thread.sleep(20_000)
        }
    }

    @Test
    fun showsTheWindowAsUnavailableRatherThanHidingIt() {
        compose.setContent {
            UsageScreen(
                state = UsageUiState(
                    view = UsageView(providers = listOf(providerWithUnreadableWindow)),
                    live = LiveState.LIVE,
                ),
                onRetry = {},
            )
        }

        // The row exists and says it does not know, rather than being absent.
        compose.onNodeWithText("5-hour window").assertIsDisplayed()
        compose.onNodeWithText("not available").assertIsDisplayed()
        // The weekly numbers are still shown.
        compose.onNodeWithText("Weekly allowance").assertIsDisplayed()
        compose.onNodeWithText("54% left").assertIsDisplayed()

        // Pass -e holdForScreenshot 1 to keep this state on screen long enough
        // to photograph.
        if (InstrumentationRegistry.getArguments().getString("holdForScreenshot") != null) {
            Thread.sleep(20_000)
        }
    }
}
