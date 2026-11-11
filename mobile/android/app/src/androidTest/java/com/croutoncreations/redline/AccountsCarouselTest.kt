package com.croutoncreations.redline

import androidx.activity.ComponentActivity
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.height
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.getBoundsInRoot
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performSemanticsAction
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeLeft
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.platform.app.InstrumentationRegistry
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test

/**
 * The accounts carousel with synthetic fixture accounts (no real account or
 * credential is involved): opens on the Overview, navigates every documented
 * way, keeps its height on every page, and stays readable at large font.
 */
class AccountsCarouselTest {

    @get:Rule val compose = createAndroidComposeRule<ComponentActivity>()

    private val hold = InstrumentationRegistry.getArguments().getString("holdForScreenshot") != null

    @Test
    fun opensOnOverviewAndNavigatesEveryWay() {
        show(CarouselFixtures.threeAccounts)

        shown("Claude · 3 accounts · 1 of 3 dispatching")
        compose.onNodeWithTag(CAROUSEL_POSITION_TAG).assert(hasText("Overview · swipe left for each account"))
        compose.onNodeWithTag(CAROUSEL_PREVIOUS_TAG).assertIsNotEnabled()
        compose.onNodeWithTag(CAROUSEL_NEXT_TAG).assertIsEnabled()
        shown("Personal")
        shown("Client — Northwind long-running engagement")
        // Plans are shown as reported, per account.
        shown("Max 20x")

        // Details › on a row jumps to that account.
        click("Client — Northwind long-running engagement")
        compose.waitForIdle()
        compose.onNodeWithTag(CAROUSEL_POSITION_TAG).assert(hasText("Account 2 of 3"))
        shown("ID claude-client")

        // Next, then the end is disabled.
        compose.onNodeWithTag(CAROUSEL_NEXT_TAG).performClick()
        compose.waitForIdle()
        compose.onNodeWithTag(CAROUSEL_POSITION_TAG).assert(hasText("Account 3 of 3"))
        compose.onNodeWithTag(CAROUSEL_NEXT_TAG).assertIsNotEnabled()

        // Previous.
        compose.onNodeWithTag(CAROUSEL_PREVIOUS_TAG).performClick()
        compose.waitForIdle()
        compose.onNodeWithTag(CAROUSEL_POSITION_TAG).assert(hasText("Account 2 of 3"))

        // System Back returns to the Overview first.
        compose.runOnUiThread { compose.activity.onBackPressedDispatcher.onBackPressed() }
        compose.waitForIdle()
        compose.onNodeWithTag(CAROUSEL_POSITION_TAG).assert(hasText("Overview · swipe left for each account"))

        // Swipe.
        compose.onNodeWithTag(CAROUSEL_PAGER_TAG).performTouchInput { swipeLeft() }
        compose.waitForIdle()
        compose.onNodeWithTag(CAROUSEL_POSITION_TAG).assert(hasText("Account 1 of 3"))

        // ‹ Overview.
        compose.onNodeWithTag(CAROUSEL_OVERVIEW_TAG).performClick()
        compose.waitForIdle()
        compose.onNodeWithTag(CAROUSEL_POSITION_TAG).assert(hasText("Overview · swipe left for each account"))
    }

    @Test
    fun heightAndTheCardBelowDoNotMoveBetweenPages() {
        show(CarouselFixtures.threeAccounts, belowTag = "below")
        val tops = mutableListOf<Float>()
        val heights = mutableListOf<Float>()
        for (page in 0..3) {
            if (page > 0) {
                compose.onNodeWithTag(CAROUSEL_NEXT_TAG).performClick()
                compose.waitForIdle()
            }
            heights += compose.onNodeWithTag(CAROUSEL_TAG).getBoundsInRoot().let { (it.bottom - it.top).value }
            tops += compose.onNodeWithTag("below").getBoundsInRoot().top.value
        }
        assertTrue("carousel heights differ: $heights", heights.distinct().size == 1)
        assertTrue("card below moved: $tops", tops.distinct().size == 1)
    }

