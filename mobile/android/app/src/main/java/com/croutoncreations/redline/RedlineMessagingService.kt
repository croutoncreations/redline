package com.croutoncreations.redline

import android.util.Log
import com.google.firebase.messaging.FirebaseMessagingService
import com.google.firebase.messaging.RemoteMessage
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout

class RedlineMessagingService : FirebaseMessagingService() {
    override fun onNewToken(token: String) {
        withinBudget { it.refreshRegistration(token) }
    }

    override fun onMessageReceived(message: RemoteMessage) {
        if (message.data["type"] == "wake") withinBudget { it.fetchWake() }
    }

    private fun withinBudget(work: suspend (PushClient) -> Unit) {
        if (!BuildConfig.PUSH_ENABLED) return
        try {
            runBlocking {
                withTimeout(9_000) {
                    val settings = corePushCall { RedlineSettings(applicationContext) }
                    work(PushClient(applicationContext, settings, CoreClientHolder.shared(settings)))
                }
            }
        } catch (_: Exception) {
            // Never log registration tokens, FCM payloads or endpoint bodies.
            Log.w("RedlinePush", "Push work did not complete; retry on the next wake or app start")
        }
    }
}
