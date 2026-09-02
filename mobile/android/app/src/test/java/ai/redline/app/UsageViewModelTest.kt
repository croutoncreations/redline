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
}
