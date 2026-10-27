package com.croutoncreations.redline

import androidx.compose.foundation.background
import androidx.compose.foundation.Image
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width

import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.IconButton
import androidx.compose.material3.TextButton
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.getValue
import androidx.compose.runtime.setValue
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.Dp
import androidx.compose.foundation.layout.BoxWithConstraintsScope
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.tooling.preview.Preview
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

@Composable
fun UsageScreen(
    state: UsageUiState,
    onRetry: () -> Unit,
    onUnpair: (() -> Unit)? = null,
    onControlProvider: (String, String) -> Unit = { _, _ -> },
    onViewQueue: (String) -> Unit = {},
    onOpenAccounts: (() -> Unit)? = null,
    accountSelection: AccountSelection? = null,
    onOpenSettings: (() -> Unit)? = null,
) {
    Surface(color = Background, modifier = Modifier.fillMaxSize()) {
        Column(modifier = Modifier.fillMaxSize()) {
            Header(state, onUnpair, onOpenAccounts, onOpenSettings)
            when {
                // Only show a spinner with nothing behind it on a cold start;
                // a refresh over existing data should not blank the screen.
                state.loading && !state.hasData -> CenteredProgress()
                !state.hasData && state.failure != null -> FailureMessage(state.failure, onRetry)
                state.view != null -> RefreshableProviderList(
                    state,
                    onRetry,
                    onControlProvider,
                    onViewQueue,
                    accountSelection ?: rememberAccountSelection(),
                )
                else -> CenteredProgress()
            }
        }
    }
}

/**
 * The provider list, refreshable by pulling down.
 *
 * The screen already refreshes on resume and streams live updates, but a pull
 * is the gesture people reach for when they want to know the number in front of
 * them is current -- and after a failed refresh, when "Offline" is showing over
 * stale data, it is the obvious way to ask again.
 *
 * The spinner is driven by the same loading flag as every other refresh, so a
 * pull cannot show a spinner that outlives the request or hide one that is
 * still running.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun RefreshableProviderList(
    state: UsageUiState,
    onRefresh: () -> Unit,
    onControlProvider: (String, String) -> Unit,
    onViewQueue: (String) -> Unit,
    accountSelection: AccountSelection,
) {
    PullToRefreshBox(
        isRefreshing = state.loading,
        onRefresh = onRefresh,
        modifier = Modifier.fillMaxSize(),
    ) {
        ProviderList(state, onControlProvider, onViewQueue, accountSelection)
    }
}

@Composable
private fun Header(state: UsageUiState, onUnpair: (() -> Unit)? = null, onOpenAccounts: (() -> Unit)? = null, onOpenSettings: (() -> Unit)? = null) {
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .background(Panel)
            .padding(horizontal = 16.dp, vertical = 14.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Image(
                painter = painterResource(R.drawable.redline_mark),
                // The wordmark beside it already names the app, so announcing
                // this too would just make a screen reader say it twice.
                contentDescription = null,
                modifier = Modifier.height(17.dp),
            )
            Spacer(Modifier.width(8.dp))
            Text(
                "REDLINE",
                color = TextPrimary,
                fontWeight = FontWeight.Bold,
                fontSize = 16.sp,
                letterSpacing = 1.sp,
            )
            Spacer(Modifier.weight(1f))
            // A degraded scheduler explains why nothing is dispatching, so it
            // outranks the connection status for space in the header.
            state.view?.health?.takeIf { it.degraded }?.let { health ->
                Text(health.status, color = Warn, fontSize = 11.sp)
                Spacer(Modifier.width(10.dp))
            }
            ConnectionPill(state.connection, state.view?.sampledAgeSeconds ?: 0)
            // Unpairing lives behind the overflow rather than on the surface:
            // it is rare, destructive, and next to controls people press often.
            if (onUnpair != null || onOpenAccounts != null || onOpenSettings != null) {
                Spacer(Modifier.width(4.dp))
                UnpairMenu(onUnpair = onUnpair, onOpenAccounts = onOpenAccounts, onOpenSettings = onOpenSettings)
            }
        }
        Spacer(Modifier.height(2.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("Capacity", color = TextMuted, fontSize = 12.sp)
            // Worth saying out loud. A relayed session is slower, costs money,
            // and crosses a third party, so someone seeing an unexpected
            // "relayed" here has a real reason to check why the direct route
            // is not working. Derived from the same status as the pill, so
            // the two lines of the header cannot disagree.
            if (state.connection == ConnectionStatus.RELAYED) {
                Text(" · relayed", color = Warn, fontSize = 12.sp)
            }
            state.view?.health?.takeIf { it.degraded && it.detail.isNotBlank() }?.let {
                Text(" · ${it.detail}", color = Warn, fontSize = 12.sp)
            }
        }
    }
}

/**
 * Says whether the numbers are updating themselves.
 *
 * Without this, a stalled stream and a live one look identical, and the user
 * has no way to know whether what they are reading is current.
 */
