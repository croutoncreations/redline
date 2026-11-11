package com.croutoncreations.redline

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class AccountsModelsTest {

    private fun claude(id: String, label: String = "", index: Int = 1, count: Int = 2, decision: String = "") =
        ProviderUsage(
            id = id, provider = "claude", label = label, accountIndex = index, accountCount = count,
            weekly = Window(remainingPercent = 50),
            scheduling = if (decision.isEmpty()) null else Scheduling(decision = decision),
        )

    @Test
    fun loneAccountsStayCardsAndSeveralBecomeOneGroupInPlace() {
        val providers = listOf(
            claude("claude-a"),
            ProviderUsage(id = "codex-main", provider = "codex"),
            claude("claude-b", index = 2),
        )
        val items = usageItems(providers)
        assertEquals(2, items.size)
        val group = items[0] as UsageItem.AccountGroup
        assertEquals(listOf("claude-a", "claude-b"), group.accounts.map { it.id })
        assertEquals("codex-main", (items[1] as UsageItem.Single).provider.id)
    }

    @Test
    fun emptyAndSingleAccountListsAreUnchanged() {
        assertTrue(usageItems(emptyList()).isEmpty())
        val single = usageItems(listOf(claude("claude-main", count = 1)))
        assertEquals(UsageItem.Single::class, single.single()::class)
    }

    @Test
    fun positionIsSaidInWords() {
        assertEquals("Overview · swipe left for each account", carouselPositionLabel(0, 3))
        assertEquals("Account 2 of 3", carouselPositionLabel(2, 3))
    }

    @Test
    fun headerCountsAccountsAndDispatchingWithoutCombiningUsage() {
        val accounts = listOf(claude("a", decision = "RUN"), claude("b", decision = "WAIT"), claude("c", index = 3, count = 3))
        assertEquals("Claude · 3 accounts · 1 of 3 dispatching", accountGroupSummary("claude", accounts))
        // A paused account whose last decision was RUN is not dispatching.
        val paused = accounts.map { if (it.id == "a") it.copy(paused = true) else it }
        assertEquals("Claude · 3 accounts · 0 of 3 dispatching", accountGroupSummary("claude", paused))
    }

    @Test
    fun announcementNamesAccountPlanAndState() {
        val account = claude("claude-client", "Client — Northwind long-running engagement", index = 2, count = 3, decision = "WAIT")
            .copy(plan = "Team")
        assertEquals(
            "Claude account 2 of 3, Client — Northwind long-running engagement, Team plan",
            accountAnnouncement("claude", account, page = 2, accounts = 3),
        )
    }

    @Test
    fun theDesktopsRunDecisionCountsAsDispatching() {
        // decision.Admit is "RUN" on the wire.
        assertTrue(claude("a", decision = "RUN").dispatching)
        assertEquals("Dispatching · surplus", schedulingLine(Scheduling(decision = "RUN", reason = "surplus")))
    }

    @Test
    fun accountNameFallsBackToId() {
        assertEquals("claude-main", claude("claude-main").accountName)
    }

    @Test
    fun dispatchStatesAreExplicit() {
        assertEquals("needs sign-in", accountDispatchState(claude("a").copy(accountStatus = "signed_out")))
        assertEquals("paused", accountDispatchState(claude("a").copy(paused = true)))
        assertEquals("out of date, dispatch held", accountDispatchState(claude("a").copy(stale = true)))
        assertEquals("no usage yet", accountDispatchState(claude("a").copy(weekly = null)))
        assertEquals("dispatching", accountDispatchState(claude("a", decision = "RUN")))
        assertEquals("no scheduler decision yet", accountDispatchState(claude("a")))
        assertEquals("usage unavailable, dispatch held", accountDispatchState(claude("a").copy(accountStatus = "error")))
    }

    @Test
    fun resetCreditsAreWordedAsExpiryNotReset() {
        val format: (String) -> String = { if (it.isEmpty()) "" else "Wed 12:00 AM" }
        val withCredits = claude("a").copy(
            bankedResets = 2,
            resetCredits = ResetCredits(available = 2, expiries = listOf("2026-10-14T00:00:00Z", "2026-10-20T00:00:00Z")),
        )
        assertEquals("2 available · next expires Wed 12:00 AM", resetCreditsLine(withCredits, formatTime = format))
        assertEquals("Credit deadlines: Wed 12:00 AM · Wed 12:00 AM", resetCreditExpiries(withCredits, formatTime = format))

        val undated = claude("a").copy(bankedResets = 3, resetCredits = ResetCredits(available = 3, expiries = listOf("2026-10-14T00:00:00Z")))
        assertEquals("Credit deadlines: Wed 12:00 AM · 2 with no expiry reported", resetCreditExpiries(undated, formatTime = format))

        val noDeadline = claude("a").copy(bankedResets = 1, resetCredits = ResetCredits(available = 1))
        assertEquals("1 available", resetCreditsLine(noDeadline, formatTime = format))
        assertEquals("", resetCreditExpiries(noDeadline, formatTime = format))

        // Unknown is never zero.
        assertEquals("Not reported", resetCreditsLine(claude("a")))
        assertEquals("", resetCreditsLine(claude("a"), reportedOnly = true))
        // An older desktop that sends only the count.
        assertEquals("0 available", resetCreditsLine(claude("a").copy(bankedResets = 0)))
    }

    @Test
    fun decodesAccountFieldsFromCoreAndDefaultsFromAnOlderOne() {
        val view = redlineJson.decodeFromString(
            UsageView.serializer(),
            """{"providers":[
              {"id":"claude-client","provider":"claude","label":"Client","plan":"Team","account_status":"stale",
               "account_index":1,"account_count":2,"banked_resets":2,
               "reset_credits":{"available":2,"expiries":["2026-10-14T00:00:00Z"],"next_expires_in_seconds":3600}},
              {"id":"codex-main","provider":"codex"}]}""",
        )
        val client = view.providers[0]
        assertEquals("Client", client.accountName)
        assertEquals("Team", client.plan)
        assertEquals(2, client.accountCount)
        assertEquals(2, client.resetCredits?.available)
        val codex = view.providers[1]
        assertEquals(1, codex.accountCount)
        assertEquals("codex-main", codex.accountName)
    }

    private val utc = java.time.ZoneId.of("UTC")
    private val now = java.time.Instant.parse("2026-10-10T12:00:00Z")
    private val us = java.util.Locale.US

    @Test
    fun outOfDateResetNamesAFixedTimeNeverACountdown() {
        // The frozen countdown says 3 hours; that number is from the last read
        // and must not be shown as if it were still ticking.
        val window = Window(remainingPercent = 41, resetsInSeconds = 3 * 3600, resetsAt = "2026-10-10T15:30:00Z")
        val line = outOfDateResetSentence(window, utc, now, us)
        assertEquals("Last reported reset today 3:30 PM", line)
        assertTrue("no present-tense countdown: $line", !line.contains("Resets in") && !line.contains(" in "))
        assertEquals(
            "Last reported reset tomorrow 9:00 AM",
            outOfDateResetSentence(window.copy(resetsAt = "2026-10-11T09:00:00Z"), utc, now, us),
        )
        assertEquals(
            "Last reported reset Wed 9:00 AM",
            outOfDateResetSentence(window.copy(resetsAt = "2026-10-14T09:00:00Z"), utc, now, us),
        )
        assertEquals(
            "Last reported reset today 3:30 PM (estimated)",
            outOfDateResetSentence(window.copy(resetInferred = true), utc, now, us),
        )
    }

    @Test
    fun outOfDateResetThatHasPassedIsDueNotRefreshed() {
        // Judged by the reset instant against now, even though the stale
        // countdown still claims time is left.
        val passed = Window(remainingPercent = 0, resetsInSeconds = 2 * 3600, resetsAt = "2026-10-10T09:15:00Z")
        val line = outOfDateResetSentence(passed, utc, now, us)
        assertEquals("Reset was due today 9:15 AM · awaiting fresh numbers", line)
        assertTrue(!line.contains("Resets") && !line.contains("now"))
        assertEquals(
            "Reset was due yesterday 11:00 PM · awaiting fresh numbers",
            outOfDateResetSentence(passed.copy(resetsAt = "2026-10-09T23:00:00Z"), utc, now, us),
        )
    }

    @Test
    fun outOfDateResetFromAnotherYearNamesTheYear() {
        assertEquals(
            "Reset was due Dec 30 2025, 9:00 AM · awaiting fresh numbers",
            outOfDateResetSentence(Window(resetsAt = "2025-12-30T09:00:00Z"), utc, now, us),
        )
        assertEquals("Last reported reset Oct 30, 9:00 AM", outOfDateResetSentence(Window(resetsAt = "2026-10-30T09:00:00Z"), utc, now, us))
    }

    @Test
    fun outOfDateResetWithoutATimeOrNotStartedInventsNothing() {
        assertEquals(
            "Reset time unknown while out of date",
            outOfDateResetSentence(Window(remainingPercent = 50, resetsInSeconds = 3600), utc, now, us),
        )
        assertEquals(
            "Not started when last read",
            outOfDateResetSentence(Window(remainingPercent = 100, notStarted = true), utc, now, us),
        )
    }

    @Test
    fun lastKnownCreditDeadlinesSayWhenTheyHaveExpired() {
        assertEquals("expired today 11:00 AM", lastKnownCreditDeadline("2026-10-10T11:00:00Z", utc, now, us))
        assertEquals("Wed 12:00 AM", lastKnownCreditDeadline("2026-10-14T00:00:00Z", utc, now, us))
        assertEquals("", lastKnownCreditDeadline("not a time", utc, now, us))
    }

    @Test
    fun expiredCreditsAreCountedOnlyFromDatedDeadlines() {
        val p = ProviderUsage(id = "a", provider = "claude", resetCredits = ResetCredits(
            available = 3, expiries = listOf("2026-10-10T11:00:00Z", "2026-10-11T11:00:00Z"),
        ))
        assertEquals(1, expiredCreditCount(p, now))
        assertEquals(0, expiredCreditCount(p.copy(resetCredits = null), now))
    }

    @Test
    fun liveResetsKeepTheirCountdown() {
        // Only out-of-date numbers lose the countdown.
        assertEquals("Resets in 3h", windowResetSentence(Window(resetsInSeconds = 3 * 3600)))
    }

    @Test
    fun weeklyNotReportedHoldsDispatchWithoutClaimingNoUsage() {
        val seat = ProviderUsage(
            id = "claude-5ea70003", provider = "claude", label = "Echo", accountStatus = "ok",
            session = Window(remainingPercent = 76), weeklyNotReported = true,
            scheduling = Scheduling(decision = "UNKNOWN"),
        )
        assertEquals("dispatch held, weekly not reported", accountDispatchState(seat))
        val decoded = redlineJson.decodeFromString(ProviderUsage.serializer(),
            """{"id":"x","provider":"claude","account_status":"ok","weekly_not_reported":true}""")
        assertTrue(decoded.weeklyNotReported)
        assertEquals(null, decoded.weekly)
        val optedIn = redlineJson.decodeFromString(ProviderUsage.serializer(),
            """{"id":"x","provider":"claude","account_status":"ok","weekly_not_reported":true,"weekly_policy":"session_only","session_reserve":0.5}""")
        assertEquals("weekly not reported, session-only scheduling", accountDispatchState(optedIn))
        assertEquals(null, optedIn.weekly)
    }
}
