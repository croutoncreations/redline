package com.croutoncreations.redline

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.TimeoutCancellationException
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeout

class NotificationActionReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (!BuildConfig.PUSH_ENABLED) return
        val id = intent.getLongExtra("id", -1)
        val action = intent.getStringExtra("action") ?: return
        val key = intent.getStringExtra("key") ?: return
        val pairing = intent.getStringExtra("pairing") ?: return
        if (id < 0 || action !in listOf("start", "stop")) return
        val pending = goAsync()
        CoroutineScope(Dispatchers.IO).launch {
            try {
                val settings = RedlineSettings(context)
                val holder = CoreClientHolder.shared(settings)
                val text = try {
                    withTimeout(9_000) { PushClient(context, settings, holder).firesaleAction(pairing, key, action) }
                    if (action == "start") "Firesale started" else "Firesale stopped"
                } catch (_: TimeoutCancellationException) {
                    "Request timed out; open Redline to check the result"
                } catch (error: Exception) {
                    "Firesale action failed: ${error.message ?: "desktop unreachable"}"
                }
                // An old PendingIntent must not resurrect a removed pairing's alert.
                if (settings.isPaired && pairing == PushClient.identity(settings)) {
                    PushNotifications(context).result(id, intent.getStringExtra("channel") ?: "firesale", intent.getStringExtra("title") ?: "Redline", text)
                }
            } catch (_: Exception) {
                Log.w("RedlinePush", "Notification action could not access local settings")
            } finally {
                pending.finish()
            }
        }
    }
}
