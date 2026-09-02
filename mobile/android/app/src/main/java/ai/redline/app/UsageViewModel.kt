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
 * What the capacity screen is currently showing.
 *
 * [lastLoaded] survives a failure on purpose: when the desktop is asleep the
 * useful thing is yesterday's numbers marked stale, not an empty screen.
 */
data class UsageUiState(
    val loading: Boolean = false,
    val view: UsageView? = null,
    val failure: Failure? = null,
) {
    enum class Failure { UNAUTHORIZED, UNREACHABLE }

    val hasData: Boolean get() = view != null

    /**
     * Records a failure while keeping any data already on screen: when the
     * desktop goes away, the last numbers marked stale beat an empty screen.
     */
    fun fail(reason: Failure): UsageUiState = copy(loading = false, failure = reason)
}

/**
 * Fetches usage through the shared Go core.
 *
 * [UsageSource] exists so this is testable on the JVM without the gomobile
 * binding, which needs a device or emulator to load its native library.
 */
interface UsageSource {
    /** Pauses, resumes, or refreshes a provider. Default keeps tests terse. */
    fun controlProvider(providerAccountId: String, control: String) = Unit

    /** Returns the core's usage JSON, or throws. */
    fun fetchUsageJson(): String

    /** Reports whether a throwable from [fetchUsageJson] was a rejected credential. */
    fun isUnauthorized(error: Throwable): Boolean
}

class UsageViewModel(
    private val source: UsageSource,
    private val ioDispatcher: CoroutineDispatcher = Dispatchers.IO,
) : ViewModel() {

    private val _state = MutableStateFlow(UsageUiState())
    val state: StateFlow<UsageUiState> = _state.asStateFlow()

    /**
     * The in-flight refresh, so a new one supersedes it.
     *
     * refresh() runs on every ON_RESUME, so overlap is routine rather than
     * exceptional. Without this, two refreshes race on read-modify-write of the
     * state and whichever finishes last wins regardless of which fetched last,
     * so a slow stale result can overwrite a fresh one.
     */
    private var refreshJob: Job? = null

    /**
     * Pauses, resumes, or refreshes a provider, then reloads.
     *
     * The numbers on screen describe the state before the control was applied,
     * so leaving them would show a paused provider as running.
     */
    fun controlProvider(providerAccountId: String, control: String) {
        viewModelScope.launch {
            val result = runCatching {
                withContext(ioDispatcher) { source.controlProvider(providerAccountId, control) }
            }
            coroutineContext.ensureActive()
            result.fold(
                onSuccess = { refresh() },
                onFailure = { error ->
                    if (error is CancellationException) throw error
                    val failure = if (source.isUnauthorized(error)) {
                        UsageUiState.Failure.UNAUTHORIZED
                    } else {
                        UsageUiState.Failure.UNREACHABLE
                    }
                    _state.update { it.fail(failure) }
                },
            )
        }
    }

    fun refresh() {
        refreshJob?.cancel()
        refreshJob = viewModelScope.launch {
            _state.update { it.copy(loading = true) }

            val result = runCatching { withContext(ioDispatcher) { source.fetchUsageJson() } }

            // A cancelled refresh was superseded, so it must leave the state to
            // its replacement rather than reporting a failure.
            coroutineContext.ensureActive()

            result.fold(
                onSuccess = { raw ->
                    runCatching { redlineJson.decodeFromString(UsageView.serializer(), raw) }.fold(
                        // A good load clears any earlier failure, or the header
                        // keeps saying "Offline" over live data.
                        onSuccess = { view -> _state.value = UsageUiState(loading = false, view = view) },
                        // Malformed JSON from a reachable server is not an auth
                        // problem; treating it as one would tell the user to
                        // re-pair, which would not help.
                        onFailure = { _state.update { it.fail(UsageUiState.Failure.UNREACHABLE) } },
                    )
                },
                onFailure = { error ->
                    // Cancellation is control flow, not a transport failure.
                    if (error is CancellationException) throw error
                    val failure = if (source.isUnauthorized(error)) {
                        UsageUiState.Failure.UNAUTHORIZED
                    } else {
                        UsageUiState.Failure.UNREACHABLE
                    }
                    _state.update { it.fail(failure) }
                },
            )
        }
    }
}
