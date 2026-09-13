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
        window.setContentSize(NSSize(width: 380, height: 560))
        window.center()
        window.delegate = self
        window.isReleasedWhenClosed = false
        self.window = window

        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        // `start()`, not `load()`: the window must first ask whether a
        // managed relay choice already exists, so a first-run Mac shows the
        // connection-choice step before the QR rather than jumping straight
        // to a code with no route configured.
        Task { await model.start() }
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
    /// Which half of the two-step flow is on screen.
    ///
    /// Step one only appears the first time: once any choice is made
    /// (including "tailnet only"), `configureRelay` records managed state and
    /// the resolver's `!managed && !bootstrap.Enabled` default no longer
    /// applies, so every later open of this window goes straight to step two.
    enum Step {
        case chooseConnection
        case code
    }

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

    @Published private(set) var step: Step = .code
    @Published private(set) var state: State = .loading

    /// Whether a trusted host exists to pair over the tailnet, inferred
    /// purely from a `createPairingToken()` response's `routes` -- there is
    /// no separate capability endpoint, so the "tailnet only" choice is
    /// enabled by the same signal the QR view already uses to describe a
    /// direct route. `nil` while unknown (still loading, or the probe
    /// itself failed).
    @Published private(set) var trustedHostAvailable: Bool?

    @Published var licenseKey: String = ""
    @Published var deviceLabel: String = ""
    @Published private(set) var isConfiguringHosted = false
    @Published private(set) var hostedResultMessage: String?

    @Published var selfHostedURL: String = ""
    @Published private(set) var isConfiguringSelfHosted = false
    @Published private(set) var selfHostedResultMessage: String?

    @Published private(set) var isConfiguringTailnetOnly = false
    @Published private(set) var tailnetOnlyErrorMessage: String?

    private let client: RedlineAPIClient
    private var watch: Task<Void, Never>?

    init(client: RedlineAPIClient) {
        self.client = client
    }

    deinit {
        watch?.cancel()
    }

    /// Entry point for opening the window: decides which step to show, then
    /// shows it.
    ///
    /// A relay status of exactly `.off` state and `.off` mode is the
    /// resolver's true zero value (`internal/config/relay_state.go`'s
    /// `RelayResolver.Resolve`, the `!managed && !bootstrap.Enabled` branch)
    /// -- nothing has ever been configured, managed or bootstrapped. Any
    /// other status means a choice already exists (including an explicit,
    /// managed "off", which the resolver cannot tell apart from the zero
    /// value by state/mode alone, but which this window never re-asks about
    /// once made -- see the note on `Step`). If the probe itself fails, this
    /// falls back to today's existing behaviour (show the code) rather than
    /// gating pairing on a second endpoint's availability.
    func start() async {
        do {
            let status = try await client.relayStatus()
            if PairDeviceModel.needsConnectionChoice(status) {
                await enterChooseConnection()
                return
            }
        } catch {
            // Relay status is a new, additional signal; a service without it
            // (or momentarily unreachable) must not block pairing, which
            // worked before this endpoint existed.
        }
        step = .code
        await load()
    }

    /// Whether the window should show step one.
    ///
    /// A pure function of the status alone, so the resolver-default check is
    /// testable without a window, a client, or a clock.
    static func needsConnectionChoice(_ status: RelayStatus) -> Bool {
        status.state == .off && status.mode == .off
    }

    /// Whether a `configureRelay` result is good enough to move on to the QR.
    static func shouldProceedToCode(after status: RelayStatus) -> Bool {
        status.state == .active || status.state == .selfHosted
    }

    /// A short, human line describing what `configureRelay` returned, for
    /// step one to show inline -- never the license key or any other secret,
    /// because the function's only input is the closed, non-secret
    /// `RelayStatus` the service already redacts before it reaches this
    /// client.
    static func relayConfigureResultDescription(_ status: RelayStatus) -> String {
        switch status.state {
        case .active:
            let renews = status.renewsAt.flatMap(RelayStatusLine.shortDate)
            return renews.map { "Relay active · renews \($0)" } ?? "Relay active"
        case .selfHosted:
            return "Self-hosted relay configured"
        case .noSeat:
            let activations = status.activations ?? []
            if activations.isEmpty {
                return "No seat is available on this license."
            }
            let lines = activations.map { activation -> String in
                guard let seen = RelayStatusLine.relativeFirstSeen(activation.firstSeen) else {
                    return activation.label
                }
                return "\(activation.label) (\(seen))"
            }
            return "No seat is available. Devices on this license: " + lines.joined(separator: ", ")
        case .invalidKey:
            return "That license key was not recognized."
        case .lapsed:
            return "This license's subscription has lapsed."
        case .needsLicense:
            return "Enter a license key to activate the relay."
        case .off, .renewPending, .unavailable:
            return "Relay status: \(status.state.rawValue)"
        }
    }

    private func enterChooseConnection() async {
        step = .chooseConnection
        trustedHostAvailable = nil
        do {
            // The same probe the existing QR flow makes: a fresh pairing
            // token's `routes` says whether this Mac has a trusted host,
            // with no separate capability endpoint required.
            let pairing = try await client.createPairingToken()
            trustedHostAvailable = pairing.routes.contains(.direct)
        } catch {
            trustedHostAvailable = false
        }
    }

    /// Reopens step one. Shown on the QR view's "Change…" link.
    func changeConnection() {
        Task { await enterChooseConnection() }
    }

    /// Choice (a): tailnet only. Setting relay explicitly to off is what
    /// records a managed choice was made, per the resolver -- an off choice
    /// with no session id or URL is exactly what `RelayManagedState`'s
    /// off-validation expects.
    func chooseTailnetOnly() async {
        guard !isConfiguringTailnetOnly else { return }
        isConfiguringTailnetOnly = true
        tailnetOnlyErrorMessage = nil
        defer { isConfiguringTailnetOnly = false }
        do {
            _ = try await client.configureRelay(RelayConfigureRequest(mode: .off))
            step = .code
            await load()
        } catch {
            tailnetOnlyErrorMessage = error.localizedDescription
        }
    }

    /// Choice (b): Redline's hosted relay.
    func continueWithHostedRelay() async {
        guard !isConfiguringHosted else { return }
        isConfiguringHosted = true
        hostedResultMessage = nil
        defer { isConfiguringHosted = false }
        do {
            let status = try await client.configureRelay(
                RelayConfigureRequest(mode: .hosted, licenseKey: licenseKey, label: deviceLabel)
            )
            hostedResultMessage = PairDeviceModel.relayConfigureResultDescription(status)
            if PairDeviceModel.shouldProceedToCode(after: status) {
                step = .code
                await load()
            }
        } catch {
            // `RedlineAPIClient.Error` carries only an HTTP status, never a
            // response body, so this cannot echo the key even on failure.
            hostedResultMessage = error.localizedDescription
        }
    }

    /// Choice (c): a self-hosted relay.
    func continueWithSelfHostedRelay() async {
        guard !isConfiguringSelfHosted else { return }
        isConfiguringSelfHosted = true
        selfHostedResultMessage = nil
        defer { isConfiguringSelfHosted = false }
        do {
            let status = try await client.configureRelay(
                RelayConfigureRequest(mode: .selfHosted, url: selfHostedURL)
            )
            selfHostedResultMessage = PairDeviceModel.relayConfigureResultDescription(status)
            if PairDeviceModel.shouldProceedToCode(after: status) {
                step = .code
                await load()
            }
        } catch {
            selfHostedResultMessage = error.localizedDescription
        }
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

/// External links step one opens. Kept together so they are easy to audit:
/// "Buy a license" is a link only, never in-app purchase or Stripe code.
private enum PairingConnectionLinks {
    static let buyLicense = URL(string: "https://redline.croutoncreations.com/relay")!
    static let selfHostedGuide = URL(string: "https://github.com/croutoncreations/redline/blob/main/docs/self-hosted-relay.md")!
}

private struct PairDeviceView: View {
    @ObservedObject var model: PairDeviceModel

    var body: some View {
        Group {
            switch model.step {
            case .chooseConnection:
                ChooseConnectionView(model: model)
            case .code:
                codeStepView
            }
        }
        .frame(width: 380, height: 560)
    }

    @ViewBuilder
    private var codeStepView: some View {
        VStack(spacing: 14) {
            switch model.state {
            case .loading:
                Spacer()
                ProgressView().controlSize(.large)
                Spacer()

            case .ready(let url, let routes, let endpoint, let expiresAt):
                changeConnectionLink
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
                changeConnectionLink
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
        .frame(width: 380, height: 560)
    }

    private var changeConnectionLink: some View {
        HStack {
            Button("Change…") { model.changeConnection() }
                .buttonStyle(.link)
                .font(.system(size: 11))
            Spacer()
        }
    }
}

/// Step one: "How should your phone reach this Mac?"
///
/// Shown only when no managed relay choice exists yet -- see
/// `PairDeviceModel.needsConnectionChoice`.
private struct ChooseConnectionView: View {
    @ObservedObject var model: PairDeviceModel

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                Text("How should your phone reach this Mac?")
                    .font(.system(size: 15, weight: .semibold))

                tailnetOnlySection
                Divider()
                hostedRelaySection
                Divider()
                selfHostedRelaySection
            }
            .padding(20)
        }
        .frame(width: 380, height: 560)
    }

    private var tailnetOnlySection: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("Over your tailnet only").font(.system(size: 13, weight: .semibold))
            Text("Pairs directly over Tailscale, with no relay involved.")
                .font(.system(size: 11))
                .foregroundStyle(.secondary)
            if model.trustedHostAvailable == false {
                Text("No trusted host configured.")
                    .font(.system(size: 11))
                    .foregroundStyle(.orange)
            }
            HStack {
                Button {
                    Task { await model.chooseTailnetOnly() }
                } label: {
                    if model.isConfiguringTailnetOnly {
                        ProgressView().controlSize(.small)
                    } else {
                        Text("Use tailnet only")
                    }
                }
                .buttonStyle(.bordered)
                .disabled(model.trustedHostAvailable != true || model.isConfiguringTailnetOnly)
            }
            if let message = model.tailnetOnlyErrorMessage {
                Text(message).font(.system(size: 11)).foregroundStyle(.red)
            }
        }
    }

    private var hostedRelaySection: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("Redline's relay").font(.system(size: 13, weight: .semibold))
            Text("Works from anywhere, no Tailscale needed.")
                .font(.system(size: 11))
                .foregroundStyle(.secondary)
            Button("Buy a license") {
                NSWorkspace.shared.open(PairingConnectionLinks.buyLicense)
            }
            .buttonStyle(.link)
            .font(.system(size: 11))
            SecureField("License key", text: $model.licenseKey)
                .textFieldStyle(.roundedBorder)
            TextField("Device label (optional)", text: $model.deviceLabel)
                .textFieldStyle(.roundedBorder)
            HStack {
                Button {
                    Task { await model.continueWithHostedRelay() }
                } label: {
                    if model.isConfiguringHosted {
                        ProgressView().controlSize(.small)
                    } else {
                        Text("Continue")
                    }
                }
                .buttonStyle(.borderedProminent)
                .disabled(model.licenseKey.isEmpty || model.isConfiguringHosted)
            }
            if let message = model.hostedResultMessage {
                Text(message)
                    .font(.system(size: 11))
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    private var selfHostedRelaySection: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("My own relay").font(.system(size: 13, weight: .semibold))
            Text("Point at a relay you run yourself.")
                .font(.system(size: 11))
                .foregroundStyle(.secondary)
            TextField("Relay URL", text: $model.selfHostedURL)
                .textFieldStyle(.roundedBorder)
            Link("How to run one", destination: PairingConnectionLinks.selfHostedGuide)
                .font(.system(size: 11))
            HStack {
                Button {
                    Task { await model.continueWithSelfHostedRelay() }
                } label: {
                    if model.isConfiguringSelfHosted {
                        ProgressView().controlSize(.small)
                    } else {
                        Text("Continue")
                    }
                }
                .buttonStyle(.bordered)
                .disabled(model.selfHostedURL.isEmpty || model.isConfiguringSelfHosted)
            }
            if let message = model.selfHostedResultMessage {
                Text(message)
                    .font(.system(size: 11))
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }
}
