import AppKit
import Foundation
import RedlineKit
import SwiftUI
import Testing
@testable import RedlineMenuBar

/// A synthetic stand-in for a real multi-seat shape (placeholder card ids,
/// NATO-alphabet labels; no real account is represented): five Team seats
/// whose OpenUsage cards report a 5-hour window, a Fable weekly window and a
/// reset balance but no account-wide weekly window; the personal default
/// login with a weekly window; and Codex and Cursor beside them.
///
/// The installed popover laid every provider card side by side, squeezing
/// eight cards into 420pt; and the installed service rejected the Team cards,
/// so every seat showed "Usage unavailable".
@MainActor
struct OwnerAccountShapeTests {
    private static func at(_ hours: Double) -> String {
        ISO8601DateFormatter().string(from: Date().addingTimeInterval(hours * 3600))
    }

    private static var others: [String] {
        [
            #"{"id":"claude-main","provider":"claude","account_status":"ok","latest_decision":{"decision":"WAIT"},"snapshot":{"plan":"Max 5x","observed_at":"\#(at(-0.05))","short":{"remaining":0,"resets_at":"\#(at(0.1))"},"weekly":{"remaining":0.45,"resets_at":"\#(at(138))"},"banked_resets":1,"banked_resets_expiries":["\#(at(282))"]}}"#,
            #"{"id":"codex-main","provider":"codex","account_status":"ok","snapshot":{"observed_at":"\#(at(-0.05))","weekly":{"remaining":0.0,"resets_at":"\#(at(78))"},"banked_resets":3,"banked_resets_expiries":["\#(at(286))"]}}"#,
            #"{"id":"cursor-main","provider":"cursor","account_status":"ok","snapshot":{"observed_at":"\#(at(-0.05))","weekly":{"remaining":0.95,"resets_at":"\#(at(55))","period_seconds":2592000}}}"#,
        ]
    }

    private static let seats = [("claude-5ea70001", "Delta"), ("claude-5ea70002", "Alpha"), ("claude-5ea70003", "Echo"),
                                ("claude-5ea70004", "Bravo"), ("claude-5ea70005", "Charlie")]

    private static func decode(_ providers: [String]) throws -> DashboardSnapshot {
        try DashboardSnapshot.decode(Data("""
        {"health":{"status":"degraded","window":"24h","active_runs":1,"dispatch_errors":45},
         "scheduler":{"enabled":true,"running":true},
         "providers":[\(providers.joined(separator: ","))],"tasks":[],"runs":[],"attempts":[]}
        """.utf8))
    }

    /// What the installed service sends: every seat in error, no snapshot.
    static func beforeFix() throws -> DashboardSnapshot {
        let team = seats.map { id, label in
            let card = "claude@\(id.dropFirst(7))"
            let error = #"openusage usage source: OpenUsage card \"\#(card)\" has no weekly limit; the login may lack usage access"#
            return #"{"id":"\#(id)","provider":"claude","label":"\#(label)","openusage_card":"\#(card)","account_status":"error","usage_source":{"active":"openusage","last_error":"\#(error)","consecutive_failures":8},"error":"\#(error)"}"#
        }
        return try decode(team + others)
    }

    /// What the fixed service sends: fresh 5-hour and Fable numbers, reset
    /// credits 0, weekly not reported, dispatch held. Echo's seat has a
    /// running 5-hour window; the rest have not started theirs.
    static func afterFix() throws -> DashboardSnapshot {
        let fable: [Double] = [30, 78, 120, 159, 74]
        let team = seats.enumerated().map { index, seat in
            let (id, label) = seat
            let short = label == "Echo" ? #""short":{"remaining":0.76,"resets_at":"\#(at(2.5))"},"# : #""short_window_not_started":true,"#
            return #"{"id":"\#(id)","provider":"claude","label":"\#(label)","openusage_card":"claude@\#(id.dropFirst(7))","account_status":"ok","latest_decision":{"decision":"UNKNOWN","reason":"weekly limit not reported"},"snapshot":{"plan":"Team 5x","observed_at":"\#(at(-0.03))","weekly_unavailable":true,"weekly":{"remaining":0.33,"resets_at":"\#(at(1))"},\#(short)"banked_resets":0,"allowances":[{"key":"model:fable:weekly","source_label":"Fable","scope":"model","role":"weekly","remaining":1,"resets_at":"\#(at(fable[index]))","period_duration_seconds":604800}]}}"#
        }
        return try decode(team + others)
    }

    private func model(_ snapshot: DashboardSnapshot, expanded: String? = nil) -> PopoverViewModel {
        let defaults = UserDefaults(suiteName: "redline-owner-shape-\(UUID().uuidString)")!
        defaults.set(true, forKey: EngagementPromptPolicy.dismissalKey)
        let model = PopoverViewModel(client: RedlineAPIClient(baseURL: URL(string: "http://127.0.0.1:1")!), defaults: defaults)
        model.apply(snapshot)
        model.expandedAccountID = expanded
        return model
    }

    private static let actions = StatusPopoverActions(
        showDashboard: {}, openBrowser: {}, showRunLogs: { _ in }, reconnectProvider: { _ in },
        checkForUpdates: {}, enableNotifications: {}, showAgentPermissionHelp: {}, showAppSetup: {},
        pairDevice: {}, manageSubscription: {}, quit: {}
    )

