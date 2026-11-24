import Foundation
import Testing
@testable import RedlineKit

/// Synthetic OpenAI API credits accounts: no real organization, email or key.
private enum CreditsFixture {
    static let now = ISO8601DateFormatter().date(from: "2026-11-24T19:00:00Z")!
    static var utc: Calendar {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "UTC")!
        return calendar
    }
    static let locale = Locale(identifier: "en_US")

    static let credits = #"""
    "credits":{"promo_remaining":62.4,"promo_granted":100,"next_expiring":62.4,
      "effective_at":"2026-10-28T00:00:00Z","expires_at":"2026-11-27T00:00:00Z",
      "paid_available":7.25,"min_promo_balance":5,"pacing":"firesale","firesale_window_seconds":259200,
      "grants":[
        {"kind":"promo","amount":100,"remaining":62.4,"effective_at":"2026-10-28T00:00:00Z","expires_at":"2026-11-27T00:00:00Z"},
        {"kind":"auto_recharge","amount":10,"remaining":7.25,"effective_at":"2026-10-15T19:00:00Z","expires_at":"2027-10-15T19:00:00Z"}]}
    """#

    static let fresh = #"""
    {"id":"api-credits","provider":"openai-api-credits","label":"Linked org","account_status":"ok",
     "latest_decision":{"decision":"RUN","mode":"api_credits","unlocked_tier":"firesale","reason":"$62.40 of promotional credit expires in 2 days; running queued work before it lapses"},
     "snapshot":{"provider":"openai-api-credits","observed_at":"2026-11-24T18:59:00Z",
       "weekly":{"remaining":0.624,"resets_at":"2026-11-27T00:00:00Z","period_seconds":2592000},
       \#(credits)}}
    """#

    static let stale = #"""
    {"id":"api-credits","provider":"openai-api-credits","label":"Linked org","account_status":"stale","snapshot_stale":true,
     "latest_decision":{"decision":"RUN","mode":"api_credits","unlocked_tier":"firesale","reason":"$62.40 of promotional credit expires in 2 days; running queued work before it lapses"},
     "snapshot":{"observed_at":"2026-11-24T17:00:00Z",
       "weekly":{"remaining":0.624,"resets_at":"2026-11-27T00:00:00Z","period_seconds":2592000},
       \#(credits)}}
    """#

    static let signedOut = #"""
    {"id":"api-credits","provider":"openai-api-credits","label":"Linked org","account_status":"signed_out","snapshot_stale":true,
     "latest_decision":{"decision":"RUN","mode":"api_credits","unlocked_tier":"firesale","reason":"$62.40 of promotional credit expires in 2 days; running queued work before it lapses"},
     "usage_source":{"reason":"signed_out","last_error":"no OpenAI platform session key is saved; run `redline account set-session-key api-credits`"},
     "snapshot":{"observed_at":"2026-11-24T17:00:00Z",
       "weekly":{"remaining":0.624,"resets_at":"2026-11-27T00:00:00Z","period_seconds":2592000},
       \#(credits)}}
    """#

    static let noPromo = #"""
    {"id":"api-credits","provider":"openai-api-credits","label":"Linked org","account_status":"ok",
     "latest_decision":{"decision":"WAIT","mode":"api_credits","reason":"no promotional API credit is available"},
     "snapshot":{"observed_at":"2026-11-24T18:59:00Z","weekly_unavailable":true,
       "credits":{"promo_remaining":0,"promo_granted":0,"paid_available":7.25,"min_promo_balance":5,"pacing":"even",
         "grants":[{"kind":"purchase","amount":10,"remaining":7.25,"expires_at":"2027-10-15T19:00:00Z"}]}}}
    """#

    static let claude = #"""
    {"id":"claude-main","provider":"claude","account_status":"ok","snapshot":{"weekly":{"remaining":0.53,"resets_at":"2026-11-27T17:00:00Z"}}}
    """#
    static let codex = #"""
    {"id":"codex-main","provider":"codex","account_status":"ok","snapshot":{"weekly":{"remaining":0.32,"resets_at":"2026-11-28T03:00:00Z"}}}
    """#

    static func snapshot(_ providers: String...) throws -> DashboardSnapshot {
        try DashboardSnapshot.decode(Data("""
        {"health":{"status":"healthy","window":"24h","active_runs":0,"dispatch_errors":0},
         "scheduler":{"enabled":true,"running":false},
         "providers":[\(providers.joined(separator: ","))]}
        """.utf8))
    }

    static func card(_ provider: String) throws -> APICreditsCard {
        let account = try #require(try snapshot(provider).providers.first)
        return try #require(APICreditsCard(account: account, now: now, calendar: utc, locale: locale))
    }
}

@Test func apiCreditsDecodeDollarsAndGrants() throws {
    let account = try #require(try CreditsFixture.snapshot(CreditsFixture.fresh).providers.first)
    let credits = try #require(account.snapshot?.credits)
    #expect(credits.promoRemaining == 62.4)
    #expect(credits.promoGranted == 100)
    #expect(credits.paidAvailable == 7.25)
    #expect(credits.minPromoBalance == 5)
    #expect(credits.pacing == "firesale")
    #expect(credits.firesaleWindowSeconds == 259200)
    #expect(credits.expiresAt == "2026-11-27T00:00:00Z")
    #expect(credits.grants.map(\.kind) == ["promo", "auto_recharge"])
    #expect(account.isAPICredits)
    #expect(account.latestDecisionReason?.hasPrefix("$62.40 of promotional credit") == true)
}

@Test func apiCreditsDisplayNameNamesTheProduct() throws {
    let account = try #require(try CreditsFixture.snapshot(CreditsFixture.fresh).providers.first)
    #expect(account.displayName == "OpenAI API credits")
    let card = try CreditsFixture.card(CreditsFixture.fresh)
    #expect(card.title == "OpenAI API credits")
    #expect(card.label == "Linked org")
}

@Test func apiCreditsHeadlineIsDollarsNeverAWindow() throws {
    let card = try CreditsFixture.card(CreditsFixture.fresh)
    #expect(card.headline == "$62.40 left")
    #expect(card.progress == 0.624)
    #expect(card.tone == .normal)
    let everything = [card.headline, card.secondary, card.promoLine ?? "", card.rule ?? "", card.paid ?? ""]
        + card.grants.flatMap { [$0.label, $0.value, $0.expires ?? ""] }
    for text in everything {
        for banned in ["weekly", "Weekly", "monthly", "Monthly", "5h", "Week", "Month", "%"] {
            #expect(!text.contains(banned), "\(banned) in \(text)")
        }
    }
}

@Test func apiCreditsSecondaryLineGivesTheExpiry() throws {
    let card = try CreditsFixture.card(CreditsFixture.fresh)
    #expect(card.secondary == "Expires in 2 days · Fri, Nov 27")
    #expect(card.promoLine == "$62.40 of $100.00 left")
    #expect(card.rule == "Jobs start above $5.00 · Firesale: runs within 3 days of expiry")
    #expect(card.decision == "Run now · firesale")
    #expect(card.decisionReason == "$62.40 of promotional credit expires in 2 days; running queued work before it lapses")
    #expect(card.grants == [
        APICreditsCard.Grant(label: "ChatGPT subscription grant", value: "$62.40 of $100.00", expires: "expires Fri, Nov 27"),
        APICreditsCard.Grant(label: "Auto recharge", value: "$7.25 of $10.00", expires: "expires Fri, Oct 15, 2027"),
    ])
}

@Test func apiCreditsPaidCreditIsDisplayOnly() throws {
    let card = try CreditsFixture.card(CreditsFixture.fresh)
    #expect(card.paid == "$7.25 · shown only, never scheduled")
    // Paid credit never enters the headline or the bar.
    #expect(card.headline == "$62.40 left")
    #expect(card.progress == 0.624)
    let zeroPaid = CreditsFixture.fresh.replacingOccurrences(of: #""paid_available":7.25"#, with: #""paid_available":0"#)
    #expect(try CreditsFixture.card(zeroPaid).paid == nil)
}

@Test func apiCreditsStaleSaysLastKnownWithAMutedBarAndNoDecision() throws {
    let card = try CreditsFixture.card(CreditsFixture.stale)
    #expect(card.status == .stale)
    #expect(card.headline == "Last known $62.40")
    #expect(card.secondary == "Last sample 2 hrs ago")
    #expect(card.progress == 0.624)
    #expect(card.tone == .muted)
    #expect(card.promoLine == "Last known $62.40 of $100.00 left")
    // The RUN decision was about a balance that is no longer current.
    #expect(card.decision == nil)
    #expect(card.decisionReason == nil)
    #expect(card.attention)
}

@Test func apiCreditsSignedOutNamesTheCLIFix() throws {
    let card = try CreditsFixture.card(CreditsFixture.signedOut)
    #expect(card.status == .signedOut)
    #expect(card.headline == "Session key needed")
    #expect(card.secondary == "Run redline account set-session-key api-credits")
    #expect(card.progress == nil)
    #expect(card.attention)
    // Signed out with a stale RUN decision and an old balance: only the fix.
    #expect(card.decision == nil)
    #expect(card.decisionReason == nil)
    #expect(card.promoLine == nil)
    #expect(card.rule == nil)
    #expect(card.paid == nil)
    #expect(card.grants.isEmpty)
    let account = try #require(try CreditsFixture.snapshot(CreditsFixture.signedOut).providers.first)
    #expect(account.staleSummary == "Session key needed · run redline account set-session-key api-credits")
}

@Test func apiCreditsWithoutPromoAreNotWeeklyNotReported() throws {
    let card = try CreditsFixture.card(CreditsFixture.noPromo)
    #expect(card.headline == "$0.00 promo")
    #expect(card.secondary == "No promotional credit")
    #expect(card.progress == 0)
    #expect(card.paid == "$7.25 · shown only, never scheduled")
    #expect(card.rule == "Jobs start above $5.00 · Even pacing across the grant")
    #expect(card.grants == [APICreditsCard.Grant(label: "Purchased credit", value: "$7.25 of $10.00", expires: "expires Fri, Oct 15, 2027")])
    #expect(card.decision == "Held · no promotional credit")
    #expect(card.decisionReason == "No promotional API credit is available")

    let tray = TrayState(snapshot: try CreditsFixture.snapshot(CreditsFixture.noPromo))
    let badge = try #require(tray.providerBadges.first)
    #expect(badge.compact == "$0.00")
    #expect(badge.spoken == "OpenAI API credits $0.00 promo")
    #expect(!tray.menuBarTitle.contains("weekly not reported"))
    #expect(!tray.menuBarTitle.contains("n/r"))
}

@Test func apiCreditsMenuBarBadgeSaysDollars() throws {
    let tray = TrayState(snapshot: try CreditsFixture.snapshot(CreditsFixture.codex, CreditsFixture.claude, CreditsFixture.fresh))
    #expect(tray.menuBarTitle == "WAIT  Codex 32% · Claude 53% · OpenAI API credits $62.40 left")
    #expect(tray.providerBadges.last?.compact == "$62.40")
    // A promotional balance is not weekly capacity: the gauge follows Codex and Claude.
    #expect(tray.lowestWeeklyPercent == 32)
    let signedOut = TrayState(snapshot: try CreditsFixture.snapshot(CreditsFixture.signedOut))
    #expect(signedOut.providerBadges.first?.compact == "—")
    #expect(signedOut.menuBarTitle == "WAIT  OpenAI API credits session key needed")
    #expect(signedOut.iconDescription == "Redline: usage unavailable")
}

@Test func malformedCreditsNeverBreakTheDashboard() throws {
    let malformed = #"""
    {"id":"api-credits","provider":"openai-api-credits","account_status":"ok",
     "snapshot":{"observed_at":"2026-11-24T18:59:00Z","credits":{"promo_remaining":"lots","grants":7}}}
    """#
    let snapshot = try CreditsFixture.snapshot(CreditsFixture.claude, malformed)
    #expect(snapshot.providers.count == 2)
    #expect(snapshot.providers[1].snapshot?.credits == nil)
    let card = try #require(APICreditsCard(account: snapshot.providers[1], now: CreditsFixture.now,
                                           calendar: CreditsFixture.utc, locale: CreditsFixture.locale))
    #expect(card.headline == "—")
    #expect(card.secondary == "Credit balance not reported")
    // One bad grant drops only the grants, never the balance.
    let badGrant = CreditsFixture.fresh.replacingOccurrences(of: #""kind":"promo","amount":100"#, with: #""kind":"promo","amount":"x""#)
    let credits = try #require(try CreditsFixture.snapshot(badGrant).providers.first?.snapshot?.credits)
    #expect(credits.promoRemaining == 62.4)
    #expect(credits.grants.map(\.kind) == ["auto_recharge"])
}

@Test func claudeAndCodexAreUnchangedBesideCredits() throws {
    let without = try CreditsFixture.snapshot(CreditsFixture.codex, CreditsFixture.claude)
    let with = try CreditsFixture.snapshot(CreditsFixture.codex, CreditsFixture.claude, CreditsFixture.fresh)
    #expect(without.providers.map(\.displayName) == ["Codex", "Claude"])
    #expect(TrayState(snapshot: without).menuBarTitle == "WAIT  Codex 32% · Claude 53%")
    #expect(Array(TrayState(snapshot: with).providerBadges.prefix(2)) == TrayState(snapshot: without).providerBadges)
    for provider in with.providers where !provider.isAPICredits {
        #expect(provider.snapshot?.credits == nil)
        #expect(APICreditsCard(account: provider) == nil)
        #expect(provider.accountWindowLabel == "Weekly")
    }
}

@Test func apiCreditsDecisionLabelsMatchTheWebDashboards() {
    let label = APICreditsCard.decisionLabel
    #expect(label("RUN", "firesale", "x") == "Run now · firesale")
    #expect(label("RUN", nil, "x") == "Run now")
    #expect(label("WAIT", nil, "promotional credit is at or below the $5.00 floor") == "Held · at the floor")
    #expect(label("WAIT", nil, "no promotional API credit is available") == "Held · no promotional credit")
    #expect(label("WAIT", nil, "holding promotional credit until it is within 3 days of expiring") == "Holding credit")
    #expect(label("UNKNOWN", nil, "usage unavailable") == "Dispatch held")
}