    @Test
    fun resetCreditsAndTheirExpiriesAreSeparateFromWindowResets() {
        show(CarouselFixtures.threeAccounts)
        compose.onNodeWithTag(CAROUSEL_NEXT_TAG).performClick()
        compose.waitForIdle()
        // Personal reports credits with deadlines; the weekly has its own reset line.
        compose.onAllNodesWithText("Banked quota resets", substring = true).fetchSemanticsNodes().let {
            assertTrue("credit row missing", it.isNotEmpty())
        }
        // The count, then every credit's own deadline, labelled as such.
        shown("2 available")
        shown("Credit deadlines:", substring = true)
        shown("Resets in 3d 4h")
        // Client does not report credits: said, not blank.
        compose.onNodeWithTag(CAROUSEL_NEXT_TAG).performClick()
        compose.waitForIdle()
        shown("Not reported")
        if (hold) Thread.sleep(15_000)
    }

    @Test
    fun talkBackActionsMoveBetweenPages() {
        show(CarouselFixtures.threeAccounts)
        // The actions live on the position label, a node TalkBack focuses
        // (it has a description), so they appear in TalkBack's actions menu.
        val label = compose.onNodeWithTag(CAROUSEL_POSITION_TAG)
        assertTrue(label.fetchSemanticsNode().config.contains(SemanticsProperties.ContentDescription))
        val actions = label.fetchSemanticsNode().config[SemanticsActions.CustomActions].map { it.label }
        assertEquals(listOf("Next account"), actions)
        val next = label.fetchSemanticsNode().config[SemanticsActions.CustomActions].first()
        compose.runOnUiThread { next.action() }
        compose.waitForIdle()
        val onPage1 = label.fetchSemanticsNode().config[SemanticsActions.CustomActions].map { it.label }
        assertEquals(listOf("Overview", "Next account"), onPage1)
        // The position label announces the account by name and plan.
        label.assert(
            SemanticsMatcher.expectValue(
                SemanticsProperties.ContentDescription,
                listOf("Claude account 1 of 3, Personal, Max 20x plan"),
            ),
        )
        // Step buttons are large enough to hit.
        val bounds = compose.onNodeWithTag(CAROUSEL_NEXT_TAG).getBoundsInRoot()
        assertTrue((bounds.right - bounds.left) >= 40.dp && (bounds.bottom - bounds.top) >= 40.dp)
    }

    @Test
    fun largeFontKeepsFullLabelsAndAStableHeight() {
        show(CarouselFixtures.threeAccounts, fontScale = 2f, belowTag = "below")
        shown("Client — Northwind long-running engagement")
        val tops = mutableListOf<Float>()
        for (page in 0..3) {
            if (page > 0) {
                compose.onNodeWithTag(CAROUSEL_NEXT_TAG).performClick()
                compose.waitForIdle()
            }
            tops += compose.onNodeWithTag("below").getBoundsInRoot().top.value
        }
        assertTrue("card below moved at 2x font: $tops", tops.distinct().size == 1)
    }

    @Test
    fun selectedAccountSurvivesLeavingAndReturning() {
        var visible by mutableStateOf(true)
        compose.setContent {
            val selection = rememberAccountSelection()
            if (visible) {
                AccountsCarousel(UsageItem.AccountGroup("claude", CarouselFixtures.threeAccounts), { _, _ -> }, {}, selection)
            }
        }
        compose.onNodeWithTag(CAROUSEL_NEXT_TAG).performClick()
        compose.onNodeWithTag(CAROUSEL_NEXT_TAG).performClick()
        compose.waitForIdle()
        compose.onNodeWithTag(CAROUSEL_POSITION_TAG).assert(hasText("Account 2 of 3"))
        // Like switching to the Queue tab and back.
        visible = false
        compose.waitForIdle()
        visible = true
        compose.waitForIdle()
        compose.onNodeWithTag(CAROUSEL_POSITION_TAG).assert(hasText("Account 2 of 3"))
    }

    @Test
    fun reorderedAccountsKeepTheSelectedAccount() {
        var accounts by mutableStateOf(CarouselFixtures.threeAccounts)
        compose.setContent {
            AccountsCarousel(UsageItem.AccountGroup("claude", accounts), { _, _ -> }, {})
        }
        compose.onNodeWithTag(CAROUSEL_NEXT_TAG).performClick()
        compose.waitForIdle()
        shown("ID claude-personal")
        accounts = accounts.reversed()
        compose.waitForIdle()
        compose.onNodeWithTag(CAROUSEL_POSITION_TAG).assert(hasText("Account 3 of 3"))
        shown("ID claude-personal")
    }

