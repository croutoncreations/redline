import Foundation

/// What the menu-bar popover shows for a provider with several accounts:
/// an overview line, then one compact row per account, one of which may be
/// expanded for details.
///
/// Built only from the dashboard snapshot the popover already fetched.
/// Nothing here talks to the service, and choosing a row is view state: no
/// row, selection or expansion ever triggers a usage refresh, which is the
/// only path that can touch a CLI's stored credentials.
///
/// Rules carried over from the Android carousel:
/// - Each account stands alone. Percentages are never averaged or summed
///   across accounts, and plans are labels, never compared.
/// - The 5-hour and weekly windows are separate columns.
/// - Out-of-date numbers say "was N%" and give the last reported reset as a
///   fixed time, never a live countdown. Missing numbers are "—", not zero.
/// - Order is the service's (sorted by account id), never by usage, so rows
///   do not jump around between refreshes.
public struct AccountTray: Sendable, Equatable {
    public let provider: String
    public let displayName: String
    public let rows: [AccountTrayRow]

    /// "3 accounts · 1 dispatching · 1 needs attention". Counts only.
    public let overview: String

    /// The most rows shown before the list scrolls. At normal text size
    /// all five fit without scrolling.
    public static let visibleRows = 5

    /// The tray for one provider family, or nil when it has fewer than two
    /// accounts (a single account keeps today's card).
    public init?(
        snapshot: DashboardSnapshot,
        provider: String = "claude",
        now: Date = Date(),
        calendar: Calendar = .current,
        locale: Locale = .current
    ) {
        let accounts = snapshot.providers.filter { $0.provider.lowercased() == provider.lowercased() }
        guard accounts.count >= 2 else { return nil }
        self.provider = provider
        self.displayName = accounts[0].displayName
        let rows = accounts.map { AccountTrayRow(account: $0, now: now, calendar: calendar, locale: locale) }
        self.rows = rows
        var parts = ["\(rows.count) accounts"]
        let dispatching = rows.filter { $0.dispatching }.count
        parts.append("\(dispatching) dispatching")
        let attention = rows.filter { $0.status.needsAttention }.count
        if attention > 0 {
            parts.append(attention == 1 ? "1 needs attention" : "\(attention) need attention")
        }
        overview = parts.joined(separator: " · ")
    }

    /// The row to expand: the requested id if it still exists, else none.
    /// An account that disappears from the configuration collapses rather
    /// than silently expanding a different one.
    public func expandedRow(_ selectedID: String?) -> AccountTrayRow? {
        guard let selectedID else { return nil }
        return rows.first { $0.id == selectedID }
    }

    /// Keyboard movement through the rows: up and down, clamped, no wrap.
    public func neighbour(of id: String?, offset: Int) -> String? {
        guard !rows.isEmpty else { return nil }
        guard let id, let index = rows.firstIndex(where: { $0.id == id }) else {
            return offset >= 0 ? rows.first?.id : rows.last?.id
        }
        let target = min(max(index + offset, 0), rows.count - 1)
        return rows[target].id
    }
}

/// One account's collection state, as the service reports it.
public enum AccountTrayStatus: Sendable, Equatable {
    case ok, stale, signedOut, error, noData

    init(_ account: ProviderSummary) {
        switch account.accountStatus {
        case "signed_out": self = .signedOut
        case "error": self = .error
        case "no_data": self = .noData
        case "stale": self = .stale
        case "ok": self = account.snapshotStale ? .stale : .ok
        default:
            // An older service without account_status.
            if account.signedOut { self = .signedOut }
            else if account.snapshot == nil { self = account.error == nil ? .noData : .error }
            else if account.snapshotStale { self = .stale }
            else { self = .ok }
        }
    }

    public var needsAttention: Bool { self != .ok }

    public var word: String {
        switch self {
        case .ok: "Up to date"
        case .stale: "Out of date"
        case .signedOut: "Signed out"
        case .error: "Usage unavailable"
        case .noData: "No usage yet"
        }
    }
}

