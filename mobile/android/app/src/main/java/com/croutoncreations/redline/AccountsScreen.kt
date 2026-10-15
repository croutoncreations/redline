package com.croutoncreations.redline

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.defaultMinSize
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
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/**
 * Every provider account the Mac knows, with its state and what to do about
 * it. Accounts Redline manages can be renamed, switched off or on, removed,
 * and new ones linked to an OpenUsage card the Mac already reads. Signing in
 * is never done here: the screen says where to do it.
 */
@Composable
fun AccountsScreen(
    state: AccountsUiState,
    onClose: () -> Unit,
    onRetry: () -> Unit,
    onDiscover: (String) -> Unit,
    onCloseDiscover: () -> Unit,
    onAdd: (id: String, provider: String, label: String, card: String) -> Unit,
    onRename: (String, String) -> Unit,
    onSetDisabled: (String, Boolean) -> Unit,
    onRemove: (String) -> Unit,
    onDismissMessage: () -> Unit,
) {
    BackHandler(onBack = onClose)
    var renaming by remember { mutableStateOf<AccountEntry?>(null) }
    var removing by remember { mutableStateOf<AccountEntry?>(null) }
    var linking by remember { mutableStateOf<DiscoveredCard?>(null) }

    Surface(color = Background, modifier = Modifier.fillMaxSize()) {
        Column(Modifier.fillMaxSize()) {
            Row(
                verticalAlignment = Alignment.CenterVertically,
                modifier = Modifier.fillMaxWidth().background(Panel).padding(horizontal = 8.dp, vertical = 6.dp),
            ) {
                TextButton(onClick = onClose) { Text("‹ Usage", color = Accent, fontSize = 14.sp) }
                Text(
                    "Accounts",
                    color = TextPrimary,
                    fontWeight = FontWeight.Bold,
                    fontSize = 16.sp,
                    modifier = Modifier.semantics { heading() },
                )
            }
            when {
                state.view == null && state.failure != null -> Box(Modifier.fillMaxSize().padding(24.dp), Alignment.Center) {
                    Column(horizontalAlignment = Alignment.CenterHorizontally) {
                        Text(
                            if (state.failure == UsageUiState.Failure.UNAUTHORIZED) {
                                "Redline rejected this device's credentials. Pair it again from the desktop app."
                            } else {
                                "Cannot reach Redline."
                            },
                            color = TextMuted, fontSize = 14.sp,
                        )
                        if (state.failure != UsageUiState.Failure.UNAUTHORIZED && state.message.isNotEmpty()) {
                            Text(state.message, color = TextMuted, fontSize = 13.sp, modifier = Modifier.padding(top = 8.dp))
                        }
                        TextButton(onClick = onRetry) { Text("Retry", color = Accent) }
                    }
                }
                state.view == null -> Box(Modifier.fillMaxSize(), Alignment.Center) { CircularProgressIndicator(color = Accent) }
                else -> LazyColumn(
                    contentPadding = PaddingValues(12.dp),
                    verticalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    if (state.message.isNotEmpty()) {
                        item(key = "message") { Notice(state.message, Danger, onDismissMessage) }
                    }
                    if (state.view.restartRequired) {
                        item(key = "restart") {
                            Notice("Saved. Restart Redline on your Mac to apply account changes.", Warn, null)
                        }
                    }
                    items(state.view.accounts, key = { it.id }) { account ->
                        AccountCard(
                            account,
                            onRename = { renaming = account },
                            onToggle = { onSetDisabled(account.id, !account.disabled) },
                            onRemove = { removing = account },
                        )
                    }
                    item(key = "add") {
                        Column {
                            Spacer(Modifier.height(4.dp))
                            ActionChip("Link another Claude account") { onDiscover("claude") }
                            Spacer(Modifier.height(6.dp))
                            Text(
                                "Sign in to each Claude account in OpenUsage on your Mac first. Redline never asks " +
                                    "for a password or token, and never moves work between accounts: a task runs on " +
                                    "the account its profile names.",
                                color = TextMuted, fontSize = 11.sp,
                            )
                        }
                    }
                }
            }
        }
    }

    state.discover?.let { found ->
        DiscoverDialog(found, onDismiss = onCloseDiscover, onPick = { linking = it })
    }
    linking?.let { card ->
        LabelDialog(
            title = "Link ${card.id}",
            initial = "",
            confirm = "Link",
            onDismiss = { linking = null },
            onConfirm = { label ->
                linking = null
                onAdd(suggestedAccountId(card.id), state.discover?.provider ?: "claude", label, card.id)
            },
        )
    }
    renaming?.let { account ->
        LabelDialog(
            title = "Rename ${account.id}",
            // The label falls back to the id; saving that unchanged would
            // turn the id into a label.
            initial = if (account.label == account.id) "" else account.label,
            confirm = "Save",
            onDismiss = { renaming = null },
            onConfirm = { label ->
                renaming = null
                onRename(account.id, label)
            },
        )
    }
    removing?.let { account ->
        AlertDialog(
            onDismissRequest = { removing = null },
            containerColor = PanelRaised,
            title = { Text("Remove ${account.label}?", color = TextPrimary) },
            text = {
                Text(
                    "Redline stops watching this account and forgets its name. Its usage history stays on the " +
                        "Mac. Nothing is signed out. Accounts that task profiles use can only be switched off.",
                    color = TextMuted, fontSize = 13.sp,
                )
            },
            confirmButton = {
                TextButton(onClick = {
                    removing = null
                    onRemove(account.id)
                }) { Text("Remove", color = Danger) }
            },
            dismissButton = { TextButton(onClick = { removing = null }) { Text("Cancel", color = TextMuted) } },
        )
    }
}

