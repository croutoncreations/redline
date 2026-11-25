package com.croutoncreations.redline

import org.junit.Assert.assertEquals
import org.junit.Test

class ResetKindsTest {
    private val format: (String) -> String = { it.substring(0, 10) }

    private fun provider(vararg kinds: ResetKindGroup) = ProviderUsage(
        id = "claude-main", provider = "claude",
        resetCredits = ResetCredits(available = kinds.sumOf { it.available }, kinds = kinds.toList()),
    )

    @Test
    fun summaryNamesEachKindWithItsCount() {
        val p = provider(
            ResetKindGroup("full", "Full", 1, expiries = listOf("2026-12-16T04:59:00Z")),
            ResetKindGroup("fable", "Fable", 3, held = 1, expiries = List(3) { "2027-03-09T07:59:00Z" }),
        )
        assertEquals("Full 1 · Fable 3 (+1 held)", resetKindsSummary(p))
    }

    @Test
    fun summaryIsEmptyFromAnOlderDesktop() {
        assertEquals("", resetKindsSummary(ProviderUsage(id = "a", provider = "claude", resetCredits = ResetCredits(available = 2))))
    }

    @Test
    fun sameDeadlineIsOneLine() {
        val fable = ResetKindGroup("fable", "Fable", 3, expiries = List(3) { "2027-03-09T07:59:00Z" })
        assertEquals(listOf("All expire 2027-03-09"), resetKindLines(fable, format))
        val one = ResetKindGroup("full", "Full", 1, expiries = listOf("2026-12-16T04:59:00Z"))
        assertEquals(listOf("Expires 2026-12-16"), resetKindLines(one, format))
    }

    @Test
    fun distinctDeadlinesCollapseBeyondThree() {
        val dates = listOf("2026-12-01", "2026-12-02", "2026-12-03", "2026-12-04", "2026-12-05").map { "${it}T00:00:00Z" }
        val group = ResetKindGroup("fable", "Fable", 6, expiries = dates + dates.last())
        assertEquals(
            listOf("Expires 2026-12-01", "Expires 2026-12-02", "Expires 2026-12-03", "+3 more, last 2026-12-05"),
            resetKindLines(group, format),
        )
    }

    @Test
    fun undatedAndHeldNotesFollowTheDeadlines() {
        val group = ResetKindGroup(
            "full", "Full", 2, held = 2, expiries = listOf("2026-12-16T04:59:00Z"),
            notes = listOf("2 paused"),
        )
        assertEquals(listOf("Expires 2026-12-16", "No expiry reported", "2 paused"), resetKindLines(group, format))
    }

    @Test
    fun undatedResetsAreNotFoldedIntoMore() {
        val dates = listOf("2026-12-01", "2026-12-02", "2026-12-03", "2026-12-04", "2026-12-05").map { "${it}T00:00:00Z" }
        val group = ResetKindGroup("fable", "Fable", 6, expiries = dates)
        assertEquals(
            listOf("Expires 2026-12-01", "Expires 2026-12-02", "Expires 2026-12-03", "+2 more, last 2026-12-05", "No expiry reported"),
            resetKindLines(group, format),
        )
    }
}