    /// Every text frame in the rendered popover, from the accessibility tree:
    /// a label squeezed to a letter per line is far taller than it is wide.
    private func textFrames(_ host: NSView) -> [(String, NSRect)] {
        var found: [(String, NSRect)] = []
        func walk(_ element: Any) {
            let object = element as AnyObject
            let role = object.accessibilityRole?() ?? nil
            let frame = object.accessibilityFrame?() ?? .zero
            let label = (object.accessibilityLabel?() ?? nil) ?? ""
            if role == .staticText, !label.isEmpty { found.append((label, frame)) }
            for child in (object.accessibilityChildren?() ?? nil) ?? [] { walk(child) }
        }
        walk(host)
        return found
    }

    private func host(_ snapshot: DashboardSnapshot, expanded: String? = nil) -> NSHostingView<some View> {
        let view = StatusPopoverView(model: model(snapshot, expanded: expanded), actions: Self.actions).frame(width: 422, height: 633)
        let host = NSHostingView(rootView: view)
        host.frame = NSRect(x: 0, y: 0, width: 422, height: 633)
        let window = NSWindow(contentRect: host.frame, styleMask: [.borderless], backing: .buffered, defer: false)
        window.contentView = host
        host.layoutSubtreeIfNeeded()
        return host
    }

    private func expectReadable(_ host: NSHostingView<some View>) {
        #expect(host.fittingSize.width <= 422 + 0.5, "popover wants \(host.fittingSize.width)pt")
        for (label, frame) in textFrames(host) where label.count > 3 && frame.width > 0 {
            #expect(frame.height <= max(frame.width * 1.5, 40), "\"\(label)\" squeezed to \(frame.size)")
        }
    }

    @Test func theInstalledShapeStacksReadablyAndKeepsTheAttention() throws {
        let snapshot = try Self.beforeFix()
        let tray = try #require(AccountTray(snapshot: snapshot))
        #expect(tray.rows.map(\.title) == ["Delta", "Alpha", "Echo", "Bravo", "Charlie", "claude-main"])
        #expect(tray.rows.prefix(5).allSatisfy { $0.status == .error && $0.stateLine.contains("Usage unavailable") })
        #expect(tray.overview.contains("6 accounts") && tray.overview.contains("5 need attention"))
        let host = host(snapshot)
        expectReadable(host)
        try render(host, name: "07-owner-before-fix")
    }

    @Test func teamSeatsShowTheirUsageAndSayWeeklyIsNotReported() throws {
        let snapshot = try Self.afterFix()
        let tray = try #require(AccountTray(snapshot: snapshot))
        #expect(tray.rows.map(\.title) == ["Delta", "Alpha", "Echo", "Bravo", "Charlie", "claude-main"])
        let team = Array(tray.rows.prefix(5))
        // Fresh usage, not errors: nothing here needs attention.
        #expect(team.allSatisfy { $0.status == .ok })
        #expect(tray.overview == "6 accounts · 0 dispatching")
        for row in team {
            // Weekly is "not reported" -- never a number, never the 5-hour
            // value copied over (the payload's stray weekly is ignored).
            #expect(row.weekly.value == "n/r" && row.weekly.percent == nil && row.weekly.detail == "Weekly limit not reported")
            #expect(row.weekly.spoken.contains("not reported"))
            #expect(row.stateLine == "Dispatch held · weekly not reported")
            #expect(row.credits == "0")
            #expect(row.plan == "Team 5x")
            #expect(row.details.contains { $0.label == "Fable" && $0.value.hasPrefix("100% left · resets ") })
        }
        let echo = try #require(team.first { $0.title == "Echo" })
        #expect(echo.session.value == "76%" && echo.session.percent == 76)
        #expect(echo.weekly.value != echo.session.value)
        let delta = try #require(team.first { $0.title == "Delta" })
        #expect(delta.session.value == "100%" && delta.session.reset == "first use")
        // The personal account still shows its weekly window.
        let main = try #require(tray.rows.last)
        #expect(main.weekly.value == "45%")
        #expect(team.allSatisfy { $0.compactState == "Dispatch held" && $0.weekly.reset == "not reported" })
        // The menu bar's spoken label says "weekly not reported", not "—".
        let badge = try #require(TrayState(snapshot: snapshot).providerBadges.first { $0.provider == "claude" })
        #expect(badge.spoken.hasPrefix("Claude 6 accounts: Delta weekly not reported, Alpha weekly not reported"))
        #expect(badge.spoken.hasSuffix("claude-main 45%"))
        #expect(!badge.spoken.contains("—"))

        let collapsed = host(snapshot)
        expectReadable(collapsed)
        try render(collapsed, name: "08-owner-after-fix")
        let expanded = host(snapshot, expanded: "claude-5ea70003")
        expectReadable(expanded)
        try render(expanded, name: "09-owner-after-fix-echo-expanded")
    }

    /// With several non-Claude providers the remaining cards stack rather
    /// than share one row.
    @Test func otherProvidersStackOnceThereAreMoreThanTwo() {
        #expect(StatusPopoverView.sideBySideLimit == 2)
    }

    private func render(_ host: NSHostingView<some View>, name: String) throws {
        guard let directory = ProcessInfo.processInfo.environment["REDLINE_TRAY_SCREENSHOTS"] else { return }
        host.appearance = NSAppearance(named: .darkAqua)
        host.display()
        let rep = try #require(host.bitmapImageRepForCachingDisplay(in: host.bounds))
        host.cacheDisplay(in: host.bounds, to: rep)
        let data = try #require(rep.representation(using: .png, properties: [:]))
        try FileManager.default.createDirectory(atPath: directory, withIntermediateDirectories: true)
        try data.write(to: URL(fileURLWithPath: directory).appendingPathComponent("\(name).png"))
    }
}
