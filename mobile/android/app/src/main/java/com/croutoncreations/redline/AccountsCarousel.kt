package com.croutoncreations.redline

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.defaultMinSize
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.pager.HorizontalPager
import androidx.compose.foundation.pager.PagerState
import androidx.compose.foundation.pager.rememberPagerState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.layout.SubcomposeLayout
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.CustomAccessibilityAction
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.customActions
import androidx.compose.ui.semantics.disabled
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.onClick
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.launch

internal const val CAROUSEL_TAG = "accounts-carousel"
internal const val CAROUSEL_PAGER_TAG = "accounts-pager"
internal const val CAROUSEL_PREVIOUS_TAG = "accounts-previous"
internal const val CAROUSEL_NEXT_TAG = "accounts-next"
internal const val CAROUSEL_OVERVIEW_TAG = "accounts-overview"
internal const val CAROUSEL_POSITION_TAG = "accounts-position"

/** Above this many accounts the dots become a chooser and the Overview shows the first few. */
private const val MAX_DOTS = 5
private const val OVERVIEW_ROWS_WHEN_MANY = 3

/** Dark red for a used-up account's dot: still visible, clearly not the current page. */
private val ExhaustedDot = Color(0xFF7A2A28)

/**
 * One provider's accounts as a single carousel in the provider's tile.
 *
 * Page 0 is the Overview, one row per account; pages 1..N are each account's
 * full detail, laid out exactly like the single-account card. The header and
 * the pager strip stay put on every page, and the pager's height is the
 * tallest page's, so swiping never moves the cards below. Numbers are never
 * combined across accounts: each row and page is one account's own.
 *
 * Opens on the Overview. The page is remembered by account id for the rest of
 * the session (across tab switches), so a reordered list does not land on a
 * different account.
 */
@Composable
internal fun AccountsCarousel(
    group: UsageItem.AccountGroup,
    onControlProvider: (String, String) -> Unit,
    onViewQueue: (String) -> Unit,
    selection: AccountSelection = rememberAccountSelection(),
) {
    val accounts = group.accounts
    val pageCount = accounts.size + 1
    // The selection is an account id ("" for the Overview), held above the
    // screen so it outlives tab switches, and resolved to a page here so a
    // reordered or changed list never lands on a different account.
    val selectedId = selection.selected(group.provider)
    val pageOfSelection = accounts.indexOfFirst { it.id == selectedId }.let { if (it < 0) 0 else it + 1 }
    val pagerState = rememberPagerState(initialPage = pageOfSelection) { pageCount }
    val scope = rememberCoroutineScope()
    val goTo: (Int) -> Unit = { page ->
        scope.launch { pagerState.animateScrollToPage(page.coerceIn(0, pageCount - 1)) }
    }
    val ids = accounts.map { it.id }
    // When the list changes under the pager, follow the selected account.
    LaunchedEffect(ids) {
        if (pagerState.currentPage != pageOfSelection) pagerState.scrollToPage(pageOfSelection)
    }
    LaunchedEffect(pagerState, ids) {
        snapshotFlow { pagerState.settledPage to pagerState.isScrollInProgress }.collect { (page, scrolling) ->
            if (!scrolling) selection.select(group.provider, if (page == 0) "" else ids.getOrNull(page - 1).orEmpty())
        }
    }
    // Back on an account page returns to the Overview before leaving.
    BackHandler(enabled = pagerState.currentPage > 0) { goTo(0) }

    val page = pagerState.currentPage
    Card(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 12.dp)
            .testTag(CAROUSEL_TAG),
        colors = CardDefaults.cardColors(containerColor = Panel),
        shape = RoundedCornerShape(12.dp),
    ) {
        Column(modifier = Modifier.padding(top = 14.dp, start = 14.dp, end = 14.dp, bottom = 14.dp)) {
            Text(
                accountGroupSummary(group.provider, accounts),
                color = TextPrimary,
                fontWeight = FontWeight.SemiBold,
                fontSize = 16.sp,
                modifier = Modifier.semantics { heading() },
            )
            Spacer(Modifier.height(8.dp))
            PagerStrip(group.provider, accounts, pagerState, goTo)
            Spacer(Modifier.height(10.dp))
            FixedHeightPager(pagerState, pageCount, ids) { index ->
                if (index == 0) {
                    OverviewPage(accounts, onOpen = { goTo(it) })
                } else {
                    AccountPage(group.provider, accounts[index - 1], onControlProvider, onViewQueue)
                }
            }
        }
    }
}

