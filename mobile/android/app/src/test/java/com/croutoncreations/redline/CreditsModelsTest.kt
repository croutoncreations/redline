package com.croutoncreations.redline

import java.time.Instant
import java.time.ZoneId
import java.util.Locale
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** OpenAI API credits accounts, as mobile/core words them. Synthetic data only. */
class CreditsModelsTest {

    private val zone = ZoneId.of("America/Los_Angeles")
    private val now = Instant.parse("2026-11-24T19:00:00Z")

    private val coreJson = """
        {"providers":[
         {"id":"api-credits","provider":"openai-api-credits","label":"Linked org","account_status":"ok",
          "source_label":"apicredits source · sampled just now",
          "credits":{"headline":"${'$'}62.40 left","detail":"Expires in 2 days",
           "expires_at":"2026-11-27T00:00:00Z","expires_in_seconds":190800,"remaining_percent":62,
           "promo_label":"${'$'}62.40 of ${'$'}100.00 left",
           "rule_line":"Jobs start above ${'$'}5.00 · Firesale: runs within 3 days of expiry",
           "paid_label":"${'$'}7.25 · shown only, never scheduled",
           "decision_label":"Run now · firesale",
           "decision_reason":"${'$'}62.40 of promotional credit expires in 2 days; running queued work before it lapses",
           "grants":[{"label":"ChatGPT subscription grant","amount_label":"${'$'}62.40 of ${'$'}100.00","expires_at":"2026-11-27T00:00:00Z"},
                     {"label":"Auto recharge","amount_label":"${'$'}7.25 of ${'$'}10.00","expires_at":"2027-10-15T19:00:00Z"},
                     {"label":"Purchased credit","amount_label":"${'$'}2.00 of ${'$'}2.00"}]},
          "scheduling":{"reserve_percent":25,"decision":"RUN","reason":"${'$'}62.40 of promotional credit expires in 2 days; running queued work before it lapses"}},
         {"id":"claude-main","provider":"claude","account_status":"ok","weekly_label":"Weekly allowance",
          "weekly":{"remaining_percent":45,"resets_in_seconds":3600,"resets_at":"2026-11-24T20:00:00Z"}}]}
    """.trimIndent()

    private fun decode(raw: String = coreJson) = redlineJson.decodeFromString(UsageView.serializer(), raw)

    @Test
    fun `decodes the core's credits view`() {
        val credits = decode().providers[0].credits!!
        assertEquals("\$62.40 left", credits.headline)
        assertEquals(62, credits.remainingPercent)
        assertEquals("\$62.40 of \$100.00 left", credits.promoLabel)
        assertEquals("\$7.25 · shown only, never scheduled", credits.paidLabel)
        assertEquals(listOf("ChatGPT subscription grant", "Auto recharge", "Purchased credit"), credits.grants.map { it.label })
        assertFalse(credits.noPromo || credits.stale || credits.signedOut)
        assertEquals("Run now · firesale", credits.decisionLabel)
        assertEquals("\$62.40 of promotional credit expires in 2 days; running queued work before it lapses", credits.decisionReason)
    }

    @Test
    fun `a stale balance's line says when it was last sampled, not the expiry`() {
        val credits = decode(
            """{"providers":[{"id":"api-credits","provider":"openai-api-credits","account_status":"stale","stale":true,
              "credits":{"headline":"Last known ${'$'}62.40","detail":"Last sample 2 hrs ago","expires_at":"2026-11-27T00:00:00Z",
               "remaining_percent":62,"stale":true}}]}""",
        ).providers[0].credits!!
        assertEquals("Last sample 2 hrs ago", creditsExpiryLine(credits, zone, now, Locale.US))
        assertEquals("", credits.decisionLabel)
    }

    @Test
    fun `names the provider OpenAI API credits and leaves the others alone`() {
        assertEquals("OpenAI API credits", providerDisplayName("openai-api-credits"))
        assertEquals("Claude", providerDisplayName("claude"))
        assertEquals("Codex", providerDisplayName("codex"))
        assertEquals("Cursor", providerDisplayName("cursor"))
        assertNull(decode().providers[1].credits)
    }

