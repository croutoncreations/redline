package ai.redline.app

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class UsageModelsTest {

    /** The app must decode exactly what mobile/core emits. */
    @Test
    fun decodesCoreUsageJson() {
        val raw = """
        {
          "generated_at": "2026-07-20T19:00:00Z",
          "providers": [{
            "id": "claude-main", "provider": "claude",
            "paused": false, "stale": false,
            "session": {"remaining_percent": 62, "resets_in_seconds": 14400,
                        "resets_at": "2026-07-20T23:00:00Z"},
            "weekly": {"remaining_percent": 53, "resets_in_seconds": 345600,
                       "resets_at": "2026-07-24T19:00:00Z"},
            "pools": [{"key": "model:fable:weekly", "label": "Fable", "scope": "model",
                       "remaining_percent": 100, "resets_in_seconds": 345600,
                       "resets_at": "2026-07-24T19:00:00Z", "reset_inferred": true}]
          }]
        }
        """.trimIndent()

        val view = redlineJson.decodeFromString(UsageView.serializer(), raw)

        assertEquals(1, view.providers.size)
        val provider = view.providers[0]
        assertEquals("claude-main", provider.id)
        assertEquals(62, provider.session?.remainingPercent)
        assertEquals(53, provider.weekly?.remainingPercent)
        assertEquals(1, provider.pools.size)
        assertEquals("Fable", provider.pools[0].label)
        assertTrue(provider.pools[0].resetInferred)
    }

    /** A newer desktop must not break an older app. */
    @Test
    fun ignoresUnknownFields() {
        val raw = """
        {"generated_at": "2026-07-20T19:00:00Z", "future_field": 42,
         "providers": [{"id": "a", "provider": "claude", "brand_new": {"x": 1}}]}
        """.trimIndent()

        val view = redlineJson.decodeFromString(UsageView.serializer(), raw)
        assertEquals("a", view.providers[0].id)
    }

    /** A provider with no snapshot renders without windows rather than crashing. */
    @Test
    fun handlesProviderWithoutSnapshot() {
        val raw = """
        {"providers": [{"id": "c", "provider": "codex", "error": "no usage snapshot yet"}]}
        """.trimIndent()

        val view = redlineJson.decodeFromString(UsageView.serializer(), raw)
        val provider = view.providers[0]
        assertNull(provider.session)
        assertNull(provider.weekly)
        assertEquals("No data", providerStatus(provider))
    }

    @Test
    fun formatsCountdowns() {
        assertEquals("now", formatCountdown(0))
        assertEquals("now", formatCountdown(-10))
        assertEquals("under a minute", formatCountdown(30))
        assertEquals("5m", formatCountdown(5 * 60))
        assertEquals("2h", formatCountdown(2 * 3600))
        assertEquals("2h 30m", formatCountdown(2 * 3600 + 30 * 60))
    }

    /**
     * Days must keep their hours.
     *
     * "1 day" covered everything from 24 to 47 hours, which is a 23 hour
     * ambiguity on exactly the number someone is planning around: whether a
     * window reopens tonight or tomorrow evening changes what you do next.
     */
    @Test
    fun countdownsOverADayKeepTheirHoursAndMinutes() {
        assertEquals("1d", formatCountdown(24 * 3600))
        assertEquals("1d 1h", formatCountdown(25 * 3600))
        assertEquals("1d 23h", formatCountdown(47 * 3600))
        assertEquals("2d", formatCountdown(48 * 3600))
        assertEquals("4d 6h", formatCountdown(4 * 24 * 3600 + 6 * 3600))
        // Minutes still matter just under a day, where the wait is short
        // enough to wait out.
        assertEquals("23h 45m", formatCountdown(23 * 3600 + 45 * 60))
        // Past a day, minutes are noise beside the hours.
        assertEquals("3d 2h", formatCountdown(3 * 24 * 3600 + 2 * 3600 + 45 * 60))
    }

    /**
     * The countdown is built into a sentence by the caller, so a value that
     * reads correctly alone can still be wrong in place: "now" produced
     * "Resets in now", and an inferred reset produced "Resets ~now".
     *
     * The label is therefore assembled in one place that knows about both.
     */
    @Test
    fun `an imminent reset reads as a sentence`() {
        assertEquals("Resets now", resetLabel(seconds = 0, inferred = false))
        assertEquals("Resets now", resetLabel(seconds = -30, inferred = false))
        assertEquals("Resets now", resetLabel(seconds = 20, inferred = false))

        // An inferred reset is a guess and still has to say so, without
        // becoming "Resets ~now".
        assertEquals("Resets about now", resetLabel(seconds = 0, inferred = true))
        assertEquals("Resets about now", resetLabel(seconds = 20, inferred = true))
    }

    @Test
    fun `an ordinary reset keeps the countdown`() {
        assertEquals("Resets in 2h 30m", resetLabel(seconds = 2 * 3600 + 30 * 60, inferred = false))
        assertEquals("Resets ~2h 30m", resetLabel(seconds = 2 * 3600 + 30 * 60, inferred = true))
        assertEquals("Resets in 1d 14h", resetLabel(seconds = 38 * 3600, inferred = false))
    }

    /**
     * The absolute time answers the other question people ask: not "how long"
     * but "when", which is what you match against a calendar.
     */
    @Test
    fun formatsTheAbsoluteResetTime() {
        // A fixed instant so the assertion does not depend on today.
        val friday = "2026-09-04T21:00:00Z"
        val zone = java.time.ZoneId.of("America/Los_Angeles")
        val now = java.time.Instant.parse("2026-09-02T21:00:00Z")

        // Two days out: named day plus time, because "Friday" is how people
        // hold a date in their head.
        assertEquals("Fri 2:00 PM", formatResetAt(friday, zone, now))

        // Later today: the day name would be noise.
        assertEquals(
            "8:30 PM",
            formatResetAt("2026-09-03T03:30:00Z", zone, java.time.Instant.parse("2026-09-03T01:00:00Z")),
        )

        // Tomorrow is worth naming as such rather than by weekday.
        assertEquals(
            "Tomorrow 2:00 PM",
            formatResetAt("2026-09-03T21:00:00Z", zone, now),
        )

        // Beyond a week a weekday alone is ambiguous, so use a date.
        assertEquals(
            "Sep 30, 2:00 PM",
            formatResetAt("2026-09-30T21:00:00Z", zone, now),
        )
    }

    /** A missing or unparseable timestamp must not crash or show junk. */
    @Test
    fun absoluteResetTimeToleratesBadInput() {
        val zone = java.time.ZoneId.of("America/Los_Angeles")
        val now = java.time.Instant.parse("2026-09-02T21:00:00Z")
        assertEquals("", formatResetAt("", zone, now))
        assertEquals("", formatResetAt("not a timestamp", zone, now))
    }

    /**
     * A five hour window the provider could not report must decode as unknown
     * rather than as absent, because the screen renders those differently: an
     * absent row says the limit does not exist, and that is the wrong thing to
     * tell someone deciding whether to start a run.
     */
    @Test
    fun `an unreadable session window decodes as unknown`() {
        val raw = """
            {"generated_at":"2026-09-03T14:57:00Z","providers":[
              {"id":"claude-main","provider":"claude","session_unknown":true,
               "weekly":{"remaining_percent":54,"resets_in_seconds":93600}}
            ]}
        """.trimIndent()

        val view = redlineJson.decodeFromString(UsageView.serializer(), raw)
        val provider = view.providers[0]

        assertNull("the window must not be invented", provider.session)
        assertTrue("the row should be shown as unknown", provider.sessionUnknown)
        // The weekly numbers were fine and must survive.
        assertEquals(54, provider.weekly?.remainingPercent)
        // Nothing here is an error or staleness: the data was fresh, one field
        // of it was simply missing.
        assertEquals("Live", providerStatus(provider))
    }

    /** An older desktop sends no such field, and the row stays absent. */
    @Test
    fun `an older desktop payload leaves the row absent`() {
        val raw = """
            {"generated_at":"2026-09-03T14:57:00Z","providers":[
              {"id":"codex-main","provider":"codex",
               "weekly":{"remaining_percent":0,"resets_in_seconds":300000}}
            ]}
        """.trimIndent()

        val provider = redlineJson.decodeFromString(UsageView.serializer(), raw).providers[0]
        assertNull(provider.session)
        assertFalse(provider.sessionUnknown)
    }

    /** Error outranks paused: without data there is nothing to label paused. */
    @Test
    fun statusPrioritisesErrorThenPausedThenStale() {
        assertEquals(
            "No data",
            providerStatus(ProviderUsage(error = "boom", paused = true, stale = true)),
        )
        assertEquals("Paused", providerStatus(ProviderUsage(paused = true, stale = true)))
        assertEquals("Stale", providerStatus(ProviderUsage(stale = true)))
        assertEquals("Live", providerStatus(ProviderUsage()))
    }
}

