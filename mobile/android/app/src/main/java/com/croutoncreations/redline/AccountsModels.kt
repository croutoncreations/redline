package com.croutoncreations.redline

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import kotlin.coroutines.coroutineContext
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/** Mirrors core.AccountsView: every account, worded by the core. */
@Serializable
data class AccountsView(
    val accounts: List<AccountEntry> = emptyList(),
    @SerialName("restart_required") val restartRequired: Boolean = false,
)

@Serializable
data class AccountEntry(
    val id: String = "",
    val provider: String = "",
    val label: String = "",
    @SerialName("openusage_card") val openUsageCard: String = "",
    val reads: String = "",
    /** False for accounts in the Mac's config file, which are edited there. */
    val editable: Boolean = false,
    val disabled: Boolean = false,
    val running: Boolean = false,
    val pending: String = "",
    val state: String = "",
    /** good, warn, danger or muted. */
    val tone: String = "",
    @SerialName("last_error") val lastError: String = "",
    @SerialName("sign_in") val signIn: String = "",
)

@Serializable
data class DiscoverView(
    val provider: String = "",
    val cards: List<DiscoveredCard> = emptyList(),
    val error: String = "",
    val hint: String = "",
)

@Serializable
data class DiscoveredCard(
    val id: String = "",
    val plan: String = "",
    val stale: Boolean = false,
    @SerialName("has_weekly") val hasWeekly: Boolean = false,
    val error: String = "",
    @SerialName("linked_account") val linkedAccount: String = "",
)

/**
 * Where account data comes from. Nothing here signs in or carries a
 * credential: the phone only names accounts the Mac can already read.
 */
interface AccountsSource {
    fun fetchAccountsJson(): String
    fun discoverJson(provider: String): String
    fun addAccount(id: String, provider: String, label: String, card: String): String
    fun renameAccount(id: String, label: String): String
    fun setDisabled(id: String, disabled: Boolean): String
    fun removeAccount(id: String): String
    fun isUnauthorized(error: Throwable): Boolean
}

data class AccountsUiState(
    val loading: Boolean = false,
    val view: AccountsView? = null,
    val discover: DiscoverView? = null,
    /** The desktop's explanation for the last refused change, shown as-is. */
    val message: String = "",
    val failure: UsageUiState.Failure? = null,
)

class AccountsViewModel(
    private val source: AccountsSource,
    private val ioDispatcher: CoroutineDispatcher = Dispatchers.IO,
) : ViewModel() {

    private val _state = MutableStateFlow(AccountsUiState())
    val state: StateFlow<AccountsUiState> = _state.asStateFlow()

    fun refresh() = run(clearMessage = false) { source.fetchAccountsJson() }

    fun discover(provider: String) {
        viewModelScope.launch {
            val result = runCatching {
                withContext(ioDispatcher) { redlineJson.decodeFromString(DiscoverView.serializer(), source.discoverJson(provider)) }
            }
            coroutineContext.ensureActive()
            result.fold(
                onSuccess = { found -> _state.update { it.copy(discover = found) } },
                onFailure = { error -> fail(error) },
            )
        }
    }

    fun closeDiscover() = _state.update { it.copy(discover = null) }

    fun add(id: String, provider: String, label: String, card: String) =
        run { source.addAccount(id, provider, label, card) }.also { closeDiscover() }

    fun rename(id: String, label: String) = run { source.renameAccount(id, label) }

    fun setDisabled(id: String, disabled: Boolean) = run { source.setDisabled(id, disabled) }

    fun remove(id: String) = run { source.removeAccount(id) }

    fun dismissMessage() = _state.update { it.copy(message = "") }

    private fun run(clearMessage: Boolean = true, call: () -> String) {
        viewModelScope.launch {
            _state.update { it.copy(loading = true, message = if (clearMessage) "" else it.message) }
            val result = runCatching {
                withContext(ioDispatcher) { redlineJson.decodeFromString(AccountsView.serializer(), call()) }
            }
            coroutineContext.ensureActive()
            result.fold(
                onSuccess = { view -> _state.update { it.copy(loading = false, view = view, failure = null) } },
                onFailure = { error -> fail(error) },
            )
        }
    }

    private fun fail(error: Throwable) {
        if (error is CancellationException) throw error
        if (source.isUnauthorized(error)) {
            _state.update { it.copy(loading = false, failure = UsageUiState.Failure.UNAUTHORIZED) }
            return
        }
        // A refused change (conflict, in use) keeps the list and says why.
        val message = refusalMessage(error.message.orEmpty())
        _state.update {
            if (it.view == null && message.isEmpty()) {
                it.copy(loading = false, failure = UsageUiState.Failure.UNREACHABLE)
            } else {
                it.copy(loading = false, message = message.ifEmpty { "Could not reach Redline. Try again." })
            }
        }
    }
}

/**
 * The desktop's reason from a "redline api: 409 …" error, or "" for a
 * transport failure. The desktop's words are shown because they name the
 * conflicting account or the profiles that use it.
 */
fun refusalMessage(raw: String): String {
    val match = Regex("""redline api: (\d{3}) (.*)""", RegexOption.DOT_MATCHES_ALL).find(raw) ?: return ""
    val text = match.groupValues[2].trim()
    return text.removePrefix("account conflict: ").replaceFirstChar { it.uppercase() }
}

/** A suggested id for a card, e.g. claude@ab12cd34 -> claude-ab12cd34. */
fun suggestedAccountId(card: String): String =
    card.lowercase().replace('@', '-').filter { it.isLetterOrDigit() || it == '-' || it == '.' || it == '_' }.take(63)

/** [AccountsSource] backed by the gomobile-bound Go core. */
class CoreAccountsSource(private val client: CoreClientHolder) : AccountsSource {
    override fun fetchAccountsJson(): String = client.client().fetchAccounts()
    override fun discoverJson(provider: String): String = client.client().discoverAccounts(provider)
    override fun addAccount(id: String, provider: String, label: String, card: String): String =
        client.client().addAccount(id, provider, label, card)
    override fun renameAccount(id: String, label: String): String = client.client().renameAccount(id, label)
    override fun setDisabled(id: String, disabled: Boolean): String = client.client().setAccountDisabled(id, disabled)
    override fun removeAccount(id: String): String = client.client().removeAccount(id)
    override fun isUnauthorized(error: Throwable): Boolean = client.isUnauthorized(error)
}
