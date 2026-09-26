import AppKit
import RedlineKit
import WebKit

@MainActor
final class DashboardWindowController: NSWindowController, WKNavigationDelegate, NSToolbarDelegate {
    private static let toolbarIdentifier = NSToolbar.Identifier("RedlineDashboardToolbar")
    private static let refreshIdentifier = NSToolbarItem.Identifier("RedlineRefresh")
    private static let statusIdentifier = NSToolbarItem.Identifier("RedlineConnectionStatus")
    private static let browserIdentifier = NSToolbarItem.Identifier("RedlineOpenBrowser")
    private static let menuIdentifier = NSToolbarItem.Identifier("RedlineDashboardMenu")

    private let dashboardURL: URL
    /// What the overflow menu can ask the app to do. Supplied by the owner so
    /// this window does not need its own copy of the pairing or update
    /// machinery.
    private let actions: DashboardMenuActions
    private let navigationPolicy: DashboardNavigationPolicy
    private let webView: WKWebView
    private let connectionLabel = NSTextField(labelWithString: "Connecting…")

    init(dashboardURL: URL, actions: DashboardMenuActions) {
        self.dashboardURL = dashboardURL
        self.actions = actions
        navigationPolicy = DashboardNavigationPolicy(dashboardURL: dashboardURL)
        let configuration = WKWebViewConfiguration()
        configuration.websiteDataStore = .default()
        webView = WKWebView(frame: .zero, configuration: configuration)

        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 1180, height: 780),
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered,
            defer: false
        )
        window.title = "Redline"
        window.minSize = NSSize(width: 760, height: 520)
        window.contentView = webView
        window.setFrameAutosaveName("RedlineDashboardWindow")
        super.init(window: window)

        webView.navigationDelegate = self
        let toolbar = NSToolbar(identifier: Self.toolbarIdentifier)
        toolbar.delegate = self
        toolbar.displayMode = .iconOnly
        toolbar.allowsUserCustomization = false
        window.toolbar = toolbar
        window.toolbarStyle = .unified
    }

    required init?(coder: NSCoder) { nil }

    func showDashboard() {
        if webView.url == nil {
            webView.load(URLRequest(url: dashboardURL))
        }
        showWindow(nil)
        window?.makeKeyAndOrderFront(nil)
        NSApplication.shared.activate(ignoringOtherApps: true)
    }

    @objc private func reloadDashboard() {
        if webView.url == nil {
            webView.load(URLRequest(url: dashboardURL))
        } else {
            webView.reload()
        }
    }

    @objc private func openInBrowser() {
        NSWorkspace.shared.open(dashboardURL)
    }

    /// Pops the overflow menu under its toolbar button.
    @objc private func showOverflowMenu(_ sender: NSButton) {
        let menu = buildMenu()
        menu.popUp(
            positioning: nil,
            at: NSPoint(x: 0, y: sender.bounds.height + 4),
            in: sender
        )
    }

    /// The tag is the command's position in the shared list, set when the menu
    /// was built, so the two cannot disagree about which entry was clicked.
    @objc private func runMenuAction(_ sender: NSMenuItem) {
        let commands = DashboardMenu.items.compactMap(\.action)
        guard sender.tag >= 0, sender.tag < commands.count else { return }
        perform(commands[sender.tag])
    }

    private func perform(_ action: DashboardMenu.Action) {
        switch action {
        case .pairDevice: actions.pairDevice()
        case .checkForUpdates: actions.checkForUpdates()
        case .showAppSetup: actions.showAppSetup()
        case .openInBrowser: openInBrowser()
        case .openMoreTools: NSWorkspace.shared.open(ProductLinks.moreTools)
        case .openBuilderUpdates: NSWorkspace.shared.open(ProductLinks.builderUpdates)
        }
    }

    /// Builds the overflow menu from the shared definition, so this window and
    /// the menu bar cannot drift apart on what they offer.
    private func buildMenu() -> NSMenu {
        let menu = NSMenu()
        var commandIndex = 0
        for item in DashboardMenu.items {
            switch item {
            case .separator:
                menu.addItem(.separator())
            case .command(let title, _):
                let menuItem = NSMenuItem(
                    title: title,
                    action: #selector(runMenuAction(_:)),
                    keyEquivalent: ""
                )
                menuItem.target = self
                menuItem.tag = commandIndex
                menu.addItem(menuItem)
                commandIndex += 1
            }
        }
        return menu
    }

    func webView(_ webView: WKWebView, didStartProvisionalNavigation navigation: WKNavigation!) {
        connectionLabel.stringValue = "Connecting…"
        connectionLabel.textColor = .secondaryLabelColor
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        connectionLabel.stringValue = "Local service connected"
        connectionLabel.textColor = .systemGreen
    }

    func webView(
        _ webView: WKWebView,
        decidePolicyFor navigationAction: WKNavigationAction
    ) async -> WKNavigationActionPolicy {
        guard navigationAction.navigationType == .linkActivated,
              navigationPolicy.destination(for: navigationAction.request.url) == .external,
              let url = navigationAction.request.url else {
            return .allow
        }
        NSWorkspace.shared.open(url)
        return .cancel
    }

    func webView(
        _ webView: WKWebView,
        didFailProvisionalNavigation navigation: WKNavigation!,
        withError error: any Error
    ) {
        showConnectionFailure()
    }

    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: any Error) {
        showConnectionFailure()
    }

    private func showConnectionFailure() {
        connectionLabel.stringValue = "Service unavailable"
        connectionLabel.textColor = .systemRed
    }

    func toolbarAllowedItemIdentifiers(_ toolbar: NSToolbar) -> [NSToolbarItem.Identifier] {
        [Self.refreshIdentifier, .flexibleSpace, Self.statusIdentifier, Self.browserIdentifier, Self.menuIdentifier]
    }

    func toolbarDefaultItemIdentifiers(_ toolbar: NSToolbar) -> [NSToolbarItem.Identifier] {
        [Self.refreshIdentifier, .flexibleSpace, Self.statusIdentifier, Self.browserIdentifier, Self.menuIdentifier]
    }

    func toolbar(
        _ toolbar: NSToolbar,
        itemForItemIdentifier itemIdentifier: NSToolbarItem.Identifier,
        willBeInsertedIntoToolbar flag: Bool
    ) -> NSToolbarItem? {
        switch itemIdentifier {
        case Self.refreshIdentifier:
            let item = NSToolbarItem(itemIdentifier: itemIdentifier)
            item.label = "Refresh"
            item.image = NSImage(systemSymbolName: "arrow.clockwise", accessibilityDescription: "Refresh dashboard")
            item.target = self
            item.action = #selector(reloadDashboard)
            return item
        case Self.statusIdentifier:
            connectionLabel.font = .systemFont(ofSize: 11)
            connectionLabel.alignment = .right
            let item = NSToolbarItem(itemIdentifier: itemIdentifier)
            item.label = "Connection"
            item.view = connectionLabel
            return item
        case Self.browserIdentifier:
            let item = NSToolbarItem(itemIdentifier: itemIdentifier)
            item.label = "Open in Browser"
            item.image = NSImage(systemSymbolName: "safari", accessibilityDescription: "Open dashboard in browser")
            item.target = self
            item.action = #selector(openInBrowser)
            return item
        case Self.menuIdentifier:
            // A plain item hosting a button rather than NSMenuToolbarItem,
            // which did not appear in this toolbar at runtime. A button that
            // pops its own menu is one less framework behaviour to rely on,
            // and it renders identically.
            let button = NSButton(
                image: NSImage(
                    systemSymbolName: "ellipsis.circle",
                    accessibilityDescription: "More actions"
                ) ?? NSImage(),
                target: self,
                action: #selector(showOverflowMenu(_:))
            )
            button.bezelStyle = .texturedRounded
            button.setAccessibilityLabel("More actions")
            let item = NSToolbarItem(itemIdentifier: itemIdentifier)
            item.label = "More"
            item.view = button
            // Keeps the item reachable from the overflow chevron when the
            // window is too narrow to show every button.
            item.menuFormRepresentation = NSMenuItem(
                title: "More",
                action: nil,
                keyEquivalent: ""
            )
            item.menuFormRepresentation?.submenu = buildMenu()
            return item
        default:
            return nil
        }
    }
}

/// Handlers the dashboard window calls when its menu is used.
///
/// Every field is required. Defaulting them to no-ops meant a caller that
/// forgot one got a menu entry that looked alive and did nothing, with no
/// compiler or test complaint -- and the menu's own tests only assert its
/// contents, so nothing else would have caught it.
@MainActor
struct DashboardMenuActions {
    let pairDevice: () -> Void
    let checkForUpdates: () -> Void
    let showAppSetup: () -> Void
}
