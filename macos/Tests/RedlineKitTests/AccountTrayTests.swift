import Foundation
import Testing
@testable import RedlineKit

/// Synthetic accounts only; labels, plans and numbers are made up.
private enum Fixture {
    static let now = ISO8601DateFormatter().date(from: "2026-10-10T12:00:00Z")!
    static var utc: Calendar {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "UTC")!
        return calendar
    }
    static let locale = Locale(identifier: "en_US_POSIX")

    static func snapshot(_ providersJSON: String) throws -> DashboardSnapshot {
        try DashboardSnapshot.decode(Data("""
        {"health":{"status":"healthy","window":"24h","active_runs":0,"dispatch_errors":0},
         "scheduler":{"enabled":true,"running":false},
         "providers":[\(providersJSON)]}
        """.utf8))
    }

    static func tray(_ providersJSON: String) throws -> AccountTray? {
        AccountTray(snapshot: try snapshot(providersJSON), now: now, calendar: utc, locale: locale)
    }

    static let client = #"""
    {"id":"claude-client","provider":"claude","label":"Client","account_status":"ok",
     "latest_decision":{"decision":"WAIT","reason":"below reserve"},
     "snapshot":{"plan":"Team","observed_at":"2026-10-10T11:58:00Z",
       "short":{"remaining":0,"resets_at":"2026-10-10T12:57:00Z"},
       "weekly":{"remaining":0.58,"resets_at":"2026-10-14T20:00:00Z"}}}
    """#
    static let personal = #"""
    {"id":"claude-main","provider":"claude","label":"Personal","account_status":"ok",
     "latest_decision":{"decision":"RUN","reason":"surplus"},
     "snapshot":{"plan":"Max 20x","observed_at":"2026-10-10T11:59:00Z",
       "short":{"remaining":0.68,"resets_at":"2026-10-10T14:11:00Z"},
       "weekly":{"remaining":0.41,"resets_at":"2026-10-13T15:00:00Z"},
       "allowances":[{"key":"model:fable:weekly","source_label":"Fable","remaining":0.64}],
       "banked_resets":2,"banked_resets_expire_at":"2026-10-14T09:00:00Z",
       "banked_resets_expiries":["2026-10-20T09:00:00Z","2026-10-14T09:00:00Z"]}}
    """#
    static let side = #"""
    {"id":"claude-side","provider":"claude","label":"Side project","account_status":"stale","snapshot_stale":true,
     "error":"OpenUsage reports this account's usage as out of date.",
     "snapshot":{"plan":"Pro","source_stale":true,"observed_at":"2026-10-10T08:00:00Z",
       "short":{"remaining":0.64,"resets_at":"2026-10-10T10:00:00Z"},
       "weekly":{"remaining":0.41,"resets_at":"2026-10-13T06:00:00Z"},
       "banked_resets":2,"banked_resets_expiries":["2026-10-10T11:00:00Z","2026-10-20T09:00:00Z"]}}
    """#
    static let codex = #"""
    {"id":"codex-main","provider":"codex","account_status":"ok","snapshot":{"weekly":{"remaining":0.43}}}
    """#
}

@Test func singleClaudeAccountKeepsTodaysCard() throws {
    #expect(try Fixture.tray(Fixture.personal) == nil)
    #expect(try Fixture.tray("\(Fixture.personal),\(Fixture.codex)") == nil)
    #expect(try Fixture.tray("") == nil)
}

@Test func rowsKeepTheServicesOrderAndNeverAverage() throws {
    let tray = try #require(try Fixture.tray("\(Fixture.client),\(Fixture.personal),\(Fixture.codex),\(Fixture.side)"))
    // The service's order (by account id), not by usage; Codex excluded.
    #expect(tray.rows.map(\.id) == ["claude-client", "claude-main", "claude-side"])
    // Counts only: no blended percentage anywhere in the overview.
    #expect(tray.overview == "3 accounts · 1 dispatching · 1 needs attention")
    #expect(!tray.overview.contains("%"))
}

@Test func compactRowShowsAliasPlanBothWindowsAndSoonestCredit() throws {
    let tray = try #require(try Fixture.tray("\(Fixture.client),\(Fixture.personal)"))
    let personal = tray.rows[1]
    #expect(personal.title == "Personal")
    #expect(personal.plan == "Max 20x")
    #expect(personal.stateLine == "Dispatching")
    #expect(personal.session == AccountTrayWindow(
        value: "68%", reset: "2h 11m", detail: "68% left · resets in 2h 11m",
        spoken: "5-hour window, 68 percent left, resets in 2 hours 11 minutes",
        percent: 68, outOfDate: false))
    #expect(personal.weekly.value == "41%")
    #expect(personal.weekly.reset == "3d 3h")
    // Count, then the soonest deadline (sorted), as a fixed time.
    #expect(personal.credits == "2 · Wed")
    #expect(personal.spoken.contains("2 banked resets, soonest expires Wed 9:00 AM"))
    // A provider not reporting credits says nothing, not zero.
    #expect(tray.rows[0].credits == nil)
}

@Test func outOfDateAccountShowsLastKnownValuesAndNoCountdown() throws {
    let tray = try #require(try Fixture.tray("\(Fixture.personal),\(Fixture.side)"))
    let side = tray.rows[1]
    #expect(side.status == .stale)
    #expect(side.stateLine == "Out of date · dispatch held")
    #expect(!side.dispatching)
    // Was, never a bar, and a fixed or due time instead of "in 2h".
    #expect(side.session.value == "was 64%")
    #expect(side.session.percent == nil)
    #expect(side.session.reset == "due 10:00 AM")
    #expect(side.session.detail == "was 64% left · reset was due today 10:00 AM · awaiting fresh numbers")
    #expect(side.weekly.value == "was 41%")
    #expect(side.weekly.reset == "Tue")
    #expect(side.weekly.detail == "was 41% left · last reported reset Tue 6:00 AM")
    for text in [side.session.reset, side.weekly.reset, side.spoken] + side.details.map(\.value) {
        #expect(!text.contains("resets in"), "live countdown in stale text: \(text)")
    }
    #expect(side.spoken.contains("last known and out of date"))
    // Credits from the same old read: labelled, with the lapsed one counted.
    #expect(side.credits == "was 2 · Oct 20")
    #expect(side.compactState == "Out of date")
    #expect(side.details.contains(AccountTrayDetail(label: "Banked resets", value: "Last known 2 available · soonest expires Oct 20 · 1 since expired")))
    #expect(side.details.contains(AccountTrayDetail(label: "Why", value: "OpenUsage says these numbers are out of date")))
}

@Test func signedOutAndMissingAccountsInventNothing() throws {
    let signedOut = #"""
    {"id":"claude-b","provider":"claude","account_status":"signed_out","snapshot_stale":true,
     "usage_source":{"reason":"signed_out"},
     "snapshot":{"weekly":{"remaining":0.9,"resets_at":"2026-10-12T00:00:00Z"}}}
    """#
    let empty = #"{"id":"claude-c","provider":"claude","account_status":"no_data"}"#
    let tray = try #require(try Fixture.tray("\(Fixture.personal),\(signedOut),\(empty)"))
    let b = tray.rows[1]
    #expect(b.title == "claude-b")  // no label: the id, never an email
    #expect(b.status == .signedOut)
    #expect(b.stateLine == "Signed out · run claude auth login")
    // A signed-out account's old numbers are not shown at all.
    #expect(b.session.value == "—")
    #expect(b.weekly.value == "—")
    let c = tray.rows[2]
    #expect(c.status == .noData)
    #expect(c.weekly.value == "—")
    #expect(c.weekly.spoken == "Weekly allowance, unknown")
    #expect(tray.overview == "3 accounts · 1 dispatching · 2 need attention")
}

@Test func pausedOrHeldAccountsAreNotCountedAsDispatching() throws {
    let paused = Fixture.personal.replacingOccurrences(of: #""label":"Personal","#, with: #""label":"Personal","paused":true,"#)
    let tray = try #require(try Fixture.tray("\(Fixture.client),\(paused)"))
    #expect(tray.rows[1].stateLine == "Paused")
    #expect(!tray.rows[1].dispatching)
    #expect(tray.overview == "2 accounts · 0 dispatching")
}

@Test func expandedRowFollowsIDAndCollapsesWhenTheAccountIsGone() throws {
    let tray = try #require(try Fixture.tray("\(Fixture.client),\(Fixture.personal),\(Fixture.side)"))
    #expect(tray.expandedRow("claude-main")?.title == "Personal")
    #expect(tray.expandedRow("claude-removed") == nil)
    #expect(tray.expandedRow(nil) == nil)
    // Keyboard movement: clamped, no wrap; from nothing, the first or last.
    #expect(tray.neighbour(of: nil, offset: 1) == "claude-client")
    #expect(tray.neighbour(of: nil, offset: -1) == "claude-side")
    #expect(tray.neighbour(of: "claude-client", offset: -1) == "claude-client")
    #expect(tray.neighbour(of: "claude-main", offset: 1) == "claude-side")
    #expect(tray.neighbour(of: "claude-side", offset: 1) == "claude-side")
}

@Test func detailsListEveryWindowAndCreditDeadlineSeparately() throws {
    let tray = try #require(try Fixture.tray("\(Fixture.client),\(Fixture.personal)"))
    let details = tray.rows[1].details
    #expect(details.map(\.label) == ["Account", "Plan", "Usage", "Read", "5-hour", "Weekly", "Fable", "Banked resets", "Scheduler"])
    #expect(details.first { $0.label == "5-hour" }?.value == "68% left · resets in 2h 11m")
    #expect(details.first { $0.label == "Read" }?.value == "1m ago")
    // Credit deadlines are "expires", listed apart from window resets.
    #expect(details.first { $0.label == "Banked resets" }?.value == "2 available · soonest expires Wed 9:00 AM · then Oct 20")
}

@Test func unstartedSessionIsFullUntilFirstUse() throws {
    let fresh = #"""
    {"id":"claude-d","provider":"claude","account_status":"ok",
     "snapshot":{"short_window_not_started":true,"weekly":{"remaining":0.9,"resets_at":"2026-10-12T00:00:00Z"}}}
    """#
    let tray = try #require(try Fixture.tray("\(Fixture.personal),\(fresh)"))
    #expect(tray.rows[1].session.value == "100%")
    #expect(tray.rows[1].session.reset == "first use")
    #expect(tray.rows[1].stateLine == "No scheduler decision yet")
}

@Test func olderServiceWithoutAccountFieldsStillDecodes() throws {
    let old = #"""
    {"id":"claude-main","provider":"claude","snapshot":{"weekly":{"remaining":0.5}}},
    {"id":"claude-work","provider":"claude","snapshot_stale":true,"snapshot":{"weekly":{"remaining":0.2}}}
    """#
    let tray = try #require(try Fixture.tray(old))
    #expect(tray.rows.map(\.title) == ["claude-main", "claude-work"])
    #expect(tray.rows[0].status == .ok)
    #expect(tray.rows[1].status == .stale)
    #expect(tray.rows[1].weekly.value == "was 20%")
}

@Test func menuBarShowsOneBadgePerProviderAndNeverBlendsAccounts() throws {
    let snapshot = try Fixture.snapshot("\(Fixture.client),\(Fixture.personal),\(Fixture.codex),\(Fixture.side)")
    let tray = TrayState(snapshot: snapshot)
    #expect(tray.providerBadges.map(\.displayName) == ["Codex", "Claude"])
    let claude = tray.providerBadges[1]
    #expect(claude.compact == "×3")
    #expect(claude.percent == nil)
    #expect(claude.spoken == "Claude 3 accounts: Client 58%, Personal 41%, Side project —")
    #expect(tray.menuBarTitle == "WAIT  Codex 43% · Claude 3 accounts: Client 58%, Personal 41%, Side project —")
}

@Test func pausedAccountStillNamesItsProblem() throws {
    let signedOut = #"""
    {"id":"claude-b","provider":"claude","label":"Work","paused":true,"account_status":"signed_out","snapshot_stale":true,
     "usage_source":{"reason":"signed_out"}}
    """#
    let tray = try #require(try Fixture.tray("\(Fixture.personal),\(signedOut)"))
    let row = tray.rows[1]
    #expect(row.compactState == "Paused · Signed out")
    #expect(row.spoken.contains("Paused, Signed out, run claude auth login"))
    #expect(row.details.last == AccountTrayDetail(label: "Scheduler", value: "Paused · Signed out · run claude auth login"))
    #expect(tray.overview.hasSuffix("1 needs attention"))
}

@Test func spokenTextUsesSingularsAndCommas() throws {
    let soon = Fixture.personal.replacingOccurrences(of: "2026-10-10T14:11:00Z", with: "2026-10-10T13:01:00Z")
    let tray = try #require(try Fixture.tray("\(Fixture.client),\(soon)"))
    #expect(tray.rows[1].session.spoken == "5-hour window, 68 percent left, resets in 1 hour 1 minute")
    #expect(!tray.rows[0].spoken.contains("·"))
}

@Test func compactTimesHoldInOtherLocales() throws {
    let french = try #require(AccountTray(snapshot: try Fixture.snapshot("\(Fixture.client),\(Fixture.personal),\(Fixture.side)"),
                                          now: Fixture.now, calendar: Fixture.utc, locale: Locale(identifier: "fr_FR")))
    // A weekday, short, in the locale's own words; never a full date-time.
    #expect(french.rows[2].weekly.reset == "mar.")
    #expect(french.rows[1].credits == "2 · mer.")
}

@Test func onlyClaudeAccountsShareABadge() throws {
    let codexTwo = #"{"id":"codex-work","provider":"codex","account_status":"ok","snapshot":{"weekly":{"remaining":0.2}}}"#
    let tray = TrayState(snapshot: try Fixture.snapshot("\(Fixture.codex),\(codexTwo),\(Fixture.personal)"))
    #expect(tray.providerBadges.map(\.compact) == ["43%", "20%", "41%"])
}
