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
    private let configURL: URL

    init(client: RedlineAPIClient, configURL: URL) {
        self.client = client
        self.configURL = configURL
    }

    func show() {
        if let window {
            window.makeKeyAndOrderFront(nil)
            NSApp.activate(ignoringOtherApps: true)
            return
        }

        let model = PairDeviceModel(client: client, configURL: configURL)
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
        case ready(url: String, host: String, expiresAt: Date?)
        case failed(String)
    }

    @Published private(set) var state: State = .loading

    private let client: RedlineAPIClient
    private let configURL: URL

    init(client: RedlineAPIClient, configURL: URL) {
        self.client = client
        self.configURL = configURL
    }

    func load() async {
        state = .loading

        // The host has to come from configuration. A code aimed at localhost
        // looks right on screen and cannot be reached from a phone.
        guard
            let yaml = try? String(contentsOf: configURL, encoding: .utf8),
            let host = PairingCode.trustedHost(inConfiguration: yaml)
        else {
            state = .failed(
                "No trusted host is configured. Add your Tailscale name under "
                    + "api.trusted_hosts in redline.yaml, then try again."
            )
            return
        }

        do {
            let pairing = try await client.createPairingToken()
            state = .ready(
                url: PairingCode.url(
                    host: host,
                    port: PairingCode.trustedPort(inConfiguration: yaml),
                    token: pairing.token
                ),
                host: host,
                expiresAt: pairing.expiry
            )
        } catch {
            state = .failed(error.localizedDescription)
        }
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

            case .ready(let url, let host, let expiresAt):
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
                    Text(host).font(.system(size: 11, design: .monospaced))
                        .foregroundStyle(.secondary)
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