/**
 * A HorizontalPager whose height is the tallest page's, measured at the real
 * width and font scale, so the tile is the same height on every page.
 *
 * Every page is measured but only the pager is placed, so the measuring
 * copies are never drawn, never touchable and absent from accessibility.
 */
@Composable
private fun FixedHeightPager(
    state: PagerState,
    pageCount: Int,
    pageKeys: List<String>,
    page: @Composable (Int) -> Unit,
) {
    SubcomposeLayout(Modifier.fillMaxWidth()) { constraints ->
        val loose = Constraints(minWidth = constraints.maxWidth, maxWidth = constraints.maxWidth)
        var tallest = 0
        for (index in 0 until pageCount) {
            // clearAndSetSemantics: the measuring copy must not reach
            // TalkBack, or every page would be read twice.
            subcompose("measure-$index") {
                Box(Modifier.clearAndSetSemantics {}) { page(index) }
            }.forEach { measurable ->
                tallest = maxOf(tallest, measurable.measure(loose).height)
            }
        }
        val fixed = Constraints.fixed(constraints.maxWidth, tallest)
        val pager = subcompose("pager") {
            HorizontalPager(
                state = state,
                modifier = Modifier.testTag(CAROUSEL_PAGER_TAG),
                verticalAlignment = Alignment.Top,
                // Each page gets a little air so a swipe shows the edge of the next.
                pageSpacing = 16.dp,
                key = { if (it == 0) "overview" else pageKeys.getOrElse(it - 1) { "page-$it" } },
            ) { index ->
                Box(Modifier.fillMaxSize(), contentAlignment = Alignment.TopStart) { page(index) }
            }
        }.map { it.measure(fixed) }
        layout(constraints.maxWidth, tallest) { pager.forEach { it.place(0, 0) } }
    }
}

/**
 * The strip that never moves: the Overview control, a dot per page, previous
 * and next, and the position in words. Position is never told by colour alone.
 */
@Composable
private fun PagerStrip(
    provider: String,
    accounts: List<ProviderUsage>,
    state: PagerState,
    goTo: (Int) -> Unit,
) {
    val page = state.currentPage
    val last = accounts.size
    Column {
        Row(verticalAlignment = Alignment.CenterVertically) {
            val overviewLabel = if (page == 0) "Overview" else "‹ Overview"
            // A 48dp touch target around a compact pill.
            Box(
                contentAlignment = Alignment.Center,
                modifier = Modifier
                    .testTag(CAROUSEL_OVERVIEW_TAG)
                    .defaultMinSize(minHeight = 48.dp)
                    .clip(RoundedCornerShape(12.dp))
                    .clickable(enabled = page != 0, role = Role.Button, onClickLabel = "Show overview") { goTo(0) }
                    .semantics(mergeDescendants = true) {
                        contentDescription = "Overview of all ${providerDisplayName(provider)} accounts"
                        if (page == 0) stateDescription = "Showing"
                    },
            ) {
                Text(
                    overviewLabel,
                    color = if (page == 0) TextPrimary else Accent,
                    fontSize = 12.sp,
                    fontWeight = FontWeight.Medium,
                    modifier = Modifier
                        .clip(RoundedCornerShape(12.dp))
                        .background(if (page == 0) Line else AccentSoft)
                        .padding(horizontal = 10.dp, vertical = 6.dp),
                )
            }
            Spacer(Modifier.width(10.dp))
            Box(Modifier.weight(1f)) {
                if (accounts.size <= MAX_DOTS) {
                    Dots(accounts, page)
                } else {
                    AccountChooser(accounts, page, goTo)
                }
            }
            StepButton("‹", if (page == 1) "Overview" else "Previous account", enabled = page > 0, tag = CAROUSEL_PREVIOUS_TAG) { goTo(page - 1) }
            StepButton("›", "Next account", enabled = page < last, tag = CAROUSEL_NEXT_TAG) { goTo(page + 1) }
        }
        // Built from the page and the account's name and plan only, so a live
        // update to usage or dispatch state does not re-announce the page.
        val current = accounts.getOrNull(page - 1)
        val announcement = remember(page, current?.id, current?.accountName, current?.plan, accounts.size) {
            if (current == null) {
                "${providerDisplayName(provider)} overview, ${accounts.size} accounts"
            } else {
                accountAnnouncement(provider, current, page, accounts.size)
            }
        }
        Text(
            carouselPositionLabel(page, accounts.size),
            color = TextMuted,
            fontSize = 11.sp,
            modifier = Modifier
                .testTag(CAROUSEL_POSITION_TAG)
                // The one focusable node that names the position carries the
                // page actions, so TalkBack offers them where they apply.
                .semantics {
                    contentDescription = announcement
                    liveRegion = LiveRegionMode.Polite
                    customActions = buildList {
                        if (page > 0) add(CustomAccessibilityAction("Overview") { goTo(0); true })
                        if (page > 1) add(CustomAccessibilityAction("Previous account") { goTo(page - 1); true })
                        if (page < last) add(CustomAccessibilityAction("Next account") { goTo(page + 1); true })
                    }
                },
        )
    }
}

