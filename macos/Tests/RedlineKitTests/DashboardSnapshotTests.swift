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
        {"id":"codex-main","provider":"codex","paused":true,"snapshot":{"weekly":{"remaining":0.32,"resets_at":"2026-07-25T03:24:11Z"},"source":"builtin"}}
      ],
      "tasks": [{"id":"tests","name":"Add focused tests","priority":60,"state":"queued","provider_account_id":"codex-main","model":"gpt-5","dispatch_tier":"behind"}],
      "unread_runs": 1,
      "runs": [{"id":"run-1","task_id":"tests","provider_account_id":"codex-main","state":"completed","started_at":"2026-07-20T23:40:00Z","summary":"Opened a PR.","outcome":"changes_proposed","artifacts":[{"type":"pull_request","label":"PR #42","url":"https://github.com/acme/app/pull/42"}],"actual_provider":"openai-codex","actual_model":"gpt-5.6-sol"}],
      "attempts": [
        {"id":9,"provider_account_id":"codex-main","outcome":"wait","decision":"WAIT","reason":"no pace threshold matched","started_at":"2026-07-20T23:51:00Z"},
        {"id":8,"provider_account_id":"claude-main","outcome":"error","error":"native usage source: Claude credentials are invalid","started_at":"2026-07-20T23:50:00Z"}
      ]
    }
    """#.utf8)

    let snapshot = try DashboardSnapshot.decode(data)

    #expect(snapshot.health.status == "degraded")
    #expect(snapshot.health.activeRuns == 2)
    #expect(snapshot.scheduler.enabled)
    #expect(snapshot.providers.count == 2)
    #expect(snapshot.providers[0].displayName == "Claude")
    #expect(!snapshot.providers[0].paused)
    #expect(snapshot.providers[0].weeklyPercent == 53)
    #expect(snapshot.providers[0].shortPercent == 96)
    #expect(snapshot.providers[0].modelAllowances.first?.displayName == "Fable")
    #expect(snapshot.providers[1].displayName == "Codex")
    #expect(snapshot.providers[1].paused)
    #expect(snapshot.providers[1].shortPercent == nil)
    #expect(snapshot.providers[1].snapshot?.weekly?.resetsAt == "2026-07-25T03:24:11Z")
    #expect(snapshot.tasks.first?.name == "Add focused tests")
    #expect(snapshot.tasks.first?.dispatchTier == "behind")
    #expect(snapshot.latestAttempt?.decision == "WAIT")
    #expect(snapshot.latestAttemptsByProvider.map(\.providerAccountID) == ["codex-main", "claude-main"])
    #expect(snapshot.latestAttemptsByProvider.last?.error == "native usage source: Claude credentials are invalid")
    #expect(snapshot.unreadRuns == 1)
    #expect(snapshot.runs.first?.isUnread == true)
    #expect(snapshot.runs.first?.artifacts.first?.type == "pull_request")
    #expect(snapshot.runs.first?.actualModel == "gpt-5.6-sol")
}

@Test func runNotificationTrackerBaselinesHistoryAndReportsStateChanges() throws {
    let initial = try DashboardSnapshot.decode(Data(#"""
    {"health":{"status":"healthy","window":"24h","active_runs":0,"dispatch_errors":0},"scheduler":{"enabled":true,"running":false},"providers":[],"tasks":[],"attempts":[],"runs":[
      {"id":"old","task_id":"task-old","provider_account_id":"codex-main","state":"completed","started_at":"2026-07-20T01:00:00Z"},
      {"id":"active","task_id":"task-active","provider_account_id":"claude-main","state":"running","started_at":"2026-07-21T01:00:00Z"}
    ]}
    """#.utf8))
    var tracker = RunNotificationTracker()
    #expect(tracker.observe(initial.runs).isEmpty)

    let update = try DashboardSnapshot.decode(Data(#"""
    {"health":{"status":"degraded","window":"24h","active_runs":0,"dispatch_errors":0},"scheduler":{"enabled":true,"running":false},"providers":[],"tasks":[],"attempts":[],"runs":[
      {"id":"new-failure","task_id":"task-new","provider_account_id":"claude-main","state":"failed","started_at":"2026-07-21T02:00:00Z","error":"boom"},
      {"id":"active","task_id":"task-active","provider_account_id":"claude-main","state":"completed","started_at":"2026-07-21T01:00:00Z"},
      {"id":"old","task_id":"task-old","provider_account_id":"codex-main","state":"completed","started_at":"2026-07-20T01:00:00Z"}
    ]}
    """#.utf8))
    let events = tracker.observe(update.runs)
    #expect(events.map(\.runID) == ["new-failure", "active"])
    #expect(events.first?.state == "failed")
    #expect(tracker.observe(update.runs).isEmpty)
}

@Test func runNotificationTrackerReportsRunStartAndLaterCompletion() throws {
    let empty = try DashboardSnapshot.decode(Data(#"""
    {"health":{"status":"healthy","window":"24h","active_runs":0,"dispatch_errors":0},"scheduler":{"enabled":true,"running":false},"providers":[],"tasks":[],"attempts":[],"runs":[]}
    """#.utf8))
    var tracker = RunNotificationTracker()
    #expect(tracker.observe(empty.runs).isEmpty)

    let running = try DashboardSnapshot.decode(Data(#"""
    {"health":{"status":"healthy","window":"24h","active_runs":1,"dispatch_errors":0},"scheduler":{"enabled":true,"running":false},"providers":[],"tasks":[],"attempts":[],"runs":[
      {"id":"new-run","task_id":"task-new","provider_account_id":"claude-main","state":"running","started_at":"2026-07-21T02:00:00Z"}
    ]}
    """#.utf8))
    #expect(tracker.observe(running.runs).map(\.state) == ["running"])

    let completed = try DashboardSnapshot.decode(Data(#"""
    {"health":{"status":"healthy","window":"24h","active_runs":0,"dispatch_errors":0},"scheduler":{"enabled":true,"running":false},"providers":[],"tasks":[],"attempts":[],"runs":[
      {"id":"new-run","task_id":"task-new","provider_account_id":"claude-main","state":"completed","started_at":"2026-07-21T02:00:00Z","completed_at":"2026-07-21T02:01:00Z"}
    ]}
    """#.utf8))
    #expect(tracker.observe(completed.runs).map(\.state) == ["completed"])
}

@Test func runNotificationTrackerReportsFailedRunFoundAtStartup() throws {
    let snapshot = try DashboardSnapshot.decode(Data(#"""
    {"health":{"status":"degraded","window":"24h","active_runs":0,"completed_runs":0,"failed_runs":1,"dispatch_attempts":1,"dispatch_errors":0,"notification_failures":0},"scheduler":{"enabled":true,"running":false},"providers":[],"tasks":[],"attempts":[],"runs":[
      {"id":"startup-failure","task_id":"task-failed","provider_account_id":"claude-main","state":"failed","started_at":"2026-07-21T02:00:00Z","error":"Claude Code is signed out."},
      {"id":"old-success","task_id":"task-old","provider_account_id":"codex-main","state":"completed","started_at":"2026-07-20T01:00:00Z"}
    ]}
    """#.utf8))

    var tracker = RunNotificationTracker()
    let events = tracker.observe(snapshot.runs)

    #expect(events.map(\.runID) == ["startup-failure"])
    #expect(tracker.observe(snapshot.runs).isEmpty)
}

@Test func runNotificationTrackerReportsOnlyNewestFailureAtStartup() throws {
    let snapshot = try DashboardSnapshot.decode(Data(#"""
    {"health":{"status":"degraded","window":"24h","active_runs":0,"dispatch_errors":0},"scheduler":{"enabled":true,"running":false},"providers":[],"tasks":[],"attempts":[],"runs":[
      {"id":"newest-failure","task_id":"task-new","provider_account_id":"claude-main","state":"failed","started_at":"2026-07-21T03:00:00Z","error":"not logged in"},
      {"id":"older-failure","task_id":"task-old","provider_account_id":"codex-main","state":"failed","started_at":"2026-07-21T02:00:00Z","error":"network error"}
    ]}
    """#.utf8))

    var tracker = RunNotificationTracker()
    #expect(tracker.observe(snapshot.runs).map(\.runID) == ["newest-failure"])
}

@Test func snapshotExposesLatestUnresolvedTaskFailure() throws {
    let snapshot = try DashboardSnapshot.decode(Data(#"""
    {"health":{"status":"degraded","window":"24h","active_runs":0,"completed_runs":0,"failed_runs":1,"dispatch_attempts":1,"dispatch_errors":0,"notification_failures":0},"scheduler":{"enabled":true,"running":false},"providers":[],"attempts":[],"tasks":[
      {"id":"failed-task","name":"Find one real bug","priority":80,"state":"failed","provider_account_id":"claude-main","model":"sonnet","dispatch_tier":"behind"}
    ],"runs":[
      {"id":"failed-run","task_id":"failed-task","provider_account_id":"claude-main","state":"failed","started_at":"2026-07-21T02:00:00Z","error":"Claude Code is signed out."}
    ]}
    """#.utf8))

    #expect(snapshot.unresolvedFailure?.id == "failed-run")
    #expect(snapshot.unresolvedFailureTask?.name == "Find one real bug")
    #expect(TrayState(snapshot: snapshot).activity == .attention)
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

@Test func staleProviderUsageIsNotPresentedAsCurrentInTrayState() throws {
    let snapshot = try DashboardSnapshot.decode(Data(#"""
    {"health":{"status":"degraded","window":"24h","active_runs":0,"dispatch_errors":1},"scheduler":{"enabled":true,"running":false},"providers":[
      {"id":"claude-main","provider":"claude","snapshot_stale":true,"error":"Usage data is stale","snapshot":{"weekly":{"remaining":0.53,"resets_at":"2026-07-24T17:00:00Z"},"source":"native"}},
      {"id":"codex-main","provider":"codex","snapshot":{"weekly":{"remaining":0.32,"resets_at":"2026-07-25T03:24:11Z"},"source":"openusage"}}
    ],"tasks":[],"runs":[],"attempts":[]}
    """#.utf8))

    #expect(snapshot.providers[0].snapshotStale)
    #expect(snapshot.providers[0].weeklyPercent == nil)
    #expect(snapshot.providers[0].error == "Usage data is stale")
    #expect(TrayState(snapshot: snapshot).lowestWeeklyPercent == 32)
    #expect(TrayState(snapshot: snapshot).menuBarTitle == "WAIT  Codex 32% · Claude —")
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
