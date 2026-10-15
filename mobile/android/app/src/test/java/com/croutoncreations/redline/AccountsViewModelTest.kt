package com.croutoncreations.redline

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
class AccountsViewModelTest {
    private val dispatcher = StandardTestDispatcher()

    @Before fun setUp() = Dispatchers.setMain(dispatcher)

    @After fun tearDown() = Dispatchers.resetMain()

    private val listJson = """{"restart_required":false,"accounts":[
        {"id":"claude-main","provider":"claude","label":"Personal","reads":"Default Claude login on the Mac",
         "editable":false,"running":true,"state":"Up to date","tone":"good","sign_in":"Run claude auth login."}]}"""

    private class Fake(val list: String) : AccountsSource {
        val calls = mutableListOf<String>()
        var refuse: String? = null
        var listFailure: String? = null
        override fun fetchAccountsJson(): String {
            calls += "list"
            listFailure?.let { throw RuntimeException(it) }
            return list
        }
        override fun discoverJson(provider: String) =
            """{"provider":"claude","cards":[{"id":"claude@ab12cd34","plan":"Team","has_weekly":true}],"hint":"h"}"""
        override fun addAccount(id: String, provider: String, label: String, card: String): String {
            calls += "add $id $provider $label $card"
            refuse?.let { throw RuntimeException(it) }
            return list.replace("\"restart_required\":false", "\"restart_required\":true")
        }
        override fun renameAccount(id: String, label: String) = list.also { calls += "rename $id $label" }
        override fun setDisabled(id: String, disabled: Boolean) = list.also { calls += "disabled $id $disabled" }
        override fun removeAccount(id: String): String {
            calls += "remove $id"
            refuse?.let { throw RuntimeException(it) }
            return list
        }
        override fun isUnauthorized(error: Throwable) = false
    }

    @Test
    fun listsLinksAndAsksForRestart() = runTest(dispatcher) {
        val source = Fake(listJson)
        val model = AccountsViewModel(source, dispatcher)
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()
        assertEquals("Personal", model.state.value.view?.accounts?.single()?.label)

        model.discover("claude")
        dispatcher.scheduler.advanceUntilIdle()
        val card = model.state.value.discover!!.cards.single()
        model.add(suggestedAccountId(card.id), "claude", "Client", card.id)
        dispatcher.scheduler.advanceUntilIdle()
        assertTrue(source.calls.contains("add claude-ab12cd34 claude Client claude@ab12cd34"))
        assertTrue(model.state.value.view!!.restartRequired)
        assertNull(model.state.value.discover)
    }

    @Test
    fun refusedChangeKeepsTheListAndSaysWhy() = runTest(dispatcher) {
        val source = Fake(listJson)
        val model = AccountsViewModel(source, dispatcher)
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()
        source.refuse = "redline api: 409 account conflict: account \"claude-x\" is used by execution profiles nightly; disable it instead, or move those profiles first"
        model.remove("claude-x")
        dispatcher.scheduler.advanceUntilIdle()
        val state = model.state.value
        assertEquals(1, state.view?.accounts?.size)
        assertTrue(state.message, state.message.startsWith("Account \"claude-x\" is used by execution profiles nightly"))
        model.dismissMessage()
        assertEquals("", model.state.value.message)
    }

    @Test
    fun olderDesktopWithoutAccountsApiShowsRetryInsteadOfSpinningForever() = runTest(dispatcher) {
        val source = Fake(listJson)
        // A desktop without /v1/accounts answers 404 through mobile/core's apiError.
        source.listFailure = "redline api: 404 404 Not Found"
        val model = AccountsViewModel(source, dispatcher)
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()
        val failed = model.state.value
        assertNull(failed.view)
        assertFalse(failed.loading)
        assertEquals(UsageUiState.Failure.UNREACHABLE, failed.failure)
        assertEquals(OLDER_DESKTOP_MESSAGE, failed.message)

        // Retry after the Mac is updated loads the list and drops the old reason.
        source.listFailure = null
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()
        val loaded = model.state.value
        assertNull(loaded.failure)
        assertEquals("", loaded.message)
        assertEquals("Personal", loaded.view?.accounts?.single()?.label)
    }

    @Test
    fun anyDesktopRefusalOnFirstLoadShowsTheFailurePanel() = runTest(dispatcher) {
        val source = Fake(listJson)
        source.listFailure = "redline api: 500 accounts file: cannot read accounts.yaml"
        val model = AccountsViewModel(source, dispatcher)
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()
        val state = model.state.value
        assertNull(state.view)
        assertEquals(UsageUiState.Failure.UNREACHABLE, state.failure)
        assertEquals("Accounts file: cannot read accounts.yaml", state.message)
    }

    @Test
    fun transportFailureOnFirstLoadStillShowsTheFailurePanel() = runTest(dispatcher) {
        val source = Fake(listJson)
        source.listFailure = "dial tcp: connection refused"
        val model = AccountsViewModel(source, dispatcher)
        model.refresh()
        dispatcher.scheduler.advanceUntilIdle()
        assertEquals(UsageUiState.Failure.UNREACHABLE, model.state.value.failure)
        assertEquals("", model.state.value.message)
    }

    @Test
    fun refusalStatusReadsTheHttpCode() {
        assertEquals(404, refusalStatus("redline api: 404 404 Not Found"))
        assertNull(refusalStatus("dial tcp: connection refused"))
    }

    @Test
    fun suggestedIdsAreValidAccountIds() {
        assertEquals("claude-ab12cd34", suggestedAccountId("claude@AB12CD34"))
        assertEquals("claude", suggestedAccountId("claude"))
    }

    @Test
    fun refusalMessageIgnoresTransportErrors() {
        assertEquals("", refusalMessage("dial tcp: connection refused"))
        assertEquals("Provider must be claude or codex", refusalMessage("redline api: 400 provider must be claude or codex"))
    }
}
