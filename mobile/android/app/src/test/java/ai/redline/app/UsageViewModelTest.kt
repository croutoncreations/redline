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
 * The route now travels in the payload rather than being asked of the client:
 * three view models share one client, so a flag on it is last-write-wins and
 * another screen's refresh could change what this one reports.
 */
class TransportReportingTest {

    private val dispatcher = StandardTestDispatcher()

    @Before fun setUp() = Dispatchers.setMain(dispatcher)

    @After fun tearDown() = Dispatchers.resetMain()

    private class RoutedSource(private val relayed: Boolean) : UsageSource {
        override fun fetchUsageJson(): String =
            """{"providers":[],"health":{"scheduler_enabled":false},"relayed":$relayed}"""
        override fun isUnauthorized(error: Throwable): Boolean = false
    }

    @Test
    fun `a relayed payload reports the relay`() = runTest(dispatcher) {
        val model = UsageViewModel(RoutedSource(relayed = true), ioDispatcher = dispatcher)
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()
        assertEquals(Transport.Relay, model.state.value.transport)
    }

    @Test
    fun `a direct payload reports direct`() = runTest(dispatcher) {
        val model = UsageViewModel(RoutedSource(relayed = false), ioDispatcher = dispatcher)
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()
        assertEquals(Transport.Direct, model.state.value.transport)
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
        //
        // runCurrent, not advanceUntilIdle, from here on: the relayed state
        // starts a polling loop that never goes idle, so advancing to idle
        // would hang this test -- and, because the suite shares a JVM, take
        // every later test with it rather than failing loudly.
        source.lastOnState?.invoke("relayed")
        dispatcher.scheduler.runCurrent()
        assertEquals(LiveState.RELAYED, model.state.value.live)

        // The tailnet returns and the screen resumes: a new stream must start
        // rather than the stale handle blocking it.
        model.startLive()
        dispatcher.scheduler.runCurrent()
        assertEquals("a terminal relayed stream must not block a restart", 2, source.subscriptions)

        model.stopLive()
        dispatcher.scheduler.runCurrent()
    }
}

/**
 * The pill describes the connection, and must not be read as describing the
 * numbers.
 *
 * The stream pushes every five seconds but the collector polls the provider
 * every five minutes, so "live" sat above data that was four minutes old and
 * looked like a lie. Both facts were true and the screen only showed one.
 *
 * The relayed route is folded in here too: a live frame can only arrive over
 * the tailnet, because the tunnel cannot carry a stream. Leaving transport
 * untouched on a frame let a relayed refresh's Relay linger over direct data.
 */
class LiveFreshnessTest {

    private val dispatcher = StandardTestDispatcher()

    @Before fun setUp() = Dispatchers.setMain(dispatcher)

    @After fun tearDown() = Dispatchers.resetMain()

    private class StreamingSource : UsageSource {
        var onUsage: ((String) -> Unit)? = null
        var onState: ((String) -> Unit)? = null
        override fun fetchUsageJson(): String =
            """{"providers":[],"health":{"scheduler_enabled":false},"relayed":true}"""
        override fun isUnauthorized(error: Throwable): Boolean = false
        override fun stream(
            onUsage: (String) -> Unit,
            onState: (String) -> Unit,
        ): AutoCloseable {
            this.onUsage = onUsage
            this.onState = onState
            return AutoCloseable { }
        }
    }

    @Test
    fun `a live frame returns the route to direct`() = runTest(dispatcher) {
        val source = StreamingSource()
        val model = UsageViewModel(source, ioDispatcher = dispatcher)

        // A relayed refresh puts the pill on the relay.
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()
        assertEquals(Transport.Relay, model.state.value.transport)

        // The tailnet returns and a live frame arrives. A stream cannot cross
        // the relay, so this frame is direct by construction.
        model.startLive()
        dispatcher.scheduler.advanceUntilIdle()
        source.onUsage?.invoke("""{"providers":[],"health":{"scheduler_enabled":false}}""")
        dispatcher.scheduler.advanceUntilIdle()

        assertEquals(
            "a live frame can only be direct, so it must clear a stale relayed route",
            Transport.Direct,
            model.state.value.transport,
        )
    }
}

