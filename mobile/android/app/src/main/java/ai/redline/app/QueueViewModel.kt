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

/** Where queue data and provider controls come from. */
interface QueueSource {
    fun fetchQueueJson(providerAccountId: String): String
    fun controlProvider(providerAccountId: String, control2: String)
    fun isUnauthorized(error: Throwable): Boolean
}

data class QueueUiState(
    val loading: Boolean = false,
    val refreshing: Boolean = false,
    val providers: List<String> = emptyList(),
    val selectedProvider: String = "",
    val queue: QueueView? = null,
    val failure: UsageUiState.Failure? = null,
)

class QueueViewModel(
    private val source: QueueSource,
    private val ioDispatcher: CoroutineDispatcher = Dispatchers.IO,
) : ViewModel() {

    private val _state = MutableStateFlow(QueueUiState())
    val state: StateFlow<QueueUiState> = _state.asStateFlow()

    private var loadJob: Job? = null

    /**
     * Supplies the providers to offer, keeping the current selection if it is
     * still present.
     *
     * The list comes from the usage screen, so this avoids a second request for
     * something the app already knows.
     */
    fun setProviders(providers: List<String>) {
        val current = _state.value.selectedProvider
        val selected = if (current.isNotBlank() && providers.contains(current)) {
            current
        } else {
            providers.firstOrNull().orEmpty()
        }
        val selectionChanged = selected != current
        val neverLoaded = _state.value.queue == null
        _state.update { it.copy(providers = providers, selectedProvider = selected) }
        if (selected.isNotBlank() && (selectionChanged || neverLoaded)) {
            load(refreshing = false)
        }
    }

    fun selectProvider(providerAccountId: String) {
        if (providerAccountId == _state.value.selectedProvider) return
        // The old provider's queue must not linger under the new provider's
        // name while the request is in flight.
        _state.update { it.copy(selectedProvider = providerAccountId, queue = null) }
        load(refreshing = false)
    }

    fun refresh() = load(refreshing = false)

    /**
     * Asks the desktop to re-read usage, then reloads.
     *
     * This is the button for "the numbers look wrong", so it forces a fresh
     * sample rather than re-rendering the same snapshot.
     */
    fun refreshUsage() {
        val provider = _state.value.selectedProvider
        if (provider.isBlank()) return
        _state.update { it.copy(refreshing = true) }
        viewModelScope.launch {
            val result = runCatching {
                withContext(ioDispatcher) { source.controlProvider(provider, "refresh") }
            }
            coroutineContext.ensureActive()
            result.fold(
                onSuccess = { load(refreshing = true) },
                onFailure = { error -> applyFailure(error) },
            )
        }
    }

    /** Pauses or resumes the selected provider. */
    fun controlProvider(control: String) {
        val provider = _state.value.selectedProvider
        if (provider.isBlank()) return
        viewModelScope.launch {
            val result = runCatching {
                withContext(ioDispatcher) { source.controlProvider(provider, control) }
            }
            coroutineContext.ensureActive()
            result.fold(
                // Pausing changes what the queue will do, so the picture on
                // screen is stale the moment the control succeeds.
                onSuccess = { load(refreshing = false) },
                onFailure = { error -> applyFailure(error) },
            )
        }
    }

    private fun load(refreshing: Boolean) {
        val provider = _state.value.selectedProvider
        if (provider.isBlank()) return

        loadJob?.cancel()
        loadJob = viewModelScope.launch {
            _state.update { it.copy(loading = true, refreshing = refreshing) }

            val result = runCatching {
                withContext(ioDispatcher) {
                    redlineJson.decodeFromString(
                        QueueView.serializer(),
                        source.fetchQueueJson(provider),
                    )
                }
            }
            coroutineContext.ensureActive()

            result.fold(
                onSuccess = { queue ->
                    _state.update {
                        it.copy(loading = false, refreshing = false, queue = queue, failure = null)
                    }
                },
                onFailure = { error -> applyFailure(error) },
            )
        }
    }

    private fun applyFailure(error: Throwable) {
        if (error is CancellationException) throw error
        val failure = if (source.isUnauthorized(error)) {
            UsageUiState.Failure.UNAUTHORIZED
        } else {
            UsageUiState.Failure.UNREACHABLE
        }
        // The provider list survives: losing the picker would strand the user
        // on a screen with no way to try a different provider.
        _state.update { it.copy(loading = false, refreshing = false, failure = failure) }
    }
}
