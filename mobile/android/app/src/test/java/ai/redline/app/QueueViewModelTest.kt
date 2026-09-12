package ai.redline.app

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.setMain
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class QueueViewModelTest {

    private val dispatcher = StandardTestDispatcher()

    @Before fun setUp() = Dispatchers.setMain(dispatcher)

    @After fun tearDown() = Dispatchers.resetMain()

    private val queueJson = """
        {"provider_account_id":"claude-main","snapshot_label":"1m ago","snapshot_stale":false,
         "dispatch_available":true,"next_up_task_id":"docs-refresh","next_up_name":"Fix docs",
         "ready_count":1,"blocked_count":1,
         "candidates":[
           {"task_id":"docs-refresh","name":"Fix docs","priority":55,"eligible":true,"is_next_up":true},
           {"task_id":"bug-hunt","name":"Find a bug","priority":80,"eligible":false,
            "reason":"cooldown for 4h 27m"}]}
    """.trimIndent()

    private fun source(
        queue: (String) -> String = { queueJson },
        control: (String, String) -> Unit = { _, _ -> },
    ) = object : QueueSource {
        override fun fetchQueueJson(providerAccountId: String): String = queue(providerAccountId)
        override fun controlProvider(providerAccountId: String, control2: String) =
            control(providerAccountId, control2)
        override fun isUnauthorized(error: Throwable): Boolean = false
    }

    @Test
    fun loadsTheQueueForTheSelectedProvider() = runTest(dispatcher) {
        var requested: String? = null
        val model = QueueViewModel(source(queue = { id -> requested = id; queueJson }), dispatcher)

        model.setProviders(listOf("claude-main", "codex-main"))
        dispatcher.scheduler.advanceUntilIdle()

        assertEquals("claude-main", requested)
        assertEquals(2, model.state.value.queue?.candidates?.size)
        assertEquals("Fix docs", model.state.value.queue?.nextUpName)
    }

    /** Switching providers must load that provider's queue, not reuse the old one. */
    @Test
    fun switchingProviderReloads() = runTest(dispatcher) {
        val requested = mutableListOf<String>()
        val model = QueueViewModel(source(queue = { id -> requested += id; queueJson }), dispatcher)

        model.setProviders(listOf("claude-main", "codex-main"))
        dispatcher.scheduler.advanceUntilIdle()
        model.selectProvider("codex-main")
        dispatcher.scheduler.advanceUntilIdle()

        assertEquals(listOf("claude-main", "codex-main"), requested)
        assertEquals("codex-main", model.state.value.selectedProvider)
    }

    /**
     * A pause changes what the queue will do, so the queue must be reloaded
     * rather than left showing the pre-pause picture.
     */
    @Test
    fun pausingReloadsTheQueue() = runTest(dispatcher) {
        var loads = 0
        var controlled: Pair<String, String>? = null
        val model = QueueViewModel(
            source(
                queue = { loads++; queueJson },
                control = { id, action -> controlled = id to action },
            ),
            dispatcher,
        )

        model.setProviders(listOf("claude-main"))
        dispatcher.scheduler.advanceUntilIdle()
        val loadsAfterFirst = loads

        model.controlProvider("pause")
        dispatcher.scheduler.advanceUntilIdle()

        assertEquals("claude-main" to "pause", controlled)
        assertTrue("a control must refresh the queue", loads > loadsAfterFirst)
    }

    @Test
    fun reportsFailuresWithoutLosingTheProviderList() = runTest(dispatcher) {
        val model = QueueViewModel(
            source(queue = { throw RuntimeException("down") }),
            dispatcher,
        )

        model.setProviders(listOf("claude-main", "codex-main"))
        dispatcher.scheduler.advanceUntilIdle()

        assertEquals(UsageUiState.Failure.UNREACHABLE, model.state.value.failure)
        // Losing the picker would strand the user on a broken screen.
        assertEquals(2, model.state.value.providers.size)
    }

    /** A good load clears a stale failure. */
    @Test
    fun successfulLoadClearsAPreviousFailure() = runTest(dispatcher) {
        var fail = true
        val model = QueueViewModel(
            source(queue = { if (fail) throw RuntimeException("down") else queueJson }),
            dispatcher,
        )

        model.setProviders(listOf("claude-main"))
        dispatcher.scheduler.advanceUntilIdle()
        assertEquals(UsageUiState.Failure.UNREACHABLE, model.state.value.failure)

        fail = false
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()

        assertNull(model.state.value.failure)
    }

    /** An empty provider list must not fire a request for an empty provider. */
    @Test
    fun doesNotLoadWithoutAProvider() = runTest(dispatcher) {
        var loads = 0
        val model = QueueViewModel(source(queue = { loads++; queueJson }), dispatcher)

        model.setProviders(emptyList())
        dispatcher.scheduler.advanceUntilIdle()

        assertEquals(0, loads)
    }
}
