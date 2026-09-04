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
/** How the live connection is behaving, mirroring the core's stream states. */
/**
 * RELAYED means the screen is updating by polling because the desktop is only
 * reachable through the relay, which cannot carry a live stream. Distinct from
 * OFFLINE (nothing is arriving) and from RECONNECTING (a live connection is
 * still expected), because over a relay it never is.
 */
enum class LiveState { OFFLINE, CONNECTING, LIVE, RECONNECTING, RELAYED }

data class UsageUiState(
    val loading: Boolean = false,
    val view: UsageView? = null,
    val failure: Failure? = null,
    val live: LiveState = LiveState.OFFLINE,
    /**
     * How the desktop is being reached.
     *
     * Surfaced because a relayed session is slower, metered, and crosses a
     * third party; someone who expected to be on their own network deserves
     * to see that they are not.
     */
    val transport: Transport = Transport.Direct,
) {
    /**
     * ENTITLEMENT_REFUSED is separate from UNREACHABLE because the remedy is
     * different: the desktop may be healthy and the network fine, and the user
     * needs to renew rather than investigate their wifi.
     */
    enum class Failure { UNAUTHORIZED, UNREACHABLE, ENTITLEMENT_REFUSED }

    val hasData: Boolean get() = view != null

    /**
     * Records a failure while keeping any data already on screen: when the
     * desktop goes away, the last numbers marked stale beat an empty screen.
     */
    fun fail(reason: Failure): UsageUiState = copy(loading = false, failure = reason)
}

/** Maps the core's stream state strings onto [LiveState]. */
internal fun liveStateOf(raw: String): LiveState = when (raw) {
    "live" -> LiveState.LIVE
    "connecting" -> LiveState.CONNECTING
    "reconnecting" -> LiveState.RECONNECTING
    // Terminal and not a failure: polling keeps the screen current at the
    // slower relayed cadence, so the pill reports the route rather than going
    // dark or promising a reconnection that cannot happen.
    "relayed" -> LiveState.RELAYED
    // "unauthorized" and "stopped" both mean no live data is coming; the
    // failure state carries the reason, so this only says the pill is dark.
    else -> LiveState.OFFLINE
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

    /**
     * Reports whether the relay declined for lack of a current subscription.
     *
     * Default false so existing sources and tests are unaffected; only the
     * core-backed source can actually tell.
     */
    fun isEntitlementRefused(error: Throwable): Boolean = false

    /**
     * How the last request actually reached the desktop.
     *
     * Default Direct so existing sources and tests are unaffected. Read after
     * every refresh rather than pushed, because the route is decided deep in
     * the core and only the holder above it knows what happened.
     */
    fun transport(): Transport = Transport.Direct

    /**
     * Subscribes to live updates, returning a handle that stops it.
     *
     * Default is a no-op returning null, so tests that only exercise polling
     * do not have to implement streaming.
     */
    fun stream(onUsage: (String) -> Unit, onState: (String) -> Unit): AutoCloseable? = null

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
    private var subscription: AutoCloseable? = null

    /**
     * Starts live updates, replacing any existing subscription.
     *
     * Live frames make the manual refresh redundant while connected, but
     * refresh-on-resume stays as the floor: a stream that failed to connect
     * must not leave the screen empty.
     */
    fun startLive() {
        // Idempotent: lifecycle callbacks can fire more than once for the same
        // visible screen, and tearing down a working stream to rebuild it
        // would drop frames and flicker the pill.
        if (subscription != null) return
        subscription = source.stream(
            onUsage = { raw ->
                // A malformed frame is skipped rather than fatal: one bad
                // payload is not a reason to stop rendering the ones after it.
                val view = runCatching {
                    redlineJson.decodeFromString(UsageView.serializer(), raw)
                }.getOrNull() ?: return@stream
                // A frame proves the desktop is reachable and the credential
                // good, so it clears any earlier failure.
                _state.update { it.copy(loading = false, view = view, failure = null) }
            },
            onState = { raw ->
                _state.update { it.copy(live = liveStateOf(raw)) }
                // The stream stops permanently on a rejected credential, and
                // the user needs to be told to pair again rather than left
                // watching a screen that quietly stopped updating.
                if (raw == "unauthorized") {
                    _state.update { it.fail(UsageUiState.Failure.UNAUTHORIZED) }
                }
                // "relayed" is terminal in the core: the stream cannot run over
                // a tunnel that carries one request and one response, so the
                // goroutine returns. Releasing the handle lets the next resume
                // start a fresh stream once the tailnet is back -- otherwise
                // the stale handle makes startLive() return early and the pill
                // wedges on "relayed" until the screen is recreated.
                if (raw == "relayed") {
                    subscription = null
                }
            },
        )
    }

    /**
     * Sets the live state directly, for tests that need to simulate a stream
     * without one.
     */
    internal fun applyLiveStateForTest(state: LiveState) {
        _state.update { it.copy(live = state) }
    }

    /** Stops live updates. Called when the screen goes away. */
    fun stopLive() {
        runCatching { subscription?.close() }
        subscription = null
        _state.update { it.copy(live = LiveState.OFFLINE) }
    }

    override fun onCleared() {
        super.onCleared()
        // A stream that outlived its view model would keep reconnecting in the
        // background for a screen nobody is looking at.
        stopLive()
    }

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
                    val failure = when {
                        source.isUnauthorized(error) -> UsageUiState.Failure.UNAUTHORIZED
                        source.isEntitlementRefused(error) ->
                            UsageUiState.Failure.ENTITLEMENT_REFUSED
                        else -> UsageUiState.Failure.UNREACHABLE
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
                        // Copied from the current state rather than built
                        // fresh: a new instance would reset fields this path
                        // knows nothing about, and silently drop the live
                        // connection status the stream is maintaining.
                        onSuccess = { view ->
                            // The route is read here rather than tracked
                            // separately: a paid relayed request must be
                            // visible on screen, and a value nothing reads is
                            // how the fallback went missing in the first place.
                            val route = source.transport()
                            _state.update {
                                it.copy(
                                    loading = false,
                                    view = view,
                                    failure = null,
                                    transport = route,
                                )
                            }
                        },
                        // Malformed JSON from a reachable server is not an auth
                        // problem; treating it as one would tell the user to
                        // re-pair, which would not help.
                        onFailure = { _state.update { it.fail(UsageUiState.Failure.UNREACHABLE) } },
                    )
                },
                onFailure = { error ->
                    // Cancellation is control flow, not a transport failure.
                    if (error is CancellationException) throw error
                    val failure = when {
                        source.isUnauthorized(error) -> UsageUiState.Failure.UNAUTHORIZED
                        source.isEntitlementRefused(error) ->
                            UsageUiState.Failure.ENTITLEMENT_REFUSED
                        else -> UsageUiState.Failure.UNREACHABLE
                    }
                    _state.update { it.fail(failure) }
                },
            )
        }
    }
}
