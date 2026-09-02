package ai.redline.app

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import kotlin.coroutines.coroutineContext
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * Where run and task data comes from.
 *
 * An interface rather than the binding directly, so the view model is testable
 * on the JVM: the gomobile library needs a device to load its native code.
 */
interface RunsSource {
    fun fetchRunsJson(): String
    fun fetchTasksJson(): String
    fun fetchRunLogs(runId: String, stream: String): String
    fun dispatchTask(taskId: String): String
    fun isUnauthorized(error: Throwable): Boolean
}

data class RunsUiState(
    val loading: Boolean = false,
    val runs: RunListView? = null,
    val tasks: TaskListView? = null,
    val logs: String? = null,
    val logsLoading: Boolean = false,
    val dispatching: Boolean = false,
    val lastDispatch: DispatchView? = null,
    val failure: UsageUiState.Failure? = null,
) {
    val hasData: Boolean get() = runs != null

    /**
     * Records a failure and stops every spinner, so a failed dispatch or log
     * fetch cannot leave the screen looking busy forever.
     *
     * Reuses the usage screen's failure vocabulary because the distinction is
     * the same one: unreachable is fixed by waking the desktop, unauthorized by
     * pairing again.
     */
    fun fail(reason: UsageUiState.Failure): RunsUiState =
        copy(loading = false, logsLoading = false, dispatching = false, failure = reason)
}

class RunsViewModel(
    private val source: RunsSource,
    private val ioDispatcher: CoroutineDispatcher = Dispatchers.IO,
) : ViewModel() {

    private val _state = MutableStateFlow(RunsUiState())
    val state: StateFlow<RunsUiState> = _state.asStateFlow()

    private var refreshJob: Job? = null

    /**
     * Reloads runs and tasks.
     *
     * Runs on every resume, so a new refresh supersedes the one in flight
     * rather than racing it.
     */
    fun refresh() = refresh(keepDispatchResult = false)

    /**
     * @param keepDispatchResult retains the dispatch banner across the reload.
     *   A dispatch triggers its own refresh so the new run appears immediately,
     *   and that refresh must not wipe the message explaining what just
     *   happened. A refresh the user asked for does clear it, because by then
     *   they have seen it.
     */
    private fun refresh(keepDispatchResult: Boolean) {
        refreshJob?.cancel()
        refreshJob = viewModelScope.launch {
            _state.update {
                it.copy(
                    loading = true,
                    lastDispatch = if (keepDispatchResult) it.lastDispatch else null,
                )
            }

            val result = runCatching {
                withContext(ioDispatcher) {
                    val runs = redlineJson.decodeFromString(
                        RunListView.serializer(),
                        source.fetchRunsJson(),
                    )
                    val tasks = redlineJson.decodeFromString(
                        TaskListView.serializer(),
                        source.fetchTasksJson(),
                    )
                    runs to tasks
                }
            }
            coroutineContext.ensureActive()

            result.fold(
                onSuccess = { (runs, tasks) ->
                    // A good load clears any earlier failure.
                    _state.update {
                        RunsUiState(
                            loading = false,
                            runs = runs,
                            tasks = tasks,
                            lastDispatch = if (keepDispatchResult) it.lastDispatch else null,
                        )
                    }
                },
                onFailure = { error -> applyFailure(error) },
            )
        }
    }

    /** Loads one log stream for a run. */
    fun loadLogs(runId: String, stream: String = "stdout") {
        viewModelScope.launch {
            _state.update { it.copy(logsLoading = true, logs = null) }
            val result = runCatching {
                withContext(ioDispatcher) { source.fetchRunLogs(runId, stream) }
            }
            coroutineContext.ensureActive()
            result.fold(
                onSuccess = { text ->
                    _state.update { it.copy(logsLoading = false, logs = text) }
                },
                onFailure = { error -> applyFailure(error) },
            )
        }
    }

    fun clearLogs() {
        _state.update { it.copy(logs = null, logsLoading = false) }
    }

    /**
     * Asks the service to run a task now.
     *
     * The result is kept rather than assumed: the scheduler can decline, and
     * saying "started" when nothing did would be a lie the user acts on.
     */
    fun dispatch(taskId: String) {
        viewModelScope.launch {
            _state.update { it.copy(dispatching = true, lastDispatch = null) }
            val result = runCatching {
                withContext(ioDispatcher) {
                    redlineJson.decodeFromString(
                        DispatchView.serializer(),
                        source.dispatchTask(taskId),
                    )
                }
            }
            coroutineContext.ensureActive()
            result.fold(
                onSuccess = { view ->
                    _state.update { it.copy(dispatching = false, lastDispatch = view) }
                    // A started run should appear in the list without the user
                    // having to pull to refresh, but the banner explaining the
                    // outcome has to survive that reload.
                    if (view.started) refresh(keepDispatchResult = true)
                },
                onFailure = { error -> applyFailure(error) },
            )
        }
    }

    fun clearDispatchResult() {
        _state.update { it.copy(lastDispatch = null) }
    }

    private fun applyFailure(error: Throwable) {
        // Cancellation is control flow, not a transport failure.
        if (error is CancellationException) throw error
        val failure = if (source.isUnauthorized(error)) {
            UsageUiState.Failure.UNAUTHORIZED
        } else {
            UsageUiState.Failure.UNREACHABLE
        }
        _state.update { it.fail(failure) }
    }
}
