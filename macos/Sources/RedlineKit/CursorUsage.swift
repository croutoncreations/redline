import Foundation

/// Display-only Cursor usage, including Grok Bot's separate weekly allowance.
///
/// Redline cannot dispatch to Cursor or Grok Bot. None of this feeds scheduling,
/// the tray gauge or the menu-bar title; it is shown for reference only.
public struct CursorUsageReport: Codable, Sendable, Equatable {
    public let plan: String?
    public let source: String?
    public let fetchedAt: String?
    public let expiresAt: String?
    public let errors: [String]
    public let refreshError: String?
    public let meters: [CursorMeter]
    public let balances: [CursorBalance]
    public let notReported: [String]

    public init(
        plan: String? = nil,
        source: String? = nil,
        fetchedAt: String? = nil,
        expiresAt: String? = nil,
        errors: [String] = [],
        refreshError: String? = nil,
        meters: [CursorMeter] = [],
        balances: [CursorBalance] = [],
        notReported: [String] = []
    ) {
        self.plan = plan
        self.source = source
        self.fetchedAt = fetchedAt
        self.expiresAt = expiresAt
        self.errors = errors
        self.refreshError = refreshError
        self.meters = meters
        self.balances = balances
        self.notReported = notReported
    }

    enum CodingKeys: String, CodingKey {
        case plan, source, errors, meters, balances
        case fetchedAt = "fetched_at"
        case expiresAt = "expires_at"
        case refreshError = "refresh_error"
        case notReported = "not_reported"
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        plan = try container.decodeIfPresent(String.self, forKey: .plan)
        source = try container.decodeIfPresent(String.self, forKey: .source)
        fetchedAt = try container.decodeIfPresent(String.self, forKey: .fetchedAt)
        expiresAt = try container.decodeIfPresent(String.self, forKey: .expiresAt)
        errors = try container.decodeIfPresent([String].self, forKey: .errors) ?? []
        refreshError = try container.decodeIfPresent(String.self, forKey: .refreshError)
        meters = try container.decodeIfPresent([CursorMeter].self, forKey: .meters) ?? []
        balances = try container.decodeIfPresent([CursorBalance].self, forKey: .balances) ?? []
        notReported = try container.decodeIfPresent([String].self, forKey: .notReported) ?? []
    }

    public enum Freshness: Sendable, Equatable {
        case upToDate, outOfDate, partial, noData

        public var label: String {
            switch self {
            case .upToDate: "Up to date"
            case .outOfDate: "Out of date"
            case .partial: "Partial"
            case .noData: "No data"
            }
        }
    }

    /// How long past `expires_at` a report stays current. Matches the server:
    /// OpenUsage rotates its card a few seconds late, which is not a missed refresh.
    public static let staleGrace: TimeInterval = 120

    /// Judged against `expires_at` on this clock, not the server's stale flag.
    public func isStale(now: Date = Date()) -> Bool {
        guard fetchedAt != nil else { return false }
        guard let expires = CursorUsageFormat.date(expiresAt) else { return true }
        return expires.addingTimeInterval(Self.staleGrace) <= now
    }

    public func freshness(now: Date = Date()) -> Freshness {
        if meters.isEmpty && balances.isEmpty { return .noData }
        if isStale(now: now) { return .outOfDate }
        if !errors.isEmpty { return .partial }
        return .upToDate
    }

    public var sourceLabel: String? {
        switch source {
        case "openusage": "OpenUsage"
        case "cursor": "Cursor (built-in)"
        default: nil
        }
    }

    public func meters(in group: CursorMeter.Group) -> [CursorMeter] {
        meters.filter { $0.group == group.rawValue }
    }
}

public struct CursorMeter: Codable, Sendable, Equatable, Identifiable {
    public enum Group: String, CaseIterable, Sendable {
        case monthly, weekly, onDemand = "on_demand"

        public var heading: String {
            switch self {
            case .monthly: "MONTHLY · BILLING CYCLE"
            case .weekly: "WEEKLY"
            case .onDemand: "ON-DEMAND SPEND"
            }
        }
    }

    public var id: String { key }
    public let key: String
    public let label: String
    public let group: String
    public let unit: String
    public let used: Double
    /// Nil when Cursor gave no denominator; never invented.
    public let limit: Double?
    public let resetsAt: String?

    public init(key: String, label: String, group: String, unit: String, used: Double, limit: Double? = nil, resetsAt: String? = nil) {
        self.key = key
        self.label = label
        self.group = group
        self.unit = unit
        self.used = used
        self.limit = limit
        self.resetsAt = resetsAt
    }

    enum CodingKeys: String, CodingKey {
        case key, label, group, unit, used, limit
        case resetsAt = "resets_at"
    }

