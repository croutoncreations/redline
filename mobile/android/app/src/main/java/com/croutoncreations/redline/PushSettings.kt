package com.croutoncreations.redline

import android.Manifest
import android.content.Intent
import android.os.Build
import android.provider.Settings
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.compose.ui.platform.LocalLifecycleOwner
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch

/** Permission is offered after pairing, never on an unpaired cold launch. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun PushSettings(push: PushClient, newlyPaired: Boolean, open: Boolean, host: String, connection: String,
                 onUnpair: () -> Unit, onClose: () -> Unit) {
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    val owner = LocalLifecycleOwner.current
    var enabled by remember { mutableStateOf(push.enabled) }
    var permissionGranted by remember { mutableStateOf(push.canNotify()) }
    var registration by remember { mutableStateOf(push.lastRegistrationResult) }
    var working by remember { mutableStateOf(false) }
    var message by remember { mutableStateOf<String?>(null) }
    var rationale by remember { mutableStateOf(false) }
    var confirming by remember { mutableStateOf(false) }
    var testForbidden by remember { mutableStateOf(false) }

    DisposableEffect(push, owner) {
        fun update() { enabled = push.enabled; permissionGranted = push.canNotify(); registration = push.lastRegistrationResult }
        val listener = push.observeRegistration { update() }
        val observer = LifecycleEventObserver { _, event ->
            if (event == Lifecycle.Event.ON_RESUME) {
                update()
                scope.launch { try { push.refreshRegistration() } catch (error: Exception) { if (error is CancellationException) throw error } }
            }
        }
        owner.lifecycle.addObserver(observer)
        onDispose { listener.close(); owner.lifecycle.removeObserver(observer) }
    }

    fun change(value: Boolean) {
        scope.launch {
            working = true
            message = null
            try { push.setEnabled(value) }
            catch (error: Exception) {
                if (error is CancellationException) throw error
                if (!value) message = "Notifications are off; removal will retry on app start."
            } finally { enabled = push.enabled; permissionGranted = push.canNotify(); registration = push.lastRegistrationResult; working = false }
        }
    }

    val permission = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (granted) change(true)
        else { permissionGranted = false; message = "Allow notifications in Android Settings to enable alerts." }
    }
    fun allow() {
        if (Build.VERSION.SDK_INT >= 33 && !permissionGranted) rationale = true
        else if (!permissionGranted) context.startActivity(Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS).putExtra(Settings.EXTRA_APP_PACKAGE, context.packageName))
        else change(true)
    }

    LaunchedEffect(newlyPaired) {
        if (!BuildConfig.PUSH_ENABLED) return@LaunchedEffect
        if (newlyPaired && push.enabled && Build.VERSION.SDK_INT >= 33 && !push.canNotify()) rationale = true
        else try { push.refreshRegistration() } catch (error: Exception) { if (error is CancellationException) throw error }
    }

    if (open) {
        ModalBottomSheet(onDismissRequest = onClose, containerColor = Panel, contentColor = TextPrimary) {
            Column(Modifier.fillMaxWidth().verticalScroll(rememberScrollState()).padding(horizontal = 20.dp).padding(bottom = 24.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                Text("Settings", fontSize = 22.sp, fontWeight = FontWeight.Bold)
                Text("Notifications", color = TextMuted, fontSize = 12.sp, fontWeight = FontWeight.Bold)
                val status = pushSettingsStatus(BuildConfig.PUSH_ENABLED, enabled, permissionGranted, registration)
                val available = BuildConfig.PUSH_ENABLED && !working
                ListItem(
                    headlineContent = { Text("Push notifications", color = if (BuildConfig.PUSH_ENABLED) TextPrimary else TextMuted, fontSize = 14.sp) },
                    supportingContent = { Text(status, color = TextMuted, fontSize = 12.sp) },
                    trailingContent = {
                        Switch(checked = enabled && permissionGranted && BuildConfig.PUSH_ENABLED, enabled = available,
                            onCheckedChange = { if (it) allow() else change(false) },
                            colors = SwitchDefaults.colors(checkedThumbColor = TextPrimary, checkedTrackColor = Accent, uncheckedThumbColor = TextMuted, uncheckedTrackColor = Line))
                    },
                    colors = ListItemDefaults.colors(containerColor = PanelRaised),
                    modifier = Modifier.clickable(enabled = available) { if (!permissionGranted) allow() else change(!enabled) },
                )
                if (BuildConfig.PUSH_ENABLED && enabled && permissionGranted) {
                    listOf(Triple("resets", "Resets", "Reset announcements and reminders"), Triple("firesale", "Firesale", "Firesales starting and ending"), Triple("jobs", "Jobs", "Job progress and results")).forEach { (id, label, description) ->
                        ListItem(headlineContent = { Text(label, color = TextPrimary, fontSize = 14.sp) },
                            supportingContent = { Text(description, color = TextMuted, fontSize = 12.sp, maxLines = 1) },
                            trailingContent = { Text("›", color = TextMuted, fontSize = 22.sp) },
                            colors = ListItemDefaults.colors(containerColor = PanelRaised),
                            modifier = Modifier.clickable {
                                PushNotifications(context).channels()
                                context.startActivity(Intent(Settings.ACTION_CHANNEL_NOTIFICATION_SETTINGS)
                                    .putExtra(Settings.EXTRA_APP_PACKAGE, context.packageName).putExtra(Settings.EXTRA_CHANNEL_ID, id))
                            })
                    }
                    if (testForbidden) Text("Send a test from your Mac: redline notification test --push", color = TextMuted, fontSize = 12.sp)
                    else OutlinedButton(enabled = !working, onClick = {
                        scope.launch {
                            working = true; message = null
                            try { push.sendTestNotification(); message = "Test notification sent" }
                            catch (error: Exception) {
                                if (error is CancellationException) throw error
                                if (push.isForbidden(error)) testForbidden = true else message = "Couldn't send test: ${error.message ?: "desktop unreachable"}"
                            } finally { working = false }
                        }
                    }) { Text("Send test notification", color = TextPrimary) }
                }
                message?.let { Text(it, color = Warn, fontSize = 12.sp) }
                HorizontalDivider(color = Line)
                Text("Device", color = TextMuted, fontSize = 12.sp, fontWeight = FontWeight.Bold)
                ListItem(headlineContent = { Text("Paired desktop", color = TextPrimary, fontSize = 14.sp) },
                    supportingContent = { Text(host, color = TextMuted, fontSize = 12.sp) }, colors = ListItemDefaults.colors(containerColor = PanelRaised))
                ListItem(headlineContent = { Text("Connection", color = TextPrimary, fontSize = 14.sp) },
                    supportingContent = { Text(connection, color = TextMuted, fontSize = 12.sp) }, colors = ListItemDefaults.colors(containerColor = PanelRaised))
                TextButton(onClick = { confirming = true }) { Text("Unpair this device", color = Danger) }
            }
        }
    }
    if (confirming) AlertDialog(onDismissRequest = { confirming = false }, containerColor = PanelRaised,
        title = { Text("Unpair this device?", color = TextPrimary) },
        text = { Text("This phone will forget its Redline credential. To pair again, run redline pair --qr on your Mac and scan the new code.", color = TextMuted) },
        confirmButton = { TextButton(onClick = { confirming = false; onClose(); onUnpair() }) { Text("Unpair", color = Danger) } },
        dismissButton = { TextButton(onClick = { confirming = false }) { Text("Cancel", color = TextPrimary) } })
    if (rationale) AlertDialog(onDismissRequest = { rationale = false }, containerColor = PanelRaised,
        title = { Text("Notifications", color = TextPrimary) },
        text = { Text("Allow Redline to alert you about resets, firesales and jobs. If permission was previously denied, allow notifications in Android Settings.", color = TextMuted) },
        confirmButton = { TextButton(onClick = {
            rationale = false
            if (Build.VERSION.SDK_INT >= 33) permission.launch(Manifest.permission.POST_NOTIFICATIONS) else change(true)
        }) { Text("Continue", color = Accent) } },
        dismissButton = { TextButton(onClick = {
            rationale = false
            context.startActivity(Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS).putExtra(Settings.EXTRA_APP_PACKAGE, context.packageName))
        }) { Text("Android Settings", color = TextPrimary) } })
}