/// One window's compact cell: "68%" with its reset, or "was 41%" with the
/// last reported reset, or "—".
public struct AccountTrayWindow: Sendable, Equatable {
    /// "68%", "was 41%", or "—".
    public let value: String
    /// "2h 11m", "Tue", "due 10:07 AM", "first use", or "": short enough
    /// for the compact column. Fixed times when out of date, never a countdown.
    public let reset: String
    /// The full sentence for the expanded row.
    public let detail: String
    /// The whole cell for VoiceOver: "5-hour window, 68 percent left, resets in 2 hours 11 minutes".
    public let spoken: String
    /// 0–100 for a bar, nil when there is no current number to draw.
    public let percent: Int?
    public let outOfDate: Bool
}

public struct AccountTrayRow: Sendable, Equatable, Identifiable {
    public let id: String
    /// The owner's label, else the account id.
    public let title: String
    public let plan: String?
    public let status: AccountTrayStatus
    public let paused: Bool
    public let dispatching: Bool
    public let session: AccountTrayWindow
    public let weekly: AccountTrayWindow
    /// "2 · Wed" (count · soonest deadline), "Last known 2 · Wed", or nil
    /// when the account does not report banked resets.
    public let credits: String?
    /// The row's full state: "Dispatching", "Signed out · run claude auth login".
    public let stateLine: String
    /// A word or two for the compact row: "Dispatching", "Out of date".
    public let compactState: String
    /// Everything VoiceOver reads for the row.
    public let spoken: String
    /// Lines for the expanded detail, in reading order.
    public let details: [AccountTrayDetail]

    init(account: ProviderSummary, now: Date, calendar: Calendar, locale: Locale) {
        id = account.id
        let label = account.label?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        title = label.isEmpty ? account.id : label
        plan = account.snapshot?.plan.flatMap { $0.isEmpty ? nil : $0 }
        status = AccountTrayStatus(account)
        paused = account.paused
        let decision = account.latestDecision?.uppercased()
        let current = status == .ok
        dispatching = current && !account.paused && (decision == "RUN" || decision == "ADMIT")
        let time = TrayTime(now: now, calendar: calendar, locale: locale)
        let snapshot = status == .signedOut ? nil : account.snapshot
        let outOfDate = status != .ok

        session = Self.window(
            name: "5-hour window",
            window: snapshot?.short,
            notStarted: snapshot?.shortWindowNotStarted == true && snapshot?.short == nil,
            outOfDate: outOfDate,
            time: time
        )
        let weeklyName = account.accountWindowLabel == "Monthly" ? "Monthly allowance" : "Weekly allowance"
        if snapshot?.weeklyUnavailable == true {
            // The account has no account-wide weekly limit to show. Not a
            // number, not "unknown": the source reports none.
            weekly = AccountTrayWindow(
                value: "n/r", reset: "not reported", detail: "Weekly limit not reported",
                spoken: "\(weeklyName), not reported for this account", percent: nil, outOfDate: outOfDate
            )
        } else {
            weekly = Self.window(
                name: weeklyName,
                window: snapshot?.weekly,
                notStarted: false,
                outOfDate: outOfDate,
                time: time
            )
        }

        let creditInfo = Self.credits(snapshot, outOfDate: outOfDate, time: time)
        credits = creditInfo?.compact

        if account.paused && status == .ok {
            stateLine = "Paused"
        } else {
            switch status {
            case .ok:
                let decided: String = switch decision {
                case "RUN", "ADMIT": "Dispatching"
                case "WAIT": "Waiting"
                case "UNKNOWN": "Dispatch held"
                default: "No scheduler decision yet"
                }
                stateLine = snapshot?.weeklyUnavailable == true && !dispatching ? "Dispatch held · weekly not reported" : decided
            case .stale: stateLine = "Out of date · dispatch held"
            case .signedOut:
                stateLine = ProviderRecovery.loginCommand(for: account.provider).map { "Signed out · run \($0)" } ?? "Signed out"
            case .error: stateLine = "Usage unavailable · dispatch held"
            case .noData: stateLine = "No usage yet · dispatch held"
            }
        }
        // A paused account with a problem still names the problem.
        let pausedPrefix = account.paused && status != .ok ? "Paused · " : ""

        compactState = status != .ok ? pausedPrefix + status.word
            : stateLine == "Dispatch held · weekly not reported" ? "Dispatch held" : stateLine

        var spokenParts = [title]
        if let plan { spokenParts.append("\(plan) plan") }
        spokenParts.append(Self.spoken(pausedPrefix + stateLine))
        spokenParts.append(session.spoken)
        spokenParts.append(weekly.spoken)
        if let spokenCredits = creditInfo?.spoken { spokenParts.append(spokenCredits) }
        spoken = spokenParts.joined(separator: ". ")
        let fullState = pausedPrefix + stateLine

        var details: [AccountTrayDetail] = []
        details.append(AccountTrayDetail(label: "Account", value: account.id))
        if let plan { details.append(AccountTrayDetail(label: "Plan", value: plan)) }
        details.append(AccountTrayDetail(label: "Usage", value: status.word))
        if let observed = TrayTime.parse(account.snapshot?.observedAt), status != .signedOut {
            details.append(AccountTrayDetail(label: "Read", value: time.ago(observed)))
        }
        if let problem = Self.problem(account, status: status) {
            details.append(AccountTrayDetail(label: "Why", value: problem))
        }
        details.append(AccountTrayDetail(label: "5-hour", value: session.detail))
        details.append(AccountTrayDetail(label: account.accountWindowLabel, value: weekly.detail))
        for allowance in (snapshot?.allowances ?? []) where allowance.key.hasPrefix("model:") {
            var value = outOfDate ? "was \(allowance.percent)% left" : "\(allowance.percent)% left"
            // A model window keeps its own reset, which on Team seats differs
            // from seat to seat.
            if let resetsAt = TrayTime.parse(allowance.resetsAt) {
                value += outOfDate ? " · last reported reset \(time.fixed(resetsAt))" : " · resets \(time.fixed(resetsAt))"
            }
            details.append(AccountTrayDetail(label: allowance.displayName, value: value))
        }
        details.append(AccountTrayDetail(label: "Banked resets", value: creditInfo?.full ?? "Not reported"))
        details.append(AccountTrayDetail(label: "Scheduler", value: fullState))
        self.details = details
    }