@Composable
private fun Dots(accounts: List<ProviderUsage>, page: Int) {
    // Decorative: the position label beside it says the same in words.
    Row(
        horizontalArrangement = Arrangement.spacedBy(5.dp),
        verticalAlignment = Alignment.CenterVertically,
        modifier = Modifier.clearAndSetSemantics {},
    ) {
        Box(
            Modifier
                .width(14.dp)
                .height(6.dp)
                .clip(RoundedCornerShape(3.dp))
                .background(if (page == 0) Accent else Line),
        )
        accounts.forEachIndexed { index, account ->
            val current = page == index + 1
            Box(
                Modifier
                    .size(6.dp)
                    .clip(CircleShape)
                    .background(
                        when {
                            current -> Accent
                            account.exhausted -> ExhaustedDot
                            else -> Line
                        },
                    ),
            )
        }
    }
}

/** For many accounts: "Account 3 of 7 ▾", opening a list of every account. */
@Composable
private fun AccountChooser(accounts: List<ProviderUsage>, page: Int, goTo: (Int) -> Unit) {
    var open by remember { mutableStateOf(false) }
    Box {
        Text(
            (if (page == 0) "Choose account" else "Account $page of ${accounts.size}") + " ▾",
            color = TextPrimary,
            fontSize = 12.sp,
            modifier = Modifier
                .clickable(role = Role.Button, onClickLabel = "Choose an account") { open = true }
                .semantics { contentDescription = if (page == 0) "Choose account" else "Account $page of ${accounts.size}, choose another" }
                .defaultMinSize(minHeight = 40.dp)
                .padding(horizontal = 6.dp, vertical = 10.dp),
        )
        DropdownMenu(expanded = open, onDismissRequest = { open = false }, modifier = Modifier.background(PanelRaised)) {
            accounts.forEachIndexed { index, account ->
                DropdownMenuItem(
                    text = {
                        Text(
                            account.accountName + if (account.plan.isNotBlank()) " · ${account.plan}" else "",
                            color = TextPrimary,
                            fontSize = 14.sp,
                        )
                    },
                    onClick = {
                        open = false
                        goTo(index + 1)
                    },
                )
            }
        }
    }
}

@Composable
private fun StepButton(glyph: String, label: String, enabled: Boolean, tag: String, onClick: () -> Unit) {
    // 48dp: the touch target Android asks for, above the 40dp floor.
    Box(
        contentAlignment = Alignment.Center,
        modifier = Modifier
            .size(48.dp)
            .testTag(tag)
            .clip(CircleShape)
            .clickable(enabled = enabled, role = Role.Button, onClick = onClick)
            .clearAndSetSemantics {
                contentDescription = label
                role = Role.Button
                if (!enabled) disabled()
                if (enabled) onClick(label) { onClick(); true }
            },
    ) {
        Text(glyph, color = if (enabled) TextPrimary else Line, fontSize = 22.sp)
    }
}

