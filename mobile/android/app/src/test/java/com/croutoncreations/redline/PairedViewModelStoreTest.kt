package com.croutoncreations.redline

import androidx.lifecycle.ViewModel
import androidx.lifecycle.ViewModelProvider
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.Job
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.setMain
import org.junit.After
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotSame
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

/**
 * Desktop-scoped view models live in a store owned by one pairing. Clearing
 * it on unpair must cancel their work, and the next pairing must get fresh
 * instances rather than the previous desktop's state.
 */
@OptIn(ExperimentalCoroutinesApi::class)
class PairedViewModelStoreTest {
    @Before fun setUp() = Dispatchers.setMain(StandardTestDispatcher())

    @After fun tearDown() = Dispatchers.resetMain()

    class Probe : ViewModel() {
        var cleared = false
        val work: Job = viewModelScope.launch(start = CoroutineStart.UNDISPATCHED) { awaitCancellation() }
        override fun onCleared() { cleared = true }
    }

    @Test
    fun unpairingClearsTheModelsAndTheNextPairingStartsFresh() {
        val first = PairedViewModelStore()
        val before = ViewModelProvider(first)[Probe::class.java]
        assertTrue(before.work.isActive)

        first.viewModelStore.clear()

        assertTrue("view model was not cleared", before.cleared)
        assertFalse("its in-flight work kept running", before.work.isActive)
        val after = ViewModelProvider(PairedViewModelStore())[Probe::class.java]
        assertNotSame(before, after)
        assertFalse(after.cleared)
    }
}
