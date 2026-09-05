import Foundation
import RedlineKit
import Testing
@testable import RedlineMenuBar

/// The pairing sheet follows the code it is showing.
///
/// A scanned code is spent -- the token is single-use -- so the sheet must
/// swap it for a confirmation the moment the redeem lands, and must offer a
/// fresh one when the old one runs out unscanned. Both are read from the
/// service's status; the rule that turns a status into a screen is tested
/// here without a window or a clock.
@MainActor
struct PairDeviceModelTests {

    @Test("A pending code stays on screen")
    func pendingKeepsTheCode() {
        #expect(PairDeviceModel.stateAfter(status: .pending, routes: [.direct]) == nil)
    }

    @Test("A redeemed code becomes a confirmation that names the route")
    func redeemedBecomesPaired() throws {
        let next = try #require(PairDeviceModel.stateAfter(status: .redeemed, routes: [.relay]))
        guard case .paired(let routes) = next else {
            Issue.record("expected .paired, got \(next)")
            return
        }
        #expect(routes == [.relay])
    }

    @Test("An expired code offers a fresh one, and is not a failure")
    func expiredOffersANewCode() throws {
        let next = try #require(PairDeviceModel.stateAfter(status: .expired, routes: []))
        guard case .expired = next else {
            Issue.record("expected .expired, got \(next)")
            return
        }
    }

    /// The poll must end on its own. It normally stops when the service says
    /// the code is spent or expired -- but with the service down every poll
    /// fails and is skipped, and the only other exit was the window closing.
    /// A deadline just past the code's own expiry makes the loop provably
    /// finite whatever the service does.
    @Test("Polling gives up a little after the code itself expires")
    func pollingHasADeadline() {
        let expires = Date(timeIntervalSince1970: 1_000)
        let started = expires.addingTimeInterval(-600)
        let margin = PairDeviceModel.redeemMemoryMargin
        #expect(PairDeviceModel.shouldKeepPolling(now: expires.addingTimeInterval(-60), expiresAt: expires, started: started))
        #expect(PairDeviceModel.shouldKeepPolling(now: expires.addingTimeInterval(10), expiresAt: expires, started: started))
        // The edge: still polling one second inside the margin, stopped on it.
        #expect(PairDeviceModel.shouldKeepPolling(now: expires.addingTimeInterval(margin - 1), expiresAt: expires, started: started))
        #expect(!PairDeviceModel.shouldKeepPolling(now: expires.addingTimeInterval(margin), expiresAt: expires, started: started))
    }

    /// The client must not out-wait the service. The service forgets a redeem
    /// a minute after it happens; a client that polled past that would read a
    /// real redeem as expired. The constant is checked against its documented
    /// twin so a change to one without the other fails here.
    @Test("The client margin does not exceed what the service remembers")
    func marginMatchesTheService() {
        // redeemedMemory in internal/api/server.go is one minute.
        #expect(PairDeviceModel.redeemMemoryMargin <= 60)
    }

    /// A service that sent no expiry still gets a bounded poll.
    @Test("Polling is bounded even without an expiry from the service")
    func pollingIsBoundedWithoutAnExpiry() {
        let started = Date(timeIntervalSince1970: 1_000)
        #expect(PairDeviceModel.shouldKeepPolling(now: started.addingTimeInterval(60), expiresAt: nil, started: started))
        #expect(!PairDeviceModel.shouldKeepPolling(now: started.addingTimeInterval(20 * 60), expiresAt: nil, started: started))
    }

    @Test("The confirmation says how the phone will connect")
    func pairedDescriptionNamesTheRoute() {
        #expect(pairedDescription(routes: [.relay]).contains("relay"))
        #expect(pairedDescription(routes: [.direct]).contains("tailnet"))
        let both = pairedDescription(routes: [.direct, .relay])
        #expect(both.contains("tailnet") && both.contains("relay"))
    }
}