/**
 * One row per account. Each is a button to that account's page. The 5-hour
 * and weekly windows stay separate, each with its own reset; nothing is
 * totalled across accounts.
 */
@Composable
private fun OverviewPage(accounts: List<ProviderUsage>, onOpen: (Int) -> Unit) {
    // Attention first when there are too many to list: signed out, stale and
    // used up before the healthy ones. Otherwise the desktop's order.
    val shown = if (accounts.size > MAX_DOTS) {
        accounts.withIndex()
            .sortedBy { (_, account) -> attentionRank(account) }
            .take(OVERVIEW_ROWS_WHEN_MANY)
    } else {
        accounts.withIndex().toList()
    }
    Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
        shown.forEach { (index, account) ->
            OverviewRow(account) { onOpen(index + 1) }
        }
        if (shown.size < accounts.size) {
            Text(
                "${accounts.size - shown.size} more · use › or the account list above",
                color = TextMuted,
                fontSize = 11.sp,
            )
        }
    }
}

private fun attentionRank(account: ProviderUsage): Int = when {
    account.accountStatus == "signed_out" || account.accountStatus == "error" -> 0
    account.stale || account.accountStatus == "stale" -> 1
    account.exhausted -> 2
    account.paused -> 3
    else -> 4
}

@Composable
private fun OverviewRow(account: ProviderUsage, onOpen: () -> Unit) {
    val state = accountDispatchState(account)
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(10.dp))
            .background(PanelRaised)
            .clickable(role = Role.Button, onClickLabel = "Open details for ${account.accountName}", onClick = onOpen)
            .padding(10.dp),
    ) {
        // The alias has the full width to itself, so a long one wraps at
        // word boundaries at any font size instead of being squeezed beside
        // the plan and the link.
        Text(account.accountName, color = TextPrimary, fontSize = 14.sp, fontWeight = FontWeight.Medium)
        Spacer(Modifier.height(2.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            if (account.plan.isNotBlank()) PlanChip(account.plan)
            Spacer(Modifier.weight(1f))
            Text("Details ›", color = Accent, fontSize = 12.sp, modifier = Modifier.clearAndSetSemantics {})
        }
        Spacer(Modifier.height(6.dp))
        Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
            val session = account.session
            val old = account.isOutOfDate
            when {
                session != null -> MiniMeter("5-hour", session, Modifier.weight(1f), old)
                account.sessionUnknown -> MiniUnknown("5-hour", Modifier.weight(1f))
            }
            val weekly = account.weekly
            if (weekly != null) {
                MiniMeter(account.weeklyLabel.ifEmpty { "Weekly" }.removeSuffix(" allowance"), weekly, Modifier.weight(1f), old)
            } else if (account.weeklyNotReported) {
                MiniUnknown("Weekly", Modifier.weight(1f), "Not reported")
            } else {
                MiniUnknown("Weekly", Modifier.weight(1f), "No usage reported")
            }
        }
        Spacer(Modifier.height(6.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            Box(Modifier.size(6.dp).clip(CircleShape).background(dispatchTone(account)))
            Spacer(Modifier.width(6.dp))
            Text(state.replaceFirstChar { it.uppercase() }, color = TextMuted, fontSize = 11.sp)
        }
        FiresaleMarker(account.id)
        val problem = providerProblem(account)
        if (problem.isNotEmpty()) {
            Spacer(Modifier.height(4.dp))
            Text(problem, color = Danger, fontSize = 11.sp)
        }
    }
}

