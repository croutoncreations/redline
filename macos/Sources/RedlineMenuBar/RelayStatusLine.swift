import Foundation
import RedlineKit

/// The one line the menu bar shows for relay status, and the small date
/// helpers it shares with the Pair a Device window's inline result messages.
///
/// Pure and stateless throughout, following `pairedDescription`'s pattern in
/// `PairDeviceWindowController.swift`: every function here takes only the
/// closed, non-secret `RelayStatus` (or a timestamp string already inside
/// one) and returns a string, so the mapping is testable without a window, a
/// client, or a clock.
enum RelayStatusLine {
    /// `active -> "Relayed · renews {short date}"`, `renew_pending ->
    /// "Relay renew pending · expires {short date}"`, and so on for every
    /// `RelayState`.
    ///
    /// Two strings here were not specified verbatim by the phase-3 spec text
    /// and are this file's own choice, made to match the tone of the six
    /// that were: `needsLicense -> "Relay: license required"` and
    /// `invalidKey -> "Relay: invalid key"`.
    static func line(for status: RelayStatus) -> String {
        switch status.state {
        case .active:
            if let date = status.renewsAt.flatMap(shortDate) {
                return "Relayed · renews \(date)"
            }
            return "Relayed"
        case .renewPending:
            if let date = status.expiresAt.flatMap(shortDate) {
                return "Relay renew pending · expires \(date)"
            }
            return "Relay renew pending"
        case .unavailable:
            return "Relay unavailable · could not renew"
        case .lapsed:
            return "Relay subscription lapsed"
        case .noSeat:
            return "Relay: no seat available"
        case .selfHosted:
            return "Relay: self-hosted"
        case .off:
            return "Relay off"
        case .needsLicense:
            return "Relay: license required"
        case .invalidKey:
            return "Relay: invalid key"
        }
    }

    /// A short calendar date ("Sep 18") from an ISO 8601 timestamp string, or
    /// nil if the string does not parse -- a date fragment is omitted rather
    /// than showing garbage or crashing.
    static func shortDate(_ iso8601: String) -> String? {
        guard let date = parseISO8601(iso8601) else { return nil }
        let formatter = DateFormatter()
        formatter.dateFormat = "MMM d"
        formatter.locale = Locale(identifier: "en_US_POSIX")
        // UTC, not the device's local zone: the service sends a UTC instant,
        // and formatting it in a zone behind UTC can shift a midnight
        // timestamp back a whole calendar day (seen as a real test failure
        // while developing this on a UTC-6 machine).
        formatter.timeZone = TimeZone(identifier: "UTC")
        return formatter.string(from: date)
    }

    /// "first seen 3 days ago" for an activation's `first_seen` timestamp, or
    /// nil if it does not parse.
    static func relativeFirstSeen(_ iso8601: String) -> String? {
        guard let date = parseISO8601(iso8601) else { return nil }
        let seconds = max(0, -date.timeIntervalSinceNow)
        let phrase: String
        if seconds < 3600 {
            phrase = "first seen just now"
        } else if seconds < 86400 {
            let hours = max(1, Int(seconds / 3600))
            phrase = "first seen \(hours)h ago"
        } else {
            let days = max(1, Int(seconds / 86400))
            phrase = "first seen \(days)d ago"
        }
        return phrase
    }

    private static func parseISO8601(_ value: String) -> Date? {
        let fractional = ISO8601DateFormatter()
        fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = fractional.date(from: value) { return date }
        let plain = ISO8601DateFormatter()
        plain.formatOptions = [.withInternetDateTime]
        return plain.date(from: value)
    }
}
