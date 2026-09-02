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
)

@Serializable
data class ProviderUsage(
    val id: String = "",
    val provider: String = "",
    val paused: Boolean = false,
    val stale: Boolean = false,
    val error: String = "",
    val session: Window? = null,
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
 * Formats a countdown for display, e.g. "5h 12m" or "3 days".
 *
 * Kept as a pure function so it is unit-testable without a device.
 */
fun formatCountdown(seconds: Long): String {
    if (seconds <= 0) return "now"
    val minutes = seconds / 60
    val hours = minutes / 60
    val days = hours / 24
    return when {
        days >= 1 -> if (days == 1L) "1 day" else "$days days"
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
