import Foundation
import Testing
@testable import RedlineKit

private let health = #""health":{"status":"ok","window":"24h0m0s","active_runs":0,"dispatch_errors":0},"scheduler":{"enabled":true,"running":false}"#
private let providers = #""providers":[{"id":"codex-main","provider":"codex","snapshot":{"weekly":{"remaining":0.9,"resets_at":"2026-10-12T00:00:00Z"}}}]"#
// Synthetic values mirroring the recorded OpenUsage Cursor card.
private let cursor = #"""
"cursor_usage":{"id":"cursor","label":"Cursor","plan":"Pro","source":"openusage","fetched_at":"2026-10-09T15:39:06Z","expires_at":"2026-10-09T15:44:06Z","stale":false,
 "meters":[
  {"key":"autoUsage","label":"Cursor Models","group":"monthly","unit":"percent","used":0,"limit":100,"resets_at":"2026-10-13T04:16:45Z"},
  {"key":"apiUsage","label":"Other Models","group":"monthly","unit":"percent","used":100,"limit":100,"resets_at":"2026-10-13T04:16:45Z"},
  {"key":"grokBot","label":"Grok Bot","group":"weekly","unit":"percent","used":58.934392,"limit":100,"resets_at":"2026-10-11T04:22:34.248Z"},
  {"key":"onDemand","label":"On-demand","group":"on_demand","unit":"usd","used":4.5}
 ],
 "balances":[{"key":"credits","label":"Credits","unit":"usd","available":22.23}]}
