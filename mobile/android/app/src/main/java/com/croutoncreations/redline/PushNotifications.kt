package com.croutoncreations.redline

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.net.Uri
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat

class PushNotifications(private val context: Context) {
    private val manager = context.getSystemService(NotificationManager::class.java)

    private fun channels() {
        manager.createNotificationChannels(listOf(
            NotificationChannel("resets", "Resets", NotificationManager.IMPORTANCE_HIGH),
            NotificationChannel("firesale", "Firesale", NotificationManager.IMPORTANCE_HIGH),
            NotificationChannel("jobs", "Jobs", NotificationManager.IMPORTANCE_DEFAULT),
        ))
    }

    fun post(delivery: PushDelivery, pairingIdentity: String) {
        val builder = builder(delivery.id, delivery.channel, delivery.displayTitle, delivery.message)
        val action = delivery.firesaleAction
        // The policy's ordering is shared with the JVM tests and the renderer.
        for (label in delivery.actionLabels) {
            val intent = if (label == "Open") open(delivery.id) else {
                checkNotNull(action)
                val intent = Intent(context, NotificationActionReceiver::class.java).apply {
                    data = uri(delivery.id, action.action)
                    putExtra("id", delivery.id)
                    putExtra("channel", delivery.channel)
                    putExtra("title", delivery.displayTitle)
                    putExtra("key", action.announcementKey)
                    putExtra("action", action.action)
                    putExtra("pairing", pairingIdentity)
                }
                PendingIntent.getBroadcast(context, requestCode(delivery.id, action.action), intent, flags)
            }
            builder.addAction(0, label, intent)
        }
        notify(delivery.id, builder)
    }

    fun result(id: Long, channel: String, title: String, text: String) {
        notify(id, builder(id, channel, title, text).addAction(0, "Open", open(id)))
    }

    private fun builder(id: Long, channel: String, title: String, text: String): NotificationCompat.Builder {
        channels()
        return NotificationCompat.Builder(context, channel)
            .setSmallIcon(R.drawable.redline_mark)
            .setContentTitle(title)
            .setContentText(text)
            .setStyle(NotificationCompat.BigTextStyle().bigText(text))
            .setContentIntent(open(id))
            .setAutoCancel(true)
            .setOnlyAlertOnce(true)
    }

    private fun notify(id: Long, builder: NotificationCompat.Builder) {
        if (NotificationManagerCompat.from(context).areNotificationsEnabled()) {
            manager.notify("delivery:$id", 0, builder.build())
        }
    }

    private fun open(id: Long): PendingIntent {
        val intent = Intent(context, MainActivity::class.java).apply {
            data = uri(id, "open")
            flags = Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP
        }
        return PendingIntent.getActivity(context, requestCode(id, "open"), intent, flags)
    }

    private fun uri(id: Long, action: String): Uri = Uri.parse("redline://notification/$id/$action")
    private fun requestCode(id: Long, action: String): Int = 31 * id.hashCode() + action.hashCode()
    private val flags = PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
}
