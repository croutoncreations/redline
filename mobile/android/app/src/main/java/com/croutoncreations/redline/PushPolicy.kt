package com.croutoncreations.redline

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

data class PushAction(
    val action: String,
    val target: String,
    val label: String,
    val kind: String = "announcement",
    val until: String = "15m",
)

@Serializable
data class PushDelivery(
    val id: Long,
    @SerialName("event_type") val eventType: String,
    val title: String,
    val message: String,
    val data: Map<String, String> = emptyMap(),
) {
    val channel: String get() = when {
        eventType.startsWith("reset.") -> "resets"
        eventType.startsWith("firesale.") -> "firesale"
        else -> "jobs"
    }

    val displayTitle: String get() =
        if (data["test"] == "true" && !title.startsWith("Test", ignoreCase = true)) "Test: $title" else title

    val firesaleAction: PushAction? get() = when {
        eventType == "firesale.started" && data["trigger"] == "manual" ->
            data["account"]?.takeIf { it.isNotBlank() }?.let { PushAction("stop", it, "Stop firesale", "manual") }
        !data["manual_account"].isNullOrBlank() ->
            PushAction("start", data.getValue("manual_account"), "Start firesale", "manual", data["manual_for"] ?: "15m")
        eventType == "reset.announced" && data["kind"] in listOf("PROMISED", "SCHEDULED") && data["retracted"] != "true" ->
            data["key"]?.takeIf { it.isNotBlank() }?.let { PushAction("start", it, "Start firesale") }
        eventType == "firesale.started" ->
            data["announcement_key"]?.takeIf { it.isNotBlank() }?.let { PushAction("stop", it, "Stop firesale") }
        else -> null
    }

    val actionLabels: List<String> get() = listOfNotNull(firesaleAction?.label, "Open")
}

/** A missing cursor is different from an initialised empty desktop (id zero). */
class PushCursor(var lastSeenId: Long?) {
    fun initialise(newestId: Long) {
        if (lastSeenId == null) lastSeenId = newestId
    }

    fun shouldNotify(id: Long): Boolean = lastSeenId?.let { id > it } ?: false

    fun advance(id: Long) {
        lastSeenId?.let { lastSeenId = maxOf(it, id) }
    }
}

object PushRegistration {
    fun isDue(tokenHash: String, registeredHash: String?, registeredAt: Long, now: Long): Boolean =
        tokenHash != registeredHash || now < registeredAt || now - registeredAt >= 7 * 24 * 60 * 60 * 1000L
}