    /// The middle dot reads aloud; VoiceOver gets commas instead.
    public static func spoken(_ text: String) -> String {
        text.replacingOccurrences(of: " · ", with: ", ")
    }

    private static func window(
        name: String,
        window: UsageWindow?,
        notStarted: Bool,
        outOfDate: Bool,
        time: TrayTime
    ) -> AccountTrayWindow {
        if notStarted {
            let value = outOfDate ? "was 100%" : "100%"
            let reset = outOfDate ? "not started" : "first use"
            let spoken = outOfDate
                ? "\(name), last known: not started"
                : "\(name), 100 percent left, starts on first use"
            let detail = outOfDate ? "was 100% left · not started when last read" : "100% left · starts on first use"
            return AccountTrayWindow(value: value, reset: reset, detail: detail, spoken: spoken, percent: outOfDate ? nil : 100, outOfDate: outOfDate)
        }
        guard let window else {
            return AccountTrayWindow(value: "—", reset: "", detail: "Unknown", spoken: "\(name), unknown", percent: nil, outOfDate: outOfDate)
        }
        let percent = Int((min(max(window.remaining, 0), 1) * 100).rounded())
        let resetsAt = TrayTime.parse(window.resetsAt)
        if outOfDate {
            // Last known, never a countdown: a fixed time, or due.
            let reset: String
            let detail: String
            let spokenReset: String
            if let resetsAt {
                let at = time.fixed(resetsAt)
                let short = time.short(resetsAt)
                if resetsAt > time.now {
                    reset = short
                    detail = "last reported reset \(at)"
                    spokenReset = "last reported reset \(at)"
                } else {
                    reset = "due \(short)"
                    detail = "reset was due \(at) · awaiting fresh numbers"
                    spokenReset = "reset was due \(at), awaiting fresh numbers"
                }
            } else {
                reset = ""
                detail = "reset time unknown"
                spokenReset = "reset time unknown"
            }
            return AccountTrayWindow(
                value: "was \(percent)%",
                reset: reset,
                detail: "was \(percent)% left · \(detail)",
                spoken: "\(name), last known and out of date: was \(percent) percent left, \(spokenReset)",
                percent: nil,
                outOfDate: true
            )
        }
        let reset = resetsAt.map { time.countdown(to: $0) } ?? ""
        let spokenReset = resetsAt.map { ", resets in \(time.spokenCountdown(to: $0))" } ?? ""
        return AccountTrayWindow(
            value: "\(percent)%",
            reset: reset,
            detail: reset.isEmpty ? "\(percent)% left" : "\(percent)% left · resets in \(reset)",
            spoken: "\(name), \(percent) percent left\(spokenReset)",
            percent: percent,
            outOfDate: false
        )
    }

