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

    @Test("The confirmation says how the phone will connect")
    func pairedDescriptionNamesTheRoute() {
        #expect(pairedDescription(routes: [.relay]).contains("relay"))
        #expect(pairedDescription(routes: [.direct]).contains("tailnet"))
        let both = pairedDescription(routes: [.direct, .relay])
        #expect(both.contains("tailnet") && both.contains("relay"))
    }
}