/**
 * The overflow menu holding "Unpair this device".
 *
 * Confirmation is not ceremony here. The pairing token is single use, so a
 * phone that unpairs by accident cannot undo it from the phone: recovering
 * means walking back to the Mac and running `redline pair --qr` again. The
 * dialog says that, because "are you sure?" on its own tells someone nothing
 * they can weigh.
 */
@Composable
private fun UnpairMenu(onUnpair: (() -> Unit)?, onOpenAccounts: (() -> Unit)? = null, onOpenSettings: (() -> Unit)? = null) {
    var menuOpen by remember { mutableStateOf(false) }
    var confirming by remember { mutableStateOf(false) }

    Box {
        IconButton(
            onClick = { menuOpen = true },
            modifier = Modifier.size(28.dp).semantics {
                contentDescription = "More options"
            },
        ) {
            Text("\u22EE", color = TextMuted, fontSize = 18.sp)
        }
        DropdownMenu(
            expanded = menuOpen,
            onDismissRequest = { menuOpen = false },
            modifier = Modifier.background(PanelRaised),
        ) {
            onOpenAccounts?.let { open ->
                DropdownMenuItem(
                    text = { Text("Accounts", color = TextPrimary, fontSize = 14.sp) },
                    onClick = {
                        menuOpen = false
                        open()
                    },
                )
            }
            onOpenSettings?.let { open ->
                DropdownMenuItem(
                    text = { Text("Settings", color = TextPrimary, fontSize = 14.sp) },
                    onClick = { menuOpen = false; open() },
                )
            }
            if (onUnpair != null) {
                DropdownMenuItem(
                    text = { Text("Unpair this device", color = TextPrimary, fontSize = 14.sp) },
                    onClick = {
                        menuOpen = false
                        confirming = true
                    },
                )
            }
        }
    }

    if (confirming) {
        AlertDialog(
            onDismissRequest = { confirming = false },
            containerColor = PanelRaised,
            title = { Text("Unpair this device?", color = TextPrimary) },
            text = {
                Text(
                    "This phone will forget its Redline credential and return to " +
                        "the pairing screen. To use it again, run redline pair --qr " +
                        "on your Mac and scan the new code.",
                    color = TextMuted,
                    fontSize = 13.sp,
                )
            },
            confirmButton = {
                TextButton(onClick = {
                    confirming = false
                    onUnpair?.invoke()
                }) {
                    Text("Unpair", color = Danger)
                }
            },
            dismissButton = {
                TextButton(onClick = { confirming = false }) {
                    Text("Cancel", color = TextMuted)
                }
            },
        )
    }
}

/**
 * How stale the numbers may be before the pill says so.
 *
 * The collector polls every five minutes, so anything under that is the normal
 * resting state and calling it out would train the user to ignore the label.
 * Past it, the numbers are older than one collection cycle and worth naming.
 */
private const val STALE_AFTER_SECONDS = 300

@Composable
private fun ConnectionPill(connection: ConnectionStatus, sampledAgeSeconds: Int) {
    // "live" describes the connection, not the numbers, and the two move at
    // very different speeds: frames arrive every few seconds while the provider
    // is sampled every few minutes. Saying only "live" over minutes-old numbers
    // reads as a claim about the data, which is how it was misread.
    if (connection == ConnectionStatus.LIVE && sampledAgeSeconds >= STALE_AFTER_SECONDS) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Box(
                Modifier
                    .size(6.dp)
                    .clip(CircleShape)
                    .background(Warn),
            )
            Spacer(Modifier.width(6.dp))
            Text(
                "live · sampled ${sampledAgeSeconds / 60}m ago",
                color = Warn,
                fontSize = 12.sp,
            )
        }
        return
    }

    val (label, tone) = when (connection) {
        ConnectionStatus.LIVE -> "live" to Good
        ConnectionStatus.CONNECTING -> "connecting" to TextMuted
        ConnectionStatus.RECONNECTING -> "reconnecting" to Warn
        // Named for the route rather than the cadence: "relayed" tells the user
        // why updates are slower and that it is expected, where "polling" would
        // describe the symptom and leave the cause a mystery.
        ConnectionStatus.RELAYED -> "relayed" to Warn
        // Showing cached numbers without saying so would be misleading.
        ConnectionStatus.STALE -> "offline" to Warn
        // The body carries the failure and the retry; a second copy here
        // would only compete with it.
        ConnectionStatus.UNREACHABLE, ConnectionStatus.NONE -> return
    }
    Row(verticalAlignment = Alignment.CenterVertically) {
        Box(
            Modifier
                .size(6.dp)
                .clip(CircleShape)
                .background(tone),
        )
        Spacer(Modifier.width(6.dp))
        Text(label, color = tone, fontSize = 11.sp)
    }
}

