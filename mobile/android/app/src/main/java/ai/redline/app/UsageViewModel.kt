package ai.redline.app

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
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
}

/**
 * Fetches usage through the shared Go core.
 *
 * [UsageSource] exists so this is testable on the JVM without the gomobile
 * binding, which needs a device or emulator to load its native library.
 */
interface UsageSource {
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

    fun refresh() {
        viewModelScope.launch {
            _state.value = _state.value.copy(loading = true)
            val result = runCatching { withContext(ioDispatcher) { source.fetchUsageJson() } }
            _state.value = result.fold(
                onSuccess = { raw ->
                    runCatching { redlineJson.decodeFromString(UsageView.serializer(), raw) }.fold(
                        onSuccess = { view -> UsageUiState(loading = false, view = view) },
                        // Malformed JSON from a reachable server is not an auth
                        // problem; treating it as one would tell the user to
                        // re-pair, which would not help.
                        onFailure = {
                            _state.value.copy(
                                loading = false,
                                failure = UsageUiState.Failure.UNREACHABLE,
                            )
                        },
                    )
                },
                onFailure = { error ->
                    _state.value.copy(
                        loading = false,
                        failure = if (source.isUnauthorized(error)) {
                            UsageUiState.Failure.UNAUTHORIZED
                        } else {
                            UsageUiState.Failure.UNREACHABLE
                        },
                    )
                },
            )
        }
    }
}
