import AppKit
import RedlineKit
import SwiftUI

/// Shows a pairing QR in a window.
///
/// Pairing was CLI-only, which meant the one step a new user has to complete
/// before the phone app does anything required finding a terminal and knowing
/// the right flags -- including `--host`, because the bundled binary cannot
/// auto-detect a Tailscale name from inside the app bundle.
@MainActor
final class PairDeviceWindowController: NSObject, NSWindowDelegate {
    private var window: NSWindow?
    private let client: RedlineAPIClient

    init(client: RedlineAPIClient) {
        self.client = client
    }

    func show() {
        if let window {
            window.makeKeyAndOrderFront(nil)
            NSApp.activate(ignoringOtherApps: true)
            return
        }

        let model = PairDeviceModel(client: client)
        let hosting = NSHostingController(rootView: PairDeviceView(model: model))
        let window = NSWindow(contentViewController: hosting)
        window.title = "Pair a device"
        window.styleMask = [.titled, .closable]
        window.setContentSize(NSSize(width: 380, height: 520))
        window.center()
        window.delegate = self
        window.isReleasedWhenClosed = false
        self.window = window

        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        Task { await model.load() }
    }

    func windowWillClose(_ notification: Notification) {
        // A pairing token is short-lived, so the window is not reused: opening
        // it again should mint a fresh code rather than show an expired one.
        window = nil
    }
}

/// State for the pairing window.
@MainActor
final class PairDeviceModel: ObservableObject {
    enum State {
        case loading
        case ready(url: String, routes: [PairingRoute], endpoint: String?, expiresAt: Date?)
        /// A phone spent the code and holds the credential.
        case paired(routes: [PairingRoute])
        /// The code ran out unscanned. Distinct from failed: nothing is wrong,
        /// the person just needs a fresh one.
        case expired
        case failed(String)
    }

    @Published private(set) var state: State = .loading

    private let client: RedlineAPIClient
    private var watch: Task<Void, Never>?

    init(client: RedlineAPIClient) {
        self.client = client
    }

    deinit {
        watch?.cancel()
    }

    /// Asks the service for a code and shows exactly what it was handed.
    ///
    /// The service composes the URL from its own config and relay identity.
    /// This window used to build one itself from the trusted host and the
    /// token, and nothing else; when the QR grew relay fields the CLI got them
    /// and this did not, so every phone paired from here had no relay and no
    /// way to know. Reading YAML for a host is no longer this window's job.
    func load() async {
        watch?.cancel()
        state = .loading
        do {
            let pairing = try await client.createPairingToken()
            guard let url = pairing.pairingURL else {
                // A token was minted but there is nowhere to point a phone.
                state = .failed(
                    "Redline has no way for a phone to reach it. Add your Tailscale "
                        + "name under api.trusted_hosts in redline.yaml, or turn on the "
                        + "relay, then try again."
                )
                return
            }
            state = .ready(
                url: url,
                routes: pairing.routes,
                endpoint: pairing.endpoint,
                expiresAt: pairing.expiry
            )
            watch = Task { [weak self] in
                await self?.watchForRedeem(of: pairing.token, routes: pairing.routes, expiresAt: pairing.expiry)
            }
        } catch {
            state = .failed(error.localizedDescription)
        }
    }

    /// Polls until the code is spent or runs out, then says which.
    ///
    /// A scanned code is dead -- the token is single-use -- so leaving it on
    /// screen after the phone got in shows something useless and says nothing
    /// about whether pairing worked. Two seconds is quick enough to feel like
    /// a response to the scan and slow enough to be nothing to a local
    /// service. A poll that fails is skipped, not fatal: the code is still
    /// good, and a blip on loopback should not take the QR off the screen.
    ///
    /// Bounded by the code's own expiry plus a margin, so the loop ends on its
    /// own even if the service never answers. Without that, a sheet opened
    /// while the service was down polled every two seconds until the window
    /// closed -- with the only guarantee of that being deinit.
    private func watchForRedeem(of token: String, routes: [PairingRoute], expiresAt: Date?) async {
        let started = Date()
        while !Task.isCancelled,
              PairDeviceModel.shouldKeepPolling(now: Date(), expiresAt: expiresAt, started: started) {
            // Task.sleep throws on cancellation; that is swallowed here and
            // read back on the next line, so the two must stay adjacent.
            try? await Task.sleep(for: .seconds(2))
            if Task.isCancelled { return }
            guard let status = try? await client.pairingStatus(of: token) else { continue }
            if let next = PairDeviceModel.stateAfter(status: status, routes: routes) {
                state = next
                return
            }
        }
        // Past the deadline without a verdict: the code is spent by time if
        // by nothing else, and saying so beats showing it for ever.
        if !Task.isCancelled {
            state = .expired
        }
    }

    /// Whether the poll should carry on, given the clock.
    ///
    /// The code's expiry plus `redeemMemoryMargin` covers a redeem in its last
    /// seconds that the service still remembers. With no expiry from the
    /// service the bound falls back to the token's known lifetime plus the
    /// same margin, measured from when polling began -- which is why `started`
    /// has no default: a stale or distant value there silently ends the poll
    /// before it begins.
    static func shouldKeepPolling(now: Date, expiresAt: Date?, started: Date) -> Bool {
        if let expiresAt {
            return now < expiresAt.addingTimeInterval(redeemMemoryMargin)
        }
        return now < started.addingTimeInterval(pairingTokenLifetime + redeemMemoryMargin)
    }

