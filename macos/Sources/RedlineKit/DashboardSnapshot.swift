import Foundation

public struct DashboardSnapshot: Codable, Sendable {
	public let demo: DemoSummary?
    public let health: HealthSummary
    public let scheduler: SchedulerSummary
    public let providers: [ProviderSummary]
    public let tasks: [TaskSummary]
    public let runs: [RunSummary]
    public let attempts: [AttemptSummary]
    public let unreadRuns: Int

    public init(
		demo: DemoSummary? = nil,
        health: HealthSummary,
        scheduler: SchedulerSummary,
        providers: [ProviderSummary],
        tasks: [TaskSummary] = [],
        runs: [RunSummary] = [],
        attempts: [AttemptSummary] = [],
        unreadRuns: Int = 0
    ) {
		self.demo = demo
        self.health = health
        self.scheduler = scheduler
        self.providers = providers
        self.tasks = tasks
        self.runs = runs
        self.attempts = attempts
        self.unreadRuns = unreadRuns
    }

    public static func decode(_ data: Data) throws -> DashboardSnapshot {
        try JSONDecoder().decode(DashboardSnapshot.self, from: data)
    }

    public var latestAttempt: AttemptSummary? { attempts.first }
    public var latestAttemptsByProvider: [AttemptSummary] {
        var seen = Set<String>()
        return attempts.filter { seen.insert($0.providerAccountID).inserted }
    }
    public var unresolvedFailure: RunSummary? {
        runs.first { run in
            guard run.state == "failed" else { return false }
            guard let task = tasks.first(where: { $0.id == run.taskID }) else { return true }
            return task.state == "failed"
        }
    }
    public var unresolvedFailureTask: TaskSummary? {
        guard let failure = unresolvedFailure else { return nil }
        return tasks.first { $0.id == failure.taskID }
    }

    enum CodingKeys: String, CodingKey {
		case demo, health, scheduler, providers, tasks, runs, attempts
        case unreadRuns = "unread_runs"
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
		demo = try container.decodeIfPresent(DemoSummary.self, forKey: .demo)
		health = try container.decode(HealthSummary.self, forKey: .health)
        scheduler = try container.decode(SchedulerSummary.self, forKey: .scheduler)
        providers = try container.decodeIfPresent([ProviderSummary].self, forKey: .providers) ?? []
        tasks = try container.decodeIfPresent([TaskSummary].self, forKey: .tasks) ?? []
        runs = try container.decodeIfPresent([RunSummary].self, forKey: .runs) ?? []
        attempts = try container.decodeIfPresent([AttemptSummary].self, forKey: .attempts) ?? []
        unreadRuns = try container.decodeIfPresent(Int.self, forKey: .unreadRuns) ?? 0
    }
}

public struct DemoSummary: Codable, Sendable, Equatable {
	public let scenario: String
	public let synthetic: Bool

	public init(scenario: String, synthetic: Bool = true) {
		self.scenario = scenario
		self.synthetic = synthetic
	}
}

public struct TaskSummary: Codable, Sendable, Identifiable {
    public let id: String
    public let name: String
    public let priority: Int
    public let state: String
    public let providerAccountID: String
    public let model: String?
    public let harnessType: String?
    public let dispatchTier: String

    enum CodingKeys: String, CodingKey {
        case id, name, priority, state, model
        case providerAccountID = "provider_account_id"
        case harnessType = "harness_type"
        case dispatchTier = "dispatch_tier"
    }
}

public struct RunSummary: Codable, Sendable, Identifiable {
    public let id: String
    public let taskID: String
    public let providerAccountID: String
    public let state: String
    public let startedAt: String
    public let completedAt: String?
    public let error: String?
    public let summary: String?
    public let outcome: String?
    public let artifacts: [RunArtifactSummary]
    public let warnings: [String]
    public let actualProvider: String?
    public let actualModel: String?
    public let activityReadAt: String?

    public var isUnread: Bool {
        (state == "completed" || state == "failed") && activityReadAt == nil
    }

    enum CodingKeys: String, CodingKey {
        case id, state, error, summary, outcome, artifacts, warnings
        case taskID = "task_id"
        case providerAccountID = "provider_account_id"
        case startedAt = "started_at"
        case completedAt = "completed_at"
        case actualProvider = "actual_provider"
        case actualModel = "actual_model"
        case activityReadAt = "activity_read_at"
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        id = try container.decode(String.self, forKey: .id)
        taskID = try container.decode(String.self, forKey: .taskID)
        providerAccountID = try container.decode(String.self, forKey: .providerAccountID)
        state = try container.decode(String.self, forKey: .state)
        startedAt = try container.decode(String.self, forKey: .startedAt)
        completedAt = try container.decodeIfPresent(String.self, forKey: .completedAt)
        error = try container.decodeIfPresent(String.self, forKey: .error)
        summary = try container.decodeIfPresent(String.self, forKey: .summary)
        outcome = try container.decodeIfPresent(String.self, forKey: .outcome)
        artifacts = try container.decodeIfPresent([RunArtifactSummary].self, forKey: .artifacts) ?? []
        warnings = try container.decodeIfPresent([String].self, forKey: .warnings) ?? []
        actualProvider = try container.decodeIfPresent(String.self, forKey: .actualProvider)
        actualModel = try container.decodeIfPresent(String.self, forKey: .actualModel)
        activityReadAt = try container.decodeIfPresent(String.self, forKey: .activityReadAt)
    }
}