@Composable
private fun CenteredProgress() {
    Box(modifier = Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        CircularProgressIndicator(color = Accent)
    }
}

@Composable
private fun FailureMessage(failure: UsageUiState.Failure, onRetry: () -> Unit) {
    val message = when (failure) {
        UsageUiState.Failure.UNAUTHORIZED ->
            "Redline rejected this device's credentials. Pair it again from the desktop app."
        UsageUiState.Failure.UNREACHABLE ->
            "Cannot reach Redline. Check that the desktop app is running and on the same network."
        // Names the remedy. "Cannot reach Redline" would send someone to look
        // at their network when the desktop is fine and the subscription is not.
        UsageUiState.Failure.ENTITLEMENT_REFUSED ->
            "Remote access needs a current subscription. Renew it to reach Redline from " +
                "outside your tailnet; on your own network the app still works."
        // The Mac, not the phone, is the thing to check: the relay itself and
        // this phone's own connection may both be fine.
        UsageUiState.Failure.HOST_OFFLINE ->
            "Your Mac is not connected to the relay right now. Check that Redline is " +
                "running there, or connect over your tailnet instead."
        // The desktop and subscription are both fine here; another phone is
        // the thing occupying the one slot the relay currently allows.
        UsageUiState.Failure.TOO_MANY_PHONES ->
            "Too many phones are already connected to this Mac through the relay. " +
                "Disconnect one, or connect over your tailnet instead."
    }
    Box(modifier = Modifier.fillMaxSize().padding(24.dp), contentAlignment = Alignment.Center) {
        Column(horizontalAlignment = Alignment.CenterHorizontally) {
            Text(message, color = TextMuted, fontSize = 14.sp)
            Spacer(Modifier.height(16.dp))
            Text(
                "Tap to retry",
                color = Accent,
                fontSize = 14.sp,
                modifier = Modifier
                    .clip(RoundedCornerShape(8.dp))
                    .background(Panel)
                    .clickable(onClick = onRetry)
                    .padding(horizontal = 16.dp, vertical = 10.dp)
                    .semantics { contentDescription = "Retry loading usage" },
            )
        }
    }
}

@Composable
private fun ProviderList(
    state: UsageUiState,
    onControlProvider: (String, String) -> Unit,
    onViewQueue: (String) -> Unit,
    accountSelection: AccountSelection,
) {
    val providers = state.view?.providers ?: emptyList()
    LazyColumn(
        contentPadding = PaddingValues(vertical = 8.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        // A provider with several accounts becomes one carousel at its first
        // account's position; a lone account keeps the card it always had.
        items(usageItems(providers), key = { it.key }) { item ->
            when (item) {
                is UsageItem.Single -> ProviderCard(item.provider, onControlProvider, onViewQueue)
                is UsageItem.AccountGroup -> AccountsCarousel(item, onControlProvider, onViewQueue, accountSelection)
            }
        }
        // Cursor's usage card follows the accounts. It has no controls:
        // nothing on it can be paused or queued from here.
        state.view?.cursor?.let { cursor ->
            item(key = "cursor-usage") { CursorUsageCard(cursor) }
        }
    }
}

/**
 * Cursor's monthly pools, Grok Bot's weekly allowance, on-demand spend and
 * credits, each in its own group. Bars show what is used, matching the web
 * and Mac cards, and are drawn only where Cursor gave a limit.
 */
@Composable
internal fun CursorUsageCard(cursor: CursorUsage) {
    Card(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 12.dp)
            .semantics { contentDescription = "Cursor usage, ${cursor.state}. ${cursor.note}" },
        colors = CardDefaults.cardColors(containerColor = Panel),
        shape = RoundedCornerShape(12.dp),
    ) {
        Column(modifier = Modifier.padding(14.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("Cursor", color = TextPrimary, fontWeight = FontWeight.SemiBold, fontSize = 16.sp)
                if (cursor.plan.isNotBlank()) {
                    Spacer(Modifier.width(6.dp))
                    Text(cursor.plan, color = TextMuted, fontSize = 11.sp, fontFamily = FontFamily.Monospace)
                }
                Spacer(Modifier.weight(1f))
                Text(
                    cursor.state,
                    color = when (cursor.tone) {
                        "healthy" -> Good
                        "near" -> Warn
                        else -> TextMuted
                    },
                    fontSize = 12.sp,
                )
            }
            Spacer(Modifier.height(4.dp))
            Text(cursor.updatedLabel, color = TextMuted, fontSize = 11.sp)
            Text(cursor.note, color = TextMuted, fontSize = 11.sp)
            cursor.errors.forEach { error ->
                Spacer(Modifier.height(4.dp))
                Text(error, color = Danger, fontSize = 11.sp)
            }
            if (cursor.groups.isEmpty() && cursor.balances.isEmpty()) {
                Spacer(Modifier.height(10.dp))
                Text("Cursor reported no usage meters.", color = TextMuted, fontSize = 12.sp)
            }
            cursor.groups.forEach { group ->
                Spacer(Modifier.height(12.dp))
                Text(group.heading.uppercase(), color = TextMuted, fontSize = 10.sp, letterSpacing = 1.sp)
                group.meters.forEach { meter ->
                    Spacer(Modifier.height(8.dp))
                    CursorMeterRow(meter, lastKnown = cursor.stale)
                }
            }
            if (cursor.balances.isNotEmpty()) {
                Spacer(Modifier.height(12.dp))
                Text("CREDITS", color = TextMuted, fontSize = 10.sp, letterSpacing = 1.sp)
                cursor.balances.forEach { balance ->
                    Spacer(Modifier.height(8.dp))
                    Row {
                        Text(
                            (if (cursor.stale) "Last known " else "") + balance.label,
                            color = TextPrimary, fontSize = 13.sp,
                        )
                        Spacer(Modifier.weight(1f))
                        Text(
                            balance.valueLabel, color = TextPrimary, fontSize = 13.sp,
                            fontWeight = FontWeight.SemiBold, fontFamily = FontFamily.Monospace,
                        )
                    }
                    val expires = formatResetAt(balance.nextExpiresAt)
                    if (expires.isNotEmpty()) {
                        Text("Next expires $expires", color = TextMuted, fontSize = 11.sp)
                    }
                }
            }
            if (cursor.notReported.isNotEmpty()) {
                Spacer(Modifier.height(10.dp))
                Text("Not reported: ${cursor.notReported.joinToString(", ")}", color = TextMuted, fontSize = 11.sp)
            }
        }
    }
}