    @Test
    fun singleAccountKeepsTodaysCard() {
        val only = CarouselFixtures.personal.copy(accountIndex = 1, accountCount = 1)
        compose.setContent {
            UsageScreen(state = UsageUiState(view = UsageView(providers = listOf(only))), onRetry = {})
        }
        compose.waitForIdle()
        assertEquals(0, compose.onAllNodesWithTag(CAROUSEL_TAG).fetchSemanticsNodes().size)
        shown("Claude")
        shown("Max 20x")
        shown("Banked quota resets")
    }

    @Test
    fun staleSignedOutAndUsedUpAccountsSayWhy() {
        show(CarouselFixtures.states)
        shown("Needs sign-in")
        shown("Out of date, dispatch held")
        shown("Signed out in OpenUsage", substring = true)
        if (hold) Thread.sleep(15_000)
    }

    @Test
    fun outOfDateAccountShowsLastKnownResetsNotACountdown() {
        show(CarouselFixtures.staleWithCredits)
        // Overview row: "was" values and fixed times, no countdown.
        shown("was 64% left")
        shown("Reset was due", substring = true)
        shown("Last reported reset", substring = true)
        compose.onNodeWithTag(CAROUSEL_NEXT_TAG).performClick()
        compose.waitForIdle()
        // Account page: the full meters say the same, and nothing on the page
        // counts down for this account.
        shown("was 41% left")
        shown("Last reported reset ${fixedTimeLabel(CarouselFixtures.staleWeeklyResetAt)}", substring = true)
        shown("awaiting fresh numbers", substring = true)
        shown("Reset time unknown while out of date")
        shown(OUT_OF_DATE_DISPATCH_LINE)
        // Nothing on screen counts down or claims a fresh value for this account.
        for (live in listOf("Resets in", "Resets ~", "Resets now", "arms in", "Dispatching", "Waiting ·")) {
            val nodes = compose.onAllNodesWithText(live, substring = true, useUnmergedTree = true)
            val visible = (0 until nodes.fetchSemanticsNodes().size).count {
                runCatching { nodes[it].assertIsDisplayed() }.isSuccess
            }
            assertEquals("stale page shows live text '$live'", 0, visible)
        }
        // Credits come from the same old read: labelled last known, the
        // passed deadline said to have expired, kept apart from window resets.
        shown("Last known: 2 available · 1 since expired")
        shown("Credit deadlines: expired", substring = true)
    }

    @Test
    fun largeFontStaleAccountWithCreditsWrapsWithoutClipping() {
        show(CarouselFixtures.staleWithCredits, fontScale = 1.6f, belowTag = "below")
        val before = compose.onNodeWithTag("below").getBoundsInRoot().top
        compose.onNodeWithTag(CAROUSEL_NEXT_TAG).performClick()
        compose.waitForIdle()
        shown("Last known: 2 available · 1 since expired")
        shown("Credit deadlines: expired", substring = true)
        shown("Last reported reset ${fixedTimeLabel(CarouselFixtures.staleWeeklyResetAt)}", substring = true)
        // Every text node, including those inside merged rows, lies within
        // the pager's fixed height and is neither ellipsized nor cut by a line
        // limit. Unclipped bounds (position + size): boundsInRoot is clipped
        // to ancestors and could never fall outside.
        val pagerNode = compose.onNodeWithTag(CAROUSEL_PAGER_TAG).fetchSemanticsNode()
        val pagerTop = pagerNode.positionInRoot.y
        val pagerBottom = pagerTop + pagerNode.size.height
        val pagerLeft = pagerNode.positionInRoot.x
        val pagerRight = pagerLeft + pagerNode.size.width
        compose.onAllNodes(SemanticsMatcher.keyIsDefined(SemanticsProperties.Text), useUnmergedTree = true)
            .fetchSemanticsNodes().forEach { node ->
            val top = node.positionInRoot.y
            val bottom = top + node.size.height
            val left = node.positionInRoot.x
            // Only text on the visible page; neighbours sit a page width away.
            val onVisiblePage = left >= pagerLeft - 1f && left < pagerRight && top >= pagerTop - 1f
            if (onVisiblePage) {
                assertTrue(
                    "below the carousel: ${node.config[SemanticsProperties.Text]} bottom=$bottom pager=$pagerBottom",
                    bottom <= pagerBottom + 1f,
                )
            }
            val layouts = mutableListOf<androidx.compose.ui.text.TextLayoutResult>()
            node.config.getOrElseNullable(SemanticsActions.GetTextLayoutResult) { null }?.action?.invoke(layouts)
            layouts.forEach { layout ->
                val text = node.config[SemanticsProperties.Text]
                // What was drawn, not the paragraph's layout box: no line is
                // ellipsized, no lines were dropped, none spills below, and no
                // drawn line is wider than the space the text was given.
                val lines = 0 until layout.lineCount
                assertTrue("ellipsized: $text", lines.none { layout.isLineEllipsized(it) })
                assertTrue("lines cut: $text", !layout.multiParagraph.didExceedMaxLines)
                assertTrue("spills below: $text", !layout.didOverflowHeight)
                val widest = lines.maxOfOrNull { layout.getLineRight(it) - layout.getLineLeft(it) } ?: 0f
                assertTrue("wider than its box: $text ($widest > ${layout.size.width})", widest <= layout.size.width + 1f)
            }
        }
        assertEquals("card below moved", before, compose.onNodeWithTag("below").getBoundsInRoot().top)
        if (hold) Thread.sleep(15_000)
    }

