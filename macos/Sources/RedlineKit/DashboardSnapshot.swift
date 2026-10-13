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
    /// Display-only Cursor and Grok Bot usage; nil when tracking is off. Kept
    /// out of `providers` so it never reaches tray state or scheduling.
    public let cursorUsage: CursorUsageReport?

    public init(
		demo: DemoSummary? = nil,
        health: HealthSummary,
        scheduler: SchedulerSummary,
        providers: [ProviderSummary],
        tasks: [TaskSummary] = [],
        runs: [RunSummary] = [],
        attempts: [AttemptSummary] = [],
        unreadRuns: Int = 0,
        cursorUsage: CursorUsageReport? = nil
    ) {
		self.demo = demo
        self.cursorUsage = cursorUsage
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
        case cursorUsage = "cursor_usage"
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
		demo = try container.decodeIfPresent(DemoSummary.self, forKey: .demo)
        // A malformed Cursor report must never take the whole tray offline.
        cursorUsage = try? container.decodeIfPresent(CursorUsageReport.self, forKey: .cursorUsage)
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
    /// Machine-readable cause of failing collection, when the service knows
    /// one a person can fix ("signed_out"); nil otherwise.
    public let usageSourceReason: String?
    /// The owner's name for this account ("Personal"); nil when unset. Never
    /// an email: the service never sends one.
    public let label: String?
    /// The service's one-word collection state: ok, stale, no_data,
    /// signed_out or error. Nil from an older service that does not send it.
    public let accountStatus: String?
    /// The scheduler's latest decision for this account (RUN, WAIT, UNKNOWN).
    public let latestDecision: String?

    public init(
        id: String,
        provider: String,
        paused: Bool = false,
        snapshot: UsageSnapshot?,
        snapshotStale: Bool = false,
        error: String? = nil,
        usageSourceReason: String? = nil,
        label: String? = nil,
        accountStatus: String? = nil,
        latestDecision: String? = nil
    ) {
        self.id = id
        self.provider = provider
        self.paused = paused
        self.snapshot = snapshot
        self.snapshotStale = snapshotStale
        self.error = error
        self.usageSourceReason = usageSourceReason
        self.label = label
        self.accountStatus = accountStatus
        self.latestDecision = latestDecision
    }

    enum CodingKeys: String, CodingKey {
        case id, provider, paused, snapshot, error, label
        case snapshotStale = "snapshot_stale"
        case usageSource = "usage_source"
        case accountStatus = "account_status"
        case latestDecision = "latest_decision"
    }

    private struct LatestDecision: Codable {
        let decision: String?
    }

    private struct UsageSourceStatus: Codable {
        let reason: String?
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        id = try container.decode(String.self, forKey: .id)
        provider = try container.decode(String.self, forKey: .provider)
        paused = try container.decodeIfPresent(Bool.self, forKey: .paused) ?? false
        snapshot = try container.decodeIfPresent(UsageSnapshot.self, forKey: .snapshot)
        snapshotStale = try container.decodeIfPresent(Bool.self, forKey: .snapshotStale) ?? false
        error = try container.decodeIfPresent(String.self, forKey: .error)
        usageSourceReason = try container.decodeIfPresent(UsageSourceStatus.self, forKey: .usageSource)?.reason
        // Additive fields: absent from an older service, and never fatal.
        label = (try? container.decodeIfPresent(String.self, forKey: .label)).flatMap { $0 }
        accountStatus = (try? container.decodeIfPresent(String.self, forKey: .accountStatus)).flatMap { $0 }
        latestDecision = (try? container.decodeIfPresent(LatestDecision.self, forKey: .latestDecision))?.flatMap { $0 }?.decision
    }

    public func encode(to encoder: Encoder) throws {
        var container = encoder.container(keyedBy: CodingKeys.self)
        try container.encode(id, forKey: .id)
        try container.encode(provider, forKey: .provider)
        try container.encode(paused, forKey: .paused)
        try container.encodeIfPresent(snapshot, forKey: .snapshot)
        try container.encode(snapshotStale, forKey: .snapshotStale)
        try container.encodeIfPresent(error, forKey: .error)
        if let usageSourceReason {
            try container.encode(UsageSourceStatus(reason: usageSourceReason), forKey: .usageSource)
        }
        try container.encodeIfPresent(label, forKey: .label)
        try container.encodeIfPresent(accountStatus, forKey: .accountStatus)
        if let latestDecision {
            try container.encode(LatestDecision(decision: latestDecision), forKey: .latestDecision)
        }
    }

    /// The provider's local login is gone; only signing in again fixes it.
    public var signedOut: Bool { usageSourceReason == "signed_out" }

    /// The Cursor CLI and the Cursor app are signed in to different
    /// accounts, so Cursor is not scheduled until they match.
    public var accountMismatch: Bool { usageSourceReason == "account_mismatch" }

    /// The line shown on a stale provider's card. A signed-out provider says
    /// so and names the fix, rather than leaving it in a hover tooltip.
    public var staleSummary: String {
        if accountMismatch {
            return "Cursor CLI and app accounts differ · sign both in to the same account"
        }
        if signedOut, let login = ProviderRecovery.loginCommand(for: provider) {
            // The CLI that holds the login, which is what signed out.
            let tool = switch provider.lowercased() {
            case "claude": "Claude Code"
            case "cursor": "Cursor CLI"
            default: displayName
            }
            return "\(tool) is signed out · run \(login)"
        }
        return "Last usage sample is stale"
    }

    public var displayName: String {
        switch provider.lowercased() {
        case "claude": "Claude"
        case "codex": "Codex"
        case "cursor": "Cursor"
        default: provider.capitalized
        }
    }

    /// "Monthly" for Cursor's subscription window, "Weekly" otherwise.
    public var accountWindowLabel: String { snapshot?.weekly?.isMonthly == true ? "Monthly" : "Weekly" }
    public var weeklyPercent: Int? { snapshotStale ? nil : snapshot?.weekly.map(Self.percent) }
    /// Fresh numbers whose source reports no weekly limit.
    public var weeklyNotReported: Bool { !snapshotStale && snapshot?.weeklyUnavailable == true }
    public var shortPercent: Int? {
        if snapshotStale { return nil }
        if let short = snapshot?.short { return Self.percent(short) }
        // An unstarted window is full; it just has no reset yet.
        return snapshot?.shortWindowNotStarted == true ? 100 : nil
    }

    /// The popover's line for an untouched five hour window, which has no
    /// reset to count down to. Nil otherwise: a started window is described
    /// with its reset by the caller, and a provider with no such window has
    /// nothing to say here.
    public var shortWindowSummary: String? {
        guard !snapshotStale, snapshot?.short == nil, snapshot?.shortWindowNotStarted == true else { return nil }
        return "5h 100% · starts on first use"
    }
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
    /// On-demand quota resets the account holds. Nil means not reported,
    /// which is different from zero.
    public let bankedResets: Int?
    /// When the soonest-expiring banked reset lapses, if known.
    public let bankedResetsExpireAt: String?
    /// The five hour window exists and is entirely unused. The provider starts
    /// its clock on first use, so there is no reset time yet -- the window is
    /// full, and `short` stays nil rather than carrying an invented reset.
    public let shortWindowNotStarted: Bool
    /// The subscription plan as the source names it ("Max 20x"). A label
    /// only: plans are never compared or converted into capacity.
    public let plan: String?
    /// Every banked reset's own deadline, soonest first when known.
    public let bankedResetsExpiries: [String]
    /// The usage source itself says these numbers are out of date.
    public let sourceStale: Bool
    /// When these numbers were read.
    public let observedAt: String?
    /// The source reports no account-wide weekly limit for this account
    /// (OpenUsage Team seats). `weekly` is nil then; nothing stands in for it.
    public let weeklyUnavailable: Bool

    public init(
        short: UsageWindow?,
        weekly: UsageWindow?,
        allowances: [AllowanceSummary],
        source: String?,
        bankedResets: Int? = nil,
        bankedResetsExpireAt: String? = nil,
        shortWindowNotStarted: Bool = false,
        plan: String? = nil,
        bankedResetsExpiries: [String] = [],
        sourceStale: Bool = false,
        observedAt: String? = nil,
        weeklyUnavailable: Bool = false
    ) {
        self.weeklyUnavailable = weeklyUnavailable
        self.plan = plan
        self.bankedResetsExpiries = bankedResetsExpiries
        self.sourceStale = sourceStale
        self.observedAt = observedAt
        self.short = short
        self.weekly = weekly
        self.allowances = allowances
        self.source = source
        self.bankedResets = bankedResets
        self.bankedResetsExpireAt = bankedResetsExpireAt
        self.shortWindowNotStarted = shortWindowNotStarted
    }

    enum CodingKeys: String, CodingKey {
        case short, weekly, allowances, source
        case bankedResets = "banked_resets"
        case bankedResetsExpireAt = "banked_resets_expire_at"
        case shortWindowNotStarted = "short_window_not_started"
        case plan
        case bankedResetsExpiries = "banked_resets_expiries"
        case sourceStale = "source_stale"
        case observedAt = "observed_at"
        case weeklyUnavailable = "weekly_unavailable"
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        short = try container.decodeIfPresent(UsageWindow.self, forKey: .short)
        allowances = try container.decodeIfPresent([AllowanceSummary].self, forKey: .allowances) ?? []
        source = try container.decodeIfPresent(String.self, forKey: .source)
        bankedResets = try? container.decodeIfPresent(Int.self, forKey: .bankedResets)
        bankedResetsExpireAt = try? container.decodeIfPresent(String.self, forKey: .bankedResetsExpireAt)
        // Absent from an older desktop, which never sent the state.
        shortWindowNotStarted = (try? container.decodeIfPresent(Bool.self, forKey: .shortWindowNotStarted)) ?? false
        plan = (try? container.decodeIfPresent(String.self, forKey: .plan)).flatMap { $0 }
        bankedResetsExpiries = ((try? container.decodeIfPresent([String].self, forKey: .bankedResetsExpiries)) ?? nil) ?? []
        sourceStale = (try? container.decodeIfPresent(Bool.self, forKey: .sourceStale)) ?? false
        observedAt = (try? container.decodeIfPresent(String.self, forKey: .observedAt)).flatMap { $0 }
        weeklyUnavailable = (try? container.decodeIfPresent(Bool.self, forKey: .weeklyUnavailable)) ?? false
        // Never trust a weekly value beside "not reported".
        weekly = weeklyUnavailable ? nil : try container.decodeIfPresent(UsageWindow.self, forKey: .weekly)
    }
}

