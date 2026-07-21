import AppKit
import RedlineKit

@MainActor
final class MenuBarController: NSObject {
    private let apiURL: URL
    private let client: RedlineAPIClient
    private let supervisor: ServiceSupervisor
    private let statusItem: NSStatusItem
    private lazy var dashboardWindow = DashboardWindowController(dashboardURL: apiURL)
    private let menu = NSMenu()
    private var refreshTimer: Timer?
    private var lastSnapshot: DashboardSnapshot?

    init(apiURL: URL, supervisor: ServiceSupervisor) {
        self.apiURL = apiURL
        client = RedlineAPIClient(baseURL: apiURL)
        self.supervisor = supervisor
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        super.init()
        statusItem.menu = menu
        statusItem.button?.image = GaugeIcon.image(for: nil)
        statusItem.button?.title = " STARTING"
        statusItem.button?.font = .monospacedSystemFont(ofSize: 11, weight: .medium)
        statusItem.button?.toolTip = "Redline is starting"
        render(message: "Connecting to Redline…")
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

    @objc private func refreshFromMenu() {
        Task { await refresh() }
    }

    private func refresh() async {
        do {
            let snapshot = try await client.dashboard()
            lastSnapshot = snapshot
            render(snapshot)
        } catch {
            render(message: "Service unavailable", detail: error.localizedDescription)
        }
    }

    private func render(_ snapshot: DashboardSnapshot) {
        menu.removeAllItems()
        let trayState = TrayState(snapshot: snapshot)
        statusItem.button?.image = GaugeIcon.image(for: trayState.level)
        statusItem.button?.title = " \(trayState.menuBarTitle)"
        statusItem.button?.toolTip = trayState.iconDescription

        let title = NSMenuItem(title: "Redline", action: nil, keyEquivalent: "")
        title.attributedTitle = NSAttributedString(
            string: "REDLINE",
            attributes: [.font: NSFont.monospacedSystemFont(ofSize: 11, weight: .bold)]
        )
        menu.addItem(title)
        menu.addItem(.separator())

        for provider in snapshot.providers {
            let usage = providerUsageTitle(provider)
            let item = NSMenuItem(title: usage, action: nil, keyEquivalent: "")
            item.image = providerImage(provider.provider)
            item.isEnabled = false
            if !provider.modelAllowances.isEmpty {
                let submenu = NSMenu()
                for allowance in provider.modelAllowances {
                    submenu.addItem(withTitle: "\(allowance.displayName): \(allowance.percent)% remaining", action: nil, keyEquivalent: "")
                }
                item.submenu = submenu
                item.isEnabled = true
            }
            menu.addItem(item)
        }

        menu.addItem(.separator())
        let healthTitle = snapshot.health.status == "healthy"
            ? "Healthy"
            : "Recent errors · \(snapshot.health.dispatchErrors) dispatch"
        menu.addItem(disabledItem(healthTitle, symbol: snapshot.health.status == "healthy" ? "checkmark.circle" : "exclamationmark.triangle"))
        let schedulerTitle = snapshot.scheduler.enabled
            ? "Scheduler on\(snapshot.scheduler.running ? " · checking now" : "")"
            : "Scheduler off"
        menu.addItem(disabledItem(schedulerTitle, symbol: "clock.arrow.circlepath"))
        if snapshot.health.activeRuns > 0 {
            menu.addItem(disabledItem("\(snapshot.health.activeRuns) active run\(snapshot.health.activeRuns == 1 ? "" : "s")", symbol: "bolt.fill"))
        }
        menu.addItem(disabledItem(serviceTitle, symbol: "server.rack"))
        addActions()
    }

    private func render(message: String, detail: String? = nil) {
        menu.removeAllItems()
        statusItem.button?.image = GaugeIcon.image(for: .degraded)
        statusItem.button?.title = " OFFLINE"
        statusItem.button?.toolTip = detail.map { "\(message): \($0)" } ?? message
        menu.addItem(disabledItem(message, symbol: "exclamationmark.circle"))
        if let detail {
            let item = disabledItem(detail, symbol: nil)
            item.toolTip = detail
            menu.addItem(item)
        }
        menu.addItem(disabledItem(serviceTitle, symbol: "server.rack"))
        addActions()
    }

    private func addActions() {
        menu.addItem(.separator())
        let dashboard = NSMenuItem(title: "Show Dashboard…", action: #selector(openDashboard), keyEquivalent: "d")
        dashboard.target = self
        dashboard.image = NSImage(systemSymbolName: "gauge.with.dots.needle.67percent", accessibilityDescription: nil)
        menu.addItem(dashboard)

        let browser = NSMenuItem(title: "Open Dashboard in Browser", action: #selector(openDashboardInBrowser), keyEquivalent: "")
        browser.target = self
        browser.image = NSImage(systemSymbolName: "safari", accessibilityDescription: nil)
        menu.addItem(browser)

        let refresh = NSMenuItem(title: "Refresh", action: #selector(refreshFromMenu), keyEquivalent: "r")
        refresh.target = self
        refresh.image = NSImage(systemSymbolName: "arrow.clockwise", accessibilityDescription: nil)
        menu.addItem(refresh)
        menu.addItem(.separator())

        let quit = NSMenuItem(title: "Quit Redline", action: #selector(quit), keyEquivalent: "q")
        quit.target = self
        menu.addItem(quit)
    }

    private func providerUsageTitle(_ provider: ProviderSummary) -> String {
        let weekly = provider.weeklyPercent.map { "Weekly \($0)%" } ?? "Weekly —"
        let short = provider.shortPercent.map { "5h \($0)%" }
        return ([provider.displayName, weekly] + (short.map { [$0] } ?? [])).joined(separator: "  ·  ")
    }

    private func providerImage(_ provider: String) -> NSImage? {
        let symbol = provider.lowercased() == "claude" ? "sparkles" : "terminal"
        return NSImage(systemSymbolName: symbol, accessibilityDescription: provider)
    }

    private func disabledItem(_ title: String, symbol: String?) -> NSMenuItem {
        let item = NSMenuItem(title: title, action: nil, keyEquivalent: "")
        item.isEnabled = false
        if let symbol { item.image = NSImage(systemSymbolName: symbol, accessibilityDescription: nil) }
        return item
    }

    private var serviceTitle: String {
        switch supervisor.state {
        case .checking: "Checking service"
        case .connectedToExistingService: "Using existing local service"
        case .runningBundledService: "Using bundled service"
        case .unavailable(let reason): "Service unavailable · \(reason)"
        }
    }

    @objc private func openDashboard() {
        dashboardWindow.showDashboard()
    }

    @objc private func openDashboardInBrowser() {
        NSWorkspace.shared.open(apiURL)
    }

    @objc private func quit() {
        NSApplication.shared.terminate(nil)
    }
}