public struct RunArtifactSummary: Codable, Sendable, Identifiable {
    public var id: String { "\(type):\(url ?? path ?? label)" }
    public let type: String
    public let label: String
    public let url: String?
    public let path: String?
}

public struct AttemptSummary: Codable, Sendable, Identifiable {
    public let id: Int
    public let providerAccountID: String
    public let outcome: String
    public let decision: String?
    public let reason: String?
    public let error: String?
    public let startedAt: String

    enum CodingKeys: String, CodingKey {
        case id, outcome, decision, reason, error
        case providerAccountID = "provider_account_id"
        case startedAt = "started_at"
    }

    public init(
        id: Int,
        providerAccountID: String,
        outcome: String,
        decision: String?,
        reason: String?,
        error: String? = nil,
        startedAt: String
    ) {
        self.id = id
        self.providerAccountID = providerAccountID
        self.outcome = outcome
        self.decision = decision
        self.reason = reason
        self.error = error
        self.startedAt = startedAt
    }
}

public struct HealthSummary: Codable, Sendable {
    public let status: String
    public let window: String
    public let activeRuns: Int
    public let dispatchErrors: Int

    enum CodingKeys: String, CodingKey {
        case status, window
        case activeRuns = "active_runs"
        case dispatchErrors = "dispatch_errors"
    }

    public init(status: String, window: String, activeRuns: Int, dispatchErrors: Int) {
        self.status = status
        self.window = window
        self.activeRuns = activeRuns
        self.dispatchErrors = dispatchErrors
    }
}

public struct SchedulerSummary: Codable, Sendable {
    public let enabled: Bool
    public let running: Bool
    public let nextCycleAt: String?

    enum CodingKeys: String, CodingKey {
        case enabled, running
        case nextCycleAt = "next_cycle_at"
    }

    public init(enabled: Bool, running: Bool, nextCycleAt: String?) {
        self.enabled = enabled
        self.running = running
        self.nextCycleAt = nextCycleAt
    }
}

public struct ProviderSummary: Codable, Sendable, Identifiable {
    public let id: String
    public let provider: String
    public let paused: Bool
    public let snapshot: UsageSnapshot?
    public let snapshotStale: Bool
    public let error: String?

    public init(
        id: String,
        provider: String,
        paused: Bool = false,
        snapshot: UsageSnapshot?,
        snapshotStale: Bool = false,
        error: String? = nil
    ) {
        self.id = id
        self.provider = provider
        self.paused = paused
        self.snapshot = snapshot
        self.snapshotStale = snapshotStale
        self.error = error
    }

    enum CodingKeys: String, CodingKey {
        case id, provider, paused, snapshot, error
        case snapshotStale = "snapshot_stale"
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        id = try container.decode(String.self, forKey: .id)
        provider = try container.decode(String.self, forKey: .provider)
        paused = try container.decodeIfPresent(Bool.self, forKey: .paused) ?? false
        snapshot = try container.decodeIfPresent(UsageSnapshot.self, forKey: .snapshot)
        snapshotStale = try container.decodeIfPresent(Bool.self, forKey: .snapshotStale) ?? false
        error = try container.decodeIfPresent(String.self, forKey: .error)
    }

    public var displayName: String {
        switch provider.lowercased() {
        case "claude": "Claude"
        case "codex": "Codex"
        default: provider.capitalized
        }
    }

    public var weeklyPercent: Int? { snapshotStale ? nil : snapshot?.weekly.map(Self.percent) }
    public var shortPercent: Int? { snapshotStale ? nil : snapshot?.short.map(Self.percent) }
    public var modelAllowances: [AllowanceSummary] {
        snapshot?.allowances.filter { $0.key.hasPrefix("model:") } ?? []
    }

    private static func percent(_ window: UsageWindow) -> Int {
        Int((min(max(window.remaining, 0), 1) * 100).rounded())
    }
}

public struct RunNotificationEvent: Sendable, Equatable {
    public let runID: String
    public let taskID: String
    public let providerAccountID: String
    public let state: String
    public let error: String?
}

public struct RunNotificationTracker: Sendable {
    private var observedRunStates = [String: String]()
    private var hasEstablishedBaseline = false

    public init() {}