    @Test
    fun `names a Claude credits account Claude API credits and waits for its balance`() {
        assertEquals("Claude API credits", providerDisplayName("anthropic-api-credits"))
        val unread = ProviderUsage(id = "claude-credits", provider = "anthropic-api-credits", accountStatus = "no_data")
        assertEquals(OverviewEmptyState("Credits", "Balance not read yet"), overviewEmptyState(unread))
    }

    @Test
    fun `the secondary line is the relative expiry with its local date`() {
        val credits = decode().providers[0].credits!!
        assertEquals("Expires in 2 days · Thu, Nov 26", creditsExpiryLine(credits, zone, now, Locale.US))
    }

    @Test
    fun `grants say when they expire, with the year when it is not this one`() {
        val grants = decode().providers[0].credits!!.grants
        assertEquals("expires Thu, Nov 26", creditGrantExpiry(grants[0], zone, now, Locale.US))
        assertEquals("expires Fri, Oct 15, 2027", creditGrantExpiry(grants[1], zone, now, Locale.US))
        assertEquals("no expiry", creditGrantExpiry(grants[2], zone, now, Locale.US))
    }

    @Test
    fun `no promotional credit is not weekly not reported`() {
        val account = decode(
            """{"providers":[{"id":"api-credits","provider":"openai-api-credits","account_status":"ok",
              "credits":{"headline":"${'$'}0.00 promo","detail":"No promotional credit","remaining_percent":0,
               "promo_label":"${'$'}0.00 of ${'$'}0.00 left","rule_line":"Jobs start above ${'$'}5.00 · Even pacing across the grant",
               "paid_label":"${'$'}7.25 · shown only, never scheduled","no_promo":true}}]}""",
        ).providers[0]
        val credits = account.credits!!
        assertEquals("No promotional credit", creditsExpiryLine(credits, zone, now, Locale.US))
        assertEquals("dispatch held, no promotional credit", accountDispatchState(account))
        assertFalse(account.weeklyNotReported)
    }

    @Test
    fun `signed out names the fix on the computer running Redline, never a key field`() {
        val account = decode(
            """{"providers":[{"id":"api-credits","provider":"openai-api-credits","account_status":"signed_out","stale":true,
              "stale_reason":"Session key expired · re-paste it on the computer running Redline",
              "credits":{"headline":"Session key needed","detail":"Session key expired · re-paste it on the computer running Redline",
               "expires_at":"2026-11-27T00:00:00Z","signed_out":true}}]}""",
        ).providers[0]
        val credits = account.credits!!
        assertEquals("Session key expired · re-paste it on the computer running Redline", creditsExpiryLine(credits, zone, now, Locale.US))
        assertEquals("Signed out", providerStatus(account))
        assertEquals("needs sign-in", accountDispatchState(account))
        // The headline and its line already say it; no second red copy.
        assertEquals("", providerProblem(account))
    }

    @Test
    fun `a live credits account dispatches like any other`() {
        val account = decode().providers[0]
        assertEquals("Live", providerStatus(account))
        assertEquals("dispatching", accountDispatchState(account))
        assertTrue(account.dispatching)
    }

    @Test
    fun `existing providers keep their problem line`() {
        val claude = ProviderUsage(id = "claude-main", provider = "claude", stale = true, staleReason = "Claude Code is signed out · run claude auth login on your Mac")
        assertEquals(claude.staleReason, providerProblem(claude))
    }

    @Test
    fun `an unread credits account waits for its balance, never a weekly allowance`() {
        val unread = ProviderUsage(id = "api-credits", provider = "openai-api-credits", accountStatus = "no_data")
        assertEquals(OverviewEmptyState("Credits", "Balance not read yet"), overviewEmptyState(unread))
        val claude = ProviderUsage(id = "claude-main", provider = "claude", accountStatus = "no_data")
        assertEquals(OverviewEmptyState("Weekly", "No usage reported"), overviewEmptyState(claude))
        val notReported = ProviderUsage(id = "cursor-main", provider = "cursor", weeklyNotReported = true)
        assertEquals(OverviewEmptyState("Weekly", "Not reported"), overviewEmptyState(notReported))
    }
}
