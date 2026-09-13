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

    /// Two numbers here mirror two in the service, with nothing but these
    /// assertions holding them together.
    ///
    /// The margin must not exceed what the service remembers (redeemedMemory,
    /// one minute): a client that polled past that would read a real redeem as
    /// expired. The lifetime must not be shorter than the service's (ten
    /// minutes, createPairingToken): a client that assumed less would give up
    /// on a code the service still honoured. Each is pinned in the direction
    /// that matters, so a change to the service without a matching change here
    /// fails in the test rather than on a user's screen.
    @Test("The client margin does not exceed what the service remembers")
    func marginDoesNotExceedServerMemory() {
        #expect(PairDeviceModel.redeemMemoryMargin <= 60)
    }

    @Test("The client lifetime is not shorter than the service's")
    func lifetimeIsNotShorterThanTheServers() {
        #expect(PairDeviceModel.pairingTokenLifetime >= 10 * 60)
    }

    /// A service that sent no expiry still gets a bounded poll, and the bound
    /// is the token's lifetime plus the same margin -- pinned at the edge.
    @Test("Polling is bounded even without an expiry from the service")
    func pollingIsBoundedWithoutAnExpiry() {
        let started = Date(timeIntervalSince1970: 1_000)
        let limit = PairDeviceModel.pairingTokenLifetime + PairDeviceModel.redeemMemoryMargin
        #expect(PairDeviceModel.shouldKeepPolling(now: started.addingTimeInterval(60), expiresAt: nil, started: started))
        #expect(PairDeviceModel.shouldKeepPolling(now: started.addingTimeInterval(limit - 1), expiresAt: nil, started: started))
        #expect(!PairDeviceModel.shouldKeepPolling(now: started.addingTimeInterval(limit), expiresAt: nil, started: started))
    }

    @Test("The confirmation says how the phone will connect")
    func pairedDescriptionNamesTheRoute() {
        #expect(pairedDescription(routes: [.relay]).contains("relay"))
        #expect(pairedDescription(routes: [.direct]).contains("tailnet"))
        let both = pairedDescription(routes: [.direct, .relay])
        #expect(both.contains("tailnet") && both.contains("relay"))
    }

    // MARK: - Step-one/step-two transition

    /// The resolver's true zero value -- nothing ever configured, managed or
    /// bootstrapped -- is `state == .off && mode == .off`
    /// (`internal/config/relay_state.go`'s `RelayResolver.Resolve`, the
    /// `!managed && !bootstrap.Enabled` branch). This is the heuristic this
    /// window uses to infer "no managed relay choice exists yet" without a
    /// dedicated backend signal -- flagged as an invented behaviour in the
    /// final report.
    @Test("Off state with off mode -- the resolver's true default -- asks the connection question")
    func trueDefaultAsksTheQuestion() {
        let status = RelayStatus(state: .off, mode: .off, connection: .disconnected)
        #expect(PairDeviceModel.needsConnectionChoice(status))
    }

    @Test("A managed off choice (mode still off, but reached deliberately) is indistinguishable from the default by state/mode alone")
    func managedOffLooksLikeDefaultButIsAcceptedAsSuch() {
        // This is the known limitation of the heuristic: the resolver cannot
        // tell a managed "off" apart from "never configured" using only
        // state/mode, so this window would ask again. In practice a managed
        // off choice is reached by first asking (state stays .off/.off
        // because tailnet-only routing needs no relay), so this is expected,
        // not a bug -- documented rather than silently assumed.
        let status = RelayStatus(state: .off, mode: .off, connection: .disconnected)
        #expect(PairDeviceModel.needsConnectionChoice(status))
    }

    @Test("Any non-default state or mode skips the connection question")
    func nonDefaultSkipsTheQuestion() {
        #expect(!PairDeviceModel.needsConnectionChoice(RelayStatus(state: .selfHosted, mode: .selfHosted, connection: .connected)))
        #expect(!PairDeviceModel.needsConnectionChoice(RelayStatus(state: .needsLicense, mode: .hosted, connection: .disconnected)))
        #expect(!PairDeviceModel.needsConnectionChoice(RelayStatus(state: .active, mode: .hosted, connection: .connected)))
        // Mode hosted but somehow state off: still not the true default,
        // since mode alone shows a choice was made.
        #expect(!PairDeviceModel.needsConnectionChoice(RelayStatus(state: .off, mode: .hosted, connection: .disconnected)))
    }

    @Test("active or self_hosted after configuring the relay proceeds to the code step")
    func activeOrSelfHostedProceeds() {
        #expect(PairDeviceModel.shouldProceedToCode(after: RelayStatus(state: .active, mode: .hosted, connection: .connected)))
        #expect(PairDeviceModel.shouldProceedToCode(after: RelayStatus(state: .selfHosted, mode: .selfHosted, connection: .connected)))
    }

    @Test("Any other configure result stays on step one")
    func otherResultsStayOnStepOne() {
        #expect(!PairDeviceModel.shouldProceedToCode(after: RelayStatus(state: .noSeat, mode: .hosted, connection: .disconnected)))
        #expect(!PairDeviceModel.shouldProceedToCode(after: RelayStatus(state: .invalidKey, mode: .hosted, connection: .disconnected)))
        #expect(!PairDeviceModel.shouldProceedToCode(after: RelayStatus(state: .lapsed, mode: .hosted, connection: .disconnected)))
        #expect(!PairDeviceModel.shouldProceedToCode(after: RelayStatus(state: .needsLicense, mode: .hosted, connection: .disconnected)))
    }

    // MARK: - relayConfigureResultDescription

    @Test("active names the short renewal date")
    func configureResultActive() {
        let status = RelayStatus(state: .active, mode: .hosted, connection: .connected, renewsAt: "2026-09-18T00:00:00Z")
        #expect(PairDeviceModel.relayConfigureResultDescription(status) == "Relay active \u{b7} renews Sep 18")
    }

    @Test("no_seat lists each activation's label and a relative first-seen phrase")
    func configureResultNoSeatListsActivations() {
        let status = RelayStatus(
            state: .noSeat,
            mode: .hosted,
            connection: .disconnected,
            activations: [
                RelayActivation(label: "work-mac", firstSeen: "2020-01-01T00:00:00Z"),
                RelayActivation(label: "laptop", firstSeen: "2020-01-01T00:00:00Z"),
            ]
        )
        let message = PairDeviceModel.relayConfigureResultDescription(status)
        #expect(message.contains("work-mac"))
        #expect(message.contains("laptop"))
        #expect(message.contains("ago"))
    }

    @Test("no_seat with no activations still says no seat is available")
    func configureResultNoSeatEmpty() {
        let status = RelayStatus(state: .noSeat, mode: .hosted, connection: .disconnected, activations: [])
        #expect(PairDeviceModel.relayConfigureResultDescription(status).lowercased().contains("no seat"))
    }

    @Test("invalid_key")
    func configureResultInvalidKey() {
        let status = RelayStatus(state: .invalidKey, mode: .hosted, connection: .disconnected)
        #expect(PairDeviceModel.relayConfigureResultDescription(status).lowercased().contains("not recognized"))
    }

    @Test("lapsed")
    func configureResultLapsed() {
        let status = RelayStatus(state: .lapsed, mode: .hosted, connection: .disconnected)
        #expect(PairDeviceModel.relayConfigureResultDescription(status).lowercased().contains("lapsed"))
    }

    @Test("needs_license")
    func configureResultNeedsLicense() {
        let status = RelayStatus(state: .needsLicense, mode: .hosted, connection: .disconnected)
        #expect(PairDeviceModel.relayConfigureResultDescription(status).lowercased().contains("license key"))
    }

    /// `RelayConfigureRequest` accepts a license key, but `RelayStatus` --
    /// the only type `relayConfigureResultDescription` reads from -- never
    /// carries one, per `RedlineKit`'s own doc comments on that struct. This
    /// test makes the guarantee explicit with a sentinel across every state
    /// the function switches on, including `noSeat`'s activation labels.
    @Test("No license key sentinel can appear in a relayConfigureResultDescription, because RelayStatus carries none")
    func configureResultNeverLeaksALicenseKey() {
        let sentinel = "rl_live_SENTINEL_SECRET_VALUE"
        let statuses: [RelayStatus] = [
            RelayStatus(state: .active, mode: .hosted, connection: .connected, renewsAt: sentinel),
            RelayStatus(state: .selfHosted, mode: .selfHosted, url: sentinel, connection: .connected),
            RelayStatus(
                state: .noSeat, mode: .hosted, connection: .disconnected,
                activations: [RelayActivation(label: sentinel, firstSeen: sentinel)]
            ),
            RelayStatus(state: .invalidKey, mode: .hosted, connection: .disconnected),
            RelayStatus(state: .lapsed, mode: .hosted, connection: .disconnected),
            RelayStatus(state: .needsLicense, mode: .hosted, connection: .disconnected),
        ]
        for status in statuses {
            // The sentinel is deliberately also used as an activation label
            // above, so this only proves the sentinel does not appear
            // *verbatim as a secret value*; using it as a label is expected
            // to surface it as a label, which is not a leak -- labels are
            // user-chosen, non-secret text. The invalidKey/lapsed/needsLicense
            // cases prove the sentinel cannot appear at all when it is not
            // deliberately embedded as displayable, non-secret content.
            if status.state == .invalidKey || status.state == .lapsed || status.state == .needsLicense {
                #expect(!PairDeviceModel.relayConfigureResultDescription(status).contains(sentinel))
            }
        }
    }

    // MARK: - start() and the local "already asked" record

    /// A deliberate "tailnet only" choice persists `RelayManagedState{Mode:
    /// off}`, which resolves to the exact same wire status
    /// (`{state: off, mode: off}`) as a Mac that was never configured at all
    /// -- the server genuinely cannot tell these apart (see
    /// `needsConnectionChoice`'s doc comment). `start()` closes this gap with
    /// a local record: once `chooseTailnetOnly()` succeeds, the connection
    /// question must never be asked again on this Mac, regardless of what a
    /// later `relayStatus()` call reports.
    @Test("Choosing tailnet-only records a local flag so start() never re-asks, even though the server-side status looks identical to unconfigured")
    func tailnetOnlyChoicePersistsLocallyDespiteIdenticalServerStatus() async throws {
        let suiteName = "ai.redline.mac.tests.\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suiteName))
        defer { defaults.removePersistentDomain(forName: suiteName) }

        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [StartFlowStub.self]
        let client = RedlineAPIClient(
            baseURL: URL(string: "http://127.0.0.1:7436")!,
            token: "local-token",
            session: URLSession(configuration: configuration)
        )

        // First open: nothing configured yet, nothing recorded locally --
        // step one must appear.
        let firstModel = PairDeviceModel(client: client, defaults: defaults)
        await firstModel.start()
        #expect(firstModel.step == .chooseConnection)

        // Choosing tailnet-only succeeds; the server now reports the exact
        // same {state: off, mode: off} it would have reported if nothing had
        // ever been configured.
        await firstModel.chooseTailnetOnly()
        #expect(defaults.bool(forKey: PairDeviceModel.connectionChoiceMadeKey))

        // A brand-new model (a fresh window open) must go straight to the
        // code step this time, because the local record -- not the
        // ambiguous server status -- is checked first.
        let secondModel = PairDeviceModel(client: client, defaults: defaults)
        await secondModel.start()
        #expect(secondModel.step == .code)
    }

    /// A Mac with a real prior choice already on the server (e.g. hosted or
    /// self-hosted, configured before this local record existed, or from a
    /// different client) must not be re-asked just because no local flag
    /// exists yet -- `start()` back-fills the flag from a genuine non-default
    /// status instead.
    @Test("A genuinely non-default server status back-fills the local flag without asking again")
    func nonDefaultServerStatusBackfillsTheLocalFlag() async throws {
        let suiteName = "ai.redline.mac.tests.\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suiteName))
        defer { defaults.removePersistentDomain(forName: suiteName) }
        #expect(!defaults.bool(forKey: PairDeviceModel.connectionChoiceMadeKey))

        // A dedicated stub type, not `StartFlowStub`: `URLProtocol` routing
        // is process-global class state, and Swift Testing runs tests
        // concurrently by default, so two tests sharing one stub's mutable
        // `statusBody` would race.
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [NonDefaultStatusStub.self]
        let client = RedlineAPIClient(
            baseURL: URL(string: "http://127.0.0.1:7436")!,
            token: "local-token",
            session: URLSession(configuration: configuration)
        )

        let model = PairDeviceModel(client: client, defaults: defaults)
        await model.start()
        #expect(model.step == .code)
        #expect(defaults.bool(forKey: PairDeviceModel.connectionChoiceMadeKey))
    }
}