    public mutating func observe(_ runs: [RunSummary]) -> [RunNotificationEvent] {
        let observable = runs.filter {
            $0.state == "running" || $0.state == "completed" || $0.state == "failed"
        }
        defer {
            for run in observable {
                observedRunStates[run.id] = run.state
            }
            hasEstablishedBaseline = true
        }
        guard hasEstablishedBaseline else {
            guard let run = observable.first(where: { $0.state == "failed" }) else { return [] }
            return [RunNotificationEvent(
                runID: run.id,
                taskID: run.taskID,
                providerAccountID: run.providerAccountID,
                state: run.state,
                error: run.error
            )]
        }
        return observable.compactMap { run in
            guard observedRunStates[run.id] != run.state else { return nil }
            return RunNotificationEvent(
                runID: run.id,
                taskID: run.taskID,
                providerAccountID: run.providerAccountID,
                state: run.state,
                error: run.error
            )
        }
    }
}

public struct UsageSnapshot: Codable, Sendable {
    public let short: UsageWindow?
    public let weekly: UsageWindow?
    public let allowances: [AllowanceSummary]
    public let source: String?

    public init(short: UsageWindow?, weekly: UsageWindow?, allowances: [AllowanceSummary], source: String?) {
        self.short = short
        self.weekly = weekly
        self.allowances = allowances
        self.source = source
    }

    enum CodingKeys: String, CodingKey { case short, weekly, allowances, source }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        short = try container.decodeIfPresent(UsageWindow.self, forKey: .short)
        weekly = try container.decodeIfPresent(UsageWindow.self, forKey: .weekly)
        allowances = try container.decodeIfPresent([AllowanceSummary].self, forKey: .allowances) ?? []
        source = try container.decodeIfPresent(String.self, forKey: .source)
    }
}

public struct UsageWindow: Codable, Sendable {
    public let remaining: Double
    public let resetsAt: String?

    enum CodingKeys: String, CodingKey {
        case remaining
        case resetsAt = "resets_at"
    }

    public init(remaining: Double, resetsAt: String? = nil) {
        self.remaining = remaining
        self.resetsAt = resetsAt
    }
}

public struct AllowanceSummary: Codable, Sendable {
    public let key: String
    public let sourceLabel: String
    public let remaining: Double

    enum CodingKeys: String, CodingKey {
        case key, remaining
        case sourceLabel = "source_label"
    }

    public var displayName: String { sourceLabel.isEmpty ? key : sourceLabel }
    public var percent: Int { Int((min(max(remaining, 0), 1) * 100).rounded()) }
}

public struct TrayState: Sendable {
    public enum Level: Sendable { case comfortable, constrained, critical, running, degraded }
    public enum Activity: Sendable { case waiting, running, attention }

    public let level: Level
    public let activity: Activity
    public let lowestWeeklyPercent: Int?
    public let iconDescription: String
    public let menuBarTitle: String
    public let providerBadges: [ProviderBadge]

    public init(snapshot: DashboardSnapshot) {
        let percentages = snapshot.providers.compactMap { provider in
            provider.snapshotStale ? nil : provider.snapshot?.weekly?.remaining
        }
        lowestWeeklyPercent = percentages.min().map { Int(($0 * 100).rounded()) }

        if snapshot.health.activeRuns > 0 || snapshot.scheduler.running {
            level = .running
            activity = .running
        } else if snapshot.unresolvedFailure != nil ||
                    snapshot.latestAttemptsByProvider.contains(where: { $0.outcome == "error" }) {
            level = .degraded
            activity = .attention
        } else if let percent = lowestWeeklyPercent, percent <= 20 {
            level = .critical
            activity = .waiting
        } else if let percent = lowestWeeklyPercent, percent <= 40 {
            level = .constrained
            activity = .waiting
        } else {
            level = .comfortable
            activity = .waiting
        }

        if let percent = lowestWeeklyPercent {
            iconDescription = "Redline: \(percent)% weekly usage remaining"
        } else {
            iconDescription = "Redline: usage unavailable"
        }

        let activityLabel = switch activity {
        case .waiting: "WAIT"
        case .running: "RUN"
        case .attention: "ATTN"
        }
        providerBadges = snapshot.providers
            .sorted { left, right in
                let leftRank = Self.providerRank(left.provider)
                let rightRank = Self.providerRank(right.provider)
                if leftRank != rightRank { return leftRank < rightRank }
                return left.displayName.localizedCaseInsensitiveCompare(right.displayName) == .orderedAscending
            }
            .map { provider in
                ProviderBadge(provider: provider.provider, displayName: provider.displayName, percent: provider.weeklyPercent)
            }
        let providers = providerBadges.map { "\($0.displayName) \($0.percent.map { "\($0)%" } ?? "—")" }
        menuBarTitle = activityLabel + (providers.isEmpty ? "" : "  " + providers.joined(separator: " · "))
    }

    private static func providerRank(_ provider: String) -> Int {
        switch provider.lowercased() {
        case "codex": 0
        case "claude": 1
        default: 2
        }
    }
}

public struct ProviderBadge: Sendable, Equatable {
    public let provider: String
    public let displayName: String
    public let percent: Int?
}
