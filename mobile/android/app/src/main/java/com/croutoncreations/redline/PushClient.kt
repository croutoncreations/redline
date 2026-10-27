package com.croutoncreations.redline

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import com.google.firebase.messaging.FirebaseMessaging
import java.security.MessageDigest
import kotlin.coroutines.EmptyCoroutineContext
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.withContext
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

/** Plain preferences contain only delivery position, opt-in and token hashes. */
class PushClient(
    context: Context,
    private val settings: RedlineSettings,
    private val holder: CoreClientHolder,
) {
    private val context = context.applicationContext
    private val prefs = context.getSharedPreferences("redline.push", Context.MODE_PRIVATE)
    val enabled: Boolean get() = prefs.getBoolean("enabled", true)

    fun canNotify(): Boolean =
        (Build.VERSION.SDK_INT < 33 || ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED) &&
            NotificationManagerCompat.from(context).areNotificationsEnabled()

    // One lock across activity, FCM services and action receivers. A wake cannot
    // post against credentials which are being removed or a cursor being reset.
    companion object {
        private val lock = Mutex()

        fun identity(settings: RedlineSettings): String = hash(settings.token)

        private fun hash(value: String): String = MessageDigest.getInstance("SHA-256")
            .digest(value.toByteArray()).joinToString("") { "%02x".format(it) }
    }

    val lastRegistrationResult: PushRegistrationResult?
        get() {
            val at = prefs.getLong("registration_attempted_at", prefs.getLong("registered_at", 0))
            return if (at == 0L) null else PushRegistrationResult(at, prefs.getString("registration_error", null))
        }

    fun observeRegistration(onChange: () -> Unit): AutoCloseable {
        val listener = android.content.SharedPreferences.OnSharedPreferenceChangeListener { _, _ -> onChange() }
        prefs.registerOnSharedPreferenceChangeListener(listener)
        return AutoCloseable { prefs.unregisterOnSharedPreferenceChangeListener(listener) }
    }

    suspend fun sendTestNotification() = corePushCall { holder.client().sendTestNotification() }
    fun isForbidden(error: Exception): Boolean = core.Core.isForbidden(error)

    suspend fun refreshRegistration(token: String? = null) = lock.withLock {
        try {
            registerLocked(token)
        } catch (error: Exception) {
            if (error is kotlinx.coroutines.CancellationException) throw error
            prefs.edit().putLong("registration_attempted_at", System.currentTimeMillis())
                .putString("registration_error", error.message ?: "desktop unreachable").apply()
            throw error
        }
    }

    private suspend fun registerLocked(token: String?) {
        if (!BuildConfig.PUSH_ENABLED || !settings.isPaired) return
        if (prefs.getBoolean("unregister_pending", false)) unregisterLocked()
        if (!enabled) return
        if (!canNotify()) return
        upgradeLegacyCredentialLocked()
        FirebaseMessaging.getInstance().isAutoInitEnabled = true
        val currentToken = token ?: firebaseToken()
        val tokenHash = hash(currentToken)
        val now = System.currentTimeMillis()
        if (!PushRegistration.isDue(tokenHash, prefs.getString("registered_hash", null), prefs.getLong("registered_at", 0), now) &&
            prefs.getString("registration_error", null) == null) return
        val client = corePushCall { holder.client() }
        val cursor = cursor()
        if (cursor.lastSeenId == null) {
            cursor.initialise(corePushCall { client.latestNotificationID() })
            save(cursor)
        }
        corePushCall { client.registerPush("android", "fcm", currentToken) }
        prefs.edit().putString("registered_hash", tokenHash).putLong("registered_at", now)
            .putLong("registration_attempted_at", now).remove("registration_error").apply()
    }

    suspend fun setEnabled(value: Boolean) {
        lock.withLock {
            prefs.edit().putBoolean("enabled", value).apply()
            if (!value) {
                if (BuildConfig.PUSH_ENABLED) FirebaseMessaging.getInstance().isAutoInitEnabled = false
                prefs.edit().putBoolean("unregister_pending", true).apply()
                NotificationManagerCompat.from(context).cancelAll()
                if (settings.isPaired) unregisterLocked()
            }
        }
        if (value) refreshRegistration()
    }

    /**
     * Phones paired before device sessions hold the desktop's owner token, and
     * push registration needs a device session. Trade it in place, once: on
     * success the scoped token replaces the old one; a refusal (already a
     * device, or an older desktop) is remembered so it is not retried.
     */
    private suspend fun upgradeLegacyCredentialLocked() {
        if (prefs.getString("upgraded_identity", null) == identity(settings)) return
        val client = corePushCall { holder.client() }
        val scoped = runCatching { corePushCall { client.exchangeDeviceSession(android.os.Build.MODEL) } }.getOrNull()
        if (scoped != null) {
            withContext(NonCancellable + Dispatchers.IO) {
                holder.dropRelay()
                settings.update(settings.baseUrl, scoped)
            }
        }
        prefs.edit().putString("upgraded_identity", identity(settings)).apply()
    }

    private suspend fun unregisterLocked() {
        val client = corePushCall { holder.client() }
        corePushCall { client.unregisterPush() }
        prefs.edit().remove("registered_hash").remove("registered_at").remove("unregister_pending")
            .remove("registration_attempted_at").remove("registration_error").apply()
    }

    /** DELETE uses the old credential; forgetting it first would make it fail. */
    suspend fun unpair() = lock.withLock {
        try {
            if (settings.isPaired) unregisterLocked()
        } finally {
            if (BuildConfig.PUSH_ENABLED) FirebaseMessaging.getInstance().isAutoInitEnabled = false
            prefs.edit().clear().apply()
            NotificationManagerCompat.from(context).cancelAll()
            withContext(NonCancellable + Dispatchers.IO) {
                holder.dropRelay()
                settings.clear()
            }
        }
    }

    suspend fun fetchWake() = lock.withLock {
        if (!BuildConfig.PUSH_ENABLED || !settings.isPaired || !enabled || !canNotify()) return@withLock
        val client = corePushCall { holder.client() }
        val cursor = cursor()
        if (cursor.lastSeenId == null) {
            cursor.initialise(corePushCall { client.latestNotificationID() })
            save(cursor)
            return@withLock
        }
        // Drain full pages while the service's outer deadline permits it.
        do {
            val raw = corePushCall { client.fetchNotificationsAfter(cursor.lastSeenId!!, 50) }
            val deliveries = redlineJson.decodeFromString<List<PushDelivery>>(raw)
            var advanced = false
            for (delivery in deliveries) {
                currentCoroutineContext().ensureActive()
                if (!cursor.shouldNotify(delivery.id)) continue
                PushNotifications(context).post(delivery, identity(settings))
                currentCoroutineContext().ensureActive()
                cursor.advance(delivery.id)
                save(cursor)
                advanced = true
            }
        } while (deliveries.size == 50 && advanced)
    }

    suspend fun firesaleAction(identity: String, target: String, action: String, kind: String = "announcement", until: String = "15m") = lock.withLock {
        check(settings.isPaired && identity == PushClient.identity(settings)) { "This notification belongs to a previous pairing" }
        require(target.isNotBlank() && action in listOf("start", "stop") && kind in listOf("announcement", "manual")) { "Invalid firesale action" }
        val client = corePushCall { holder.client() }
        corePushCall {
            when {
                kind == "announcement" -> client.firesaleAction(target, action)
                action == "start" -> client.startFiresale(target, until)
                else -> client.stopFiresale(target)
            }
        }
    }

    private suspend fun firebaseToken(): String = suspendCancellableCoroutine { continuation ->
        FirebaseMessaging.getInstance().token.addOnCompleteListener { task ->
            if (task.isSuccessful) continuation.resumeWith(Result.success(task.result))
            else continuation.resumeWith(Result.failure(IllegalStateException("Could not get a push token")))
        }
    }

    private fun cursor() = PushCursor(if (prefs.contains("last_seen_id")) prefs.getLong("last_seen_id", 0) else null)
    private fun save(cursor: PushCursor) {
        cursor.lastSeenId?.let { prefs.edit().putLong("last_seen_id", it).apply() }
    }
}

/**
 * gomobile calls block and do not honour coroutine cancellation. Await their
 * result separately so the FCM/receiver deadline can return in nine seconds.
 * A timed-out read may finish later, but cannot post or advance the cursor.
 */
internal suspend fun <T> corePushCall(block: () -> T): T = suspendCancellableCoroutine { continuation ->
    Dispatchers.IO.dispatch(EmptyCoroutineContext) {
        continuation.resumeWith(runCatching(block))
    }
}