    private static func credits(
        _ snapshot: UsageSnapshot?,
        outOfDate: Bool,
        time: TrayTime
    ) -> (compact: String, spoken: String, full: String)? {
        guard let snapshot, let count = snapshot.bankedResets else { return nil }
        var deadlines = snapshot.bankedResetsExpiries.compactMap(TrayTime.parse)
        if deadlines.isEmpty, let single = TrayTime.parse(snapshot.bankedResetsExpireAt) {
            deadlines = [single]
        }
        deadlines.sort()
        let upcoming = deadlines.filter { $0 > time.now }
        let expired = deadlines.count - upcoming.count
        let prefix = outOfDate ? "Last known " : ""
        let compactPrefix = outOfDate ? "was " : ""
        let noun = count == 1 ? "banked reset" : "banked resets"
        guard count > 0 else {
            return ("\(compactPrefix)0", "\(prefix.lowercased())0 banked resets", "\(prefix)0 available")
        }
        var compact = "\(compactPrefix)\(count)"
        var spoken = "\(prefix.lowercased())\(count) \(noun)"
        var full = "\(prefix)\(count) available"
        if let soonest = upcoming.first {
            let at = time.fixed(soonest)
            compact += " · \(time.short(soonest, dayOnly: true))"
            spoken += ", soonest expires \(at)"
            full += " · soonest expires \(at)"
        }
        if outOfDate && expired > 0 {
            spoken += ", \(expired) since expired"
            full += " · \(expired) since expired"
        }
        if upcoming.count > 1 {
            full += " · then " + upcoming.dropFirst().map { time.fixed($0) }.joined(separator: ", ")
        }
        return (compact, spoken, full)
    }

    private static func problem(_ account: ProviderSummary, status: AccountTrayStatus) -> String? {
        switch status {
        case .ok: return nil
        case .signedOut: return account.staleSummary
        case .stale:
            if account.snapshot?.sourceStale == true { return "OpenUsage says these numbers are out of date" }
            return account.error ?? "The last usage sample is older than Redline accepts"
        case .error, .noData:
            return account.error
        }
    }
}

public struct AccountTrayDetail: Sendable, Equatable, Hashable {
    public let label: String
    public let value: String
}

/// Time words for the tray, with the clock, calendar and locale injected so
/// tests do not depend on the machine.
struct TrayTime: Sendable {
    let now: Date
    let calendar: Calendar
    let locale: Locale

    static func parse(_ timestamp: String?) -> Date? {
        guard let timestamp, !timestamp.isEmpty else { return nil }
        let fractional = ISO8601DateFormatter()
        fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return fractional.date(from: timestamp) ?? ISO8601DateFormatter().date(from: timestamp)
    }

