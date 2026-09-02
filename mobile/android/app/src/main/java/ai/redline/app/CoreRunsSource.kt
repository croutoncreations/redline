package ai.redline.app

/**
 * [RunsSource] backed by the gomobile-bound Go core.
 *
 * Shares the client cache with [CoreUsageSource] rather than building a second
 * one, so both screens use the same credentials and a re-pair takes effect
 * everywhere at once.
 */
class CoreRunsSource(private val client: CoreClientHolder) : RunsSource {

    override fun fetchRunsJson(): String = client.client().fetchRuns()

    override fun fetchTasksJson(): String = client.client().fetchTasks()

    override fun fetchRunLogs(runId: String, stream: String): String =
        client.client().fetchRunLogs(runId, stream)

    override fun dispatchTask(taskId: String): String = client.client().dispatchTask(taskId)

    override fun isUnauthorized(error: Throwable): Boolean = client.isUnauthorized(error)
}
