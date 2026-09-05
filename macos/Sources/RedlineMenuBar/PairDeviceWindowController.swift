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
        case failed(String)
    }

    @Published private(set) var state: State = .loading

    private let client: RedlineAPIClient

    init(client: RedlineAPIClient) {
        self.client = client
    }

    /// Asks the service for a code and shows exactly what it was handed.
    ///
    /// The service composes the URL from its own config and relay identity.
    /// This window used to build one itself from the trusted host and the
    /// token, and nothing else; when the QR grew relay fields the CLI got them
    /// and this did not, so every phone paired from here had no relay and no
    /// way to know. Reading YAML for a host is no longer this window's job.
    func load() async {
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
        } catch {
            state = .failed(error.localizedDescription)
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