    /// "2h 11m", "3d 3h", "45m", "now".
    func countdown(to date: Date) -> String {
        let seconds = Int(date.timeIntervalSince(now))
        if seconds <= 0 { return "now" }
        let minutes = Int((Double(seconds) / 60).rounded(.up))
        if minutes < 60 { return "\(minutes)m" }
        let hours = minutes / 60
        if hours < 24 {
            let rest = minutes % 60
            return rest == 0 ? "\(hours)h" : "\(hours)h \(rest)m"
        }
        let days = hours / 24
        let restHours = hours % 24
        return restHours == 0 ? "\(days)d" : "\(days)d \(restHours)h"
    }

    func spokenCountdown(to date: Date) -> String {
        let text = countdown(to: date)
        if text == "now" { return "now" }
        return text.split(separator: " ").map { part -> String in
            let number = Int(part.dropLast()) ?? 0
            let unit: String = switch part.last {
            case "d": number == 1 ? "day" : "days"
            case "h": number == 1 ? "hour" : "hours"
            default: number == 1 ? "minute" : "minutes"
            }
            return "\(number) \(unit)"
        }.joined(separator: " ")
    }

    /// A fixed local time: "6:00 AM" today, "Tomorrow 6:00 AM", "Wed 6:00 AM"
    /// within the week, "Oct 20" beyond, with the year when it differs.
    func fixed(_ date: Date) -> String {
        var calendar = calendar
        calendar.locale = locale
        let day = calendar.dateComponents([.day], from: calendar.startOfDay(for: now), to: calendar.startOfDay(for: date)).day ?? 0
        let clock = DateFormatter()
        clock.locale = locale
        clock.calendar = calendar
        clock.timeZone = calendar.timeZone
        clock.setLocalizedDateFormatFromTemplate("jmm")
        // Foundation puts a narrow no-break space before AM/PM; a plain one
        // reads the same and keeps copy, search and tests predictable.
        let time = clock.string(from: date)
            .replacingOccurrences(of: "\u{202F}", with: " ")
            .replacingOccurrences(of: "\u{00A0}", with: " ")
        let formatter = DateFormatter()
        formatter.locale = locale
        formatter.calendar = calendar
        formatter.timeZone = calendar.timeZone
        switch day {
        case 0: return "today \(time)"
        case 1: return "tomorrow \(time)"
        case -1: return "yesterday \(time)"
        case 2...6:
            formatter.setLocalizedDateFormatFromTemplate("EEE")
            return "\(formatter.string(from: date)) \(time)"
        default:
            let sameYear = calendar.component(.year, from: date) == calendar.component(.year, from: now)
            formatter.setLocalizedDateFormatFromTemplate(sameYear ? "MMMd" : "MMMdyyyy")
            return formatter.string(from: date)
        }
    }

    /// The compact form for a narrow column, built from the day offset (not
    /// by parsing `fixed`), so it holds in any locale: the time today, else
    /// the weekday within the week, else the month and day.
    func short(_ date: Date, dayOnly: Bool = false) -> String {
        var calendar = calendar
        calendar.locale = locale
        let day = calendar.dateComponents([.day], from: calendar.startOfDay(for: now), to: calendar.startOfDay(for: date)).day ?? 0
        let formatter = DateFormatter()
        formatter.locale = locale
        formatter.calendar = calendar
        formatter.timeZone = calendar.timeZone
        if day == 0 && !dayOnly {
            formatter.setLocalizedDateFormatFromTemplate("jmm")
        } else if day == 0 {
            return "today"
        } else if (-6...6).contains(day) {
            formatter.setLocalizedDateFormatFromTemplate("EEE")
        } else {
            formatter.setLocalizedDateFormatFromTemplate("MMMd")
        }
        return formatter.string(from: date)
            .replacingOccurrences(of: "\u{202F}", with: " ")
            .replacingOccurrences(of: "\u{00A0}", with: " ")
    }

    func ago(_ date: Date) -> String {
        let seconds = max(0, Int(now.timeIntervalSince(date)))
        if seconds < 60 { return "just now" }
        if seconds < 3600 { return "\(seconds / 60)m ago" }
        if seconds < 86400 { return "\(seconds / 3600)h ago" }
        return "\(seconds / 86400)d ago"
    }
}
