package com.croutoncreations.redline

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
    /** Cursor's usage card, when the Mac tracks it. Null otherwise. */
    val cursor: CursorUsage? = null,
)

/**
 * Cursor's usage card, interpreted by the core: monthly model pools, Grok
 * Bot's weekly allowance, on-demand spend and credits, kept apart. Every
 * label is already worded; this layer only lays it out.
 */
@Serializable
data class CursorUsage(
    val plan: String = "",
    val state: String = "",
    /** healthy, near or paused. */
    val tone: String = "",
    /** Past the source's refresh: every value is the last known one. */
    val stale: Boolean = false,
    @SerialName("updated_label") val updatedLabel: String = "",
    /** Whether the Mac also schedules Cursor jobs from these pools. */
    val scheduled: Boolean = false,
    /** What this card is and is not used for. */
    val note: String = "",
    val errors: List<String> = emptyList(),
    @SerialName("not_reported") val notReported: List<String> = emptyList(),
    val groups: List<CursorGroup> = emptyList(),
    val balances: List<CursorBalance> = emptyList(),
)

@Serializable
data class CursorGroup(
    val key: String = "",
    val heading: String = "",
    val meters: List<CursorMeter> = emptyList(),
)

@Serializable
data class CursorMeter(
    val key: String = "",
    val label: String = "",
    @SerialName("value_label") val valueLabel: String = "",
    /** False when Cursor gave no limit: show the spend, never a bar. */
    @SerialName("has_limit") val hasLimit: Boolean = false,
    /** What is left, 0-100, like every provider meter. */
    @SerialName("remaining_percent") val remainingPercent: Int = 0,
    val exhausted: Boolean = false,
    /** Empty when the reset was not reported; never made up. */
    @SerialName("resets_at") val resetsAt: String = "",
    @SerialName("resets_in_seconds") val resetsInSeconds: Long = 0,
)

@Serializable
data class CursorBalance(
    val key: String = "",
    val label: String = "",
    @SerialName("value_label") val valueLabel: String = "",
    @SerialName("next_expires_at") val nextExpiresAt: String = "",
)

/**
 * The line under a Cursor meter. A used-up pool says when it is available
 * again; one with no reported reset says so rather than inventing a date.
 */
fun cursorMeterDetail(
    meter: CursorMeter,
    formatTime: (String) -> String = { formatResetAt(it) },
): String {
    if (meter.resetsAt.isEmpty()) {
        return if (meter.hasLimit) "Reset date not reported" else "No limit reported"
    }
    val countdown = formatCountdown(meter.resetsInSeconds)
    val at = formatTime(meter.resetsAt)
    val lead = if (meter.exhausted) "Available again" else "Resets"
    val timing = if (meter.resetsInSeconds <= 0) "$lead now" else "$lead in $countdown"
    return if (at.isEmpty()) timing else "$timing · $at"
}

@Serializable
data class ProviderUsage(
    val id: String = "",
    val provider: String = "",
    /** The owner's name for the account; the core falls back to the id. Never an email. */
    val label: String = "",
    /** The plan as the source reported it ("Max 20x", "Team"); empty when not reported. */
    val plan: String = "",
    /** ok, stale, no_data, signed_out, error, or unknown from an older desktop. */
    @SerialName("account_status") val accountStatus: String = "",
    /** 1-based position among this provider's accounts, in the desktop's order. */
    @SerialName("account_index") val accountIndex: Int = 1,
    /** How many accounts this provider has. Two or more get the accounts carousel. */
    @SerialName("account_count") val accountCount: Int = 1,
    val paused: Boolean = false,
    val stale: Boolean = false,
    val error: String = "",
    /**
     * Why a stale provider is stale, in one line, when the Mac knows a cause
     * a person can fix -- its CLI is signed out. Empty otherwise.
     */
    @SerialName("stale_reason") val staleReason: String = "",
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
    /**
     * The same balance with each credit's own deadline. A credit's deadline is
     * not the allowance's reset and is never worded as one. Null exactly when
     * [bankedResets] is.
     */
    @SerialName("reset_credits") val resetCredits: ResetCredits? = null,
    val weekly: Window? = null,
    /**
     * The source reports no account-wide weekly limit for this account
     * (OpenUsage Team seats). [weekly] is null then; the screen says "not
     * reported" and shows nothing in its place.
     */
    @SerialName("weekly_not_reported") val weeklyNotReported: Boolean = false,
    @SerialName("weekly_policy") val weeklyPolicy: String = "",
    @SerialName("session_reserve") val sessionReserve: Double = 0.0,
    /**
     * "Weekly allowance", or "Monthly allowance" for Cursor. Empty from an
     * older desktop, where every account window was weekly.
     */
    @SerialName("weekly_label") val weeklyLabel: String = "",
    val pools: List<Pool> = emptyList(),
    /** Where Redline will act on the meters. Null from a desktop that sends no policy. */
    val scheduling: Scheduling? = null,
)