/**
 * The header must describe what is actually happening, derived from all of
 * the state rather than from the stream's opinion alone.
 *
 * Three fields each told a partial truth. The stream said "relayed" the moment
 * it found a relay configured -- before any relayed request had succeeded --
 * so on a real phone the pill read "relayed" above "Cannot reach Redline" for
 * a day while the relay had never carried a byte. Meanwhile [UsageUiState.transport]
 * knew the last successful route and [UsageUiState.failure] knew the last
 * attempt had failed, and neither was consulted.
 *
 * A route is only a fact once a request has travelled it.
 */
class ConnectionStatusTest {

    private val data = UsageView()

    @Test
    fun `a configured relay that has never carried data is not relayed`() {
        val state = UsageUiState(live = LiveState.RELAYED, failure = UsageUiState.Failure.UNREACHABLE)
        assertEquals(ConnectionStatus.UNREACHABLE, state.connection)
    }

    @Test
    fun `relayed means a relayed request succeeded`() {
        val state = UsageUiState(live = LiveState.RELAYED, view = data, transport = Transport.Relay)
        assertEquals(ConnectionStatus.RELAYED, state.connection)
    }

    @Test
    fun `a failure over good data is stale, whichever route carried the data`() {
        for (route in Transport.values()) {
            val state = UsageUiState(
                live = LiveState.RELAYED,
                view = data,
                transport = route,
                failure = UsageUiState.Failure.UNREACHABLE,
            )
            assertEquals("route $route", ConnectionStatus.STALE, state.connection)
        }
    }