"""#

private func decode(_ body: String) throws -> DashboardSnapshot {
    try DashboardSnapshot.decode(Data("{\(body)}".utf8))
}

private let fetched = ISO8601DateFormatter().date(from: "2026-10-09T15:40:00Z")!

@Test func cursorUsageDecodesSeparateGroupsWithoutBecomingAProvider() throws {
    let snapshot = try decode("\(health),\(providers),\(cursor)")
    let report = try #require(snapshot.cursorUsage)
    #expect(snapshot.providers.map(\.id) == ["codex-main"])
    #expect(report.meters(in: .monthly).map(\.label) == ["Cursor Models", "Other Models"])
    #expect(report.meters(in: .weekly).map(\.label) == ["Grok Bot"])
    #expect(report.meters(in: .onDemand).map(\.key) == ["onDemand"])
    #expect(report.balances.first?.valueText == "$22.23 available")
    #expect(report.sourceLabel == "OpenUsage")
}

@Test func cursorUsageNeverAffectsTrayGaugeOrTitle() throws {
    let without = TrayState(snapshot: try decode("\(health),\(providers)"))
    let with = TrayState(snapshot: try decode("\(health),\(providers),\(cursor)"))
    #expect(with.lowestWeeklyPercent == without.lowestWeeklyPercent)
    #expect(with.menuBarTitle == without.menuBarTitle)
    #expect(with.providerBadges == without.providerBadges)
    #expect(!with.menuBarTitle.contains("Cursor"))
}

@Test func cursorMeterTextKeepsUnknownsUnknown() throws {
    let report = try #require(try decode("\(health),\(providers),\(cursor)").cursorUsage)
    let grok = try #require(report.meters.first { $0.key == "grokBot" })
    #expect(grok.valueText == "41% left")
    #expect(grok.remainingPercent.map { Int($0.rounded()) } == 41)
    let other = try #require(report.meters.first { $0.key == "apiUsage" })
    #expect(other.isExhausted)
    #expect(other.valueText == "Used up")
    let onDemand = try #require(report.meters.first { $0.key == "onDemand" })
    #expect(onDemand.remainingPercent == nil)
    #expect(onDemand.valueText == "$4.50 spent")
    #expect(CursorUsageFormat.resetLine(for: onDemand) == "No limit reported")

    let noReset = CursorMeter(key: "grokBot", label: "Grok Bot", group: "weekly", unit: "percent", used: 10, limit: 100)
    #expect(CursorUsageFormat.resetLine(for: noReset) == "Reset date not reported")
    let zeroLimit = CursorMeter(key: "onDemand", label: "On-demand", group: "on_demand", unit: "usd", used: 3, limit: 0)
    #expect(zeroLimit.remainingPercent == nil)
    #expect(zeroLimit.valueText == "$3.00 spent")
}

@Test func cursorResetLinesUseViewerTimeZoneAndExhaustedWording() throws {
    let report = try #require(try decode("\(health),\(providers),\(cursor)").cursorUsage)
    let la = TimeZone(identifier: "America/Los_Angeles")!
    let locale = Locale(identifier: "en_US")
    let grok = try #require(report.meters.first { $0.key == "grokBot" })
    let grokLine = CursorUsageFormat.resetLine(for: grok, now: fetched, timeZone: la, locale: locale)
    #expect(grokLine.hasPrefix("Resets in 2d · "))
    #expect(grokLine.contains("Oct 10"))
    #expect(grokLine.contains("9:22"))
    #expect(grokLine.contains("PDT"))
    let other = try #require(report.meters.first { $0.key == "apiUsage" })
    #expect(CursorUsageFormat.resetLine(for: other, now: fetched, timeZone: la, locale: locale).hasPrefix("Available again in 4d"))
}

@Test func cursorFreshnessUsesExpiryOnThisClock() throws {
    let report = try #require(try decode("\(health),\(providers),\(cursor)").cursorUsage)
    #expect(report.freshness(now: fetched) == .upToDate)
    // Expires 15:44:06; within the 2-minute grace OpenUsage is just rotating.
    #expect(report.freshness(now: fetched.addingTimeInterval(5 * 60)) == .upToDate)
    #expect(report.freshness(now: fetched.addingTimeInterval(10 * 60)) == .outOfDate)

    let partial = CursorUsageReport(fetchedAt: "2026-10-09T15:39:06Z", expiresAt: "2026-10-09T15:44:06Z", errors: ["Grok Bot unavailable"], meters: report.meters)
    #expect(partial.freshness(now: fetched) == .partial)
    let empty = CursorUsageReport(refreshError: "OpenUsage is not running")
    #expect(empty.freshness(now: fetched) == .noData)
    #expect(empty.isStale(now: fetched) == false)
    let noExpiry = CursorUsageReport(fetchedAt: "2026-10-09T15:39:06Z", meters: report.meters)
    #expect(noExpiry.freshness(now: fetched) == .outOfDate)
}

@Test func malformedCursorUsageDoesNotBreakTheDashboard() throws {
    let snapshot = try decode(#"\#(health),\#(providers),"cursor_usage":{"meters":"nope"}"#)
    #expect(snapshot.cursorUsage == nil)
    #expect(snapshot.providers.count == 1)
    #expect(try decode("\(health),\(providers)").cursorUsage == nil)
}

@Test func cursorProviderAccountSaysMonthlyAndNamesAgentLogin() throws {
    let json = #"""
    {"health":{"status":"ok","window":"24h0m0s","active_runs":0,"dispatch_errors":0},"scheduler":{"enabled":true,"running":false},
     "providers":[
      {"id":"cursor-main","provider":"cursor","snapshot":{"weekly":{"remaining":0.95,"resets_at":"2026-10-13T04:16:45Z","period_seconds":2592000},"source":"openusage"}},
      {"id":"cursor-out","provider":"cursor","snapshot_stale":true,"usage_source":{"reason":"signed_out"},"snapshot":{"weekly":{"remaining":0.95,"resets_at":"2026-10-13T04:16:45Z","period_seconds":2592000}}},
      {"id":"codex-main","provider":"codex","snapshot":{"weekly":{"remaining":0.9,"resets_at":"2026-10-12T00:00:00Z"}}}]}
    """#
    let snapshot = try DashboardSnapshot.decode(Data(json.utf8))
    let cursor = try #require(snapshot.providers.first { $0.id == "cursor-main" })
    #expect(cursor.displayName == "Cursor")
    #expect(cursor.accountWindowLabel == "Monthly")
    #expect(cursor.weeklyPercent == 95)
    let signedOut = try #require(snapshot.providers.first { $0.id == "cursor-out" })
    #expect(signedOut.staleSummary == "Cursor CLI is signed out · run agent login")
    let codex = try #require(snapshot.providers.first { $0.id == "codex-main" })
    #expect(codex.accountWindowLabel == "Weekly")
    #expect(ProviderRecovery.loginCommand(for: "cursor") == "agent login")
}

@Test func cursorAccountMismatchIsNamedNotSignedOut() throws {
    let json = #"""
    {"health":{"status":"ok","window":"24h0m0s","active_runs":0,"dispatch_errors":0},"scheduler":{"enabled":true,"running":false},
     "providers":[
      {"id":"cursor-main","provider":"cursor","snapshot_stale":true,"usage_source":{"reason":"account_mismatch"},"snapshot":{"weekly":{"remaining":0.95,"resets_at":"2026-10-13T04:16:45Z","period_seconds":2592000}}}]}
    """#
    let snapshot = try DashboardSnapshot.decode(Data(json.utf8))
    let cursor = try #require(snapshot.providers.first)
    #expect(cursor.accountMismatch)
    #expect(!cursor.signedOut)
    #expect(cursor.staleSummary == "Cursor CLI and app accounts differ · sign both in to the same account")
}