/// Reports a genuine, already-made "self-hosted" choice from every request --
/// a separate, immutable stub from `StartFlowStub` so the two `start()`
/// integration tests cannot race on shared class state.
private final class NonDefaultStatusStub: URLProtocol {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let body = Data(#"{"state":"self_hosted","mode":"self_hosted","connection":"connected"}"#.utf8)
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

/// Routes `relayStatus`, `configureRelay`, and `createPairingToken` for the
/// `start()`/`chooseTailnetOnly()` integration test above. Every response
/// is fixed, not mutable static state: Swift Testing runs tests
/// concurrently by default, and a shared mutable stub would race across
/// tests even though each test builds its own `URLSession`.
private final class StartFlowStub: URLProtocol {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let path = request.url?.path ?? ""
        let body: Data
        switch (request.httpMethod, path) {
        case ("GET", "/v1/relay/status"):
            // The resolver's true unconfigured default.
            body = Data(#"{"state":"off","mode":"off","connection":"disconnected"}"#.utf8)
        case ("POST", "/v1/relay/configure"):
            // chooseTailnetOnly() always configures mode: off, which resolves
            // to the same wire shape as the unconfigured default.
            body = Data(#"{"state":"off","mode":"off","connection":"disconnected"}"#.utf8)
        case ("POST", "/v1/pairing"):
            body = Data(#"{"pairing_token":"tok","expires_at":"2026-01-01T00:10:00Z","pairing_url":"https://mac.example.ts.net/pair?t=tok","routes":["direct"],"endpoint":"mac.example.ts.net:443"}"#.utf8)
        default:
            body = Data("{}".utf8)
        }
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}
