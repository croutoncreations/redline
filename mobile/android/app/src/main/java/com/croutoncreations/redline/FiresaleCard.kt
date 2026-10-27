package com.croutoncreations.redline

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun FiresaleCard(state: FiresaleUiState, onRetry: () -> Unit, onStart: () -> Unit, onStop: (String) -> Unit,
                 onAccount: (String) -> Unit, onDuration: (String) -> Unit) {
    if (!state.visible) return
    var choosing by remember { mutableStateOf(false) }
    var submitted by remember { mutableStateOf(false) }
    LaunchedEffect(state.working) {
        if (submitted && !state.working) {
            if (state.error == null) choosing = false
            submitted = false
        }
    }
    Card(Modifier.fillMaxWidth().padding(horizontal = 12.dp), shape = RoundedCornerShape(12.dp),
        colors = CardDefaults.cardColors(containerColor = Panel)) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text("Firesale", color = TextPrimary, fontSize = 15.sp, fontWeight = FontWeight.Bold)
            state.view?.manual.orEmpty().forEach { sale ->
                val label = state.view?.eligible?.find { it.account == sale.account }?.label ?: sale.account
                val deadline = remember(sale.deadline) {
                    runCatching { DateTimeFormatter.ofPattern("EEE HH:mm").withZone(ZoneId.systemDefault()).format(Instant.parse(sale.deadline)) }.getOrDefault(sale.deadline)
                }
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("Firesale on · $label · until $deadline", color = Warn, fontSize = 13.sp, modifier = Modifier.weight(1f))
                    TextButton(onClick = { onStop(sale.account) }, enabled = !state.working) { Text("Stop", color = Danger) }
                }
            }
            if (state.view != null && state.view.manual.isEmpty()) {
                Text("Make capacity available on demand.", color = TextMuted, fontSize = 13.sp)
                TextButton(onClick = { choosing = true }, enabled = !state.working) { Text("Start firesale", color = Accent) }
            }
            if (state.working) LinearProgressIndicator(Modifier.fillMaxWidth(), color = Accent, trackColor = Line)
            state.error?.let { Text(it, color = Danger, fontSize = 13.sp); TextButton(onClick = onRetry, enabled = !state.working) { Text("Retry", color = TextPrimary) } }
        }
    }
    if (choosing) {
        ModalBottomSheet(onDismissRequest = { choosing = false }, containerColor = Panel, contentColor = TextPrimary) {
            Column(Modifier.fillMaxWidth().verticalScroll(rememberScrollState()).padding(horizontal = 20.dp).padding(bottom = 24.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text("Start firesale", fontSize = 20.sp, fontWeight = FontWeight.Bold)
                Text("Account", color = TextMuted, fontSize = 12.sp)
                if (state.view?.eligible.isNullOrEmpty()) Text("No eligible accounts on your Mac.", color = Warn)
                state.view?.eligible.orEmpty().forEach { account ->
                    Column(Modifier.fillMaxWidth().background(PanelRaised, RoundedCornerShape(10.dp)).clickable(enabled = !state.working) { onAccount(account.account) }.padding(12.dp)) {
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            RadioButton(selected = state.selectedAccount == account.account, onClick = { onAccount(account.account) }, enabled = !state.working,
                                colors = RadioButtonDefaults.colors(selectedColor = Accent, unselectedColor = TextMuted))
                            Column { Text(account.label, color = TextPrimary, fontSize = 14.sp); Text(account.provider, color = TextMuted, fontSize = 12.sp) }
                        }
                        account.reasonUnavailable?.let { Text(it, color = Warn, fontSize = 12.sp) }
                    }
                }
                Text("Duration", color = TextMuted, fontSize = 12.sp)
                listOf("15m" to "15 min", "2h" to "2 hours", "weekly_reset" to "Until weekly reset").forEach { (value, label) ->
                    val enabled = !state.working && (value != "weekly_reset" || state.selected?.weeklyResetsAt != null)
                    Row(Modifier.fillMaxWidth().clickable(enabled = enabled) { onDuration(value) }, verticalAlignment = Alignment.CenterVertically) {
                        RadioButton(selected = state.until == value, onClick = { onDuration(value) }, enabled = enabled,
                            colors = RadioButtonDefaults.colors(selectedColor = Accent, unselectedColor = TextMuted))
                        Text(label, color = if (enabled) TextPrimary else TextMuted, fontSize = 14.sp)
                    }
                }
                state.error?.let { Text(it, color = Danger, fontSize = 13.sp) }
                Button(onClick = { submitted = true; onStart() }, enabled = state.canStart,
                    colors = ButtonDefaults.buttonColors(containerColor = Accent, contentColor = TextPrimary)) { Text(if (state.working) "Starting…" else "Start") }
            }
        }
    }
}