@Composable
private fun CursorMeterRow(meter: CursorMeter, lastKnown: Boolean) {
    val label = (if (lastKnown) "Last known " else "") + meter.label
    Column(modifier = Modifier.semantics {
        contentDescription = "$label: ${meter.valueLabel}. ${cursorMeterDetail(meter)}"
    }) {
        Row {
            Text(label, color = TextPrimary, fontSize = 13.sp)
            Spacer(Modifier.weight(1f))
            Text(
                meter.valueLabel,
                color = if (meter.exhausted) Danger else TextPrimary,
                fontSize = 13.sp,
                fontWeight = FontWeight.SemiBold,
                fontFamily = FontFamily.Monospace,
            )
        }
        if (meter.hasLimit) {
            Spacer(Modifier.height(6.dp))
            // What is left, the same direction and colours as the provider
            // meters above it.
            LinearProgressIndicator(
                progress = { meter.remainingPercent / 100f },
                modifier = Modifier.fillMaxWidth().height(6.dp).clip(RoundedCornerShape(3.dp)),
                color = toneFor(meter.remainingPercent),
                trackColor = Line,
            )
        }
        Spacer(Modifier.height(4.dp))
        Text(cursorMeterDetail(meter), color = TextMuted, fontSize = 11.sp)
    }
}

@Composable
private fun ProviderCard(
    provider: ProviderUsage,
    onControlProvider: (String, String) -> Unit = { _, _ -> },
    onViewQueue: (String) -> Unit = {},
) {
    Card(
        modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp),
        colors = CardDefaults.cardColors(containerColor = Panel),
        shape = RoundedCornerShape(12.dp),
    ) {
        Column(modifier = Modifier.padding(14.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(
                    providerDisplayName(provider.provider),
                    color = TextPrimary,
                    fontWeight = FontWeight.SemiBold,
                    fontSize = 16.sp,
                )
                // The plan as reported, when the source gives one. Shown
                // as a label, never as a capacity: plans are not comparable.
                if (provider.plan.isNotBlank()) {
                    Spacer(Modifier.width(6.dp))
                    PlanChip(provider.plan)
                }
                Spacer(Modifier.weight(1f))
                Text(
                    providerStatus(provider),
                    color = if (provider.paused) Warn else TextMuted,
                    fontSize = 12.sp,
                )
            }
            ProviderDetails(provider, onControlProvider, onViewQueue, accountPage = false)
        }
    }
}

/** A small outlined label for a reported plan name. */
@Composable
internal fun PlanChip(plan: String) {
    Text(
        plan,
        color = TextMuted,
        fontSize = 11.sp,
        fontFamily = FontFamily.Monospace,
        modifier = Modifier
            .clip(RoundedCornerShape(4.dp))
            .background(Line)
            .padding(horizontal = 6.dp, vertical = 1.dp),
    )
}

