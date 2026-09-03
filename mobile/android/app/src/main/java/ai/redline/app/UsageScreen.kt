package ai.redline.app

import androidx.compose.foundation.background
import androidx.compose.foundation.Image
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
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
) {
    Surface(color = Background, modifier = Modifier.fillMaxSize()) {
        Column(modifier = Modifier.fillMaxSize()) {
            Header(state, onUnpair)
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
) {
    PullToRefreshBox(
        isRefreshing = state.loading,
        onRefresh = onRefresh,
        modifier = Modifier.fillMaxSize(),
    ) {
        ProviderList(state, onControlProvider, onViewQueue)
    }
}

@Composable
private fun Header(state: UsageUiState, onUnpair: (() -> Unit)? = null) {
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
            when {
                // Showing cached numbers without saying so would be misleading.
                state.failure != null && state.hasData ->
                    Text("Offline", color = Warn, fontSize = 12.sp)

                else -> LivePill(state.live)
            }
            // Unpairing lives behind the overflow rather than on the surface:
            // it is rare, destructive, and next to controls people press often.
            onUnpair?.let {
                Spacer(Modifier.width(4.dp))
                UnpairMenu(onUnpair = it)
            }
        }
        Spacer(Modifier.height(2.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("Capacity", color = TextMuted, fontSize = 12.sp)
            // Worth saying out loud. A relayed session is slower, costs money,
            // and crosses a third party, so someone seeing an unexpected
            // "relayed" here has a real reason to check why the direct route
            // is not working.
            if (state.transport == Transport.Relay) {
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
private fun UnpairMenu(onUnpair: () -> Unit) {
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
            DropdownMenuItem(
                text = { Text("Unpair this device", color = TextPrimary, fontSize = 14.sp) },
                onClick = {
                    menuOpen = false
                    confirming = true
                },
            )
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
                    onUnpair()
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

@Composable
private fun LivePill(live: LiveState) {
    val (label, tone) = when (live) {
        LiveState.LIVE -> "live" to Good
        LiveState.CONNECTING -> "connecting" to TextMuted
        LiveState.RECONNECTING -> "reconnecting" to Warn
        LiveState.OFFLINE -> return
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
) {
    val providers = state.view?.providers ?: emptyList()
    LazyColumn(
        contentPadding = PaddingValues(vertical = 8.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        items(providers, key = { it.id }) { provider ->
            ProviderCard(provider, onControlProvider, onViewQueue)
        }
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
                    provider.provider.replaceFirstChar { it.uppercase() },
                    color = TextPrimary,
                    fontWeight = FontWeight.SemiBold,
                    fontSize = 16.sp,
                )
                Spacer(Modifier.weight(1f))
                Text(
                    providerStatus(provider),
                    color = if (provider.paused) Warn else TextMuted,
                    fontSize = 12.sp,
                )
            }

            // Where the numbers came from and how fresh they are: the first
            // thing to check when this and the desktop disagree.
            if (provider.sourceLabel.isNotBlank()) {
                Spacer(Modifier.height(4.dp))
                Text(provider.sourceLabel, color = TextMuted, fontSize = 11.sp)
            }

            if (provider.error.isNotEmpty()) {
                Spacer(Modifier.height(8.dp))
                Text(provider.error, color = Danger, fontSize = 12.sp)
            }

            provider.session?.let {
                Spacer(Modifier.height(12.dp))
                Meter(
                    "5-hour window", it.remainingPercent, it.resetsInSeconds,
                    it.resetInferred, it.resetsAt,
                )
            }
            // The window exists but the number could not be read. Showing
            // nothing here reads as "there is no 5-hour limit", which is the
            // wrong thing to tell someone deciding whether to start a run.
            if (provider.sessionUnknown) {
                Spacer(Modifier.height(12.dp))
                UnknownMeter("5-hour window")
            }
            provider.weekly?.let {
                Spacer(Modifier.height(12.dp))
                Meter(
                    "Weekly allowance", it.remainingPercent, it.resetsInSeconds,
                    it.resetInferred, it.resetsAt,
                )
            }
            // Worth showing beside an exhausted window, because spending one is
            // what gets work moving again. Labelled in full: a bare number here
            // would read as yet another usage meter.
            provider.bankedResets?.let { resets ->
                Spacer(Modifier.height(10.dp))
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("Banked quota resets", color = TextMuted, fontSize = 11.sp)
                    Spacer(Modifier.weight(1f))
                    Text(
                        if (resets == 1) "1 available" else "$resets available",
                        color = if (resets > 0) TextPrimary else TextMuted,
                        fontSize = 11.sp,
                        fontFamily = FontFamily.Monospace,
                    )
                }
            }
            provider.pools.forEach { pool ->
                Spacer(Modifier.height(12.dp))
                Meter(
                    pool.label, pool.remainingPercent, pool.resetsInSeconds,
                    pool.resetInferred, pool.resetsAt,
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
private fun UnknownMeter(label: String) {
    Column(modifier = Modifier.semantics {
        contentDescription = "$label: not available right now"
    }) {
        Row {
            Text(label, color = TextPrimary, fontSize = 13.sp)
            Spacer(Modifier.weight(1f))
            Text(
                "not available",
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
            "Provider did not report this window",
            color = TextMuted,
            fontSize = 11.sp,
        )
    }
}

@Composable
private fun Meter(
    label: String,
    percent: Int,
    resetsInSeconds: Long,
    resetInferred: Boolean,
    resetsAt: String = "",
) {
    val resetSentence = resetLabel(resetsInSeconds, resetInferred)
    // Only computed for display; an unparseable or absent timestamp yields "".
    val absolute = formatResetAt(resetsAt)
    Column(modifier = Modifier.semantics {
        contentDescription = buildString {
            // Same sentence a sighted reader gets, so the two cannot drift.
            append("$label: $percent percent remaining. $resetSentence")
            if (absolute.isNotEmpty()) append(", at $absolute")
        }
    }) {
        Row {
            Text(label, color = TextPrimary, fontSize = 13.sp)
            Spacer(Modifier.weight(1f))
            Text(
                "$percent% left",
                color = TextPrimary,
                fontSize = 13.sp,
                fontWeight = FontWeight.SemiBold,
                fontFamily = FontFamily.Monospace,
            )
        }
        Spacer(Modifier.height(6.dp))
        LinearProgressIndicator(
            progress = { percent / 100f },
            modifier = Modifier.fillMaxWidth().height(6.dp).clip(RoundedCornerShape(3.dp)),
            color = toneFor(percent),
            trackColor = Line,
        )
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
                        session = Window(remainingPercent = 100, resetsInSeconds = 18000),
                        weekly = Window(remainingPercent = 93, resetsInSeconds = 259200),
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
                        weekly = Window(remainingPercent = 47, resetsInSeconds = 432000),
                    ),
                ),
            ),
        ),
        onRetry = {},
    )
}
