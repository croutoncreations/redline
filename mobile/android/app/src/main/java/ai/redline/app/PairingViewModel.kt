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
 * Turns a scanned QR code into a stored credential.
 *
 * An interface so the pairing flow is testable on the JVM without a camera or
 * the native library.
 */
interface PairingSource {
    /** Parses a scanned code, returning the pairing details as JSON. */
    fun parsePairingUrl(raw: String): String

    /** Exchanges the one-time token for the durable credential. */
    fun redeem(baseUrl: String, pairingToken: String): String
}

data class PairingUiState(
    val working: Boolean = false,
    val error: String? = null,
    val pairedTo: String? = null,
) {
    val isPaired: Boolean get() = pairedTo != null
}

class PairingViewModel(
    private val source: PairingSource,
    private val settings: RedlineSettingsWriter,
    private val ioDispatcher: CoroutineDispatcher = Dispatchers.IO,
) : ViewModel() {

    private val _state = MutableStateFlow(PairingUiState())
    val state: StateFlow<PairingUiState> = _state.asStateFlow()

    private var pairingJob: Job? = null

    /**
     * Returns to the unpaired state after the credential has been cleared.
     *
     * The model holds its own success from the last pairing, so without this
     * the app would forget the credential and still believe it was paired,
     * leaving a dashboard on screen that can no longer fetch anything. Any
     * in-flight scan is cancelled too: its result would write a credential back
     * moments after the user asked to remove one.
     */
    fun forget() {
        pairingJob?.cancel()
        pairingJob = null
        _state.value = PairingUiState()
    }

    /**
     * Pairs using a scanned code.
     *
     * The camera delivers a stream of frames and will report the same code many
     * times a second, so a scan in flight suppresses the ones behind it. Without
     * that, a single QR would be redeemed repeatedly and every attempt after the
     * first would fail: the token is deliberately single-use.
     */
    fun pair(scanned: String) {
        if (pairingJob?.isActive == true) return

        pairingJob = viewModelScope.launch {
            _state.update { it.copy(working = true, error = null) }

            val result = runCatching {
                withContext(ioDispatcher) {
                    val request = redlineJson.decodeFromString(
                        PairingRequest.serializer(),
                        source.parsePairingUrl(scanned),
                    )
                    val token = source.redeem(request.baseUrl, request.pairingToken)
                    Triple(request.baseUrl, token, request)
                }
            }
            coroutineContext.ensureActive()

            result.fold(
                onSuccess = { (baseUrl, token, request) ->
                    settings.update(baseUrl, token)
                    // Stored even when empty, so re-pairing with a desktop that
                    // has dropped its relay clears the stale details rather
                    // than leaving the phone aimed at a relay nobody answers.
                    settings.updateRelay(
                        request.relayUrl,
                        request.desktopKey,
                        request.relaySession,
                        request.entitlementToken,
                    )
                    _state.value = PairingUiState(working = false, pairedTo = baseUrl)
                },
                onFailure = { error ->
                    if (error is CancellationException) throw error
                    // The core's messages are written for a person to read, so
                    // they are shown as-is rather than replaced with a generic
                    // failure that hides which of several things went wrong.
                    _state.update {
                        it.copy(
                            working = false,
                            error = error.message ?: "Pairing failed.",
                        )
                    }
                },
            )
        }
    }

    fun clearError() {
        _state.update { it.copy(error = null) }
    }
}