/**
 * Everything under a provider's heading: source, problem, meters, the
 * scheduler's line, reset credits, pools and actions. Shared by the single
 * card and each account page of the carousel, so an account reads the same
 * either way.
 *
 * @param accountPage on a carousel page, reset credits always get a row
 *   ("Not reported" when absent), since a missing row there reads as none.
 */
@Composable
internal fun ProviderDetails(
    provider: ProviderUsage,
    onControlProvider: (String, String) -> Unit,
    onViewQueue: (String) -> Unit,
    accountPage: Boolean,
) {
        Column {

            // Where the numbers came from and how fresh they are: the first
            // thing to check when this and the desktop disagree.
            if (provider.sourceLabel.isNotBlank()) {
                Spacer(Modifier.height(4.dp))
                Text(provider.sourceLabel, color = TextMuted, fontSize = 11.sp)
            }

            val problem = providerProblem(provider)
            if (problem.isNotEmpty()) {
                Spacer(Modifier.height(8.dp))
                Text(problem, color = Danger, fontSize = 12.sp)
            }
            // On a carousel page, out-of-date numbers read as last known: "was"
            // values and a fixed last-reported reset, never a live countdown.
            // The single-account card keeps its existing presentation.
            val staleNumbers = accountPage && provider.isOutOfDate
            // Stale wording is fixed times judged against the clock ("was due",
            // "today"); without fresh data nothing else recomposes the page,
            // so a minute tick keeps those judgements true while it sits open.
            if (staleNumbers) MinuteTick()

            provider.session?.let {
                Spacer(Modifier.height(12.dp))
                Meter(
                    "5-hour window", it.remainingPercent, it.resetsInSeconds,
                    it.resetInferred, it.resetsAt, it.elapsedPercent,
                    zones = if (staleNumbers) MeterZones() else MeterZones(reservePercent = provider.scheduling?.reservePercent ?: 0),
                    notStarted = it.notStarted,
                    outOfDate = staleNumbers,
                )
            }
            // The window exists but the number could not be read. Showing
            // nothing here reads as "there is no 5-hour limit", which is the
            // wrong thing to tell someone deciding whether to start a run.
            if (provider.sessionUnknown) {
                Spacer(Modifier.height(12.dp))
                UnknownMeter("5-hour window")
            }
            if (provider.weekly == null && provider.weeklyNotReported) {
                Spacer(Modifier.height(12.dp))
                UnknownMeter(
                    "Weekly allowance", "not reported",
                    spoken = "Weekly allowance: not reported for this account",
                    note = "Weekly limit not reported for this account · dispatch holds",
                )
            }
            provider.weekly?.let {
                Spacer(Modifier.height(12.dp))
                Meter(
                    provider.weeklyLabel.ifEmpty { "Weekly allowance" }, it.remainingPercent, it.resetsInSeconds,
                    it.resetInferred, it.resetsAt, it.elapsedPercent,
                    zones = if (staleNumbers) MeterZones() else MeterZones(floors = provider.scheduling?.weeklyFloors ?: emptyList()),
                    outOfDate = staleNumbers,
                )
            }
            // What the scheduler is doing with this provider, under the bars
            // that show where its lines are.
            // A stale account's scheduler state comes from the same old read:
            // a floor "arming in 2d" would be a live-looking countdown.
            if (staleNumbers) {
                Spacer(Modifier.height(8.dp))
                Text(OUT_OF_DATE_DISPATCH_LINE, color = TextMuted, fontSize = 11.sp)
            }
            // A seat with no weekly limit already says dispatch holds, just
            // above; a red "Waiting" line would only repeat it as alarm.
            provider.scheduling?.takeIf { !staleNumbers && !(provider.weeklyNotReported && !it.admits) }?.let { scheduling ->
                val line = schedulingLine(scheduling)
                if (line.isNotEmpty()) {
                    Spacer(Modifier.height(8.dp))
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Box(
                            Modifier
                                .size(6.dp)
                                .clip(CircleShape)
                                .background(if (scheduling.admits) Good else Accent),
                        )
                        Spacer(Modifier.width(6.dp))
                        Text(line, color = TextMuted, fontSize = 11.sp)
                    }
                }
            }
            // Worth showing beside an exhausted window, because spending one is
            // what gets work moving again. Labelled in full: a bare number here
            // would read as yet another usage meter. A credit's own deadline
            // is "expires", never "resets", so it cannot be mistaken for the
            // allowance's reset above.
            // Out of date, the count and deadlines are the last read too:
            // labelled so, with passed deadlines said to have expired.
            val deadline: (String) -> String = if (staleNumbers) {
                { lastKnownCreditDeadline(it) }
            } else {
                { formatResetAt(it) }
            }
            val all = resetCreditExpiries(provider, formatTime = deadline)
            // On an account page with the full deadline list below, the count
            // stands alone rather than repeating the first deadline.
            val creditsReported = (provider.resetCredits?.available ?: provider.bankedResets) != null
            val expired = if (staleNumbers) expiredCreditCount(provider) else 0
            val credits = (if (staleNumbers && creditsReported) "Last known: " else "") +
                (
                    if (accountPage && all.isNotEmpty()) {
                        resetCreditsLine(provider, reportedOnly = false, formatTime = { "" })
                    } else {
                        resetCreditsLine(provider, reportedOnly = !accountPage, formatTime = deadline)
                    }
                    ) +
                (if (expired > 0) " · $expired since expired" else "")
            if (credits.isNotEmpty()) {
                val count = provider.resetCredits?.available ?: provider.bankedResets
                Spacer(Modifier.height(10.dp))
                val creditsColor = if ((count ?: 0) > 0 && !staleNumbers) TextPrimary else TextMuted
                if (accountPage) {
                    // Stacked: the line with its deadline is long, and side by
                    // side with the label it squeezes at large font sizes.
                    Column(Modifier.semantics(mergeDescendants = true) {}) {
                        Text("Banked quota resets", color = TextMuted, fontSize = 11.sp)
                        Text(credits, color = creditsColor, fontSize = 11.sp, fontFamily = FontFamily.Monospace)
                    }
                } else {
                    Row(
                        verticalAlignment = Alignment.CenterVertically,
                        modifier = Modifier.semantics(mergeDescendants = true) {},
                    ) {
                        Text("Banked quota resets", color = TextMuted, fontSize = 11.sp)
                        Spacer(Modifier.width(8.dp))
                        Spacer(Modifier.weight(1f))
                        Text(credits, color = creditsColor, fontSize = 11.sp, fontFamily = FontFamily.Monospace)
                    }
                }
                if (accountPage && all.isNotEmpty()) {
                    Text(all, color = TextMuted, fontSize = 11.sp)
                }
            }
            provider.pools.forEach { pool ->
                Spacer(Modifier.height(12.dp))
                Meter(
                    pool.label, pool.remainingPercent, pool.resetsInSeconds,
                    pool.resetInferred, pool.resetsAt, pool.elapsedPercent,
                    // Spark's short allowance is independent of Codex's main
                    // weekly budget, but a Spark task consumes it. The core
                    // attaches the reserve only when the scheduler protects
                    // this pool, keeping the line and enforcement together.
                    zones = if (staleNumbers) MeterZones() else MeterZones(reservePercent = pool.reservePercent),
                    outOfDate = staleNumbers,
                )
            }

            Spacer(Modifier.height(14.dp))
            Row(verticalAlignment = Alignment.CenterVertically) {
                CardAction("View queue") { onViewQueue(provider.id) }
                Spacer(Modifier.width(8.dp))
                // Pause is the emergency stop for a provider that is spending
                // capacity on the wrong thing, so it is one tap from the
                // numbers that prompt it.
                CardAction(
                    if (provider.paused) "Resume" else "Pause",
                    emphasised = true,
                ) {
                    onControlProvider(provider.id, if (provider.paused) "resume" else "pause")
                }
                Spacer(Modifier.weight(1f))
                Text(
                    "Refresh",
                    color = TextMuted,
                    fontSize = 12.sp,
                    modifier = Modifier
                        .clickable { onControlProvider(provider.id, "refresh") }
                        .padding(8.dp),
                )
            }
        }
}