    @Test
    fun `a live stream outranks everything`() {
        val state = UsageUiState(live = LiveState.LIVE, view = data, transport = Transport.Direct)
        assertEquals(ConnectionStatus.LIVE, state.connection)
    }

    @Test
    fun `connecting and reconnecting are reported while there is nothing to contradict them`() {
        assertEquals(ConnectionStatus.CONNECTING, UsageUiState(live = LiveState.CONNECTING).connection)
        assertEquals(
            ConnectionStatus.RECONNECTING,
            UsageUiState(live = LiveState.RECONNECTING, view = data).connection,
        )
    }

    @Test
    fun `direct data with no stream yet is neither live nor offline`() {
        // First load succeeded over the tailnet but the stream has not reported.
        val state = UsageUiState(live = LiveState.OFFLINE, view = data, transport = Transport.Direct)
        assertEquals(ConnectionStatus.CONNECTING, state.connection)
    }

    @Test
    fun `nothing loaded and nothing failing is quiet`() {
        assertEquals(ConnectionStatus.NONE, UsageUiState().connection)
    }

    @Test
    fun `a failure with nothing on screen is unreachable`() {
        val state = UsageUiState(failure = UsageUiState.Failure.UNREACHABLE)
        assertEquals(ConnectionStatus.UNREACHABLE, state.connection)
    }
}

/**
 * The pace mark: where the remaining bar's edge would be if usage were spread
 * evenly across the window. The bar shows what is left, so the mark sits at
 * 100 minus the elapsed fraction, and the two are read together -- bar past the
 * mark means there is more left than the clock would suggest; bar short of it
 * means the window is being spent faster than it is passing.
 */
class PaceTest {

    @Test
    fun `the mark is where the bar would be on an even spend`() {
        assertEquals(80, paceMarkPercent(elapsedPercent = 20))
        assertEquals(50, paceMarkPercent(elapsedPercent = 50))
        assertEquals(0, paceMarkPercent(elapsedPercent = 100))
    }

    @Test
    fun `more left than the clock suggests is ahead`() {
        assertEquals(Pace.AHEAD, paceOf(remainingPercent = 70, elapsedPercent = 50))
    }

    @Test
    fun `less left than the clock suggests is behind`() {
        assertEquals(Pace.BEHIND, paceOf(remainingPercent = 30, elapsedPercent = 50))
    }

    @Test
    fun `within a few points either way is on pace`() {
        assertEquals(Pace.ON_PACE, paceOf(remainingPercent = 50, elapsedPercent = 50))
        assertEquals(Pace.ON_PACE, paceOf(remainingPercent = 53, elapsedPercent = 50))
        assertEquals(Pace.ON_PACE, paceOf(remainingPercent = 47, elapsedPercent = 50))
    }

    @Test
    fun `the window model carries the elapsed fraction`() {
        val window = redlineJson.decodeFromString(
            Window.serializer(),
            """{"remaining_percent":58,"resets_in_seconds":3600,"elapsed_percent":42}""",
        )
        assertEquals(42, window.elapsedPercent)
    }

    @Test
    fun `an older desktop that sends no elapsed reads as the window start`() {
        val window = redlineJson.decodeFromString(
            Window.serializer(),
            """{"remaining_percent":58,"resets_in_seconds":3600}""",
        )
        assertEquals(0, window.elapsedPercent)
    }
}
