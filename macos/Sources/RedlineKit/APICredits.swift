import Foundation

/// The dollar detail of an OpenAI API credits account (`snapshot.credits`).
/// Promotional credit (from a ChatGPT subscription) is what Redline spends;
/// paid credit is shown and never scheduled.
public struct APICredits: Codable, Sendable, Equatable {
    public let promoRemaining: Double
    public let promoGranted: Double
    public let nextExpiring: Double?
    public let effectiveAt: String?
    /// When the soonest promotional grant lapses; nil when there is none.
    public let expiresAt: String?
    public let paidAvailable: Double
    /// No job starts at or below this promotional balance.
    public let minPromoBalance: Double?
    /// "firesale" or "even".
    public let pacing: String?
    public let firesaleWindowSeconds: Int?
    /// Grants with money left, soonest expiry first.
    public let grants: [APICreditGrant]

    enum CodingKeys: String, CodingKey {
        case promoRemaining = "promo_remaining"
        case promoGranted = "promo_granted"
        case nextExpiring = "next_expiring"
        case effectiveAt = "effective_at"
        case expiresAt = "expires_at"
        case paidAvailable = "paid_available"
        case minPromoBalance = "min_promo_balance"
        case pacing
        case firesaleWindowSeconds = "firesale_window_seconds"
        case grants
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        // The balance is the one field the card cannot do without.
        promoRemaining = try container.decode(Double.self, forKey: .promoRemaining)
        promoGranted = ((try? container.decodeIfPresent(Double.self, forKey: .promoGranted)) ?? nil) ?? 0
        nextExpiring = (try? container.decodeIfPresent(Double.self, forKey: .nextExpiring)) ?? nil
        effectiveAt = (try? container.decodeIfPresent(String.self, forKey: .effectiveAt)) ?? nil
        expiresAt = (try? container.decodeIfPresent(String.self, forKey: .expiresAt)) ?? nil
        paidAvailable = ((try? container.decodeIfPresent(Double.self, forKey: .paidAvailable)) ?? nil) ?? 0
        minPromoBalance = (try? container.decodeIfPresent(Double.self, forKey: .minPromoBalance)) ?? nil
        pacing = (try? container.decodeIfPresent(String.self, forKey: .pacing)) ?? nil
        firesaleWindowSeconds = (try? container.decodeIfPresent(Int.self, forKey: .firesaleWindowSeconds)) ?? nil
        // A malformed grant drops itself, not the balance.
        var kept: [APICreditGrant] = []
        if var list = try? container.nestedUnkeyedContainer(forKey: .grants) {
            while !list.isAtEnd {
                if let grant = try? list.decode(APICreditGrant.self) {
                    kept.append(grant)
                } else if (try? list.decode(SkippedElement.self)) == nil {
                    break
                }
            }
        }
        grants = kept
    }

    private struct SkippedElement: Decodable {}
}

public struct APICreditGrant: Codable, Sendable, Equatable {
    /// "promo", "auto_recharge", "purchase" or "other".
    public let kind: String
    public let amount: Double
    public let remaining: Double
    public let effectiveAt: String?
    public let expiresAt: String?

    enum CodingKeys: String, CodingKey {
        case kind, amount, remaining
        case effectiveAt = "effective_at"
        case expiresAt = "expires_at"
    }

    public var kindLabel: String {
        switch kind {
        case "promo": "ChatGPT subscription grant"
        case "auto_recharge": "Auto recharge"
        case "purchase": "Purchased credit"
        default: "Credit grant"
        }
    }
}

/// Everything the native tray and popover say about one OpenAI API credits
/// account. Dollars, never a weekly, monthly or 5-hour window.
public struct APICreditsCard: Sendable, Equatable {
    /// Normal is healthy (green); muted is a last-known, stale balance.
    public enum Tone: Sendable, Equatable { case normal, warning, danger, muted }

    public struct Grant: Sendable, Equatable, Hashable {
        public let label: String
        /// "$62.40 of $100.00".
        public let value: String
        /// "expires Fri, Nov 27"; nil when the grant does not expire.
        public let expires: String?

        public init(label: String, value: String, expires: String?) {
            self.label = label
            self.value = value
            self.expires = expires
        }
    }