@Composable
private fun Notice(text: String, tone: Color, onDismiss: (() -> Unit)?) {
    Row(
        verticalAlignment = Alignment.CenterVertically,
        modifier = Modifier.fillMaxWidth().clip(RoundedCornerShape(10.dp)).background(Panel).padding(12.dp),
    ) {
        Text(text, color = tone, fontSize = 12.sp, modifier = Modifier.weight(1f))
        if (onDismiss != null) TextButton(onClick = onDismiss) { Text("OK", color = TextMuted) }
    }
}

private fun toneColor(tone: String): Color = when (tone) {
    "good" -> Good
    "warn" -> Warn
    "danger" -> Danger
    else -> TextMuted
}

@Composable
private fun AccountCard(account: AccountEntry, onRename: () -> Unit, onToggle: () -> Unit, onRemove: () -> Unit) {
    Card(
        modifier = Modifier.fillMaxWidth(),
        colors = CardDefaults.cardColors(containerColor = Panel),
        shape = RoundedCornerShape(12.dp),
    ) {
        Column(Modifier.padding(14.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(
                    account.label,
                    color = TextPrimary,
                    fontSize = 15.sp,
                    fontWeight = FontWeight.SemiBold,
                    modifier = Modifier.weight(1f, fill = false),
                )
                Spacer(Modifier.width(8.dp))
                Text(providerDisplayName(account.provider), color = TextMuted, fontSize = 12.sp)
            }
            Text("ID ${account.id}", color = TextMuted, fontSize = 11.sp, fontFamily = FontFamily.Monospace)
            Spacer(Modifier.height(6.dp))
            Row(verticalAlignment = Alignment.CenterVertically) {
                Box(Modifier.size(6.dp).clip(CircleShape).background(toneColor(account.tone)))
                Spacer(Modifier.width(6.dp))
                Text(account.state, color = toneColor(account.tone), fontSize = 12.sp)
            }
            Spacer(Modifier.height(4.dp))
            Text(account.reads, color = TextMuted, fontSize = 11.sp)
            if (account.lastError.isNotEmpty() && account.tone == "danger") {
                Spacer(Modifier.height(4.dp))
                Text(account.lastError, color = Danger, fontSize = 11.sp)
            }
            // How to connect it, in words. Shown when something is wrong or
            // the account has never reported, which is when it is needed.
            if (account.tone == "danger" || account.state.startsWith("Waiting") || account.pending == "add") {
                Spacer(Modifier.height(6.dp))
                Text(account.signIn, color = TextPrimary, fontSize = 12.sp)
            }
            Spacer(Modifier.height(10.dp))
            if (account.editable) {
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    ActionChip("Rename", onClick = onRename)
                    ActionChip(if (account.disabled) "Turn on" else "Turn off", onClick = onToggle)
                    ActionChip("Remove", danger = true, onClick = onRemove)
                }
            } else {
                Text("Set in the Mac's Redline config file; change it there.", color = TextMuted, fontSize = 11.sp)
            }
        }
    }
}