private fun dispatchTone(account: ProviderUsage): Color = when {
    account.accountStatus == "signed_out" || account.accountStatus == "error" -> Danger
    account.paused || account.stale -> Warn
    account.dispatching -> Good
    else -> TextMuted
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun MiniMeter(label: String, window: Window, modifier: Modifier, outOfDate: Boolean = false) {
    val reset = if (outOfDate) outOfDateResetSentence(window) else windowResetSentence(window)
    Column(modifier.semantics(mergeDescendants = true) {
        contentDescription = if (outOfDate) {
            "$label, last known and out of date: was ${window.remainingPercent} percent left. $reset"
        } else {
            "$label: ${window.remainingPercent} percent left. $reset"
        }
    }) {
        // Wraps instead of squeezing: at large font sizes the value moves
        // under the label rather than running into it.
        FlowRow(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            Text(label, color = TextMuted, fontSize = 11.sp)
            Text(
                (if (outOfDate) "was " else "") + "${window.remainingPercent}% left",
                color = if (outOfDate) TextMuted else TextPrimary,
                fontSize = 11.sp,
                fontFamily = FontFamily.Monospace,
                maxLines = 1,
                softWrap = false,
            )
        }
        Spacer(Modifier.height(3.dp))
        LinearProgressIndicator(
            progress = { window.remainingPercent / 100f },
            modifier = Modifier.fillMaxWidth().height(4.dp).clip(RoundedCornerShape(2.dp)),
            // Out-of-date numbers are drawn muted so they never pass for live ones.
            color = if (outOfDate) TextMuted.copy(alpha = 0.5f) else toneFor(window.remainingPercent),
            trackColor = Line,
        )
        Spacer(Modifier.height(2.dp))
        Text(reset, color = TextMuted, fontSize = 10.sp)
    }
}

/** A window that exists but has no number right now: a flat track, never an empty bar. */
@Composable
private fun MiniUnknown(label: String, modifier: Modifier, detail: String = "Not available") {
    Column(modifier.semantics(mergeDescendants = true) { contentDescription = "$label: ${detail.lowercase()}" }) {
        Row {
            Text(label, color = TextMuted, fontSize = 11.sp)
            Spacer(Modifier.weight(1f))
            Text("—", color = TextMuted, fontSize = 11.sp, fontFamily = FontFamily.Monospace)
        }
        Spacer(Modifier.height(3.dp))
        Box(Modifier.fillMaxWidth().height(4.dp).clip(RoundedCornerShape(2.dp)).background(Line))
        Spacer(Modifier.height(2.dp))
        Text(detail, color = TextMuted, fontSize = 10.sp)
    }
}

/** One account's full detail, the same as a single-account card's body. */
@Composable
private fun AccountPage(
    provider: String,
    account: ProviderUsage,
    onControlProvider: (String, String) -> Unit,
    onViewQueue: (String) -> Unit,
) {
    Column(Modifier.fillMaxWidth()) {
        Text(
            account.accountName,
            color = TextPrimary,
            fontSize = 15.sp,
            fontWeight = FontWeight.SemiBold,
            modifier = Modifier.semantics { heading() },
        )
        Spacer(Modifier.height(2.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            if (account.plan.isNotBlank()) {
                PlanChip(account.plan)
                Spacer(Modifier.width(6.dp))
            }
            // Redline's own id for the account, never an email.
            Text("ID ${account.id}", color = TextMuted, fontSize = 11.sp, fontFamily = FontFamily.Monospace)
        }
        // Dispatch state on its own line, apart from usage, so neither is
        // squeezed at large font sizes.
        Text(
            accountDispatchState(account).replaceFirstChar { it.uppercase() },
            color = dispatchTone(account),
            fontSize = 12.sp,
        )
        ProviderDetails(account, onControlProvider, onViewQueue, accountPage = true)
    }
}

/**
 * Which page each provider's carousel shows, by account id ("" = Overview).
 * Held by the activity so it survives tab switches and the accounts screen;
 * saved across rotation. A fresh start opens on the Overview.
 */
class AccountSelection internal constructor(private val state: androidx.compose.runtime.MutableState<Map<String, String>>) {
    fun selected(provider: String): String = state.value[provider].orEmpty()
    fun select(provider: String, accountId: String) {
        if (state.value[provider].orEmpty() != accountId) state.value = state.value + (provider to accountId)
    }
}

@Composable
fun rememberAccountSelection(): AccountSelection {
    val state = rememberSaveable(
        saver = androidx.compose.runtime.saveable.Saver<androidx.compose.runtime.MutableState<Map<String, String>>, List<String>>(
            save = { it.value.flatMap { (k, v) -> listOf(k, v) } },
            restore = { flat -> mutableStateOf(flat.chunked(2).associate { it[0] to it[1] }) },
        ),
    ) { mutableStateOf(emptyMap<String, String>()) }
    return remember(state) { AccountSelection(state) }
}
