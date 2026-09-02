package ai.redline.app

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.setMain
import org.junit.After
import org.junit.Assert.assertEquals
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

