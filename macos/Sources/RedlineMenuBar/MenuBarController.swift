import AppKit
import RedlineKit

@MainActor
final class MenuBarController: NSObject {
    private let apiURL: URL
    private let client: RedlineAPIClient
    private let supervisor: ServiceSupervisor
    private let statusItem: NSStatusItem
    private let popoverModel: PopoverViewModel
    private lazy var dashboardWindow = DashboardWindowController(dashboardURL: apiURL)
    private lazy var popoverController = StatusPopoverController(
        model: popoverModel,
        actions: StatusPopoverActions(
            showDashboard: { [weak self] in self?.showDashboard() },
            openBrowser: { [weak self] in self?.openDashboardInBrowser() },
            quit: { NSApplication.shared.terminate(nil) }
        )
    )
    private var refreshTimer: Timer?

    init(apiURL: URL, supervisor: ServiceSupervisor) {
        self.apiURL = apiURL
        client = RedlineAPIClient(baseURL: apiURL)
        self.supervisor = supervisor
        popoverModel = PopoverViewModel(client: client)
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        super.init()

        popoverModel.onSnapshot = { [weak self] snapshot in self?.render(snapshot) }
        popoverModel.onError = { [weak self] message in self?.renderOffline(message) }

        guard let button = statusItem.button else { return }
        button.image = GaugeIcon.image(activity: nil, remainingPercent: nil)
        button.attributedTitle = providerSummary([])
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

    private func refresh() async {
        do {
            let snapshot = try await client.dashboard()
            popoverModel.apply(snapshot)
        } catch {
            let message = error.localizedDescription
            popoverModel.apply(error: message)
        }
    }

    private func render(_ snapshot: DashboardSnapshot) {
        let trayState = TrayState(snapshot: snapshot)
        guard let button = statusItem.button else { return }
        button.image = GaugeIcon.image(
            activity: trayState.activity,
            remainingPercent: trayState.lowestWeeklyPercent
        )
        button.attributedTitle = providerSummary(trayState.providerBadges)
        button.toolTip = trayState.menuBarTitle
        button.setAccessibilityLabel("Redline \(trayState.menuBarTitle)")
    }

    private func renderOffline(_ detail: String) {
        guard let button = statusItem.button else { return }
        button.image = GaugeIcon.image(activity: nil, remainingPercent: nil, offline: true)
        button.attributedTitle = providerSummary([])
        button.toolTip = "Redline offline: \(detail)"
        button.setAccessibilityLabel("Redline is offline")
    }

    private func providerSummary(_ badges: [ProviderBadge]) -> NSAttributedString {
        let result = NSMutableAttributedString()
        for (index, badge) in badges.enumerated() {
            if index > 0 { result.append(NSAttributedString(string: "  ")) }
            let attachment = NSTextAttachment()
            attachment.attachmentCell = NSTextAttachmentCell(
                imageCell: ProviderArtwork.image(for: badge.provider, template: true, size: 12)
            )
            result.append(NSAttributedString(attachment: attachment))
            let value = badge.percent.map { " \($0)%" } ?? " —"
            result.append(NSAttributedString(
                string: value,
                attributes: [.font: NSFont.monospacedDigitSystemFont(ofSize: 11, weight: .medium)]
            ))
        }
        return result
    }

    @objc private func togglePopover() {
        guard let button = statusItem.button else { return }
        popoverController.toggle(relativeTo: button)
    }

    private func openDashboardInBrowser() {
        NSWorkspace.shared.open(apiURL)
    }
}