/**
 * Once the stream cannot run, something has to keep fetching.
 *
 * Over a relay the stream is terminal by design: the tunnel carries one
 * request and one response. The screen then depended entirely on a manual
 * pull, so a single failed attempt -- the normal case while the desktop
 * reconnects -- stayed on screen indefinitely. That is the "relayed" header
 * over a "Cannot reach Redline" body, reported from a real phone.
 */
class RelayedPollingTest {

    private val dispatcher = StandardTestDispatcher()

    @Before fun setUp() = Dispatchers.setMain(dispatcher)

    @After fun tearDown() = Dispatchers.resetMain()

    private class CountingSource : UsageSource {
        var fetches = 0
        var onState: ((String) -> Unit)? = null
        override fun fetchUsageJson(): String {
            fetches += 1
            return """{"providers":[],"health":{"scheduler_enabled":false},"relayed":true}"""
        }
        override fun isUnauthorized(error: Throwable): Boolean = false
        override fun stream(
            onUsage: (String) -> Unit,
            onState: (String) -> Unit,
        ): AutoCloseable {
            this.onState = onState
            return AutoCloseable { }
        }
    }

    @Test
    fun `a relayed screen keeps fetching after the stream gives up`() = runTest(dispatcher) {
        val source = CountingSource()
        val model = UsageViewModel(source, ioDispatcher = dispatcher)

        model.startLive()
        dispatcher.scheduler.advanceUntilIdle()

        // The core reports it cannot stream over the relay.
        source.onState?.invoke("relayed")
        // runCurrent, not advanceUntilIdle: the poll loop never goes idle, so
        // advancing to idle would spin forever.
        dispatcher.scheduler.runCurrent()

        val afterGivingUp = source.fetches
        // Time passes as it would with the screen open.
        dispatcher.scheduler.advanceTimeBy(70_000)
        dispatcher.scheduler.runCurrent()

        assertTrue(
            "a relayed screen must poll; it fetched $afterGivingUp times and never again",
            source.fetches > afterGivingUp,
        )

        // Leave no live coroutine behind: runTest waits for the scheduler to
        // drain, and an endless poll would hang the whole suite rather than
        // fail it. That hang is what made this look like a failing assertion.
        model.stopLive()
        dispatcher.scheduler.runCurrent()
    }

    @Test
    fun `stopping the screen stops the polling`() = runTest(dispatcher) {
        val source = CountingSource()
        val model = UsageViewModel(source, ioDispatcher = dispatcher)

        model.startLive()
        dispatcher.scheduler.advanceUntilIdle()
        source.onState?.invoke("relayed")
        dispatcher.scheduler.runCurrent()

        model.stopLive()
        dispatcher.scheduler.runCurrent()
        val afterStopping = source.fetches

        // A closed screen must not keep paying for relayed round trips. One
        // full interval is enough to prove the loop is gone; advancing further
        // would only spin the scheduler against a job that no longer exists.
        dispatcher.scheduler.advanceTimeBy(60_000)
        dispatcher.scheduler.runCurrent()

        assertEquals(
            "a stopped screen must not keep polling in the background",
            afterStopping,
            source.fetches,
        )
    }
}

/**
 * A screen showing an unreachable error must keep trying.
 *
 * The poll was hung off the stream's terminal "relayed" state, so it only ran
 * when a stream had started and then given up. A refresh that failed on its
 * own -- which is what happens when the tailnet drops while the desktop is
 * mid-reconnect -- set UNREACHABLE and stopped, and the screen sat there with
 * a working relay one attempt away. Observed on a real phone: "relayed" in the
 * header, "Cannot reach Redline" in the body, unchanged for minutes.
 */
class UnreachableScreenRetriesTest {

    private val dispatcher = StandardTestDispatcher()

    @Before fun setUp() = Dispatchers.setMain(dispatcher)

    @After fun tearDown() = Dispatchers.resetMain()

    private class FailsThenWorksSource : UsageSource {
        var fetches = 0
        override fun fetchUsageJson(): String {
            fetches += 1
            if (fetches == 1) throw RuntimeException("reach redline: no route to host")
            return """{"providers":[],"health":{"scheduler_enabled":false},"relayed":true}"""
        }
        override fun isUnauthorized(error: Throwable): Boolean = false
    }