    /// A positive limit, or nil. Zero is treated as "no denominator".
    public var boundedLimit: Double? {
        guard let limit, limit > 0 else { return nil }
        return limit
    }

    public var isExhausted: Bool {
        guard let limit = boundedLimit else { return false }
        return used >= limit
    }

    /// What is left, 0...100, or nil when there is no denominator to measure
    /// against. Every provider meter in the app shows what is left, so
    /// Cursor's does too.
    public var remainingPercent: Double? {
        guard let limit = boundedLimit else { return nil }
        return min(max((limit - used) / limit * 100, 0), 100)
    }

    /// "42% left", "Used up", "$15.50 of $20.00 left", or "$4.50 spent" with
    /// no limit (spend has nothing to be left of).
    public var valueText: String {
        guard let limit = boundedLimit else {
            return unit == "usd" ? "\(CursorUsageFormat.money(used)) spent" : "\(CursorUsageFormat.amount(used, unit: unit)) used"
        }
        if isExhausted { return "Used up" }
        if unit == "percent" && limit == 100 { return "\(Int((remainingPercent ?? 0).rounded()))% left" }
        return "\(CursorUsageFormat.amount(limit - used, unit: unit)) of \(CursorUsageFormat.amount(limit, unit: unit)) left"
    }
}

public struct CursorBalance: Codable, Sendable, Equatable, Identifiable {
    public var id: String { key }
    public let key: String
    public let label: String
    public let unit: String
    public let available: Double
    public let expiresAt: [String]

    public init(key: String, label: String, unit: String, available: Double, expiresAt: [String] = []) {
        self.key = key
        self.label = label
        self.unit = unit
        self.available = available
        self.expiresAt = expiresAt
    }

    enum CodingKeys: String, CodingKey {
        case key, label, unit, available
        case expiresAt = "expires_at"
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        key = try container.decode(String.self, forKey: .key)
        label = try container.decode(String.self, forKey: .label)
        unit = try container.decode(String.self, forKey: .unit)
        available = try container.decode(Double.self, forKey: .available)
        expiresAt = try container.decodeIfPresent([String].self, forKey: .expiresAt) ?? []
    }

    public var valueText: String { "\(CursorUsageFormat.amount(available, unit: unit)) available" }

    public func nextExpiry(now: Date = Date()) -> Date? {
        expiresAt.compactMap(CursorUsageFormat.date).filter { $0 > now }.min()
    }
}

public enum CursorUsageFormat {
    public static func date(_ value: String?) -> Date? {
        guard let value else { return nil }
        let fractional = ISO8601DateFormatter()
        fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return fractional.date(from: value) ?? ISO8601DateFormatter().date(from: value)
    }

    public static func money(_ value: Double) -> String {
        let formatter = NumberFormatter()
        formatter.numberStyle = .currency
        formatter.currencyCode = "USD"
        return formatter.string(from: NSNumber(value: value)) ?? String(format: "$%.2f", value)
    }

    public static func amount(_ value: Double, unit: String) -> String {
        switch unit {
        case "usd": return money(value)
        case "percent": return "\(Int(value.rounded()))%"
        default:
            let number = value == value.rounded() ? String(Int(value)) : String(format: "%.1f", value)
            return unit.isEmpty ? number : "\(number) \(unit)"
        }
    }

    /// Absolute reset in the viewer's time zone, with the zone named.
    public static func absolute(_ date: Date, timeZone: TimeZone = .current, locale: Locale = .current) -> String {
        let formatter = DateFormatter()
        formatter.locale = locale
        formatter.timeZone = timeZone
        formatter.setLocalizedDateFormatFromTemplate("EEEMMMdjmmz")
        return formatter.string(from: date)
    }

    public static func relative(_ date: Date, now: Date = Date()) -> String {
        let seconds = date.timeIntervalSince(now)
        let magnitude = abs(seconds)
        let text: String
        if magnitude < 3600 { text = "\(max(1, Int((magnitude / 60).rounded())))m" }
        else if magnitude < 86400 { text = "\(Int((magnitude / 3600).rounded()))h" }
        else { text = "\(Int((magnitude / 86400).rounded()))d" }
        return seconds >= 0 ? "in \(text)" : "\(text) ago"
    }

    /// "Resets in 2d · Sat, Oct 11, 9:22 PM PDT", or the exhausted wording.
    public static func resetLine(for meter: CursorMeter, now: Date = Date(), timeZone: TimeZone = .current, locale: Locale = .current) -> String {
        guard let reset = date(meter.resetsAt) else {
            return meter.boundedLimit == nil ? "No limit reported" : "Reset date not reported"
        }
        let prefix = meter.isExhausted ? "Available again" : "Resets"
        return "\(prefix) \(relative(reset, now: now)) · \(absolute(reset, timeZone: timeZone, locale: locale))"
    }
}
