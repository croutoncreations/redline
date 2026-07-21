import Foundation
import Testing
@testable import RedlineKit

@Test func dashboardSnapshotDecodesProviderWindowsAndOperationalState() throws {
    let data = Data(#"""
    {
      "health": {"status":"degraded","window":"24h0m0s","active_runs":2,"dispatch_errors":3},
      "scheduler": {"enabled":true,"running":false,"next_cycle_at":"2026-07-20T23:58:51Z"},
      "providers": [
        {"id":"claude-main","provider":"claude","snapshot":{"short":{"remaining":0.96,"resets_at":"2026-07-21T04:00:00Z"},"weekly":{"remaining":0.53,"resets_at":"2026-07-24T17:00:00Z"},"allowances":[{"key":"model:fable:weekly","source_label":"Fable","remaining":0.12}],"source":"openusage"}},
        {"id":"codex-main","provider":"codex","snapshot":{"weekly":{"remaining":0.32,"resets_at":"2026-07-25T03:24:11Z"},"source":"builtin"}}
      ],
      "tasks": [{"id":"tests","name":"Add focused tests","priority":60,"state":"queued","provider_account_id":"codex-main","model":"gpt-5","dispatch_tier":"behind"}],
      "runs": [],
      "attempts": [
        {"id":9,"provider_account_id":"codex-main","outcome":"wait","decision":"WAIT","reason":"no pace threshold matched","started_at":"2026-07-20T23:51:00Z"},
        {"id":8,"provider_account_id":"claude-main","outcome":"wait","decision":"WAIT","reason":"no actionable weekly overflow","started_at":"2026-07-20T23:50:00Z"}
      ]
    }
    """#.utf8)

    let snapshot = try DashboardSnapshot.decode(data)

    #expect(snapshot.health.status == "degraded")
    #expect(snapshot.health.activeRuns == 2)
    #expect(snapshot.scheduler.enabled)
    #expect(snapshot.providers.count == 2)
    #expect(snapshot.providers[0].displayName == "Claude")
    #expect(snapshot.providers[0].weeklyPercent == 53)
    #expect(snapshot.providers[0].shortPercent == 96)
    #expect(snapshot.providers[0].modelAllowances.first?.displayName == "Fable")
    #expect(snapshot.providers[1].displayName == "Codex")
    #expect(snapshot.providers[1].shortPercent == nil)
    #expect(snapshot.providers[1].snapshot?.weekly?.resetsAt == "2026-07-25T03:24:11Z")
    #expect(snapshot.tasks.first?.name == "Add focused tests")
    #expect(snapshot.tasks.first?.dispatchTier == "behind")
    #expect(snapshot.latestAttempt?.decision == "WAIT")
    #expect(snapshot.latestAttemptsByProvider.map(\.providerAccountID) == ["codex-main", "claude-main"])
}

@Test func trayStateUsesWorstRelevantSignal() throws {
    let healthy = TrayState(snapshot: .fixture(health: "healthy", activeRuns: 0, lowestWeekly: 0.75))
    #expect(healthy.level == .comfortable)
    #expect(healthy.activity == .waiting)
    #expect(healthy.iconDescription == "Redline: 75% weekly usage remaining")
    #expect(healthy.menuBarTitle == "WAIT  Codex 75%")

    let running = TrayState(snapshot: .fixture(health: "healthy", activeRuns: 1, lowestWeekly: 0.42))
    #expect(running.level == .running)
    #expect(running.activity == .running)
    #expect(running.menuBarTitle == "RUN  Codex 42%")

    let recovered = TrayState(snapshot: .fixture(health: "degraded", activeRuns: 0, lowestWeekly: 0.42, latestOutcome: "wait"))
    #expect(recovered.level == .comfortable)
    #expect(recovered.activity == .waiting)
    #expect(recovered.menuBarTitle == "WAIT  Codex 42%")

    let failing = TrayState(snapshot: .fixture(health: "degraded", activeRuns: 0, lowestWeekly: 0.42, latestOutcome: "error"))
    #expect(failing.level == .degraded)
    #expect(failing.activity == .attention)
}

@Test func trayStateShowsEveryProviderInStableOrder() {
    let snapshot = DashboardSnapshot(
        health: HealthSummary(status: "healthy", window: "24h", activeRuns: 0, dispatchErrors: 0),
        scheduler: SchedulerSummary(enabled: true, running: false, nextCycleAt: nil),
        providers: [
            ProviderSummary(id: "claude-main", provider: "claude", snapshot: UsageSnapshot(short: nil, weekly: UsageWindow(remaining: 0.53), allowances: [], source: "test")),
            ProviderSummary(id: "codex-main", provider: "codex", snapshot: UsageSnapshot(short: nil, weekly: UsageWindow(remaining: 0.32), allowances: [], source: "test")),
            ProviderSummary(id: "other", provider: "other", snapshot: nil),
        ]
    )

    #expect(TrayState(snapshot: snapshot).menuBarTitle == "WAIT  Codex 32% · Claude 53% · Other —")
    #expect(TrayState(snapshot: snapshot).providerBadges.map(\.displayName) == ["Codex", "Claude", "Other"])
}

private extension DashboardSnapshot {
    static func fixture(health: String, activeRuns: Int, lowestWeekly: Double, latestOutcome: String? = nil) -> DashboardSnapshot {
        DashboardSnapshot(
            health: HealthSummary(status: health, window: "24h", activeRuns: activeRuns, dispatchErrors: 0),
            scheduler: SchedulerSummary(enabled: true, running: false, nextCycleAt: nil),
            providers: [
                ProviderSummary(
                    id: "codex-main",
                    provider: "codex",
                    snapshot: UsageSnapshot(short: nil, weekly: UsageWindow(remaining: lowestWeekly), allowances: [], source: "test")
                )
            ],
            attempts: latestOutcome.map {
                [AttemptSummary(id: 1, providerAccountID: "codex-main", outcome: $0, decision: nil, reason: nil, startedAt: "2026-07-20T23:50:00Z")]
            } ?? []
        )
    }
}