    public let id: String
    /// "OpenAI API credits".
    public let title: String
    /// The owner's label; nil when unset.
    public let label: String?
    public let status: AccountTrayStatus
    public let paused: Bool
    /// "$62.40 left", "$0.00 promo", "Last known $62.40", "Session key needed", or "—".
    public let headline: String
    /// "Expires in 2 days · Fri, Nov 27", "No promotional credit",
    /// "Last sample 2 hrs ago", or the fix.
    public let secondary: String
    /// 0–1 of the promotional grant left (last known when stale); nil when
    /// there is no number, as when signed out.
    public let progress: Double?
    public let tone: Tone
    /// "Run now · firesale", "Held · at the floor", …: the same words as the
    /// web dashboards. Nil when signed out or stale, where the last decision
    /// was about a balance that is no longer current.
    public let decision: String?
    /// The scheduler's reason, first letter capitalised; nil with no decision.
    public let decisionReason: String?
    /// "$62.40 of $100.00 left"; nil without promotional credit.
    public let promoLine: String?
    /// "Jobs start above $5.00 · Firesale: runs within 3 days of expiry".
    public let rule: String?
    /// "$7.25 · shown only, never scheduled"; nil when there is none.
    public let paid: String?
    public let grants: [Grant]
    /// The menu bar's value beside the "$" glyph: "$62.40", or "—" without a
    /// current balance.
    public let compact: String

    /// The card needs a look: out of date, signed out, or no usage.
    public var attention: Bool { status.needsAttention }

    public init?(account: ProviderSummary, now: Date = Date(), calendar: Calendar = .current, locale: Locale = .current) {
        guard account.isAPICredits else { return nil }
        id = account.id
        title = account.displayName
        let trimmed = account.label?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        label = trimmed.isEmpty ? nil : trimmed
        // A snapshot the service last called current stops being current
        // once its promotional grant has expired, for example while a
        // refresh keeps failing: show it as last known, never as money left.
        let reported = AccountTrayStatus(account)
        let promoExpired = TrayTime.parse(account.snapshot?.credits?.expiresAt).map { $0 <= now } ?? false
        status = reported == .ok && promoExpired && (account.snapshot?.credits?.promoRemaining ?? 0) > 0 ? .stale : reported
        paused = account.paused
        let money = { (value: Double) in CursorUsageFormat.money(value, locale: locale) }
        let dates = CreditDates(now: now, calendar: calendar, locale: locale)

        let credits = status == .signedOut ? nil : account.snapshot?.credits
        let outOfDate = status != .ok
        let reason = account.latestDecisionReason.flatMap { $0.isEmpty ? nil : $0 }
        if outOfDate || account.latestDecision == nil {
            decision = nil
            decisionReason = nil
        } else {
            decision = Self.decisionLabel(account.latestDecision, tier: account.latestDecisionTier, reason: reason)
            decisionReason = reason.map { $0.prefix(1).uppercased() + $0.dropFirst() }
        }
        let noPromo = !outOfDate && credits != nil && account.snapshot?.weeklyUnavailable == true
        let expiry = TrayTime.parse(credits?.expiresAt)

        switch status {
        case .signedOut:
            headline = "Session key needed"
            secondary = "Run redline account set-session-key \(account.id)"
        case .stale:
            headline = credits.map { "Last known \(money($0.promoRemaining))" } ?? "—"
            secondary = TrayTime.parse(account.snapshot?.observedAt).map { "Last sample \(dates.ago($0))" } ?? "Out of date"
        case .error, .noData:
            headline = "—"
            secondary = status.word
        case .ok:
            if let credits, noPromo {
                headline = "\(money(credits.promoRemaining)) promo"
                secondary = "No promotional credit"
            } else if let credits {
                headline = "\(money(credits.promoRemaining)) left"
                secondary = expiry.map { "Expires \(dates.relative($0)) · \(dates.day($0))" } ?? "Expiry not reported"
            } else {
                headline = "—"
                secondary = "Credit balance not reported"
            }
        }

        compact = !outOfDate ? credits.map { money($0.promoRemaining) } ?? "—" : "—"

        if noPromo {
            progress = 0
        } else if status == .ok || status == .stale, let credits, credits.promoGranted > 0 {
            progress = min(max(credits.promoRemaining / credits.promoGranted, 0), 1)
        } else {
            progress = nil
        }
        tone = switch progress {
        case _ where status == .stale: .muted
        case let value? where value < 0.15: .danger
        case let value? where value < 0.35: .warning
        default: .normal
        }

        let lastKnown = outOfDate ? "Last known " : ""
        promoLine = credits.flatMap { credits in
            noPromo || credits.promoGranted <= 0 ? nil
                : "\(lastKnown)\(money(credits.promoRemaining)) of \(money(credits.promoGranted)) left"
        }
        rule = credits.flatMap { credits in
            var parts: [String] = []
            if let floor = credits.minPromoBalance { parts.append("Jobs start above \(money(floor))") }
            switch credits.pacing {
            case "even": parts.append("Even pacing across the grant")
            case "firesale":
                parts.append(credits.firesaleWindowSeconds.map { "Firesale: runs within \(Self.span($0)) of expiry" }
                    ?? "Firesale: runs before expiry")
            default: break
            }
            return parts.joined(separator: " · ").nilIfEmpty
        }
        paid = credits.flatMap { $0.paidAvailable > 0 ? "\(lastKnown)\(money($0.paidAvailable)) · shown only, never scheduled" : nil }
        grants = (credits?.grants ?? []).map { grant in
            Grant(
                label: grant.kindLabel,
                value: "\(money(grant.remaining)) of \(money(grant.amount))",
                expires: TrayTime.parse(grant.expiresAt).map { "expires \(dates.date($0))" }
            )
        }
    }

