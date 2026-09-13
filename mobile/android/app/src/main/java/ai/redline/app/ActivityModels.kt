package ai.redline.app

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/** A run's timeline, as produced by the Go core. */
@Serializable
data class RunEventsView(val events: List<RunEvent> = emptyList())

@Serializable
data class RunEvent(
    val type: String = "",
    /** The event type in words. */
    val label: String = "",
    /** How long after the run began this happened. */
    @SerialName("since_start_label") val sinceStartLabel: String = "",
)

/** Whether the scheduler is actually working. */
@Serializable
data class HealthView(
    val status: String = "",
    val degraded: Boolean = false,
    val detail: String = "",
    @SerialName("active_runs") val activeRuns: Int = 0,
)
