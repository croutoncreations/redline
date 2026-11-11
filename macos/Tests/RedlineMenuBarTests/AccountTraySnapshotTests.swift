import AppKit
import Foundation
import RedlineKit
import SwiftUI
import Testing
@testable import RedlineMenuBar

/// Renders the real popover with synthetic accounts (no service, no
/// network: the client points at a closed port and is never called) and
/// checks layout facts a person would notice. Set REDLINE_TRAY_SCREENSHOTS
/// to a directory to also write the renders as PNGs for review.
@MainActor
struct AccountTraySnapshotTests {
    private static func account(
        _ id: String, label: String?, plan: String?, status: String = "ok", decision: String? = nil,
        short: (Double, Double)? = nil, weekly: (Double, Double)? = nil, extra: String = "", snapshotExtra: String = ""
    ) -> String {
        let now = Date()
        func at(_ hours: Double) -> String {
            ISO8601DateFormatter().string(from: now.addingTimeInterval(hours * 3600))
        }
        var snapshot: [String] = []
        if let plan { snapshot.append(#""plan":"\#(plan)""#) }
        if let short { snapshot.append(#""short":{"remaining":\#(short.0),"resets_at":"\#(at(short.1))"}"#) }
        if let weekly { snapshot.append(#""weekly":{"remaining":\#(weekly.0),"resets_at":"\#(at(weekly.1))"}"#) }
        snapshot.append(#""observed_at":"\#(at(status == "stale" ? -3 : -0.02))""#)
        if !snapshotExtra.isEmpty { snapshot.append(snapshotExtra) }
        let labelJSON = label.map { #""label":"\#($0)","# } ?? ""
        let decisionJSON = decision.map { #""latest_decision":{"decision":"\#($0)"},"# } ?? ""
        return #"{"id":"\#(id)","provider":"claude",\#(labelJSON)\#(decisionJSON)"account_status":"\#(status)",\#(extra)"snapshot":{\#(snapshot.joined(separator: ","))}}"#
    }

    private static func expiry(_ days: Double) -> String {
        ISO8601DateFormatter().string(from: Date().addingTimeInterval(days * 86400))
    }

    /// Five synthetic Claude accounts plus Codex.
    static func fiveAccounts() throws -> DashboardSnapshot {
        let providers = [
            account("claude-client", label: "Client — Northwind", plan: "Team", decision: "WAIT", short: (0, 0.95), weekly: (0.58, 104)),
            account("claude-main", label: "Personal", plan: "Max 20x", decision: "RUN", short: (0.68, 2.2), weekly: (0.41, 75),
                    snapshotExtra: #""banked_resets":2,"banked_resets_expiries":["\#(expiry(3.6))","\#(expiry(9.6))"]"#),
            account("claude-research", label: "Research", plan: "Max 5x", decision: "WAIT", short: (0.92, 4.1), weekly: (0.17, 30),
                    snapshotExtra: #""banked_resets":0"#),
            account("claude-side", label: "Side project", plan: "Pro", status: "stale", short: (0.64, -1), weekly: (0.41, 66),
                    extra: #""snapshot_stale":true,"error":"OpenUsage reports this account's usage as out of date.","#,
                    snapshotExtra: #""source_stale":true,"banked_resets":1,"banked_resets_expiries":["\#(expiry(5))"]"#),
            #"{"id":"claude-staging","provider":"claude","label":"Staging","account_status":"signed_out","snapshot_stale":true,"usage_source":{"reason":"signed_out"}}"#,
            #"{"id":"codex-main","provider":"codex","account_status":"ok","snapshot":{"weekly":{"remaining":0.43,"resets_at":"\#(expiry(4))"}}}"#,
        ]
        return try DashboardSnapshot.decode(Data("""
        {"demo":{"scenario":"multi-account","synthetic":true},
         "health":{"status":"healthy","window":"24h","active_runs":0,"dispatch_errors":0},
         "scheduler":{"enabled":true,"running":false},
         "providers":[\(providers.joined(separator: ","))],"tasks":[],"runs":[],"attempts":[]}
        """.utf8))
    }

    private func model(_ snapshot: DashboardSnapshot, expanded: String? = nil) -> PopoverViewModel {
        let defaults = UserDefaults(suiteName: "redline-tray-test-\(UUID().uuidString)")!
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

    /// The account section alone, at the popover's width, for measuring.
    private func section(_ tray: AccountTray, expanded: String?) -> some View {
        AccountTrayList(tray: tray, expandedID: .constant(expanded), pausing: [], setPaused: { _, _ in })
            .padding(18)
            .frame(width: 420)
    }

    private func size<V: View>(_ view: V) -> CGSize {
        let host = NSHostingView(rootView: view)
        host.layout()
        return host.fittingSize
    }

    private func render<V: View>(_ view: V, name: String, appearance: NSAppearance.Name = .darkAqua) throws {
        guard let directory = ProcessInfo.processInfo.environment["REDLINE_TRAY_SCREENSHOTS"] else { return }
        let host = NSHostingView(rootView: view)
        host.appearance = NSAppearance(named: appearance)
        let size = host.fittingSize
        host.frame = NSRect(origin: .zero, size: size)
        let window = NSWindow(contentRect: host.frame, styleMask: [.borderless], backing: .buffered, defer: false)
        window.appearance = NSAppearance(named: appearance)
        window.contentView = host
        host.layoutSubtreeIfNeeded()
        host.display()
        let rep = try #require(host.bitmapImageRepForCachingDisplay(in: host.bounds))
        host.cacheDisplay(in: host.bounds, to: rep)
        let data = try #require(rep.representation(using: .png, properties: [:]))
        try FileManager.default.createDirectory(atPath: directory, withIntermediateDirectories: true)
        try data.write(to: URL(fileURLWithPath: directory).appendingPathComponent("\(name).png"))
    }

    @Test func fiveAccountsFitWithoutScrollingAtNormalSize() throws {
        let tray = try #require(AccountTray(snapshot: try Self.fiveAccounts()))
        #expect(tray.rows.count == 5)
        let height = size(section(tray, expanded: nil)).height
        // Header + column labels + five rows: well inside the 640pt popover,
        // leaving room for the other sections.
        #expect(height < 380, "five collapsed rows took \(height)pt")
        try render(
            StatusPopoverView(model: model(try Self.fiveAccounts()), actions: Self.actions)
                .frame(width: 420, height: 640),
            name: "01-tray-five-accounts"
        )
    }

    @Test func expandingARowGrowsOnlyThatRowAndKeepsOrder() throws {
        let snapshot = try Self.fiveAccounts()
        let tray = try #require(AccountTray(snapshot: snapshot))
        let collapsed = size(section(tray, expanded: nil)).height
        let expanded = size(section(tray, expanded: "claude-main")).height
        #expect(expanded > collapsed + 100, "detail did not open: \(collapsed) → \(expanded)")
        // Expanding is view state: the order of rows is unchanged.
        #expect(AccountTray(snapshot: snapshot)?.rows.map(\.id) == tray.rows.map(\.id))
        try render(
            StatusPopoverView(model: model(snapshot, expanded: "claude-main"), actions: Self.actions)
                .frame(width: 420, height: 640),
            name: "02-tray-expanded-personal"
        )
        try render(
            StatusPopoverView(model: model(snapshot, expanded: "claude-side"), actions: Self.actions)
                .frame(width: 420, height: 640),
            name: "03-tray-expanded-stale"
        )
        try render(
            StatusPopoverView(model: model(snapshot, expanded: "claude-main"), actions: Self.actions)
                .frame(width: 420, height: 640),
            name: "05-tray-light",
            appearance: .aqua
        )
    }

    @Test func moreThanFiveAccountsScrollInAFixedFrame() throws {
        var snapshot = try Self.fiveAccounts()
        let extra = (1...3).map { index in
            ProviderSummary(id: "claude-extra-\(index)", provider: "claude",
                            snapshot: UsageSnapshot(short: nil, weekly: UsageWindow(remaining: 0.5), allowances: [], source: "test"),
                            label: "Extra \(index)", accountStatus: "ok")
        }
        snapshot = DashboardSnapshot(demo: snapshot.demo, health: snapshot.health, scheduler: snapshot.scheduler,
                                     providers: snapshot.providers + extra)
        let tray = try #require(AccountTray(snapshot: snapshot))
        #expect(tray.rows.count == 8)
        let five = size(section(try #require(AccountTray(snapshot: try Self.fiveAccounts())), expanded: nil)).height
        let eight = size(section(tray, expanded: nil)).height
        // The list scrolls in a frame sized for five rows, so the sections
        // below do not move as accounts are added.
        #expect(abs(eight - five) < 60, "eight accounts grew the section: \(five) → \(eight)")
        // An open row's details are added to the frame, not squeezed into it.
        let open = size(section(tray, expanded: "claude-main")).height
        let detail = AccountTrayDetailView.estimatedHeight(try #require(tray.expandedRow("claude-main")))
        #expect(open >= eight + detail - 1, "detail squeezed: \(eight) + \(detail) > \(open)")
        try render(
            section(tray, expanded: "claude-main").background(Color(nsColor: .windowBackgroundColor)),
            name: "06-tray-eight-accounts-expanded"
        )
    }

    /// The details' measured height stays within the estimate the >5 frame
    /// adds, even with many credit deadlines and a long reason.
    @Test func detailFitsTheRoomTheFrameGivesIt() throws {
        let many = (1...5).map { "\"\(Self.expiry(Double($0) * 4))\"" }.joined(separator: ",")
        let snapshot = try DashboardSnapshot.decode(Data("""
        {"health":{"status":"healthy","window":"24h","active_runs":0,"dispatch_errors":0},"scheduler":{"enabled":true,"running":false},
         "providers":[\(Self.account("claude-a", label: "A", plan: "Team", status: "stale", short: (0.5, 2), weekly: (0.5, 50),
                       extra: #""snapshot_stale":true,"error":"A long collection error that explains in some detail why the numbers could not be refreshed recently.","#,
                       snapshotExtra: "\"banked_resets\":5,\"banked_resets_expiries\":[\(many)]")),
                      \(Self.account("claude-b", label: "B", plan: "Pro", decision: "WAIT", short: (0.5, 2), weekly: (0.5, 50)))]}
        """.utf8))
        let tray = try #require(AccountTray(snapshot: snapshot))
        let row = tray.rows[0]
        let measured = size(
            AccountTrayDetailView(row: row, pausing: false, setPaused: { _ in })
                .frame(width: 420 - 36)
        ).height
        #expect(measured <= AccountTrayDetailView.estimatedHeight(row) + 2,
                "detail \(measured)pt > estimate \(AccountTrayDetailView.estimatedHeight(row))pt")
    }

    /// A long label is cut short in the row (which keeps its height) and
    /// given in full in the open detail; it never pushes the numbers out.
    @Test func longLabelTruncatesInTheRowAndIsWholeInTheDetail() throws {
        let long = "Client — Northwind long-running platform engagement (EU)"
        let snapshot = try DashboardSnapshot.decode(Data("""
        {"health":{"status":"healthy","window":"24h","active_runs":0,"dispatch_errors":0},"scheduler":{"enabled":true,"running":false},
         "providers":[\(Self.account("claude-a", label: long, plan: "Team", decision: "WAIT", short: (0.5, 2), weekly: (0.5, 50))),
                      \(Self.account("claude-b", label: "B", plan: "Pro", decision: "WAIT", short: (0.5, 2), weekly: (0.5, 50)))]}
        """.utf8))
        let tray = try #require(AccountTray(snapshot: snapshot))
        #expect(tray.rows[0].title == long)
        let shortTray = try #require(AccountTray(snapshot: try DashboardSnapshot.decode(Data("""
        {"health":{"status":"healthy","window":"24h","active_runs":0,"dispatch_errors":0},"scheduler":{"enabled":true,"running":false},
         "providers":[\(Self.account("claude-a", label: "A", plan: "Team", decision: "WAIT", short: (0.5, 2), weekly: (0.5, 50))),
                      \(Self.account("claude-b", label: "B", plan: "Pro", decision: "WAIT", short: (0.5, 2), weekly: (0.5, 50)))]}
        """.utf8))))
        // Same collapsed height with a long label as with a short one.
        #expect(size(section(tray, expanded: nil)).height == size(section(shortTray, expanded: nil)).height)
        try render(
            section(tray, expanded: "claude-a").background(Color(nsColor: .windowBackgroundColor)),
            name: "04-tray-long-label"
        )
    }
}
