package com.croutoncreations.redline

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.unit.dp
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.Density
import androidx.test.platform.app.InstrumentationRegistry
import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Rule
import org.junit.Test

/**
 * Renders the real screens from the real Go core against `redline demo serve
 * --scenario multi-account` (synthetic data, no credentials), reached through
 * `adb reverse tcp:7446 tcp:7446`. Skips when the demo is not running.
 *
 * Each state is captured to the app's external files directory for review.
 */
class MultiAccountDemoScreenshotTest {

    @get:Rule val compose = createAndroidComposeRule<ComponentActivity>()

    private val baseUrl = "http://127.0.0.1:7446"
    // The demo server has no API token; any value is accepted. Not a secret.
    private val client by lazy { core.Core.newClient(baseUrl, "synthetic-demo") }

    private fun usage(): UsageView? = runCatching {
        redlineJson.decodeFromString(UsageView.serializer(), client.fetchUsage())
    }.getOrNull()

    private fun capture(name: String) {
        val bitmap = compose.onRoot().captureToImage().asAndroidBitmap()
        val directory = InstrumentationRegistry.getInstrumentation().targetContext.getExternalFilesDir("screenshots")!!
        directory.mkdirs()
        File(directory, "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private fun next() {
        compose.onNodeWithTag(CAROUSEL_NEXT_TAG).performClick()
        compose.waitForIdle()
        compose.mainClock.advanceTimeBy(600)
        compose.waitForIdle()
    }

    @Test
    fun capturesEveryCarouselPageFromTheDemo() {
        val view = usage()
        assumeTrue("needs redline demo serve --scenario multi-account on $baseUrl", view != null)
        val claude = view!!.providers.filter { it.provider == "claude" }
        assertEquals(3, claude.size)
        // Each account kept its own numbers and plan end to end.
        assertEquals(listOf("Team", "Max 20x", "Pro"), claude.map { it.plan })
        assertEquals(2, claude.first { it.id == "claude-main" }.resetCredits?.available)

        compose.setContent { UsageScreen(state = UsageUiState(view = view, live = LiveState.LIVE), onRetry = {}) }
        compose.waitForIdle()
        capture("01-overview")
        for (page in 1..3) {
            next()
            capture("0${page + 1}-account-$page")
        }
        // The fourth account page does not exist; › is disabled at the end.
        assertTrue(compose.onAllNodesWithText("Account 3 of 3").fetchSemanticsNodes().isNotEmpty())
    }

    @Test
    fun capturesLargeFontOverviewAndDetail() {
        val view = usage()
        assumeTrue("needs the multi-account demo", view != null)
        compose.setContent {
            val density = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(density.density, 1.6f)) {
                UsageScreen(state = UsageUiState(view = view, live = LiveState.LIVE), onRetry = {})
            }
        }
        compose.waitForIdle()
        capture("06-large-font-overview")
        next()
        capture("07-large-font-account-1")
        // Personal (claude-main) holds banked resets with deadlines.
        next()
        capture("07b-large-font-account-2-credits")
    }

    /**
     * Synthetic fixture (no demo needed): an out-of-date account holding
     * banked resets, at 1.6x font, on its account page.
     */
    @Test
    fun capturesLargeFontStaleAccountWithCredits() {
        compose.setContent {
            val density = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(density.density, 1.6f)) {
                androidx.compose.foundation.layout.Column(
                    androidx.compose.ui.Modifier.fillMaxSize().background(Background).padding(16.dp),
                ) {
                    AccountsCarousel(UsageItem.AccountGroup("claude", CarouselFixtures.staleWithCredits), { _, _ -> }, {})
                }
            }
        }
        compose.waitForIdle()
        capture("13-large-font-stale-overview")
        next()
        capture("14-large-font-stale-account-credits")
    }

    @Test
    fun capturesTeamSeatsWithWeeklyNotReported() {
        compose.setContent {
            androidx.compose.foundation.layout.Column(
                androidx.compose.ui.Modifier.fillMaxSize().background(Background).padding(16.dp),
            ) {
                AccountsCarousel(UsageItem.AccountGroup("claude", CarouselFixtures.teamSeats), { _, _ -> }, {})
            }
        }
        compose.waitForIdle()
        capture("15-team-seats-overview")
        repeat(3) { next() }
        capture("16-team-seat-echo-weekly-not-reported")
    }

    @Test
    fun capturesAccountsScreen() {
        val accounts = runCatching {
            redlineJson.decodeFromString(AccountsView.serializer(), client.fetchAccounts())
        }.getOrNull()
        assumeTrue("needs the multi-account demo", accounts != null)
        assertEquals(4, accounts!!.accounts.size)
        compose.setContent {
            AccountsScreen(
                state = AccountsUiState(view = accounts),
                onClose = {}, onRetry = {}, onDiscover = {}, onCloseDiscover = {},
                onAdd = { _, _, _, _ -> }, onRename = { _, _ -> }, onSetDisabled = { _, _ -> }, onRemove = {},
                onDismissMessage = {},
            )
        }
        compose.waitForIdle()
        capture("08-accounts")
    }
}