@Serializable
data class ResetCredits(
    val available: Int = 0,
    /** Each credit's deadline, soonest first; fewer than [available] when some had none. */
    val expiries: List<String> = emptyList(),
    @SerialName("next_expires_in_seconds") val nextExpiresInSeconds: Long = 0,
)

/** The display name for one account: its label, or its id from an older desktop. */
val ProviderUsage.accountName: String get() = label.ifBlank { id }

/** A used-up account: no weekly allowance left, so nothing can run on it until it resets. */
val ProviderUsage.exhausted: Boolean get() = weekly?.remainingPercent == 0

val ProviderUsage.dispatching: Boolean get() = scheduling?.admits == true

/**
 * One entry on the capacity screen: a lone account keeps today's card, and a
 * provider with several accounts becomes one carousel, at the position its
 * first account had, so the cards around it do not reorder.
 */
sealed interface UsageItem {
    val key: String
    data class Single(val provider: ProviderUsage) : UsageItem {
        override val key: String get() = provider.id
    }
    data class AccountGroup(val provider: String, val accounts: List<ProviderUsage>) : UsageItem {
        override val key: String get() = "accounts:$provider"
    }
}

fun usageItems(providers: List<ProviderUsage>): List<UsageItem> {
    val byType = providers.groupBy { it.provider.lowercase() }
    val emitted = mutableSetOf<String>()
    val items = mutableListOf<UsageItem>()
    for (provider in providers) {
        val type = provider.provider.lowercase()
        val accounts = byType.getValue(type)
        if (accounts.size < 2) {
            items += UsageItem.Single(provider)
        } else if (emitted.add(type)) {
            items += UsageItem.AccountGroup(type, accounts)
        }
    }
    return items
}

/**
 * The carousel's position label. Says the position in words, so it is never
 * conveyed by the dots' colour alone.
 */
fun carouselPositionLabel(page: Int, accounts: Int): String =
    if (page <= 0) "Overview · swipe left for each account" else "Account $page of $accounts"

/** The header line: how many accounts, and how many Redline is dispatching on. */
fun accountGroupSummary(provider: String, accounts: List<ProviderUsage>): String {
    // The same rule as each account's own badge, so the header never counts
    // an account as dispatching that says it is paused or out of date.
    val dispatching = accounts.count { accountDispatchState(it) == "dispatching" }
    return "${providerDisplayName(provider)} · ${accounts.size} accounts · $dispatching of ${accounts.size} dispatching"
}

/** What TalkBack announces when an account page comes into view. */
fun accountAnnouncement(provider: String, account: ProviderUsage, page: Int, accounts: Int): String = buildString {
    append("${providerDisplayName(provider)} account $page of $accounts, ")
    append(account.accountName)
    if (account.plan.isNotBlank()) append(", ${account.plan} plan")
}

/** Numbers that should not be read as current. */
val ProviderUsage.isOutOfDate: Boolean get() = stale || accountStatus == "stale"

/** Dispatch state in a few words, kept apart from usage. */
fun accountDispatchState(account: ProviderUsage): String = when {
    account.accountStatus == "signed_out" -> "needs sign-in"
    account.paused -> "paused"
    account.stale || account.accountStatus == "stale" -> "out of date, dispatch held"
    account.accountStatus == "error" -> "usage unavailable, dispatch held"
    account.weeklyNotReported && account.weeklyPolicy == "session_only" -> "weekly not reported, session-only scheduling"
    account.weeklyNotReported && !account.dispatching -> "dispatch held, weekly not reported"
    account.accountStatus == "no_data" || account.weekly == null -> "no usage yet"
    account.dispatching -> "dispatching"
    account.scheduling?.decision.isNullOrEmpty() -> "no scheduler decision yet"
    else -> "dispatch held"
}

