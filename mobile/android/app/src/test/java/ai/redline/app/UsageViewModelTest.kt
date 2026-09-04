package ai.redline.app

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.setMain
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class UsageViewModelTest {

    private val dispatcher = StandardTestDispatcher()

    @Before fun setUp() = Dispatchers.setMain(dispatcher)

    @After fun tearDown() = Dispatchers.resetMain()

    private class FakeSource(
        private val result: Result<String>,
        private val unauthorized: Boolean = false,
    ) : UsageSource {
        override fun fetchUsageJson(): String = result.getOrThrow()
        override fun isUnauthorized(error: Throwable): Boolean = unauthorized
    }

    private val validJson = """
        {"generated_at": "2026-07-20T19:00:00Z",
         "providers": [{"id": "claude-main", "provider": "claude",
                        "session": {"remaining_percent": 62, "resets_in_seconds": 14400},
                        "weekly": {"remaining_percent": 53, "resets_in_seconds": 345600}}]}
    """.trimIndent()

    @Test
    fun loadsUsage() = runTest(dispatcher) {
        val model = UsageViewModel(FakeSource(Result.success(validJson)), dispatcher)
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()

        val state = model.state.value
        assertFalse(state.loading)
        assertNull(state.failure)
        assertEquals(62, state.view?.providers?.get(0)?.session?.remainingPercent)
    }

    /** A rejected credential must be distinguishable so the UI can offer re-pairing. */
    @Test
    fun reportsUnauthorizedDistinctly() = runTest(dispatcher) {
        val model = UsageViewModel(
            FakeSource(Result.failure(RuntimeException("401")), unauthorized = true),
            dispatcher,
        )
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()

        assertEquals(UsageUiState.Failure.UNAUTHORIZED, model.state.value.failure)
    }

    @Test
    fun reportsUnreachableDistinctly() = runTest(dispatcher) {
        val model = UsageViewModel(
            FakeSource(Result.failure(RuntimeException("connection refused"))),
            dispatcher,
        )
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()

        assertEquals(UsageUiState.Failure.UNREACHABLE, model.state.value.failure)
    }

    /**
     * When the desktop goes away, the last numbers are more useful than an
     * empty screen, so a failure must not discard them.
     */
    @Test
    fun keepsLastGoodDataWhenARefreshFails() = runTest(dispatcher) {
        var payload: Result<String> = Result.success(validJson)
        val source = object : UsageSource {
            override fun fetchUsageJson(): String = payload.getOrThrow()
            override fun isUnauthorized(error: Throwable): Boolean = false
        }
        val model = UsageViewModel(source, dispatcher)

        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()
        assertTrue(model.state.value.hasData)

        payload = Result.failure(RuntimeException("desktop asleep"))
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()

        val state = model.state.value
        assertEquals(UsageUiState.Failure.UNREACHABLE, state.failure)
        assertNotNull("stale data should survive a failed refresh", state.view)
        assertEquals(62, state.view?.providers?.get(0)?.session?.remainingPercent)
    }

    /** Malformed JSON from a reachable server is not an auth problem. */
    @Test
    fun malformedResponseIsNotReportedAsUnauthorized() = runTest(dispatcher) {
        val model = UsageViewModel(FakeSource(Result.success("{not json")), dispatcher)
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()

        assertEquals(UsageUiState.Failure.UNREACHABLE, model.state.value.failure)
    }

    /**
     * A refresh that succeeds after an earlier failure must clear the failure,
     * or the header keeps saying "Offline" over live data.
     */
    @Test
    fun successfulRefreshClearsAPreviousFailure() = runTest(dispatcher) {
        var payload: Result<String> = Result.failure(RuntimeException("desktop asleep"))
        val source = object : UsageSource {
            override fun fetchUsageJson(): String = payload.getOrThrow()
            override fun isUnauthorized(error: Throwable): Boolean = false
        }
        val model = UsageViewModel(source, dispatcher)

        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()
        assertEquals(UsageUiState.Failure.UNREACHABLE, model.state.value.failure)

        payload = Result.success(validJson)
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()

        assertNull("a good refresh must clear the stale failure", model.state.value.failure)
        assertEquals(62, model.state.value.view?.providers?.get(0)?.session?.remainingPercent)
    }

    /**
     * refresh() runs on every ON_RESUME, so overlapping calls are routine.
     * They must not leave the spinner stuck or lose the newest result.
     */
    @Test
    fun overlappingRefreshesSettleCorrectly() = runTest(dispatcher) {
        val source = object : UsageSource {
            override fun fetchUsageJson(): String = validJson
            override fun isUnauthorized(error: Throwable): Boolean = false
        }
        val model = UsageViewModel(source, dispatcher)

        model.refresh()
        model.refresh()
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()

        val state = model.state.value
        assertFalse("loading must not remain stuck after concurrent refreshes", state.loading)
        assertNull(state.failure)
        assertEquals(62, state.view?.providers?.get(0)?.session?.remainingPercent)
    }

    /**
     * A refresh must not clobber the live connection status.
     *
     * refresh() runs on resume, which is moments after the stream connects, so
     * a success path that rebuilds the state from scratch resets fields it
     * knows nothing about and the pill goes dark while frames keep arriving.
     */
    @Test
    fun refreshPreservesTheLiveConnectionState() = runTest(dispatcher) {
        val source = object : UsageSource {
            override fun fetchUsageJson(): String = validJson
            override fun isUnauthorized(error: Throwable): Boolean = false
        }
        val model = UsageViewModel(source, dispatcher)

        // Simulate the stream reporting itself live, then a resume refresh.
        model.applyLiveStateForTest(LiveState.LIVE)
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()

        assertEquals(
            "a refresh must not reset the live state",
            LiveState.LIVE,
            model.state.value.live,
        )
        assertEquals(62, model.state.value.view?.providers?.get(0)?.session?.remainingPercent)
    }
}


