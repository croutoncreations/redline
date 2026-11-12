import Foundation
import RedlineKit
import Testing
@testable import RedlineMenuBar

/// The menu-bar relay status line is a pure function of `RelayStatus`, so
/// every state is pinned here without a window, a client, or the real clock.
@MainActor
struct RelayStatusLineTests {

    private func status(
        state: RelayState,
        mode: RelayMode = .hosted,
        connection: RelayConnection = .connected,
        renewsAt: String? = nil,
        expiresAt: String? = nil
    ) -> RelayStatus {
        RelayStatus(state: state, mode: mode, connection: connection, renewsAt: renewsAt, expiresAt: expiresAt)
    }

    @Test("active names the short renewal date")
    func activeShowsRenewalDate() {
        let line = RelayStatusLine.line(for: status(state: .active, renewsAt: "2026-09-18T00:00:00Z"))
        #expect(line == "Relayed · renews Sep 18")
    }

    @Test("active with an unparseable renewal date omits the date fragment rather than crashing or showing garbage")
    func activeToleratesBadDate() {
        let line = RelayStatusLine.line(for: status(state: .active, renewsAt: "not-a-date"))
        #expect(line == "Relayed")
        #expect(!line.contains("not-a-date"))
    }

    @Test("active with no renewal date at all omits the date fragment")
    func activeToleratesMissingDate() {
        let line = RelayStatusLine.line(for: status(state: .active, renewsAt: nil))
        #expect(line == "Relayed")
    }

    @Test("renew_pending names the short expiry date")
    func renewPendingShowsExpiryDate() {
        let line = RelayStatusLine.line(for: status(state: .renewPending, connection: .connecting, expiresAt: "2026-09-20T00:00:00Z"))
        #expect(line == "Relay renew pending · expires Sep 20")
    }

    @Test("renew_pending with an unparseable expiry omits the date fragment")
    func renewPendingToleratesBadDate() {
        let line = RelayStatusLine.line(for: status(state: .renewPending, connection: .connecting, expiresAt: "garbage"))
        #expect(line == "Relay renew pending")
        #expect(!line.contains("garbage"))
    }

    @Test("unavailable")
    func unavailable() {
        #expect(RelayStatusLine.line(for: status(state: .unavailable, connection: .disconnected)) == "Relay unavailable · could not renew")
    }

    @Test("lapsed")
    func lapsed() {
        #expect(RelayStatusLine.line(for: status(state: .lapsed, connection: .disconnected)) == "Relay subscription lapsed")
    }

    @Test("no_seat")
    func noSeat() {
        #expect(RelayStatusLine.line(for: status(state: .noSeat, connection: .disconnected)) == "Relay: no seat available")
    }

    @Test("self_hosted")
    func selfHosted() {
        #expect(RelayStatusLine.line(for: status(state: .selfHosted, mode: .selfHosted, connection: .connected)) == "Relay: self-hosted")
    }

    @Test("off")
    func off() {
        #expect(RelayStatusLine.line(for: status(state: .off, mode: .off, connection: .disconnected)) == "Relay off")
    }

    /// This exact string was not specified verbatim by the phase-3 spec text;
    /// it is this implementation's own choice, made to match the tone of the
    /// other seven. Flagged in the final report for the integrating session.
    @Test("needs_license (string chosen to match the tone of the specified strings; not verbatim from spec)")
    func needsLicense() {
        #expect(RelayStatusLine.line(for: status(state: .needsLicense, connection: .disconnected)) == "Relay: license required")
    }

    /// Same note as `needsLicense`: chosen, not quoted from the spec text.
    @Test("invalid_key (string chosen to match the tone of the specified strings; not verbatim from spec)")
    func invalidKey() {
        #expect(RelayStatusLine.line(for: status(state: .invalidKey, connection: .disconnected)) == "Relay: invalid key")
    }

    @Test("shortDate formats an ISO 8601 timestamp as a short calendar date")
    func shortDateFormatsKnownDate() {
        #expect(RelayStatusLine.shortDate("2026-09-13T12:00:00Z") == "Sep 13")
    }

    @Test("shortDate returns nil, not garbage, on an unparseable timestamp")
    func shortDateFailsGracefully() {
        #expect(RelayStatusLine.shortDate("not-a-timestamp") == nil)
    }

    @Test("relativeFirstSeen returns nil, not garbage, on an unparseable timestamp")
    func relativeFirstSeenFailsGracefully() {
        #expect(RelayStatusLine.relativeFirstSeen("not-a-timestamp") == nil)
    }

    @Test("relativeFirstSeen describes a timestamp days in the past")
    func relativeFirstSeenDescribesPastDate() {
        let past = ISO8601DateFormatter().string(from: Date().addingTimeInterval(-3 * 86400))
        let phrase = RelayStatusLine.relativeFirstSeen(past)
        #expect(phrase != nil)
        #expect(phrase?.contains("ago") == true)
    }

    /// `RelayStatus` never carries a raw license key -- only the closed,
    /// non-secret fields `RedlineKit`'s doc comments describe -- so no input
    /// to `line(for:)` can smuggle one through. Every state is exercised
    /// against a sentinel to make that explicit rather than assumed.
    @Test("no license key sentinel can appear in any status line, because the function never takes one")
    func neverLeaksALicenseKey() {
        let sentinel = "rl_live_SENTINEL_SECRET_VALUE"
        for state in [
            RelayState.off, .selfHosted, .needsLicense, .active,
            .renewPending, .unavailable, .lapsed, .noSeat, .invalidKey,
        ] {
            let line = RelayStatusLine.line(for: status(state: state, renewsAt: sentinel, expiresAt: sentinel))
            #expect(!line.contains(sentinel))
        }
    }
}
