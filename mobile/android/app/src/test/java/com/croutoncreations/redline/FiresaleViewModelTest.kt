package com.croutoncreations.redline

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.*
import org.junit.After
import org.junit.Before
import org.junit.Test
import org.junit.Assert.*

@OptIn(ExperimentalCoroutinesApi::class)
class FiresaleViewModelTest {
    private val dispatcher = StandardTestDispatcher()
    @Before fun setup() = Dispatchers.setMain(dispatcher)
    @After fun cleanup() = Dispatchers.resetMain()
    private class Source : FiresaleSource {
        var raw = """{"manual":[],"eligible":[{"account":"blocked","label":"Blocked","provider":"claude","reason_unavailable":"No capacity"},{"account":"main","label":"Main","provider":"claude","weekly_resets_at":null}]}"""
        var error: Exception? = null
        var missing = false
        var actionError: Exception? = null
        var lists = 0
        var started: Pair<String,String>? = null
        var stopped: String? = null
        override fun list(): String { lists++; error?.let { throw it }; return raw }
        override fun start(account: String, until: String) { started = account to until; actionError?.let { throw it } }
        override fun stop(account: String) { stopped = account }
        override fun isNotFound(error: Throwable) = missing
    }
    @Test fun preselectsAvailableAndValidatesDuration() = runTest(dispatcher) {
        val source = Source(); val model = FiresaleViewModel(source, dispatcher)
        model.refresh(); runCurrent()
        assertEquals("main", model.state.value.selectedAccount)
        model.selectDuration("weekly_reset")
        assertEquals("15m", model.state.value.until)
        model.selectAccount("blocked")
        assertFalse(model.state.value.canStart)
        model.selectAccount("main"); model.selectDuration("2h"); model.start(); runCurrent()
        assertEquals("main" to "2h", source.started)
        assertEquals(2, source.lists)
        assertNull(model.state.value.error)
    }
    @Test fun weeklyDurationIsResetWhenSwitchingToAccountWithoutWeeklyReset() = runTest(dispatcher) {
        val source = Source()
        source.raw = """{"eligible":[{"account":"weekly","label":"Weekly","provider":"claude","weekly_resets_at":"2026-07-21T10:00:00Z"},{"account":"other","label":"Other","provider":"claude"}]}"""
        val model = FiresaleViewModel(source, dispatcher)
        model.refresh(); runCurrent()
        model.selectDuration("weekly_reset")
        assertEquals("weekly_reset", model.state.value.until)
        model.selectAccount("other")
        assertEquals("15m", model.state.value.until)
        model.selectDuration("invalid")
        assertEquals("15m", model.state.value.until)
        val loaded = model.state.value.loaded(FiresalesView())
        assertNull(loaded.selectedAccount)
        assertFalse(loaded.canStart)
    }
    @Test fun action404DoesNotHideTheCardOrDropTheSelection() = runTest(dispatcher) {
        val source = Source(); val model = FiresaleViewModel(source, dispatcher)
        model.refresh(); runCurrent()
        source.missing = true; source.actionError = IllegalStateException("sale refused")
        model.start(); model.start(); runCurrent()
        assertTrue(model.state.value.visible)
        assertEquals("sale refused", model.state.value.error)
        assertEquals("main", model.state.value.selectedAccount)
        assertEquals(1, source.lists)
    }
    @Test fun actionsRefreshAndFailuresKeepDataBut404HidesCard() = runTest(dispatcher) {
        val source = Source(); val model = FiresaleViewModel(source, dispatcher)
        model.refresh(); runCurrent()
        source.raw = """{"manual":[{"account":"main","started_at":"now","deadline":"later","started_by":"phone"}],"eligible":[]}"""
        model.stop("main"); runCurrent()
        assertEquals("main", source.stopped)
        assertEquals(1, model.state.value.view?.manual?.size)
        source.error = IllegalStateException("asleep")
        model.refresh(); runCurrent()
        assertEquals("asleep", model.state.value.error)
        assertTrue(model.state.value.visible)
        source.missing = true
        model.refresh(); runCurrent()
        assertFalse(model.state.value.visible)
    }
}
