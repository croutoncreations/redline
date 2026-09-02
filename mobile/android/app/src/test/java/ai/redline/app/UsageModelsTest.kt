package ai.redline.app

import org.junit.Assert.assertEquals
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
        assertEquals("1 day", formatCountdown(24 * 3600))
        assertEquals("4 days", formatCountdown(4 * 24 * 3600))
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