    @Test
    fun manyAccountsUseAChooserAndListAttentionFirst() {
        show(CarouselFixtures.many)
        shown("Choose account ▾")
        shown("4 more · use › or the account list above")
        click("Choose account ▾")
        click("Research · Pro")
        compose.waitForIdle()
        compose.onNodeWithTag(CAROUSEL_POSITION_TAG).assert(hasText("Account 5 of 7"))
    }

    @Test
    fun teamSeatsShowTheirUsageAndWeeklyNotReported() {
        show(CarouselFixtures.teamSeats)
        // Six accounts, none "needs attention": the seats report fresh usage.
        shown("6 accounts", substring = true)
        assertEquals(0, compose.onAllNodesWithText("need attention", substring = true).fetchSemanticsNodes().size)
        // Overview rows: the weekly cell says not reported, never a number.
        shown("Not reported")
        shown("Dispatch held, weekly not reported")
        click("Echo")
        compose.waitForIdle()
        // Echo's page: the running 5-hour window, Fable, credits 0, and the
        // weekly row saying not reported -- never 76% (the 5-hour value).
        shown("76% left")
        shown("Fable", substring = true)
        shown("not reported")
        shown("Weekly limit not reported for this account", substring = true)
        // The weekly row on this page is "not reported", wherever else the
        // 5-hour value appears (overview row, pager neighbours).
        val weekly = compose.onAllNodes(hasContentDescription("Weekly allowance: not reported for this account"))
        assertTrue("weekly row is not 'not reported'", weekly.fetchSemanticsNodes().isNotEmpty())
        val spoken = weekly.fetchSemanticsNodes().flatMap { it.config.getOrElse(SemanticsProperties.ContentDescription) { emptyList() } }
        assertTrue("weekly row carries a number: $spoken", spoken.none { it.contains("%") })
    }

    /** At least one node with this text is on screen (the pager may hold a neighbour off screen). */
    private fun shown(text: String, substring: Boolean = false) {
        val nodes = compose.onAllNodesWithText(text, substring = substring)
        val count = nodes.fetchSemanticsNodes().size
        val visible = (0 until count).any { index ->
            runCatching { nodes[index].assertIsDisplayed() }.isSuccess
        }
        assertTrue("no displayed node with text '$text' (found $count)", visible)
    }

    private fun click(text: String) {
        val nodes = compose.onAllNodesWithText(text)
        val count = nodes.fetchSemanticsNodes().size
        val index = (0 until count).first { runCatching { nodes[it].assertIsDisplayed() }.isSuccess }
        nodes[index].performClick()
    }

    private fun hasText(text: String) = SemanticsMatcher.expectValue(SemanticsProperties.Text, listOf(androidx.compose.ui.text.AnnotatedString(text)))

    private fun show(accounts: List<ProviderUsage>, fontScale: Float = 1f, belowTag: String? = null) {
        compose.setContent {
            val density = LocalDensity.current
            CompositionLocalProvider(LocalDensity provides Density(density.density, fontScale)) {
                Column {
                    AccountsCarousel(
                        UsageItem.AccountGroup("claude", accounts),
                        onControlProvider = { _, _ -> },
                        onViewQueue = {},
                    )
                    if (belowTag != null) {
                        Spacer(Modifier.height(8.dp))
                        Text("Codex", modifier = Modifier.testTag(belowTag))
                    }
                }
            }
        }
        compose.waitForIdle()
    }
}
