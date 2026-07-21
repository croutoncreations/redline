import Foundation

public struct DashboardSnapshot: Codable, Sendable {
    public let health: HealthSummary
    public let scheduler: SchedulerSummary
    public let providers: [ProviderSummary]

    public init(health: HealthSummary, scheduler: SchedulerSummary, providers: [ProviderSummary]) {
        self.health = health
        self.scheduler = scheduler
        self.providers = providers
    }

    public static func decode(_ data: Data) throws -> DashboardSnapshot {
        try JSONDecoder().decode(DashboardSnapshot.self, from: data)
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
    public let snapshot: UsageSnapshot?

    public init(id: String, provider: String, snapshot: UsageSnapshot?) {
        self.id = id
        self.provider = provider
        self.snapshot = snapshot
    }

    public var displayName: String {
        switch provider.lowercased() {
        case "claude": "Claude"
        case "codex": "Codex"
        default: provider.capitalized
        }
    }

    public var weeklyPercent: Int? { snapshot?.weekly.map(Self.percent) }
    public var shortPercent: Int? { snapshot?.short.map(Self.percent) }
    public var modelAllowances: [AllowanceSummary] {
        snapshot?.allowances.filter { $0.key.hasPrefix("model:") } ?? []
    }

    private static func percent(_ window: UsageWindow) -> Int {
        Int((min(max(window.remaining, 0), 1) * 100).rounded())
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

    public init(remaining: Double) {
        self.remaining = remaining
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

    public init(snapshot: DashboardSnapshot) {
        let percentages = snapshot.providers.compactMap(\.snapshot?.weekly?.remaining)
        lowestWeeklyPercent = percentages.min().map { Int(($0 * 100).rounded()) }

        if snapshot.health.activeRuns > 0 || snapshot.scheduler.running {
            level = .running
            activity = .running
        } else if snapshot.health.status != "healthy" && snapshot.health.status != "ok" {
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
        let providers = snapshot.providers
            .sorted { left, right in
                let leftRank = Self.providerRank(left.provider)
                let rightRank = Self.providerRank(right.provider)
                if leftRank != rightRank { return leftRank < rightRank }
                return left.displayName.localizedCaseInsensitiveCompare(right.displayName) == .orderedAscending
            }
            .map { provider in
                let percent = provider.weeklyPercent.map { "\($0)%" } ?? "—"
                return "\(provider.displayName) \(percent)"
            }
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