/**
 * A stream that cannot run over the relay must say so.
 *
 * The tunnel is request/response, so a long-lived event stream cannot cross
 * it. Falling through to OFFLINE would darken the pill with no explanation,
 * while the screen kept updating by polling -- live data with a dead
 * indicator. RELAYED says the cadence is slower and the reason is the route.
 */
class StreamRelayedStateTest {

    @Test
    fun `the relayed stream state is its own thing, not offline`() {
        assertEquals(LiveState.RELAYED, liveStateOf("relayed"))
    }

    @Test
    fun `relayed is distinct from reconnecting`() {
        // Reconnecting promises a live connection is coming. Over a relay it
        // is not, and saying so was the bug: the pill sat on "reconnecting"
        // forever with the tailnet down.
        assertNotEquals(liveStateOf("relayed"), liveStateOf("reconnecting"))
    }

    @Test
    fun `an unknown state is still offline`() {
        assertEquals(LiveState.OFFLINE, liveStateOf("something-new"))
    }
}

/**
 * An expired subscription is not a network problem.
 *
 * The relay refuses with 402 before any tunnel exists. Reported as UNREACHABLE
 * the app said "check that the desktop app is running and on the same
 * network", which sends a lapsed subscriber to look at their wifi. Verified
 * against the live relay: the dial returned a flat "connect failed".
 */
class EntitlementFailureTest {

    private val dispatcher = StandardTestDispatcher()

    @Before fun setUp() = Dispatchers.setMain(dispatcher)

    @After fun tearDown() = Dispatchers.resetMain()

    private class RefusingSource(private val refused: Boolean) : UsageSource {
        override fun fetchUsageJson(): String = throw RuntimeException("dial relay: refused")
        override fun controlProvider(providerAccountId: String, control: String) = Unit
        override fun isUnauthorized(error: Throwable): Boolean = false
        override fun isEntitlementRefused(error: Throwable): Boolean = refused
    }

    @Test
    fun `a refused entitlement is its own failure, not unreachable`() = runTest(dispatcher) {
        val model = UsageViewModel(RefusingSource(refused = true), ioDispatcher = dispatcher)
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()
        assertEquals(UsageUiState.Failure.ENTITLEMENT_REFUSED, model.state.value.failure)
    }

