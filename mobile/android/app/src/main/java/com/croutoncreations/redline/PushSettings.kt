package com.croutoncreations.redline

import android.Manifest
import android.os.Build
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch

/** Permission is offered after pairing, never on an unpaired cold launch. */
@Composable
fun PushSettings(push: PushClient, newlyPaired: Boolean, open: Boolean, onClose: () -> Unit) {
    val scope = rememberCoroutineScope()
    var enabled by remember { mutableStateOf(push.enabled && push.canNotify()) }
    var working by remember { mutableStateOf(false) }
    var message by remember { mutableStateOf<String?>(null) }
    var rationale by remember { mutableStateOf(false) }

    fun change(value: Boolean) {
        scope.launch {
            working = true
            message = null
            try {
                push.setEnabled(value)
                enabled = value
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                enabled = push.enabled && push.canNotify()
                message = if (value) "Registration failed; try again or reopen Redline." else "Notifications are off; removal will retry on app start."
            } finally {
                working = false
            }
        }
    }

    val permission = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        change(granted)
        if (!granted) message = "Allow notifications in Android Settings to enable alerts."
    }

    LaunchedEffect(newlyPaired) {
        if (newlyPaired && push.enabled && Build.VERSION.SDK_INT >= 33 && !push.canNotify()) {
            rationale = true
        } else {
            try {
                push.refreshRegistration()
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                message = "Registration failed; try again or reopen Redline."
            }
        }
    }

    if (open) {
        AlertDialog(
            onDismissRequest = onClose,
            title = { Text("Settings") },
            text = {
                Column {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text("Notifications")
                        Switch(
                            checked = enabled,
                            enabled = !working,
                            onCheckedChange = { value ->
                                if (value && Build.VERSION.SDK_INT >= 33 && !push.canNotify()) rationale = true
                                else change(value)
                            },
                        )
                    }
                    message?.let { Text(it) }
                }
            },
            confirmButton = { TextButton(onClick = onClose) { Text("Done") } },
        )
    }

    if (rationale) {
        AlertDialog(
            onDismissRequest = { rationale = false; change(false) },
            title = { Text("Notifications") },
            text = { Text("Allow Redline to alert you about resets, firesales and jobs.") },
            confirmButton = {
                TextButton(onClick = {
                    rationale = false
                    if (Build.VERSION.SDK_INT >= 33) permission.launch(Manifest.permission.POST_NOTIFICATIONS)
                    else change(true)
                }) { Text("Continue") }
            },
            dismissButton = {
                TextButton(onClick = { rationale = false; change(false) }) { Text("Not now") }
            },
        )
    }
}
