package com.croutoncreations.redline

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class FiresaleControlsTest {
    private val view = FiresalesView(
        manual = listOf(ManualFiresale("running", "2026-10-27T16:00:00Z", "2026-10-27T17:00:00Z", "owner")),
        eligible = listOf(
            FiresaleAccount("main", "Main", "claude", weeklyResetsAt = "2026-10-30T17:00:00Z"),
            FiresaleAccount("joe", "Joe", "claude", reasonUnavailable = "weekly usage not reported"),
            FiresaleAccount("running", "Running", "codex", weeklyResetsAt = "2026-10-30T17:00:00Z"),
        ),
    )
    private fun controls(state: FiresaleUiState = FiresaleUiState(view = view)) = FiresaleControls(state, {}, {})

    @Test fun offersFiresaleOnlyWhereAStartCanHelp() {
        val c = controls()
        assertTrue(c.canOffer("main"))
        assertFalse("blocked account", c.canOffer("joe"))
        assertFalse("already running shows Stop instead", c.canOffer("running"))
        assertFalse("account the desktop doesn't list", c.canOffer("cursor-main"))
    }

    @Test fun activeSaleIsFoundPerAccount() {
        val c = controls()
        assertEquals("2026-10-27T17:00:00Z", c.activeSale("running")?.deadline)
        assertNull(c.activeSale("main"))
    }

    @Test fun olderDesktopOrUnloadedListOffersNothing() {
        assertFalse(controls(FiresaleUiState(view = view, visible = false)).canOffer("main"))
        assertNull(controls(FiresaleUiState(view = view, visible = false)).activeSale("running"))
        assertFalse(controls(FiresaleUiState(view = null)).canOffer("main"))
    }
}