/**
 * The reset-credit line: how many one-off limit resets the account holds and
 * when the soonest lapses. Worded as "expires", never "resets", so it cannot
 * be read as the allowance's own reset time.
 *
 * @param reportedOnly when true (the single-account card), returns "" for an
 *   account that does not report credits, as that card always has; account
 *   pages pass false and say "Not reported" instead, since a blank there
 *   reads as "none".
 */
fun resetCreditsLine(
    provider: ProviderUsage,
    reportedOnly: Boolean = false,
    formatTime: (String) -> String = { formatResetAt(it) },
): String {
    val credits = provider.resetCredits
    val count = credits?.available ?: provider.bankedResets ?: return if (reportedOnly) "" else "Not reported"
    val base = if (count == 1) "1 available" else "$count available"
    if (credits == null || count == 0 || credits.expiries.isEmpty()) return base
    val next = formatTime(credits.expiries.first())
    return if (next.isEmpty()) base else "$base · next expires $next"
}

/**
 * Every credit's deadline, for the account page, or "" when there is at most
 * one deadline (the line above already says it).
 */
fun resetCreditExpiries(
    provider: ProviderUsage,
    formatTime: (String) -> String = { formatResetAt(it) },
): String {
    val credits = provider.resetCredits ?: return ""
    if (credits.expiries.size < 2 && credits.expiries.size >= credits.available) return ""
    val parts = credits.expiries.map(formatTime).filter { it.isNotEmpty() }.toMutableList()
    val undated = credits.available - credits.expiries.size
    if (undated > 0 && credits.expiries.isNotEmpty()) parts += "$undated with no expiry reported"
    return if (parts.isEmpty()) "" else "Credit deadlines: " + parts.joinToString(" · ")
}

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
    /**
     * Whether the last decision let work run. The desktop's value is "RUN"
     * (decision.Admit); "ADMIT" is accepted too, as earlier builds of this
     * screen expected it.
     */
    val admits: Boolean get() = decision == "RUN" || decision == "ADMIT"

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
    /**
     * The window's clock has not begun: nothing is used yet, and the provider
     * starts the countdown on first use. Full, with no reset time -- the
     * screen describes how the clock will run instead of counting down to a
     * time that does not exist yet.
     */
    @SerialName("not_started") val notStarted: Boolean = false,
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

/** The provider's name as people know it. */
fun providerDisplayName(provider: String): String = when (provider.lowercase()) {
    "claude" -> "Claude"
    "codex" -> "Codex"
    "cursor" -> "Cursor"
    else -> provider.replaceFirstChar { it.uppercase() }
}

/**
 * The one-line status shown under a provider's name.
 *
 * Order matters: an error means the numbers are absent, and paused or stale
 * both mean the numbers should not be read as current.
 */
fun providerStatus(provider: ProviderUsage): String = when {
    provider.stale && provider.staleReason.startsWith("Cursor CLI and app") -> "Accounts differ"
    provider.stale && provider.staleReason.isNotEmpty() -> "Signed out"
    provider.error.isNotEmpty() -> "No data"
    provider.paused -> "Paused"
    provider.stale -> "Stale"
    else -> "Live"
}

/**
 * The problem line under a provider's name, or "" when there is none. A
 * signed-out provider gets the short fix rather than the desktop's full
 * paragraph, which is written for a wider screen.
 */
fun providerProblem(provider: ProviderUsage): String =
    provider.staleReason.ifEmpty { provider.error }

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
    if (scheduling.admits) return "Dispatching · ${scheduling.reason}"

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
/**
 * The sentence under a window's bar.
 *
 * A window that has not started has no reset to count down to, so it says
 * how its clock actually runs -- it starts on first use and resets five hours
 * after -- rather than a made-up time.
 */
fun windowResetSentence(window: Window): String =
    if (window.notStarted) {
        "Starts on first use · resets 5h after"
    } else {
        resetLabel(window.resetsInSeconds, window.resetInferred)
    }