public struct UsageWindow: Codable, Sendable {
    public let remaining: Double
    public let resetsAt: String?
    /// Length of the allowance period in seconds; nil means the usual week.
    /// Cursor's subscription window is monthly.
    public let periodSeconds: Int?

    enum CodingKeys: String, CodingKey {
        case remaining
        case resetsAt = "resets_at"
        case periodSeconds = "period_seconds"
    }

    public init(remaining: Double, resetsAt: String? = nil, periodSeconds: Int? = nil) {
        self.remaining = remaining
        self.resetsAt = resetsAt
        self.periodSeconds = periodSeconds
    }

    /// True when the window is longer than a week (Cursor's monthly pools).
    public var isMonthly: Bool { (periodSeconds ?? 0) > 8 * 86400 }
}

public struct AllowanceSummary: Codable, Sendable {
    public let key: String
    public let sourceLabel: String
    public let remaining: Double
    /// When this window resets. Model windows keep their own reset, which
    /// can differ from the account's weekly reset (or stand without one).
    public let resetsAt: String?

    public init(key: String, sourceLabel: String, remaining: Double, resetsAt: String? = nil) {
        self.key = key
        self.sourceLabel = sourceLabel
        self.remaining = remaining
        self.resetsAt = resetsAt
    }

    enum CodingKeys: String, CodingKey {
        case key, remaining
        case sourceLabel = "source_label"
        case resetsAt = "resets_at"
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
        // Several Claude accounts share one badge showing the account count,
        // never a blended percentage: accounts' allowances are separate and
        // are not averaged or summed. Other providers keep one badge per
        // account, as their popover keeps one card per account.
        var families: [(key: String, provider: String, accounts: [ProviderSummary])] = []
        for provider in snapshot.providers {
            let family = provider.provider.lowercased()
            let key = family == "claude" ? family : "\(family)#\(provider.id)"
            if let index = families.firstIndex(where: { $0.key == key }) {
                families[index].accounts.append(provider)
            } else {
                families.append((key, family, [provider]))
            }
        }
        // A stable sort: same-rank badges keep the service's order.
        providerBadges = families.enumerated()
            .sorted { left, right in
                let leftRank = Self.providerRank(left.element.provider)
                let rightRank = Self.providerRank(right.element.provider)
                if leftRank != rightRank { return leftRank < rightRank }
                let byName = left.element.accounts[0].displayName.localizedCaseInsensitiveCompare(right.element.accounts[0].displayName)
                if byName != .orderedSame { return byName == .orderedAscending }
                return left.offset < right.offset
            }
            .map(\.element)
            .map { family in
                let first = family.accounts[0]
                return ProviderBadge(
                    provider: first.provider,
                    displayName: first.displayName,
                    percent: family.accounts.count == 1 ? first.weeklyPercent : nil,
                    accountCount: family.accounts.count,
                    accounts: family.accounts.count == 1 ? [] : family.accounts.map {
                        ProviderBadge.Account(
                            name: $0.label.map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }.flatMap { $0.isEmpty ? nil : $0 } ?? $0.id,
                            weeklyPercent: $0.weeklyPercent,
                            weeklyNotReported: $0.weeklyNotReported
                        )
                    },
                    weeklyNotReported: family.accounts.count == 1 && first.weeklyNotReported
                )
            }
        let providers = providerBadges.map(\.spoken)
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
    public struct Account: Sendable, Equatable {
        public let name: String
        public let weeklyPercent: Int?
        /// The source reports no weekly limit (Team seats): said as "weekly
        /// not reported", never as an unknown "—".
        public let weeklyNotReported: Bool

