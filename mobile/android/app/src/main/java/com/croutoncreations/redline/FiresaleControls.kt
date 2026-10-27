package com.croutoncreations.redline

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter

/**
 * Firesale controls live on each account rather than in a headline card: the
 * account a firesale is for is the one whose numbers you are looking at.
 * Provided once at the top of the Usage screen so every account surface (the
 * single-account card, carousel pages and overview rows) can read it without
 * threading it through each signature.
 */
data class FiresaleControls(
    val state: FiresaleUiState,
    val onOpen: (String) -> Unit,
    val onStop: (String) -> Unit,
) {
    /** Null when the desktop has no firesale endpoint or the list hasn't loaded. */
    fun account(id: String): FiresaleAccount? =
        state.takeIf { it.visible }?.view?.eligible?.find { it.account == id }

    fun activeSale(id: String): ManualFiresale? =
        state.takeIf { it.visible }?.view?.manual?.find { it.account == id }

    /**
     * The account's Firesale action is offered only when a start could help:
     * the desktop lists it, nothing is already running there, and it has no
     * blocker (weekly usage not reported, allowance used up, paused). A
     * button that starts a sale which can never dispatch is worse than none.
     */
    fun canOffer(id: String): Boolean {
        val account = account(id) ?: return false
        return activeSale(id) == null && account.reasonUnavailable == null
    }
}

val LocalFiresale = staticCompositionLocalOf<FiresaleControls?> { null }

internal fun firesaleDeadline(deadline: String): String =
    runCatching { DateTimeFormatter.ofPattern("EEE HH:mm").withZone(ZoneId.systemDefault()).format(Instant.parse(deadline)) }
        .getOrDefault(deadline)

/** The active-sale line shown on an account, with its Stop action. */
@Composable
internal fun AccountFiresaleStatus(accountId: String) {
    val controls = LocalFiresale.current ?: return
    val sale = controls.activeSale(accountId) ?: return
    Spacer(Modifier.height(10.dp))
    Row(
        verticalAlignment = Alignment.CenterVertically,
        modifier = Modifier
            .fillMaxWidth()
            .clip(RoundedCornerShape(10.dp))
            .background(AccentSoft)
            .padding(start = 12.dp, end = 4.dp, top = 2.dp, bottom = 2.dp),
    ) {
        Box(Modifier.size(6.dp).clip(CircleShape).background(Warn))
        Spacer(Modifier.width(8.dp))
        Text("Firesale on · until ${firesaleDeadline(sale.deadline)}", color = Warn, fontSize = 12.sp, modifier = Modifier.weight(1f))
        TextButton(onClick = { controls.onStop(accountId) }, enabled = !controls.state.working) {
            Text("Stop", color = Danger, fontSize = 12.sp, fontWeight = FontWeight.Medium)
        }
    }
}

/** True when this account can be offered a Firesale action at all. */
@Composable
internal fun firesaleAvailable(accountId: String): Boolean =
    LocalFiresale.current?.canOffer(accountId) ?: false

/** Compact marker for overview rows. */
@Composable
internal fun FiresaleMarker(accountId: String) {
    val sale = LocalFiresale.current?.activeSale(accountId) ?: return
    Text("Firesale on · until ${firesaleDeadline(sale.deadline)}", color = Warn, fontSize = 11.sp)
}

/** Duration sheet for one account, opened from that account's Firesale action. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun FiresaleSheet(state: FiresaleUiState, onDuration: (String) -> Unit, onStart: () -> Unit, onDismiss: () -> Unit) {
    val account = state.selected ?: return
    var submitted by remember { mutableStateOf(false) }
    LaunchedEffect(state.working) {
        if (submitted && !state.working) {
            submitted = false
            if (state.error == null) onDismiss()
        }
    }
    ModalBottomSheet(onDismissRequest = onDismiss, containerColor = Panel, contentColor = TextPrimary) {
        Column(
            Modifier.fillMaxWidth().verticalScroll(rememberScrollState()).padding(horizontal = 20.dp).padding(bottom = 24.dp),
            verticalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            Text("Start firesale", fontSize = 20.sp, fontWeight = FontWeight.Bold)
            Text(
                "${account.label} · runs queued firesale work now. Pauses, budgets and the 5-hour reserve still apply.",
                color = TextMuted, fontSize = 13.sp,
            )
            account.reasonUnavailable?.let {
                Text("Can't help right now: $it", color = Warn, fontSize = 13.sp)
            }
            Spacer(Modifier.height(4.dp))
            Text("Duration", color = TextMuted, fontSize = 12.sp)
            listOf("15m" to "15 minutes", "2h" to "2 hours", "weekly_reset" to "Until weekly reset").forEach { (value, label) ->
                val enabled = !state.working && (value != "weekly_reset" || account.weeklyResetsAt != null)
                Row(
                    Modifier
                        .fillMaxWidth()
                        .clip(RoundedCornerShape(10.dp))
                        .background(PanelRaised)
                        .clickable(enabled = enabled) { onDuration(value) }
                        .padding(horizontal = 4.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    RadioButton(
                        selected = state.until == value, onClick = { onDuration(value) }, enabled = enabled,
                        colors = RadioButtonDefaults.colors(selectedColor = Accent, unselectedColor = TextMuted),
                    )
                    Text(label, color = if (enabled) TextPrimary else TextMuted, fontSize = 14.sp)
                }
            }
            state.error?.let { Text(it, color = Danger, fontSize = 13.sp) }
            Spacer(Modifier.height(4.dp))
            Button(
                onClick = { submitted = true; onStart() }, enabled = state.canStart, modifier = Modifier.fillMaxWidth(),
                colors = ButtonDefaults.buttonColors(containerColor = Accent, contentColor = TextPrimary),
            ) { Text(if (state.working) "Starting…" else "Start firesale") }
        }
    }
}
