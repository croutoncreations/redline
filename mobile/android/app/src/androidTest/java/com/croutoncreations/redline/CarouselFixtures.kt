package com.croutoncreations.redline

/**
 * Synthetic accounts for tests and screenshots. Labels and numbers are made
 * up; no real account, plan or credential is represented.
 */
object CarouselFixtures {
    private const val HOUR = 3_600L

    /** When the synthetic numbers were "read"; everything dated is relative to it. */
    val readAt: java.time.Instant = java.time.Instant.now().truncatedTo(java.time.temporal.ChronoUnit.HOURS)

    private fun scheduling(decision: String, reason: String, weeklyResetIn: Long) = Scheduling(
        reservePercent = 25,
        weeklyFloors = listOf(
            WeeklyFloor(percent = 20, armsInSeconds = maxOf(0, weeklyResetIn - 24 * HOUR), armed = weeklyResetIn <= 24 * HOUR),
            WeeklyFloor(percent = 50, armsInSeconds = maxOf(0, weeklyResetIn - 72 * HOUR), armed = weeklyResetIn <= 72 * HOUR),
        ).sortedBy { it.armsInSeconds },
        decision = decision,
        reason = reason,
    )

    val personal = ProviderUsage(
        id = "claude-personal", provider = "claude", label = "Personal", plan = "Max 20x",
        accountStatus = "ok", accountIndex = 1, accountCount = 3,
        sourceLabel = "openusage source · 0/1 active · sampled 1m ago",
        session = Window(remainingPercent = 64, resetsInSeconds = 3 * HOUR + 55 * 60, elapsedPercent = 22),
        weekly = Window(remainingPercent = 41, resetsInSeconds = 76 * HOUR, elapsedPercent = 55),
        bankedResets = 2,
        resetCredits = ResetCredits(
            available = 2,
            expiries = listOf(readAt.plusSeconds(4 * 24 * HOUR).toString(), readAt.plusSeconds(10 * 24 * HOUR).toString()),
            nextExpiresInSeconds = 4 * 24 * HOUR,
        ),
        pools = listOf(
            Pool(key = "model:fable:weekly", label = "Fable", scope = "model", role = "weekly",
                remainingPercent = 72, resetsInSeconds = 76 * HOUR, resetInferred = true, elapsedPercent = 55),
        ),
        scheduling = scheduling("RUN", "short-window overflow", 76 * HOUR),
    )

    val client = ProviderUsage(
        id = "claude-client", provider = "claude", label = "Client — Northwind long-running engagement", plan = "Team",
        accountStatus = "ok", accountIndex = 2, accountCount = 3,
        sourceLabel = "openusage source · 0/1 active · sampled 2m ago",
        session = Window(remainingPercent = 0, resetsInSeconds = 57 * 60, elapsedPercent = 81),
        weekly = Window(remainingPercent = 58, resetsInSeconds = 4 * 24 * HOUR + 8 * HOUR, elapsedPercent = 38),
        scheduling = scheduling("WAIT", "5-hour window below reserve", 104 * HOUR),
    )

    val side = ProviderUsage(
        id = "claude-side", provider = "claude", label = "Side project", plan = "Pro",
        accountStatus = "ok", accountIndex = 3, accountCount = 3,
        sourceLabel = "openusage source · sampled 1m ago",
        session = Window(remainingPercent = 100, notStarted = true),
        weekly = Window(remainingPercent = 0, resetsInSeconds = 30 * HOUR, elapsedPercent = 82),
        bankedResets = 0, resetCredits = ResetCredits(available = 0),
        scheduling = scheduling("WAIT", "weekly allowance exhausted", 30 * HOUR),
    )

    val threeAccounts = listOf(personal, client, side)

    /** Weekly reset of [staleWithCredits], as last reported: three days out. */
    val staleWeeklyResetAt: java.time.Instant = readAt.plusSeconds(3 * 24 * HOUR)