    /// How long past a code's expiry the service still reports a redeem.
    ///
    /// Mirrors `redeemedMemory` in `internal/api/server.go`, and must not
    /// exceed it: polling longer than the service remembers would read a real
    /// redeem as expired. The two are the same number in two languages with
    /// nothing but this comment and its twin holding them together, so change
    /// them together.
    static let redeemMemoryMargin: TimeInterval = 60

    /// How long a pairing token lives, per `createPairingToken` in
    /// `internal/api/server.go`. Used only when the service sent no expiry.
    static let pairingTokenLifetime: TimeInterval = 10 * 60

    /// What a status means for the window, or nil to keep showing the code.
    ///
    /// Separate from the polling so the rule is testable without a window or
    /// a clock.
    static func stateAfter(status: PairingStatus, routes: [PairingRoute]) -> State? {
        switch status {
        case .pending: return nil
        case .redeemed: return .paired(routes: routes)
        case .expired: return .expired
        }
    }
}

/// The one line under the code that says how the phone will connect.
///
/// Worth saying: a relayed session is slower, metered, and crosses a third
/// party, and a code with no direct route is exactly what a user who never set
/// up Tailscale should expect to see -- not an error.
func pairingRouteDescription(routes: [PairingRoute], endpoint: String?) -> String {
    let direct = routes.contains(.direct)
    let relay = routes.contains(.relay)
    switch (direct, relay) {
    case (true, true):
        return "Pairs over your tailnet (\(endpoint ?? "")) and falls back to the relay"
    case (true, false):
        return "Pairs over your tailnet (\(endpoint ?? ""))"
    case (false, true):
        return "Pairs over the relay only — no Tailscale needed"
    case (false, false):
        return ""
    }
}

/// What to tell someone whose phone just got in.
///
/// Names the route so a relay-only pairing is seen for what it is: expected,
/// working, and metered.
func pairedDescription(routes: [PairingRoute]) -> String {
    let direct = routes.contains(.direct)
    let relay = routes.contains(.relay)
    switch (direct, relay) {
    case (true, true):
        return "Your phone can reach Redline over your tailnet, and through the relay when it is away from it."
    case (true, false):
        return "Your phone can reach Redline over your tailnet."
    case (false, true):
        return "Your phone reaches Redline through the relay."
    case (false, false):
        return "Your phone is connected."
    }
}

private struct PairDeviceView: View {
    @ObservedObject var model: PairDeviceModel

    var body: some View {
        VStack(spacing: 14) {
            switch model.state {
            case .loading:
                Spacer()
                ProgressView().controlSize(.large)
                Spacer()

            case .ready(let url, let routes, let endpoint, let expiresAt):
                Text("Scan with the Redline app")
                    .font(.system(size: 15, weight: .semibold))

                if let image = PairingCode.image(for: url, size: 260) {
                    Image(nsImage: image)
                        .interpolation(.none)
                        .resizable()
                        .frame(width: 260, height: 260)
                        // A white plate regardless of appearance: a scanner
                        // needs the light-on-dark contrast the code was drawn
                        // for, and dark mode would invert it.
                        .background(Color.white)
                        .cornerRadius(6)
                }

                VStack(spacing: 3) {
                    Text(pairingRouteDescription(routes: routes, endpoint: endpoint))
                        .font(.system(size: 11))
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                    if let expiresAt {
                        Text("Code expires \(expiresAt, style: .relative) from now")
                            .font(.system(size: 11))
                            .foregroundStyle(.secondary)
                    }
                }

                // Anyone who photographs this screen can pair. Saying so is
                // cheaper than explaining a compromised token afterwards.
                Text("This code grants full access to your Redline. Do not share it.")
                    .font(.system(size: 10))
                    .foregroundStyle(.orange)
                    .multilineTextAlignment(.center)

                Button("New code") { Task { await model.load() } }
                    .buttonStyle(.bordered)

            case .paired(let routes):
                Spacer()
                Image(systemName: "checkmark.circle.fill")
                    .font(.system(size: 44))
                    .foregroundStyle(.green)
                Text("Paired")
                    .font(.system(size: 17, weight: .semibold))
                Text(pairedDescription(routes: routes))
                    .font(.system(size: 12))
                    .multilineTextAlignment(.center)
                    .foregroundStyle(.secondary)
                // The code that was here is spent, so there is nothing to go
                // back to; the only forward action is another device.
                Button("Pair another device") { Task { await model.load() } }
                    .buttonStyle(.bordered)
                Spacer()

            case .expired:
                Spacer()
                Image(systemName: "clock.badge.xmark")
                    .font(.system(size: 30))
                    .foregroundStyle(.secondary)
                Text("That code has expired")
                    .font(.system(size: 14, weight: .semibold))
                Text("Codes last ten minutes. Nothing went wrong -- just make a new one.")
                    .font(.system(size: 12))
                    .multilineTextAlignment(.center)
                    .foregroundStyle(.secondary)
                Button("New code") { Task { await model.load() } }
                    .buttonStyle(.borderedProminent)
                Spacer()

            case .failed(let message):
                Spacer()
                Image(systemName: "exclamationmark.triangle")
                    .font(.system(size: 26))
                    .foregroundStyle(.orange)
                Text(message)
                    .font(.system(size: 12))
                    .multilineTextAlignment(.center)
                    .foregroundStyle(.secondary)
                Button("Try again") { Task { await model.load() } }
                    .buttonStyle(.bordered)
                Spacer()
            }
        }
        .padding(20)
        .frame(width: 380, height: 520)
    }
}