/**
 * The reset line for numbers that are out of date.
 *
 * A countdown ticks, and a ticking line reads as live even beside "was 41%".
 * So stale numbers never get one: they name the reset time the source last
 * reported, fixed. Once that time has passed they say the reset was due and
 * fresh numbers are awaited -- never "Resets now" or a new countdown, which
 * would claim a refresh nobody has seen. Due-ness is decided from the reset
 * instant against the present clock, not from `resetsInSeconds`, which is a
 * countdown frozen at the last read.
 *
 * @param zone the display timezone, injected for tests
 * @param now the present instant, injected for tests
 */
fun outOfDateResetSentence(
    window: Window,
    zone: java.time.ZoneId = java.time.ZoneId.systemDefault(),
    now: java.time.Instant = java.time.Instant.now(),
    locale: java.util.Locale = java.util.Locale.getDefault(),
): String {
    if (window.notStarted) return "Not started when last read"
    val instant = runCatching { java.time.Instant.parse(window.resetsAt) }.getOrNull()
        ?: return "Reset time unknown while out of date"
    val at = fixedTimeLabel(instant, zone, now, locale)
    val approx = if (window.resetInferred) " (estimated)" else ""
    return if (!instant.isAfter(now)) {
        "Reset was due $at$approx · awaiting fresh numbers"
    } else {
        "Last reported reset $at$approx"
    }
}

/**
 * A banked credit's deadline as last reported, for out-of-date numbers: a
 * fixed time, and "expired" once it has passed -- never a bare time that
 * would read as later today.
 */
fun lastKnownCreditDeadline(
    expiresAt: String,
    zone: java.time.ZoneId = java.time.ZoneId.systemDefault(),
    now: java.time.Instant = java.time.Instant.now(),
    locale: java.util.Locale = java.util.Locale.getDefault(),
): String {
    val instant = runCatching { java.time.Instant.parse(expiresAt) }.getOrNull() ?: return ""
    val at = fixedTimeLabel(instant, zone, now, locale)
    return if (instant.isAfter(now)) at else "expired $at"
}

/** What replaces the scheduler line while an account's numbers are out of date. */
const val OUT_OF_DATE_DISPATCH_LINE = "Scheduler waits for fresh numbers"

/**
 * How many of an account's last-known credits have a deadline that has since
 * passed. Undated credits never count as expired.
 */
fun expiredCreditCount(
    provider: ProviderUsage,
    now: java.time.Instant = java.time.Instant.now(),
): Int = provider.resetCredits?.expiries.orEmpty().count { expiry ->
    runCatching { java.time.Instant.parse(expiry) }.getOrNull()?.let { !it.isAfter(now) } ?: false
}

/**
 * A reset instant as a fixed local time, past or future, never ambiguous: a
 * bare "9:00 AM" is today's only, other days are named.
 */
fun fixedTimeLabel(
    instant: java.time.Instant,
    zone: java.time.ZoneId = java.time.ZoneId.systemDefault(),
    now: java.time.Instant = java.time.Instant.now(),
    locale: java.util.Locale = java.util.Locale.getDefault(),
): String {
    fun pattern(p: String) = java.time.format.DateTimeFormatter.ofPattern(p, locale)
    val target = instant.atZone(zone)
    val days = java.time.temporal.ChronoUnit.DAYS.between(now.atZone(zone).toLocalDate(), target.toLocalDate())
    val time = target.format(pattern("h:mm a"))
    return when {
        days == 0L -> "today $time"
        days == 1L -> "tomorrow $time"
        days == -1L -> "yesterday $time"
        days in 2L..6L -> target.format(pattern("EEE")) + " $time"
        // Old stale data can be from another year; say so.
        target.year != now.atZone(zone).year ->
            target.format(pattern("MMM d yyyy,")) + " $time"
        else -> target.format(pattern("MMM d,")) + " $time"
    }
}

fun resetLabel(seconds: Long, inferred: Boolean): String {
    // Under a minute there is nothing useful left to count, and "in 12s" is a
    // precision the upstream data does not really have.
    if (seconds < 60) {
        return if (inferred) "Resets about now" else "Resets now"
    }
    val countdown = formatCountdown(seconds)
    return if (inferred) "Resets ~$countdown" else "Resets in $countdown"
}
