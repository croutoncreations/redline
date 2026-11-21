package com.croutoncreations.redline

import java.util.concurrent.CountDownLatch
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
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

/**
 * Every screen that waits on the core must end in an answer.
 *
 * The core's calls block in Go and cannot be cancelled from Kotlin, and
 * withContext joins its block, so a stuck call left a spinner or a disabled
 * button up forever. Each test hangs the call on a real IO thread -- as on the
 * phone -- advances virtual time past the bound, and checks that the screen
 * gave up and said so.
 */
@OptIn(ExperimentalCoroutinesApi::class)
class BoundedLoadScreensTest {

    private val dispatcher = StandardTestDispatcher()
    private val hang = CountDownLatch(1)

    @Before fun setUp() = Dispatchers.setMain(dispatcher)

    @After fun tearDown() {
        hang.countDown() // release any blocked IO thread
        Dispatchers.resetMain()
    }

    private fun blocked(): Nothing {
        hang.await()
        throw RuntimeException("released")
    }

    private fun passTheBound() {
        dispatcher.scheduler.runCurrent()
        dispatcher.scheduler.advanceTimeBy(LOAD_TIMEOUT_MILLIS + 1)
        dispatcher.scheduler.runCurrent()
    }

    @Test
    fun `accounts stops loading when the list hangs`() = runTest(dispatcher) {
        val source = object : AccountsSource {
            override fun fetchAccountsJson(): String = blocked()
            override fun discoverJson(provider: String): String = blocked()
            override fun addAccount(id: String, provider: String, label: String, card: String) = blocked()
            override fun renameAccount(id: String, label: String): String = blocked()
            override fun setDisabled(id: String, disabled: Boolean): String = blocked()
            override fun removeAccount(id: String): String = blocked()
            override fun isUnauthorized(error: Throwable) = false
        }
        val model = AccountsViewModel(source, Dispatchers.IO)
        model.refresh()
        dispatcher.scheduler.runCurrent()
        assertTrue("the load should start", model.state.value.loading)
        passTheBound()
        assertFalse("the spinner must stop", model.state.value.loading)
        assertEquals(UsageUiState.Failure.UNREACHABLE, model.state.value.failure)
    }

    @Test
    fun `firesale stops working when the list hangs`() = runTest(dispatcher) {
        val source = object : FiresaleSource {
            override fun list(): String = blocked()
            override fun start(account: String, until: String) = Unit
            override fun stop(account: String) = Unit
            override fun isNotFound(error: Throwable) = false
        }
        val model = FiresaleViewModel(source, Dispatchers.IO)
        model.refresh()
        dispatcher.scheduler.runCurrent()
        assertTrue("the load should start", model.state.value.working)
        passTheBound()
        assertFalse("the controls must come back", model.state.value.working)
        assertNotNull("the card must say why", model.state.value.error)
    }

    @Test
    fun `pairing stops working when the redeem hangs`() = runTest(dispatcher) {
        val source = object : PairingSource {
            override fun parsePairingUrl(raw: String): String =
                """{"base_url":"https://macbook.example.ts.net","pairing_token":"t"}"""
            override fun redeem(request: PairingRequest): String = blocked()
        }
        val model = PairingViewModel(source, NoopPairingStore(), Dispatchers.IO)
        model.pair("https://macbook.example.ts.net/pair#pairing_token=t")
        dispatcher.scheduler.runCurrent()
        assertTrue("pairing should start", model.state.value.working)
        passTheBound()
        assertFalse("the scanner must come back", model.state.value.working)
        assertNotNull("the screen must say why", model.state.value.error)
    }

    @Test
    fun `run logs stop loading when they hang`() = runTest(dispatcher) {
        val source = object : RunsSource {
            override fun fetchRunsJson(): String = """{"runs":[],"total_count":0,"failed_count":0,"running_count":0}"""
            override fun fetchTasksJson(): String = """{"tasks":[]}"""
            override fun fetchRunLogs(runId: String, stream: String): String = blocked()
            override fun dispatchTask(taskId: String): String = blocked()
            override fun isUnauthorized(error: Throwable) = false
        }
        val model = RunsViewModel(source, Dispatchers.IO)
        model.loadLogs("run-1")
        dispatcher.scheduler.runCurrent()
        assertTrue("logs should start loading", model.state.value.logsLoading)
        passTheBound()
        assertFalse("the log spinner must stop", model.state.value.logsLoading)
        assertEquals(UsageUiState.Failure.UNREACHABLE, model.state.value.failure)
    }

    private class NoopPairingStore : PairingStore {
        override fun updatePairing(configuration: PairingConfiguration) = Unit
        override fun clear() = Unit
    }
}
