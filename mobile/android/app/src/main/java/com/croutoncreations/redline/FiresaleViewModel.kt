package com.croutoncreations.redline

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

@Serializable
data class ManualFiresale(
    val account: String,
    @SerialName("started_at") val startedAt: String,
    val deadline: String,
    @SerialName("started_by") val startedBy: String,
)

@Serializable
data class FiresaleAccount(
    val account: String,
    val label: String,
    val provider: String,
    @SerialName("weekly_resets_at") val weeklyResetsAt: String? = null,
    @SerialName("reason_unavailable") val reasonUnavailable: String? = null,
)

@Serializable
data class FiresalesView(val manual: List<ManualFiresale> = emptyList(), val eligible: List<FiresaleAccount> = emptyList())

data class FiresaleUiState(
    val view: FiresalesView? = null,
    val visible: Boolean = true,
    val working: Boolean = false,
    val error: String? = null,
    val selectedAccount: String? = null,
    val until: String = "15m",
) {
    val selected: FiresaleAccount? get() = view?.eligible?.find { it.account == selectedAccount }
    val canStart: Boolean get() = !working && selected != null && selected?.reasonUnavailable == null &&
        (until != "weekly_reset" || selected?.weeklyResetsAt != null)

    fun loaded(value: FiresalesView): FiresaleUiState {
        val account = value.eligible.find { it.account == selectedAccount }
            ?: value.eligible.firstOrNull { it.reasonUnavailable == null }
            ?: value.eligible.firstOrNull()
        return copy(view = value, visible = true, working = false, error = null, selectedAccount = account?.account,
            until = if (until == "weekly_reset" && account?.weeklyResetsAt == null) "15m" else until)
    }
}

interface FiresaleSource {
    fun list(): String
    fun start(account: String, until: String)
    fun stop(account: String)
    fun isNotFound(error: Throwable): Boolean
}

class FiresaleViewModel(private val source: FiresaleSource, private val ioDispatcher: CoroutineDispatcher = Dispatchers.IO) : ViewModel() {
    private val mutableState = MutableStateFlow(FiresaleUiState())
    val state = mutableState.asStateFlow()
    private var job: Job? = null

    fun selectAccount(account: String) {
        val selected = state.value.view?.eligible?.find { it.account == account } ?: return
        mutableState.value = state.value.copy(selectedAccount = account,
            until = if (selected.weeklyResetsAt == null && state.value.until == "weekly_reset") "15m" else state.value.until)
    }

    fun selectDuration(until: String) {
        if (until !in listOf("15m", "2h", "weekly_reset")) return
        if (until == "weekly_reset" && state.value.selected?.weeklyResetsAt == null) return
        mutableState.value = state.value.copy(until = until)
    }

    fun refresh() { if (!state.value.working) load() }
    fun start() {
        val current = state.value
        if (current.canStart) load { source.start(checkNotNull(current.selectedAccount), current.until) }
    }
    fun stop(account: String) = load { source.stop(account) }

    private fun load(action: (() -> Unit)? = null) {
        if (state.value.working) return
        job?.cancel()
        mutableState.value = state.value.copy(working = true, error = null)
        job = viewModelScope.launch {
            try {
                if (action != null) withContext(ioDispatcher) { action() }
                // Only GET 404 means an old desktop. An action's 404 is an inline error.
                val view = try {
                    withContext(ioDispatcher) { redlineJson.decodeFromString<FiresalesView>(source.list()) }
                } catch (error: Exception) {
                    if (error is CancellationException) throw error
                    if (source.isNotFound(error)) {
                        mutableState.value = state.value.copy(visible = false, working = false, error = null)
                        return@launch
                    }
                    throw error
                }
                ensureActive()
                mutableState.value = state.value.loaded(view)
            } catch (error: Exception) {
                if (error is CancellationException) throw error
                mutableState.value = state.value.copy(working = false, error = error.message ?: "Couldn't reach your Mac")
            }
        }
    }
}
