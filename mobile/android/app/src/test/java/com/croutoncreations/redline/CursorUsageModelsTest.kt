package com.croutoncreations.redline

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class CursorUsageModelsTest {

    /** The app must decode exactly what mobile/core emits for Cursor. */
    @Test
    fun `decodes the core's Cursor card`() {
        val raw = """
        {"providers":[{"id":"cursor-main","provider":"cursor","weekly_label":"Monthly allowance",
          "weekly":{"remaining_percent":60,"resets_in_seconds":1296000,"resets_at":"2026-10-24T12:00:00Z","elapsed_percent":50}}],
         "cursor":{"plan":"Pro","state":"Up to date","tone":"healthy","stale":false,
          "updated_label":"Updated 1m ago · from OpenUsage","scheduled":true,
          "note":"Monthly pools schedule Cursor jobs. Grok Bot, on-demand and credits never do.",
          "groups":[{"key":"monthly","heading":"Monthly","meters":[
            {"key":"apiUsage","label":"Other Models","value_label":"Used up","has_limit":true,
             "remaining_percent":0,"exhausted":true,"resets_at":"2026-10-13T04:16:45Z","resets_in_seconds":317805}]},
           {"key":"on_demand","heading":"On-demand","meters":[
            {"key":"onDemand","label":"On-demand","value_label":"$1.20 spent","has_limit":false,"resets_in_seconds":0}]}],
          "balances":[{"key":"credits","label":"Credits","value_label":"$22.23 available"}]}}
        """.trimIndent()

        val view = redlineJson.decodeFromString(UsageView.serializer(), raw)

        assertEquals("Monthly allowance", view.providers[0].weeklyLabel)
        val cursor = view.cursor!!
        assertTrue(cursor.scheduled)
        assertEquals(listOf("Monthly", "On-demand"), cursor.groups.map { it.heading })
        val other = cursor.groups[0].meters[0]
        assertTrue(other.exhausted)
        assertEquals("Used up", other.valueLabel)
        assertEquals(0, other.remainingPercent)
        assertFalse(cursor.groups[1].meters[0].hasLimit)
        assertEquals("\$22.23 available", cursor.balances[0].valueLabel)
    }

    /** An older desktop sends no card and no label; nothing is invented. */
    @Test
    fun `an older desktop has no Cursor card`() {
        val view = redlineJson.decodeFromString(
            UsageView.serializer(),
            """{"providers":[{"id":"claude-main","provider":"claude"}]}""",
        )
        assertNull(view.cursor)
        assertEquals("", view.providers[0].weeklyLabel)
    }

    @Test
    fun `a used up pool says when it is available again`() {
        val meter = CursorMeter(
            label = "Other Models", hasLimit = true, exhausted = true,
            resetsAt = "2026-10-13T04:16:45Z", resetsInSeconds = 3 * 86400 + 4 * 3600,
        )
        assertEquals("Available again in 3d 4h · Tue 4:16 AM", cursorMeterDetail(meter) { "Tue 4:16 AM" })
    }

    @Test
    fun `a missing reset is said rather than made up`() {
        assertEquals("Reset date not reported", cursorMeterDetail(CursorMeter(hasLimit = true)))
        assertEquals("No limit reported", cursorMeterDetail(CursorMeter(hasLimit = false)))
    }

    @Test
    fun `mismatched Cursor accounts read as such, not as signed out`() {
        val provider = ProviderUsage(
            provider = "cursor", stale = true,
            staleReason = "Cursor CLI and app are on different accounts · sign both in to the same one on your Mac",
        )
        assertEquals("Accounts differ", providerStatus(provider))
    }

    @Test
    fun `cursor is named as itself`() {
        assertEquals("Cursor", providerDisplayName("cursor"))
        assertEquals("Claude", providerDisplayName("claude"))
    }
}