    /// The credits decision in the web dashboards' words.
    static func decisionLabel(_ decision: String?, tier: String?, reason: String?) -> String {
        switch decision?.uppercased() {
        case "RUN", "ADMIT": return tier == "firesale" ? "Run now · firesale" : "Run now"
        case "UNKNOWN": return "Dispatch held"
        default:
            let reason = reason ?? ""
            if reason.contains("in flight") { return "Held · run in flight" }
            if reason.contains("floor") { return "Held · at the floor" }
            if reason.contains("no promotional") { return "Held · no promotional credit" }
            return "Holding credit"
        }
    }

    /// "3 days", "1 day", "36 hours".
    private static func span(_ seconds: Int) -> String {
        if seconds >= 86400 && seconds % 86400 == 0 {
            let days = seconds / 86400
            return days == 1 ? "1 day" : "\(days) days"
        }
        let hours = max(1, Int((Double(seconds) / 3600).rounded()))
        return hours == 1 ? "1 hour" : "\(hours) hours"
    }

}

/// Expiry wording for credits, with the clock, calendar and locale injected.
private struct CreditDates {
    let now: Date
    let calendar: Calendar
    let locale: Locale

    /// "in 2 days", "in 5 hours", "in 40 minutes", or "now" once lapsed.
    func relative(_ date: Date) -> String {
        let seconds = date.timeIntervalSince(now)
        if seconds <= 0 { return "now" }
        let days = Int(seconds / 86400)
        if days >= 1 { return days == 1 ? "in 1 day" : "in \(days) days" }
        let hours = Int(seconds / 3600)
        if hours >= 1 { return hours == 1 ? "in 1 hour" : "in \(hours) hours" }
        let minutes = max(1, Int((seconds / 60).rounded(.up)))
        return minutes == 1 ? "in 1 minute" : "in \(minutes) minutes"
    }

    /// "Fri, Nov 27".
    func day(_ date: Date) -> String { format(date, template: "EEEMMMd") }

    /// "Fri, Nov 27", with the year when it is not this year.
    func date(_ date: Date) -> String {
        let sameYear = calendar.component(.year, from: date) == calendar.component(.year, from: now)
        return format(date, template: sameYear ? "EEEMMMd" : "EEEMMMdyyyy")
    }

    /// "2 hrs ago", "5 mins ago", "1 day ago": the web dashboards' wording.
    func ago(_ date: Date) -> String {
        let seconds = max(0, Int(now.timeIntervalSince(date)))
        let (amount, unit): (Int, String) =
            seconds < 60 ? (seconds, "sec")
            : seconds < 3600 ? (Int((Double(seconds) / 60).rounded()), "min")
            : seconds < 86400 ? (Int((Double(seconds) / 3600).rounded()), "hr")
            : (Int((Double(seconds) / 86400).rounded()), "day")
        return "\(amount) \(unit)\(amount == 1 ? "" : "s") ago"
    }

    private func format(_ date: Date, template: String) -> String {
        let formatter = DateFormatter()
        formatter.locale = locale
        formatter.calendar = calendar
        formatter.timeZone = calendar.timeZone
        formatter.setLocalizedDateFormatFromTemplate(template)
        return formatter.string(from: date)
    }
}

private extension String {
    var nilIfEmpty: String? { isEmpty ? nil : self }
}
