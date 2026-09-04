package ai.redline.app

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

/**
 * Mirrors the JSON produced by the shared Go core.
 *
 * The core owns interpretation — percentages, countdowns, and which pools to
 * show — so this layer only decodes and renders. Anything that looks like a
 * calculation here probably belongs in mobile/core instead, where it is tested
 * once for both platforms.
 */
@Serializable
data class UsageView(
    @SerialName("generated_at") val generatedAt: String = "",
    val providers: List<ProviderUsage> = emptyList(),
    /** Whether the scheduler itself is working, shown as a header pill. */
    val health: HealthView? = null,
    /**
     * Whether this response crossed the relay rather than the tailnet.
     *
     * Part of the payload rather than asked of the client afterwards: three
     * view models share one client, so a flag on the client is last-write-wins
     * and another screen's refresh could change what this one reports.
     */
    val relayed: Boolean = false,
)

@Serializable
data class ProviderUsage(
    val id: String = "",
    val provider: String = "",
    val paused: Boolean = false,
    val stale: Boolean = false,
    val error: String = "",
    /** Where the numbers came from and how fresh they are. */
    @SerialName("source_label") val sourceLabel: String = "",
    val session: Window? = null,
    /**
     * The five hour window exists but its number could not be read.
     *
     * Absent from an older desktop, hence the default: the row is then simply
     * missing, as it was before, rather than making a claim either way.
     */
    @SerialName("session_unknown") val sessionUnknown: Boolean = false,
    /**
     * Quota resets the account can spend on demand to refill an exhausted
     * window. Null when the provider does not report them, which is not the
     * same as having none.
     */
    @SerialName("banked_resets") val bankedResets: Int? = null,
    val weekly: Window? = null,
    val pools: List<Pool> = emptyList(),
)

@Serializable
data class Window(
    @SerialName("remaining_percent") val remainingPercent: Int = 0,
    @SerialName("resets_in_seconds") val resetsInSeconds: Long = 0,
    @SerialName("resets_at") val resetsAt: String = "",
    @SerialName("reset_inferred") val resetInferred: Boolean = false,
)

@Serializable
data class Pool(
    val key: String = "",
    val label: String = "",
    val scope: String = "",
    @SerialName("remaining_percent") val remainingPercent: Int = 0,
    @SerialName("resets_in_seconds") val resetsInSeconds: Long = 0,
    @SerialName("resets_at") val resetsAt: String = "",
    @SerialName("reset_inferred") val resetInferred: Boolean = false,
)

/** Lenient so a newer desktop adding fields cannot break an older app. */
val redlineJson: Json = Json { ignoreUnknownKeys = true }

/**
 * Formats a countdown for display, e.g. "5h 12m" or "1d 6h".
 *
 * Each unit keeps the one below it. Collapsing to a bare "1 day" covered
 * everything from 24 to 47 hours, which is a 23 hour ambiguity on exactly the
 * number someone is planning around. Minutes are dropped past a day, where
 * they are noise beside the hours.
 *
 * Kept as a pure function so it is unit-testable without a device.
 */
fun formatCountdown(seconds: Long): String {
    if (seconds <= 0) return "now"
    val minutes = seconds / 60
    val hours = minutes / 60
    val days = hours / 24
    return when {
        days >= 1 -> {
            val remainingHours = hours % 24
            if (remainingHours == 0L) "${days}d" else "${days}d ${remainingHours}h"
        }
        hours >= 1 -> {
            val remainingMinutes = minutes % 60
            if (remainingMinutes == 0L) "${hours}h" else "${hours}h ${remainingMinutes}m"
        }
        minutes >= 1 -> "${minutes}m"
        else -> "under a minute"
    }
}

/**
 * The one-line status shown under a provider's name.
 *
 * Order matters: an error means the numbers are absent, and paused or stale
 * both mean the numbers should not be read as current.
 */
fun providerStatus(provider: ProviderUsage): String = when {
    provider.error.isNotEmpty() -> "No data"
    provider.paused -> "Paused"
    provider.stale -> "Stale"
    else -> "Live"
}

/**
 * Formats when a window reopens, in the reader's own timezone.
 *
 * A countdown answers "how long"; this answers "when", which is the question
 * you match against a calendar. Showing both covers the two ways people hold a
 * future time in their head, and the phone has room for it.
 *
 * The wording narrows as the date approaches: a weekday is meaningless for
 * something later today, and ambiguous beyond a week.
 *
 * @param resetsAt an RFC 3339 timestamp from the core, or "" when unknown
 * @param zone the display timezone, injected so tests do not depend on the
 *   machine's own
 * @param now the instant to measure from, injected for the same reason
 */
fun formatResetAt(
    resetsAt: String,
    zone: java.time.ZoneId = java.time.ZoneId.systemDefault(),
    now: java.time.Instant = java.time.Instant.now(),
): String {
    // A malformed or absent timestamp is not worth crashing over, and showing
    // nothing beats showing junk beside a real countdown.
    val instant = runCatching { java.time.Instant.parse(resetsAt) }.getOrNull() ?: return ""

    val target = instant.atZone(zone)
    val today = now.atZone(zone).toLocalDate()
    val resetDay = target.toLocalDate()
    val daysAhead = java.time.temporal.ChronoUnit.DAYS.between(today, resetDay)

    val time = target.format(java.time.format.DateTimeFormatter.ofPattern("h:mm a"))
    return when {
        daysAhead <= 0L -> time
        daysAhead == 1L -> "Tomorrow $time"
        // Within the week a weekday is the most natural handle.
        daysAhead < 7L -> target.format(java.time.format.DateTimeFormatter.ofPattern("EEE")) + " $time"
        // Beyond that "Friday" could be any of several, so name the date.
        else -> target.format(java.time.format.DateTimeFormatter.ofPattern("MMM d,")) + " $time"
    }
}

/**
 * Builds the whole "Resets ..." line.
 *
 * The countdown and the sentence around it have to be decided together. Left
 * apart, a value that reads correctly on its own becomes wrong in place: a
 * reset that is due produced "Resets in now", and an inferred one produced
 * "Resets ~now". A window about to roll over is the moment someone is most
 * likely to be reading this line, so it is the worst place to sound broken.
 */
fun resetLabel(seconds: Long, inferred: Boolean): String {
    // Under a minute there is nothing useful left to count, and "in 12s" is a
    // precision the upstream data does not really have.
    if (seconds < 60) {
        return if (inferred) "Resets about now" else "Resets now"
    }
    val countdown = formatCountdown(seconds)
    return if (inferred) "Resets ~$countdown" else "Resets in $countdown"
}
