import AppKit
import RedlineKit

@MainActor
final class MenuBarController: NSObject {
    private let client: RedlineAPIClient
    private let supervisor: ServiceSupervisor
    private let showAppSetup: @MainActor () -> Void
    private let installationIssue: @MainActor () -> InstallationIssue?
    private let statusItem: NSStatusItem
    private let popoverModel: PopoverViewModel
    private let updates: NativeUpdateController
    private let dashboardURL: URL
    private lazy var dashboardWindow = DashboardWindowController(dashboardURL: dashboardURL)
    private lazy var runLogWindow = RunLogWindowController(client: client)
    private lazy var notifications = NativeNotificationController(
        onOpenRun: { [weak self] runID in self?.openRun(runID) }
    )
    private lazy var popoverController = StatusPopoverController(
        model: popoverModel,
        actions: StatusPopoverActions(
            showDashboard: { [weak self] in self?.showDashboard() },
            openBrowser: { [weak self] in self?.openDashboardInBrowser() },
            showRunLogs: { [weak self] run in self?.showRun(run) },
            reconnectProvider: { [weak self] provider in self?.reconnectProvider(provider) },
            checkForUpdates: { [weak self] in self?.updates.checkForUpdates() },
            enableNotifications: { [weak self] in self?.notifications.enable() },
            showAgentPermissionHelp: { [weak self] in self?.showAgentPermissionHelp() },
            showAppSetup: showAppSetup,
            quit: { NSApplication.shared.terminate(nil) }
        )
    )
    private var refreshTimer: Timer?

    init(
        apiURL: URL,
        apiToken: String,
        supervisor: ServiceSupervisor,
        installationIssue: @escaping @MainActor () -> InstallationIssue? = { nil },
        showAppSetup: @escaping @MainActor () -> Void = {}
    ) {
        client = RedlineAPIClient(baseURL: apiURL, token: apiToken)
        dashboardURL = APICredentialStore.authenticatedDashboardURL(baseURL: apiURL, token: apiToken)
        self.supervisor = supervisor
        self.installationIssue = installationIssue
        self.showAppSetup = showAppSetup
        popoverModel = PopoverViewModel(client: client)
        updates = NativeUpdateController()
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        super.init()

        popoverModel.onSnapshot = { [weak self] snapshot in self?.render(snapshot) }
        popoverModel.onError = { [weak self] message in self?.renderOffline(message) }

        guard let button = statusItem.button else { return }
        button.image = GaugeIcon.image(activity: nil, remainingPercent: nil)
        button.attributedTitle = providerSummary([], unreadRuns: 0)
        button.toolTip = "Redline is starting"
        button.target = self
        button.action = #selector(togglePopover)
        button.sendAction(on: [.leftMouseUp])
        button.setAccessibilityLabel("Redline is starting")
    }

    func start() {
        Task {
            await supervisor.ensureRunning()
            await refresh()
        }
        refreshTimer = Timer.scheduledTimer(withTimeInterval: 20, repeats: true) { [weak self] _ in
            Task { @MainActor in await self?.refresh() }
        }
    }

    func stop() {
        refreshTimer?.invalidate()
        supervisor.stopOwnedService()
    }

    func showDashboard() {
        dashboardWindow.showDashboard()
    }

    func showPopover() {
        guard let button = statusItem.button else { return }
        popoverController.show(relativeTo: button)
    }

    func showPopoverPreview() {
        popoverController.showPreviewWindow()
    }

    private func showAgentPermissionHelp() {
        let alert = NSAlert()
        alert.messageText = AgentPermissionGuidance.title
        alert.informativeText = "\(AgentPermissionGuidance.summary)\n\n\(AgentPermissionGuidance.detail)"
        alert.alertStyle = .informational
        alert.addButton(withTitle: "OK")
        alert.runModal()
    }

    func reconnectAfterServiceMigration() async {
        await supervisor.ensureRunning()
        await refresh()
    }