    @Test
    fun `an ordinary failure is still unreachable`() = runTest(dispatcher) {
        val model = UsageViewModel(RefusingSource(refused = false), ioDispatcher = dispatcher)
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()
        assertEquals(UsageUiState.Failure.UNREACHABLE, model.state.value.failure)
    }
}

/**
 * The screen has to say when it is on the relay.
 *
 * CoreClientHolder tracked the transport and nothing ever read it, so
 * UsageUiState.transport stayed Direct forever and the "relayed" marker in
 * UsageScreen could not appear. Requests would silently succeed over the paid
 * route with the UI still claiming the tailnet -- the same shape as the bug
 * this whole change set exists to fix: a value maintained and never consulted.
 */
class TransportReportingTest {

    private val dispatcher = StandardTestDispatcher()

    @Before fun setUp() = Dispatchers.setMain(dispatcher)

    @After fun tearDown() = Dispatchers.resetMain()

    private class RoutedSource(private val route: Transport) : UsageSource {
        override fun fetchUsageJson(): String = EMPTY_USAGE_JSON
        override fun isUnauthorized(error: Throwable): Boolean = false
        override fun transport(): Transport = route
    }

    @Test
    fun `a relayed refresh reports the relay`() = runTest(dispatcher) {
        val model = UsageViewModel(RoutedSource(Transport.Relay), ioDispatcher = dispatcher)
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()
        assertEquals(Transport.Relay, model.state.value.transport)
    }

    @Test
    fun `a direct refresh reports direct`() = runTest(dispatcher) {
        val model = UsageViewModel(RoutedSource(Transport.Direct), ioDispatcher = dispatcher)
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()
        assertEquals(Transport.Direct, model.state.value.transport)
    }

    private companion object {
        const val EMPTY_USAGE_JSON = """{"providers":[],"health":{"scheduler_enabled":false}}"""
    }
}

/**
 * A stream that stopped because only the relay was reachable must restart when
 * the tailnet comes back.
 *
 * StreamStateRelayed is terminal in the core: the goroutine returns and never
 * retries. The Kotlin handle stayed non-null, and startLive() returns early
 * when it is -- so after a flap the live pill wedged on "relayed" and polled
 * forever, even with the tailnet healthy, until the screen was recreated.
 */
class StreamRestartAfterRelayTest {

    private val dispatcher = StandardTestDispatcher()

    @Before fun setUp() = Dispatchers.setMain(dispatcher)

    @After fun tearDown() = Dispatchers.resetMain()

    private class CountingStreamSource : UsageSource {
        var subscriptions = 0
        var lastOnState: ((String) -> Unit)? = null
        override fun fetchUsageJson(): String =
            """{"providers":[],"health":{"scheduler_enabled":false}}"""
        override fun isUnauthorized(error: Throwable): Boolean = false
        override fun stream(
            onUsage: (String) -> Unit,
            onState: (String) -> Unit,
        ): AutoCloseable {
            subscriptions += 1
            lastOnState = onState
            return AutoCloseable { }
        }
    }

    @Test
    fun `a relayed stream can be restarted once direct returns`() = runTest(dispatcher) {
        val source = CountingStreamSource()
        val model = UsageViewModel(source, ioDispatcher = dispatcher)

        model.startLive()
        dispatcher.scheduler.advanceUntilIdle()
        assertEquals(1, source.subscriptions)

        // The core reports the terminal relayed state and stops.
        source.lastOnState?.invoke("relayed")
        dispatcher.scheduler.advanceUntilIdle()
        assertEquals(LiveState.RELAYED, model.state.value.live)

        // The tailnet returns and the screen resumes: a new stream must start
        // rather than the stale handle blocking it.
        model.startLive()
        dispatcher.scheduler.advanceUntilIdle()
        assertEquals("a terminal relayed stream must not block a restart", 2, source.subscriptions)
    }
}
