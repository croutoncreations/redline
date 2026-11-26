import Foundation
import Testing
@testable import RedlineKit

/// Synthetic Claude (Anthropic) API credits accounts: no real organization,
/// email or key. They reuse the OpenAI credits card; only the words change.
private enum ClaudeCreditsFixture {
    static let now = ISO8601DateFormatter().date(from: "2026-11-24T19:00:00Z")!
    static var utc: Calendar {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "UTC")!
        return calendar
    }

    static let credits = #"""
    "credits":{"promo_remaining":62.4,"promo_granted":100,"next_expiring":62.4,
      "effective_at":"2026-10-28T00:00:00Z","expires_at":"2026-11-27T00:00:00Z",
      "paid_available":25.25,"min_promo_balance":5,"pacing":"firesale","firesale_window_seconds":259200,
      "grants":[
        {"kind":"promo","amount":100,"remaining":62.4,"effective_at":"2026-10-28T00:00:00Z","expires_at":"2026-11-27T00:00:00Z"},
        {"kind":"other","amount":25,"remaining":18,"effective_at":"2026-10-15T00:00:00Z","expires_at":"2027-01-01T00:00:00Z"},
        {"kind":"purchase","amount":10,"remaining":7.25,"effective_at":"2026-10-15T19:00:00Z"}]}
    """#

    static let fresh = #"""
    {"id":"claude-credits","provider":"anthropic-api-credits","label":"Console org","account_status":"ok",
     "latest_decision":{"decision":"RUN","mode":"api_credits","unlocked_tier":"firesale","reason":"$62.40 of promotional credit expires in 2 days; running queued work before it lapses"},
     "snapshot":{"provider":"anthropic-api-credits","observed_at":"2026-11-24T18:59:00Z",
       "weekly":{"remaining":0.624,"resets_at":"2026-11-27T00:00:00Z","period_seconds":2592000},
       \#(credits)}}
    """#

    static let signedOut = #"""
    {"id":"claude-credits","provider":"anthropic-api-credits","label":"Console org","account_status":"signed_out","snapshot_stale":true,
     "usage_source":{"reason":"signed_out","last_error":"Claude Console rejected the saved session key"}}
    """#

    static func account(_ json: String) throws -> ProviderSummary {
        let snapshot = try DashboardSnapshot.decode(Data("""
        {"health":{"status":"healthy","window":"24h","active_runs":0,"dispatch_errors":0},
         "scheduler":{"enabled":true,"running":false},
         "providers":[\(json)]}
        """.utf8))
        return try #require(snapshot.providers.first)
    }

    static func card(_ json: String) throws -> APICreditsCard {
        try #require(APICreditsCard(account: try account(json), now: now, calendar: utc, locale: Locale(identifier: "en_US")))
    }
}

@Test func claudeCreditsAreAPICreditsNamedForClaude() throws {
    let account = try ClaudeCreditsFixture.account(ClaudeCreditsFixture.fresh)
    #expect(account.isAPICredits)
    #expect(account.isClaudeAPICredits)
    #expect(account.displayName == "Claude API credits")
    let card = try ClaudeCreditsFixture.card(ClaudeCreditsFixture.fresh)
    #expect(card.title == "Claude API credits")
    #expect(card.headline == "$62.40 left")
    #expect(card.paid == "$25.25 · shown only, never scheduled")
}

@Test func claudeCreditsGrantsUseClaudeWords() throws {
    let card = try ClaudeCreditsFixture.card(ClaudeCreditsFixture.fresh)
    #expect(card.grants.map(\.label) == ["Claude subscription credit", "Other promotional credit", "Purchased credit"])
    #expect(card.grants.last?.expires == nil)
    for grant in card.grants {
        #expect(!grant.label.contains("ChatGPT"))
    }
}

@Test func claudeCreditsSignedOutNamesTheConsole() throws {
    let card = try ClaudeCreditsFixture.card(ClaudeCreditsFixture.signedOut)
    #expect(card.headline == "Session key needed")
    #expect(card.secondary == "Paste the sessionKey cookie from platform.claude.com: redline account set-session-key claude-credits")
    let account = try ClaudeCreditsFixture.account(ClaudeCreditsFixture.signedOut)
    #expect(account.staleSummary == "Session key needed · paste the sessionKey cookie from platform.claude.com: redline account set-session-key claude-credits")
}

@Test func openAICreditsKeepTheirWords() throws {
    let account = try ClaudeCreditsFixture.account(#"""
    {"id":"api-credits","provider":"openai-api-credits","account_status":"signed_out","snapshot_stale":true,"usage_source":{"reason":"signed_out"}}
    """#)
    #expect(account.isAPICredits)
    #expect(!account.isClaudeAPICredits)
    #expect(account.displayName == "OpenAI API credits")
    #expect(account.staleSummary == "Session key needed · run redline account set-session-key api-credits")
}