    private func refresh() async {
        popoverModel.apply(installationIssue: installationIssue())
        do {
            let snapshot = try await client.dashboard()
            popoverModel.apply(snapshot)
        } catch {
            let message = error.localizedDescription
            popoverModel.apply(error: message)
        }
    }

    private func render(_ snapshot: DashboardSnapshot) {
        notifications.observe(snapshot)
        let trayState = TrayState(snapshot: snapshot)
        let setupIssue = installationIssue()
        popoverModel.apply(installationIssue: setupIssue)
        guard let button = statusItem.button else { return }
        button.image = GaugeIcon.image(
            activity: setupIssue == nil ? trayState.activity : .attention,
            remainingPercent: trayState.lowestWeeklyPercent
        )
        button.attributedTitle = providerSummary(trayState.providerBadges, unreadRuns: snapshot.unreadRuns)
        button.toolTip = setupIssue?.title ?? trayState.menuBarTitle
        button.setAccessibilityLabel(setupIssue.map { "Redline needs attention: \($0.title)" } ?? "Redline \(trayState.menuBarTitle)")
    }

    private func renderOffline(_ detail: String) {
        guard let button = statusItem.button else { return }
        button.image = GaugeIcon.image(activity: nil, remainingPercent: nil, offline: true)
        button.attributedTitle = providerSummary([], unreadRuns: 0)
        button.toolTip = "Redline offline: \(detail)"
        button.setAccessibilityLabel("Redline is offline")
    }

    private func providerSummary(_ badges: [ProviderBadge], unreadRuns: Int) -> NSAttributedString {
        let result = NSMutableAttributedString()
        let font = NSFont.monospacedDigitSystemFont(ofSize: 11, weight: .medium)
        for (index, badge) in badges.enumerated() {
            if index > 0 { result.append(NSAttributedString(string: "  ")) }
            let attachment = NSTextAttachment()
            attachment.attachmentCell = NSTextAttachmentCell(
                imageCell: ProviderArtwork.image(for: badge.provider, template: true, size: 14)
            )
            let artwork = NSMutableAttributedString(attachment: attachment)
            artwork.addAttribute(
                .baselineOffset,
                value: -2.0,
                range: NSRange(location: 0, length: artwork.length)
            )
            result.append(artwork)
            let value = badge.percent.map { " \($0)%" } ?? " —"
            result.append(NSAttributedString(
                string: value,
                attributes: [.font: font]
            ))
        }
        if unreadRuns > 0 {
            let unreadFont = NSFont.monospacedDigitSystemFont(ofSize: 12, weight: .bold)
            result.append(NSAttributedString(
                string: "  +\(unreadRuns)",
                attributes: [.font: unreadFont, .foregroundColor: NSColor.systemBlue]
            ))
        }
        return result
    }

    private func showRun(_ run: RunSummary) {
        runLogWindow.show(run: run)
        Task {
            try? await client.markRunRead(run.id)
            await refresh()
        }
    }

    private func openRun(_ runID: String) {
        Task {
            guard let run = try? await client.run(runID) else {
                showDashboard()
                return
            }
            showRun(run)
        }
    }

    private func reconnectProvider(_ provider: ProviderSummary) {
        guard let command = ProviderRecovery.loginCommand(for: provider.provider) else {
            showDashboard()
            return
        }
        let escaped = command
            .replacingOccurrences(of: "\\", with: "\\\\")
            .replacingOccurrences(of: "\"", with: "\\\"")
        let source = """
        tell application "Terminal"
            activate
            do script "\(escaped)"
        end tell
        """
        var error: NSDictionary?
        if NSAppleScript(source: source)?.executeAndReturnError(&error) == nil {
            showDashboard()
        }
    }

    @objc private func togglePopover() {
        guard let button = statusItem.button else { return }
        popoverController.toggle(relativeTo: button)
    }

    private func openDashboardInBrowser() {
        NSWorkspace.shared.open(dashboardURL)
    }
}