        public init(name: String, weeklyPercent: Int?, weeklyNotReported: Bool = false) {
            self.name = name
            self.weeklyPercent = weeklyPercent
            self.weeklyNotReported = weeklyNotReported
        }

        var spokenWeekly: String {
            if let weeklyPercent { return "\(weeklyPercent)%" }
            return weeklyNotReported ? "weekly not reported" : "—"
        }
    }

    public let provider: String
    public let displayName: String
    /// The single account's weekly percent; nil for several accounts or
    /// when unknown.
    public let percent: Int?
    public let accountCount: Int
    /// Each account's own weekly figure when there are several.
    public let accounts: [Account]
    /// A single account whose source reports no weekly limit.
    public let weeklyNotReported: Bool

    public init(provider: String, displayName: String, percent: Int?, accountCount: Int = 1, accounts: [Account] = [], weeklyNotReported: Bool = false) {
        self.provider = provider
        self.displayName = displayName
        self.percent = percent
        self.accountCount = accountCount
        self.accounts = accounts
        self.weeklyNotReported = weeklyNotReported
    }

    /// What the menu bar shows beside the provider's icon: "41%", "—", or
    /// "×3" for several accounts.
    public var compact: String {
        if accountCount > 1 { return "×\(accountCount)" }
        if let percent { return "\(percent)%" }
        return weeklyNotReported ? "n/r" : "—"
    }

    /// "Codex 32%", "Claude 3 accounts: Personal 41%, Client 58%, Side —".
    public var spoken: String {
        if accountCount > 1 {
            let each = accounts.map { "\($0.name) \($0.spokenWeekly)" }
            return "\(displayName) \(accountCount) accounts: " + each.joined(separator: ", ")
        }
        return weeklyNotReported && percent == nil ? "\(displayName) weekly not reported" : "\(displayName) \(compact)"
    }
}
