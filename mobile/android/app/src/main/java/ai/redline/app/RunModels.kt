package ai.redline.app

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * The runs screen, as produced by the Go core.
 *
 * These mirror the core's view types rather than the service's own records:
 * the interpretation (durations, relative times, success) is already applied,
 * so this layer only renders.
 */
@Serializable
data class RunListView(
    val runs: List<RunSummary> = emptyList(),
    @SerialName("total_count") val totalCount: Int = 0,
    @SerialName("failed_count") val failedCount: Int = 0,
    @SerialName("running_count") val runningCount: Int = 0,
)

@Serializable
data class RunSummary(
    val id: String,
    @SerialName("short_id") val shortId: String = "",
    @SerialName("task_id") val taskId: String = "",
    /** The task's human name, or its id when the task no longer exists. */
    val name: String = "",
    /** The harness and model that actually ran. */
    @SerialName("meta_label") val metaLabel: String = "",
    val state: String = "",
    val outcome: String = "",
    @SerialName("exit_code") val exitCode: Int = 0,
    val running: Boolean = false,
    val succeeded: Boolean = false,
    @SerialName("relative_label") val relativeLabel: String = "",
    @SerialName("duration_label") val durationLabel: String = "",
    val summary: String = "",
    val error: String = "",
    @SerialName("pull_request_url") val pullRequestUrl: String = "",
)

@Serializable
data class TaskListView(val tasks: List<TaskSummary> = emptyList())

@Serializable
data class TaskSummary(
    val id: String,
    val name: String = "",
    val type: String = "",
    val state: String = "",
    val enabled: Boolean = false,
    val dispatchable: Boolean = false,
)

/**
 * The outcome of asking the service to run a task now.
 *
 * Dispatch has three distinct answers and collapsing them would mislead: a run
 * started, the scheduler held back, or the request was refused. Only [started]
 * means something is now happening.
 */
@Serializable
data class DispatchView(
    val started: Boolean = false,
    val refused: Boolean = false,
    @SerialName("run_id") val runId: String = "",
    val reason: String = "",
    val mode: String = "",
)