    /**
     * An out-of-date account holding banked resets. Its numbers were read a
     * while ago: the 5-hour reset it reported has since passed (its frozen
     * countdown still says 3 hours) and the weekly one is still ahead.
     */
    val staleWithCredits = listOf(
        personal.copy(
            id = "claude-stale", label = "Personal", stale = true, accountStatus = "stale",
            staleReason = "OpenUsage says these numbers are out of date",
            session = Window(remainingPercent = 64, resetsInSeconds = 3 * HOUR, resetsAt = readAt.minusSeconds(2 * HOUR).toString(), elapsedPercent = 22),
            weekly = Window(remainingPercent = 41, resetsInSeconds = 76 * HOUR, resetsAt = staleWeeklyResetAt.toString(), elapsedPercent = 55),
            // One credit's deadline passed since the read; one is ahead.
            resetCredits = ResetCredits(
                available = 2,
                expiries = listOf(readAt.minusSeconds(HOUR).toString(), readAt.plusSeconds(10 * 24 * HOUR).toString()),
            ),
            // An inferred pool reset with no time reported.
            pools = listOf(
                Pool(key = "model:fable:weekly", label = "Fable", scope = "model", role = "weekly",
                    remainingPercent = 72, resetsInSeconds = 76 * HOUR, resetInferred = true, elapsedPercent = 55),
            ),
            // Kept, so the test proves its floor countdowns are not shown.
            scheduling = scheduling("WAIT", "weekly floor", 76 * HOUR),
        ),
        client,
    )

    val states = listOf(
        personal.copy(accountCount = 3),
        client.copy(
            stale = true, accountStatus = "stale", accountIndex = 2, accountCount = 3,
            staleReason = "Signed out in OpenUsage · sign this account in to OpenUsage on your Mac",
        ),
        side.copy(
            accountStatus = "signed_out", stale = true, accountIndex = 3, accountCount = 3,
            staleReason = "Claude Code is signed out · run claude auth login on your Mac",
        ),
    )

    val many = (1..7).map { index ->
        val label = listOf("Personal", "Client", "Side project", "Ops", "Research", "Staging", "Archive")[index - 1]
        personal.copy(
            id = "claude-$index", label = label, plan = if (label == "Research") "Pro" else "Max 5x",
            accountIndex = index, accountCount = 7,
            stale = index == 6, accountStatus = if (index == 6) "stale" else "ok",
        )
    }

    /**
     * Synthetic Team seats as the desktop sends them once OpenUsage cards
     * without a weekly window are accepted (placeholder card ids,
     * NATO-alphabet labels; no real account). Fresh 5-hour and Fable numbers, credits 0,
     * weekly not reported, dispatch held; one seat has a running 5-hour
     * window. Beside them, the personal account with a weekly window.
     */
    private fun teamSeat(id: String, label: String, index: Int, session: Window, fableIn: Long) = ProviderUsage(
        id = id, provider = "claude", label = label, plan = "Team 5x",
        accountStatus = "ok", accountIndex = index, accountCount = 6,
        sourceLabel = "openusage source · sampled 1m ago",
        session = session, weeklyNotReported = true,
        bankedResets = 0, resetCredits = ResetCredits(available = 0),
        pools = listOf(
            Pool(key = "model:fable:weekly", label = "Fable", scope = "model", role = "weekly",
                remainingPercent = 100, resetsInSeconds = fableIn, elapsedPercent = 10),
        ),
        scheduling = Scheduling(decision = "UNKNOWN", reason = "weekly limit not reported"),
    )

    val teamSeats = listOf(
        teamSeat("claude-5ea70001", "Delta", 1, Window(remainingPercent = 100, notStarted = true), 30 * HOUR),
        teamSeat("claude-5ea70002", "Alpha", 2, Window(remainingPercent = 100, notStarted = true), 78 * HOUR),
        teamSeat("claude-5ea70003", "Echo", 3, Window(remainingPercent = 76, resetsInSeconds = 2 * HOUR + 30 * 60, elapsedPercent = 50), 120 * HOUR),
        teamSeat("claude-5ea70004", "Bravo", 4, Window(remainingPercent = 100, notStarted = true), 159 * HOUR),
        teamSeat("claude-5ea70005", "Charlie", 5, Window(remainingPercent = 100, notStarted = true), 74 * HOUR),
        personal.copy(id = "claude-main", label = "claude-main", plan = "Max 5x", accountIndex = 6, accountCount = 6),
    )
}