@Composable
private fun ActionChip(label: String, danger: Boolean = false, onClick: () -> Unit) {
    Text(
        label,
        color = if (danger) Danger else TextPrimary,
        fontSize = 12.sp,
        fontWeight = FontWeight.Medium,
        modifier = Modifier
            .clip(RoundedCornerShape(8.dp))
            .background(if (danger) AccentSoft else Line)
            .clickable(role = Role.Button, onClick = onClick)
            .defaultMinSize(minHeight = 40.dp)
            .padding(horizontal = 14.dp, vertical = 11.dp),
    )
}

@Composable
private fun DiscoverDialog(found: DiscoverView, onDismiss: () -> Unit, onPick: (DiscoveredCard) -> Unit) {
    AlertDialog(
        onDismissRequest = onDismiss,
        containerColor = PanelRaised,
        title = { Text("Claude accounts in OpenUsage", color = TextPrimary) },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                if (found.error.isNotEmpty()) Text(found.error, color = Danger, fontSize = 13.sp)
                found.cards.forEach { card ->
                    val linked = card.linkedAccount.isNotEmpty()
                    Column(
                        Modifier
                            .fillMaxWidth()
                            .clip(RoundedCornerShape(8.dp))
                            .background(Panel)
                            .clickable(enabled = !linked, role = Role.Button) { onPick(card) }
                            .padding(10.dp),
                    ) {
                        Row {
                            Text(card.id, color = TextPrimary, fontSize = 13.sp, fontFamily = FontFamily.Monospace)
                            Spacer(Modifier.weight(1f))
                            if (card.plan.isNotEmpty()) Text(card.plan, color = TextMuted, fontSize = 12.sp)
                        }
                        val detail = when {
                            linked -> "Already read by ${card.linkedAccount}"
                            card.error.isNotEmpty() -> card.error
                            card.stale -> "Out of date in OpenUsage"
                            !card.hasWeekly -> "No weekly limit reported"
                            else -> "Tap to link"
                        }
                        Text(detail, color = if (linked) TextMuted else Accent, fontSize = 11.sp)
                    }
                }
                if (found.cards.isEmpty() && found.error.isEmpty()) {
                    Text("No Claude cards found.", color = TextMuted, fontSize = 13.sp)
                }
                Text(found.hint, color = TextMuted, fontSize = 11.sp)
            }
        },
        confirmButton = { TextButton(onClick = onDismiss) { Text("Close", color = TextMuted) } },
    )
}

@Composable
private fun LabelDialog(title: String, initial: String, confirm: String, onDismiss: () -> Unit, onConfirm: (String) -> Unit) {
    var text by remember { mutableStateOf(initial) }
    // Close to the Mac's rules (it has the final say): trimmed, counted in
    // characters rather than UTF-16 units, printable, no '@'.
    val trimmed = text.trim()
    val invalid = trimmed.contains('@') || trimmed.codePointCount(0, trimmed.length) > 60 ||
        trimmed.any { !isPrintableLabelChar(it) }
    AlertDialog(
        onDismissRequest = onDismiss,
        containerColor = PanelRaised,
        title = { Text(title, color = TextPrimary) },
        text = {
            Column {
                OutlinedTextField(
                    value = text,
                    onValueChange = { text = it },
                    singleLine = true,
                    label = { Text("Name shown in Redline") },
                    isError = invalid,
                    colors = OutlinedTextFieldDefaults.colors(
                        focusedTextColor = TextPrimary, unfocusedTextColor = TextPrimary,
                        focusedBorderColor = Accent, unfocusedBorderColor = Line,
                        focusedLabelColor = TextMuted, unfocusedLabelColor = TextMuted,
                    ),
                )
                Spacer(Modifier.height(6.dp))
                Text(
                    if (invalid) "Use a nickname of up to 60 characters, not an email address." else "For example Personal or Client.",
                    color = if (invalid) Danger else TextMuted,
                    fontSize = 11.sp,
                )
            }
        },
        confirmButton = {
            TextButton(enabled = !invalid, onClick = { onConfirm(text.trim()) }) { Text(confirm, color = Accent) }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel", color = TextMuted) } },
    )
}

private fun isPrintableLabelChar(c: Char): Boolean = when (Character.getType(c).toByte()) {
    Character.CONTROL, Character.FORMAT, Character.LINE_SEPARATOR, Character.PARAGRAPH_SEPARATOR,
    Character.SURROGATE, Character.UNASSIGNED, Character.PRIVATE_USE,
    -> false
    Character.SPACE_SEPARATOR -> c == ' '
    else -> true
}
