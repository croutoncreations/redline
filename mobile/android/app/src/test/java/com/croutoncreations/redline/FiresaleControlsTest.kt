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
    private fun controls(state: FiresaleUiState = FiresaleUiState(view = view)) =
        FiresaleControls(state, {}, {}, now = { java.time.Instant.parse("2026-10-27T16:30:00Z") })

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

    // The list is a snapshot: a sale past its deadline has ended on the
    // desktop even if the phone hasn't reloaded, so it must not show as on.
    @Test fun saleIsOverOnceItsDeadlinePasses() {
        val before = FiresaleControls(FiresaleUiState(view = view), {}, {}, now = { java.time.Instant.parse("2026-10-27T16:59:00Z") })
        val after = FiresaleControls(FiresaleUiState(view = view), {}, {}, now = { java.time.Instant.parse("2026-10-27T17:00:00Z") })
        assertEquals("2026-10-27T17:00:00Z", before.activeSale("running")?.deadline)
        assertNull(after.activeSale("running"))
        assertTrue("Firesale is offered again once the old sale is over", after.canOffer("running"))
    }

    @Test fun olderDesktopOrUnloadedListOffersNothing() {
        assertFalse(controls(FiresaleUiState(view = view, visible = false)).canOffer("main"))
        assertNull(controls(FiresaleUiState(view = view, visible = false)).activeSale("running"))
        assertFalse(controls(FiresaleUiState(view = null)).canOffer("main"))
    }
}