    @Test
    fun `a failed refresh retries on its own and recovers`() = runTest(dispatcher) {
        val source = FailsThenWorksSource()
        val model = UsageViewModel(source, ioDispatcher = dispatcher)

        model.refresh()
        dispatcher.scheduler.runCurrent()
        assertEquals(UsageUiState.Failure.UNREACHABLE, model.state.value.failure)

        // Nobody touches the phone. The screen must recover by itself.
        dispatcher.scheduler.advanceTimeBy(35_000)
        dispatcher.scheduler.runCurrent()

        assertNull(
            "an unreachable screen must retry without the user swiping",
            model.state.value.failure,
        )
        assertEquals(Transport.Relay, model.state.value.transport)

        model.stopLive()
        dispatcher.scheduler.runCurrent()
    }
}

/**
 * A relayed screen must keep polling, and must not strand a stale failure.
 *
 * Two defects met here, and together they put "Offline" above perfectly good
 * relayed data -- reported from a real phone after re-pairing.
 *
 * The poll stopped only when the stream was LIVE, which cannot happen over a
 * relay: the tunnel carries one request and one response, so the state is
 * RELAYED forever. It therefore ran its ten attempts and stopped, leaving
 * nothing to refresh the screen.
 *
 * And the header shows "Offline" whenever a failure is set and data exists.
 * One transient failure inside that window -- the desktop reconnecting -- was
 * enough to set it, and once the poll had stopped nothing ever cleared it.
 */
class RelayedScreenStaysCurrentTest {

    private val dispatcher = StandardTestDispatcher()

    @Before fun setUp() = Dispatchers.setMain(dispatcher)

    @After fun tearDown() = Dispatchers.resetMain()

    /** Succeeds, then fails once, then succeeds again. */
    private class BlipSource : UsageSource {
        var fetches = 0
        var onState: ((String) -> Unit)? = null
        override fun fetchUsageJson(): String {
            fetches += 1
            if (fetches == 2) throw RuntimeException("reach redline: connection reset")
            return """{"providers":[],"health":{"scheduler_enabled":false},"relayed":true}"""
        }
        override fun isUnauthorized(error: Throwable): Boolean = false
        override fun stream(
            onUsage: (String) -> Unit,
            onState: (String) -> Unit,
        ): AutoCloseable {
            this.onState = onState
            return AutoCloseable { }
        }
    }

    @Test
    fun `a blip over the relay does not strand Offline over good data`() = runTest(dispatcher) {
        val source = BlipSource()
        val model = UsageViewModel(source, ioDispatcher = dispatcher)

        model.startLive()
        dispatcher.scheduler.runCurrent()
        source.onState?.invoke("relayed")
        dispatcher.scheduler.runCurrent()

        // First poll succeeds, second fails, third must recover.
        repeat(3) {
            dispatcher.scheduler.advanceTimeBy(31_000)
            dispatcher.scheduler.runCurrent()
        }

        assertNull(
            "a later success must clear the blip, or the header reads Offline over live data",
            model.state.value.failure,
        )

        model.stopLive()
        dispatcher.scheduler.runCurrent()
    }

    @Test
    fun `a relayed screen is still polling after the recovery budget`() = runTest(dispatcher) {
        val source = BlipSource()
        val model = UsageViewModel(source, ioDispatcher = dispatcher)

        model.startLive()
        dispatcher.scheduler.runCurrent()
        source.onState?.invoke("relayed")
        dispatcher.scheduler.runCurrent()

        // Well past ten attempts.
        repeat(14) {
            dispatcher.scheduler.advanceTimeBy(31_000)
            dispatcher.scheduler.runCurrent()
        }
        val afterBudget = source.fetches

        dispatcher.scheduler.advanceTimeBy(31_000)
        dispatcher.scheduler.runCurrent()

        assertTrue(
            "a relayed screen has no live stream, so its poll is the only thing keeping it " +
                "current and must not expire; it stopped at $afterBudget fetches",
            source.fetches > afterBudget,
        )

        model.stopLive()
        dispatcher.scheduler.runCurrent()
    }
}
