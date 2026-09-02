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
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class RunsViewModelTest {

    private val dispatcher = StandardTestDispatcher()

    // viewModelScope dispatches on Dispatchers.Main, which has no
    // implementation on the JVM until the test module installs one.
    @Before fun setUp() = Dispatchers.setMain(dispatcher)

    @After fun tearDown() = Dispatchers.resetMain()

    private val runsJson = """
        {"runs":[
          {"id":"aaaa1111-2222","short_id":"aaaa1111","task_id":"release-notes","state":"completed",
           "outcome":"completed","succeeded":true,"running":false,"duration_label":"1m 41s",
           "relative_label":"15h ago","summary":"Updated the changelog.",
           "pull_request_url":"https://example.com/pr/61"},
          {"id":"bbbb1111-2222","short_id":"bbbb1111","task_id":"nightly","state":"failed",
           "outcome":"failed","succeeded":false,"running":false,"duration_label":"30m",
           "relative_label":"2h ago","error":"tests failed"}
        ],"total_count":2,"failed_count":1,"running_count":0}
    """.trimIndent()

    private val tasksJson = """
        {"tasks":[
          {"id":"nightly","name":"Nightly tests","type":"recurring","state":"queued",
           "enabled":true,"dispatchable":true},
          {"id":"old","name":"Retired","type":"one_off","state":"disabled",
           "enabled":false,"dispatchable":false}
        ]}
    """.trimIndent()

    private fun source(
        runs: () -> String = { runsJson },
        tasks: () -> String = { tasksJson },
        logs: (String, String) -> String = { _, _ -> "log line\n" },
        dispatch: (String) -> String = { """{"started":true,"run_id":"new-run","reason":"capacity available"}""" },
        unauthorized: (Throwable) -> Boolean = { false },
    ) = object : RunsSource {
        override fun fetchRunsJson(): String = runs()
        override fun fetchTasksJson(): String = tasks()
        override fun fetchRunLogs(runId: String, stream: String): String = logs(runId, stream)
        override fun dispatchTask(taskId: String): String = dispatch(taskId)
        override fun isUnauthorized(error: Throwable): Boolean = unauthorized(error)
    }

    @Test
    fun loadsRunsAndCountsFailures() = runTest(dispatcher) {
        val model = RunsViewModel(source(), dispatcher)
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()

        val state = model.state.value
        assertFalse(state.loading)
        assertNull(state.failure)
        assertEquals(2, state.runs?.runs?.size)
        assertEquals(1, state.runs?.failedCount)
        assertEquals("release-notes", state.runs?.runs?.get(0)?.taskId)
    }

    /** A transport failure must not be reported as a credential problem. */
    @Test
    fun reportsUnreachableSeparatelyFromUnauthorized() = runTest(dispatcher) {
        val model = RunsViewModel(
            source(runs = { throw RuntimeException("connection refused") }),
            dispatcher,
        )
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()

        assertEquals(UsageUiState.Failure.UNREACHABLE, model.state.value.failure)
    }

    @Test
    fun reportsUnauthorized() = runTest(dispatcher) {
        val model = RunsViewModel(
            source(
                runs = { throw RuntimeException("401") },
                unauthorized = { true },
            ),
            dispatcher,
        )
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()

        assertEquals(UsageUiState.Failure.UNAUTHORIZED, model.state.value.failure)
    }

    /**
     * A started dispatch and a held-back one look similar but mean opposite
     * things, so the UI must be able to tell them apart.
     */
    @Test
    fun dispatchReportsStarted() = runTest(dispatcher) {
        val model = RunsViewModel(source(), dispatcher)
        model.dispatch("nightly")
        dispatcher.scheduler.advanceUntilIdle()

        val result = model.state.value.lastDispatch
        assertTrue("a started dispatch must report started", result?.started == true)
        assertEquals("new-run", result?.runId)
    }

    @Test
    fun dispatchReportsHeldBackWithTheSchedulersReason() = runTest(dispatcher) {
        val model = RunsViewModel(
            source(dispatch = { """{"started":false,"reason":"weekly pace would be exceeded"}""" }),
            dispatcher,
        )
        model.dispatch("nightly")
        dispatcher.scheduler.advanceUntilIdle()

        val result = model.state.value.lastDispatch
        assertFalse("nothing started", result?.started == true)
        assertEquals("weekly pace would be exceeded", result?.reason)
    }

    @Test
    fun dispatchReportsRefusal() = runTest(dispatcher) {
        val model = RunsViewModel(
            source(dispatch = { """{"started":false,"refused":true,"reason":"task is disabled"}""" }),
            dispatcher,
        )
        model.dispatch("old")
        dispatcher.scheduler.advanceUntilIdle()

        val result = model.state.value.lastDispatch
        assertTrue("a refusal must be marked", result?.refused == true)
        assertEquals("task is disabled", result?.reason)
    }

    /** Refreshing after a dispatch must not leave the old banner on screen. */
    @Test
    fun refreshClearsAStaleDispatchResult() = runTest(dispatcher) {
        val model = RunsViewModel(source(), dispatcher)
        model.dispatch("nightly")
        dispatcher.scheduler.advanceUntilIdle()
        assertTrue(model.state.value.lastDispatch != null)

        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()

        assertNull("a refresh starts a new picture", model.state.value.lastDispatch)
    }

    @Test
    fun loadsLogsForARun() = runTest(dispatcher) {
        var requested: Pair<String, String>? = null
        val model = RunsViewModel(
            source(logs = { id, stream -> requested = id to stream; "stdout contents" }),
            dispatcher,
        )
        model.loadLogs("aaaa1111-2222")
        dispatcher.scheduler.advanceUntilIdle()

        assertEquals("aaaa1111-2222" to "stdout", requested)
        assertEquals("stdout contents", model.state.value.logs)
    }

    /**
     * A successful load must clear an earlier failure, or the screen keeps
     * claiming to be offline over live data.
     */
    @Test
    fun successfulRefreshClearsAPreviousFailure() = runTest(dispatcher) {
        var fail = true
        val model = RunsViewModel(
            source(runs = { if (fail) throw RuntimeException("down") else runsJson }),
            dispatcher,
        )
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()
        assertEquals(UsageUiState.Failure.UNREACHABLE, model.state.value.failure)

        fail = false
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()

        assertNull(model.state.value.failure)
        assertEquals(2, model.state.value.runs?.runs?.size)
    }
}