@Composable
private fun CardAction(label: String, emphasised: Boolean = false, onClick: () -> Unit) {
    Text(
        label,
        color = if (emphasised) Accent else TextPrimary,
        fontSize = 12.sp,
        fontWeight = FontWeight.Medium,
        modifier = Modifier
            .clip(RoundedCornerShape(8.dp))
            .background(if (emphasised) AccentSoft else Line)
            .clickable(onClick = onClick)
            .padding(horizontal = 14.dp, vertical = 8.dp),
    )
}

/**
 * A limit that exists but whose number is currently unreadable.
 *
 * Deliberately not a zeroed progress bar: an empty bar means "none left",
 * which is the opposite of "we do not know" and would be the more damaging
 * misreading of the two.
 */
@Composable
private fun UnknownMeter(
    label: String,
    value: String = "not available",
    spoken: String = "$label: not available right now",
    note: String = "Provider did not report this window",
) {
    Column(modifier = Modifier.semantics {
        contentDescription = spoken
    }) {
        Row {
            Text(label, color = TextPrimary, fontSize = 13.sp)
            Spacer(Modifier.weight(1f))
            Text(
                value,
                color = TextMuted,
                fontSize = 13.sp,
                fontWeight = FontWeight.SemiBold,
                fontFamily = FontFamily.Monospace,
            )
        }
        Spacer(Modifier.height(6.dp))
        // A flat track with no fill: there is a bar here, and it has no value.
        Box(
            Modifier
                .fillMaxWidth()
                .height(6.dp)
                .clip(RoundedCornerShape(3.dp))
                .background(Line),
        )
        Spacer(Modifier.height(4.dp))
        Text(
            note,
            color = TextMuted,
            fontSize = 11.sp,
        )
    }
}

