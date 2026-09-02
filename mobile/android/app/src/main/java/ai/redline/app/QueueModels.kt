package ai.redline.app

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/** The dispatch queue for one provider, as produced by the Go core. */
@Serializable
data class QueueView(
    @SerialName("provider_account_id") val providerAccountId: String = "",
    @SerialName("snapshot_label") val snapshotLabel: String = "",
    @SerialName("snapshot_stale") val snapshotStale: Boolean = false,
    @SerialName("provider_reason") val providerReason: String = "",
    @SerialName("dispatch_available") val dispatchAvailable: Boolean = false,
    @SerialName("next_up_task_id") val nextUpTaskId: String = "",
    @SerialName("next_up_name") val nextUpName: String = "",
    @SerialName("ready_count") val readyCount: Int = 0,
    @SerialName("blocked_count") val blockedCount: Int = 0,
    val candidates: List<QueueCandidate> = emptyList(),
)

@Serializable
data class QueueCandidate(
    @SerialName("task_id") val taskId: String,
    val name: String = "",
    val priority: Int = 0,
    val eligible: Boolean = false,
    val reason: String = "",
    @SerialName("is_next_up") val isNextUp: Boolean = false,
)
