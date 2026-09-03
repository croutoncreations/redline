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
