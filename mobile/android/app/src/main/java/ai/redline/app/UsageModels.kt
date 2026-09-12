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
     * How old the oldest provider's numbers are.
     *
     * The stream pushes every few seconds but the collector polls the provider
     * every few minutes, so a connected stream can sit above numbers that are
     * minutes old.
     */
    @SerialName("sampled_age_seconds") val sampledAgeSeconds: Int = 0,
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
    /** Where Redline will act on the meters. Null from a desktop that sends no policy. */
    val scheduling: Scheduling? = null,
)

/**
 * The scheduler's lines on the bars, interpreted by the core so both platforms
 * draw the same ones for the same policy.
 */
@Serializable
data class Scheduling(
    /** Below this much of the 5-hour window, Redline never dispatches. */
    @SerialName("reserve_percent") val reservePercent: Int = 0,
    /** Lines on the weekly bar, soonest to arm first. */
    @SerialName("weekly_floors") val weeklyFloors: List<WeeklyFloor> = emptyList(),
    val decision: String = "",
    val reason: String = "",
    @SerialName("projected_trigger_at") val projectedTriggerAt: String = "",
) {
    /** The floor that is live now, if any: the one the scheduler is actually holding to. */
    val armedFloor: WeeklyFloor? get() = weeklyFloors.lastOrNull { it.armed }

    /** The next floor to arm, for saying what is coming. */
    val nextFloor: WeeklyFloor? get() = weeklyFloors.firstOrNull { !it.armed }
}

@Serializable
data class WeeklyFloor(
    val percent: Int = 0,
    @SerialName("arms_in_seconds") val armsInSeconds: Long = 0,
    val armed: Boolean = false,
)

@Serializable
data class Window(
    @SerialName("remaining_percent") val remainingPercent: Int = 0,
    @SerialName("resets_in_seconds") val resetsInSeconds: Long = 0,
    @SerialName("resets_at") val resetsAt: String = "",
    @SerialName("reset_inferred") val resetInferred: Boolean = false,
    /**
     * How far through the window now is, 0-100. Zero from an older desktop
     * that does not send it, which draws the pace mark at the full end where
     * it says nothing wrong.
     */
    @SerialName("elapsed_percent") val elapsedPercent: Int = 0,
)

@Serializable
data class Pool(
    val key: String = "",
    val label: String = "",
    val scope: String = "",
    /** short or weekly; explicit so the screen never guesses from the label. */
    val role: String = "",
    /** The protected floor for a model short pool such as Spark. */
    @SerialName("reserve_percent") val reservePercent: Int = 0,
    @SerialName("remaining_percent") val remainingPercent: Int = 0,
    @SerialName("resets_in_seconds") val resetsInSeconds: Long = 0,
    @SerialName("resets_at") val resetsAt: String = "",
    @SerialName("reset_inferred") val resetInferred: Boolean = false,
    @SerialName("elapsed_percent") val elapsedPercent: Int = 0,
)

/**
 * Where the remaining bar's edge would sit if the window were being spent
 * evenly: what is left of the time is what would be left of the allowance.
 */
fun paceMarkPercent(elapsedPercent: Int): Int = (100 - elapsedPercent).coerceIn(0, 100)

/** How the spend compares with the clock. */
enum class Pace { AHEAD, ON_PACE, BEHIND }

/**
 * A few points either side of the mark is on pace: both numbers are rounded,
 * and a mark that flipped colour on every percent would read as noise rather
 * than as a warning.
 */
private const val PACE_TOLERANCE = 5

fun paceOf(remainingPercent: Int, elapsedPercent: Int): Pace {
    // A window that has run out has no pace left to be on or off. The numbers
    // are the last sample before the reset, and "behind" against a mark at
    // zero would call every snapshot taken in the final minutes a warning.
    if (elapsedPercent >= 100) return Pace.ON_PACE
    val mark = paceMarkPercent(elapsedPercent)
    return when {
        remainingPercent > mark + PACE_TOLERANCE -> Pace.AHEAD
        remainingPercent < mark - PACE_TOLERANCE -> Pace.BEHIND
        else -> Pace.ON_PACE
    }
}

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
 * The line under a provider's meters that says what Redline is doing with it.
 *
 * The zones on the bars show where the scheduler's lines are; this says which
 * side of them the provider is on and, when waiting, what it is waiting for --
 * in the order a person would ask: is it running? if not, when will it? if
 * that is unknown, what is the next thing that changes? Assembled here so the
 * words and the drawn lines cannot disagree.
 *
 * @param formatTime turns the projected RFC 3339 instant into a short local
 *   time; injected so the sentence is testable without a clock or a zone.
 */
fun schedulingLine(
    scheduling: Scheduling,
    formatTime: (String) -> String = { formatResetAt(it) },
): String {
    if (scheduling.decision.isEmpty()) return ""
    if (scheduling.decision == "ADMIT") return "Dispatching · ${scheduling.reason}"

    if (scheduling.projectedTriggerAt.isNotEmpty()) {
        val at = formatTime(scheduling.projectedTriggerAt)
        if (at.isNotEmpty()) return "Waiting · jobs from $at if usage stays flat"
    }
    scheduling.armedFloor?.let { return "Waiting · runs while weekly stays above ${it.percent}%" }
    scheduling.nextFloor?.let {
        return "Waiting · ${it.percent}% floor arms in ${formatCountdown(it.armsInSeconds)}"
    }
    return "Waiting · ${scheduling.reason}"
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