/**
 * One of the scheduler's lines: bar-height, one pixel, kept inside the bar so
 * a floor at 0% or 100% still shows rather than falling off the end.
 */
@Composable
private fun BoxWithConstraintsScope.SchedulerLine(x: Dp, maxWidth: Dp, color: Color) {
    Box(
        Modifier
            .offset(x = (x - 0.5.dp).coerceIn(0.dp, maxWidth - 1.dp))
            .width(1.dp)
            .height(6.dp)
            .align(Alignment.CenterStart)
            .background(color),
    )
}

/** The scheduler's lines on one meter. Empty draws nothing. */
data class MeterZones(
    val reservePercent: Int = 0,
    val floors: List<WeeklyFloor> = emptyList(),
)

@Composable
private fun Meter(
    label: String,
    percent: Int,
    resetsInSeconds: Long,
    resetInferred: Boolean,
    resetsAt: String = "",
    elapsedPercent: Int = 0,
    zones: MeterZones = MeterZones(),
    notStarted: Boolean = false,
    outOfDate: Boolean = false,
) {
    val window = Window(resetsInSeconds = resetsInSeconds, resetsAt = resetsAt, resetInferred = resetInferred, notStarted = notStarted)
    // A window that has not started has no reset yet: say how its clock runs
    // instead of counting down to a time the provider has not set. Numbers
    // that are out of date get the last reported reset as a fixed time, with
    // no countdown, since a ticking line reads as live.
    val resetSentence = if (outOfDate) outOfDateResetSentence(window) else windowResetSentence(window)
    // Only computed for display; an unparseable or absent timestamp yields "".
    // Out of date, the time is already in the sentence.
    val absolute = if (notStarted || outOfDate) "" else formatResetAt(resetsAt)
    val pace = paceOf(percent, elapsedPercent)
    // The pace mark compares the old numbers with the present clock, which
    // says nothing true; it is not drawn or spoken for stale numbers.
    val showPace = elapsedPercent > 0 && !outOfDate
    val valueText = (if (outOfDate) "was " else "") + "$percent% left"
    Column(modifier = Modifier.semantics {
        contentDescription = buildString {
            // Same sentence a sighted reader gets, so the two cannot drift.
            if (outOfDate) {
                append("$label, last known and out of date: was $percent percent remaining. $resetSentence")
            } else {
                append("$label: $percent percent remaining. $resetSentence")
            }
            if (absolute.isNotEmpty()) append(", at $absolute")
            // The pace mark is visual; this is its sentence.
            if (showPace) {
                append(
                    when (pace) {
                        Pace.AHEAD -> ". Ahead of pace"
                        Pace.BEHIND -> ". Behind pace"
                        Pace.ON_PACE -> ". On pace"
                    },
                )
            }
        }
    }) {
        Row {
            Text(label, color = TextPrimary, fontSize = 13.sp)
            Spacer(Modifier.weight(1f))
            Text(
                valueText,
                color = if (outOfDate) TextMuted else TextPrimary,
                fontSize = 13.sp,
                fontWeight = FontWeight.SemiBold,
                fontFamily = FontFamily.Monospace,
            )
        }
        Spacer(Modifier.height(6.dp))
        // The bar is what is left; the mark is where its edge would be if the
        // window were being spent evenly. Bar past the mark: more left than
        // the clock suggests. Bar short of it: spending faster than time is
        // passing. Half left means nothing on its own -- it is comfortable
        // near the end of a window and a problem near the start -- and this
        // is the one glance that tells which.
        BoxWithConstraints(Modifier.fillMaxWidth().height(14.dp)) {
            LinearProgressIndicator(
                progress = { percent / 100f },
                modifier = Modifier
                    .fillMaxWidth()
                    .height(6.dp)
                    .align(Alignment.Center)
                    .clip(RoundedCornerShape(3.dp)),
                color = if (outOfDate) TextMuted.copy(alpha = 0.5f) else toneFor(percent),
                trackColor = Line,
            )
            // Redline's own lines. Every one is a line at a percent, drawn over
            // the fill, because a line is visible whether the bar has reached
            // it or not. The reserve was first drawn as a shaded band under
            // the bar and, on a real phone, could not be seen in any state: the
            // fill covered it when remaining was above the reserve, and tinted
            // it into mud when below.
            //
            // The scheduler's lines are the bar's own height; the pace mark
            // above is taller and lighter, so the two kinds tell apart by shape
            // at a glance and not only by colour.
            //
            // The reserve is the floor on the 5-hour bar Redline never spends
            // past: to its left is the person's. A weekly floor is solid while
            // armed (the scheduler is holding to it now) and faint while not
            // yet -- faint rather than absent, because "what happens at 72h"
            // is the question the bar is there to answer. Below the reserve
            // the fill itself is hatched, so a bar inside the protected zone
            // looks protected.
            if (zones.reservePercent > 0) {
                val reserveWidth = maxWidth * zones.reservePercent / 100f
                Box(
                    Modifier
                        .width(minOf(reserveWidth, maxWidth * percent / 100f))
                        .height(6.dp)
                        .align(Alignment.CenterStart)
                        .clip(RoundedCornerShape(topStart = 3.dp, bottomStart = 3.dp))
                        .background(Panel.copy(alpha = 0.45f)),
                )
                SchedulerLine(x = reserveWidth, maxWidth = maxWidth, color = Accent)
            }
            zones.floors.forEach { floor ->
                SchedulerLine(
                    x = maxWidth * floor.percent / 100f,
                    maxWidth = maxWidth,
                    color = if (floor.armed) Accent else Accent.copy(alpha = 0.4f),
                )
            }
            // Drawn only when the desktop sent an elapsed fraction: an older
            // one leaves the mark at the very end, where it says nothing.
            // Taller than the bar and than the scheduler's lines: the one mark
            // that is about the clock rather than the policy. Pace remains in
            // the accessibility sentence; the visual mark is always white so
            // every meter has the same easily scanned landmark.
            if (showPace) {
                val mark = paceMarkPercent(elapsedPercent)
                Box(
                    Modifier
                        .offset(x = (maxWidth * mark / 100f - 1.dp).coerceIn(0.dp, maxWidth - 2.dp))
                        .width(2.dp)
                        .fillMaxHeight()
                        .clip(RoundedCornerShape(1.dp))
                        .background(TextPrimary),
                )
            }
        }
        Spacer(Modifier.height(4.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(
                // Assembled in one place: the countdown and the sentence around
                // it have to agree, or a due reset reads "Resets in now".
                resetSentence,
                color = TextMuted,
                fontSize = 11.sp,
            )
            // The absolute time answers "when", which is the question you
            // match against a calendar; the countdown answers "how long".
            // People reach for one or the other depending on what they are
            // deciding, and there is room here for both.
            if (absolute.isNotEmpty()) {
                Spacer(Modifier.weight(1f))
                Text(absolute, color = TextMuted, fontSize = 11.sp)
            }
        }
    }
}

@Preview(showBackground = true, backgroundColor = 0xFF0D0F12)
@Composable
private fun UsageScreenPreview() {
    UsageScreen(
        state = UsageUiState(
            view = UsageView(
                providers = listOf(
                    ProviderUsage(
                        id = "claude-main",
                        provider = "claude",
                        session = Window(remainingPercent = 100, resetsInSeconds = 18000, elapsedPercent = 15),
                        weekly = Window(remainingPercent = 93, resetsInSeconds = 259200, elapsedPercent = 57),
                        scheduling = Scheduling(
                            reservePercent = 25,
                            weeklyFloors = listOf(
                                WeeklyFloor(percent = 50, armsInSeconds = 0, armed = true),
                                WeeklyFloor(percent = 20, armsInSeconds = 172800),
                            ),
                            decision = "WAIT",
                            reason = "no pace threshold matched",
                        ),
                        pools = listOf(
                            Pool(
                                key = "model:fable:weekly",
                                label = "Fable",
                                remainingPercent = 100,
                                resetsInSeconds = 259200,
                                resetInferred = true,
                            ),
                        ),
                    ),
                    ProviderUsage(
                        id = "codex-main",
                        provider = "codex",
                        weekly = Window(remainingPercent = 47, resetsInSeconds = 432000, elapsedPercent = 29),
                    ),
                ),
            ),
        ),
        onRetry = {},
    )
}

/**
 * Recomposes its caller once a minute while composed. Used where the words
 * depend on the clock but no new data will arrive to refresh them.
 */
@Composable
private fun MinuteTick() {
    var minute by remember { mutableStateOf(0L) }
    androidx.compose.runtime.LaunchedEffect(Unit) {
        while (true) {
            kotlinx.coroutines.delay(60_000L - System.currentTimeMillis() % 60_000L)
            minute = System.currentTimeMillis() / 60_000L
        }
    }
    @Suppress("UNUSED_EXPRESSION") minute
}
