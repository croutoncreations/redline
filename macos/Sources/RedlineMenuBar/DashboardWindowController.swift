import AppKit
import RedlineKit
import WebKit

@MainActor
final class DashboardWindowController: NSWindowController, WKNavigationDelegate, WKUIDelegate, NSToolbarDelegate {
    private static let toolbarIdentifier = NSToolbar.Identifier("RedlineDashboardToolbar")
    private static let refreshIdentifier = NSToolbarItem.Identifier("RedlineRefresh")
    private static let statusIdentifier = NSToolbarItem.Identifier("RedlineConnectionStatus")
    private static let browserIdentifier = NSToolbarItem.Identifier("RedlineOpenBrowser")

    private let dashboardURL: URL
    private let navigationPolicy: DashboardNavigationPolicy
    private let webView: WKWebView
    private let connectionLabel = NSTextField(labelWithString: "Connecting…")

    init(dashboardURL: URL) {
        self.dashboardURL = dashboardURL
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
        // Without a UI delegate WKWebView silently suppresses alert()/confirm(),
        // and confirm() returns false — which made dashboard Delete buttons no-ops.
        webView.uiDelegate = self
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

    func webView(
        _ webView: WKWebView,
        runJavaScriptAlertPanelWithMessage message: String,
        initiatedByFrame frame: WKFrameInfo
    ) async {
        let alert = NSAlert()
        alert.messageText = message
        alert.addButton(withTitle: "OK")
        _ = await present(alert)
    }

    func webView(
        _ webView: WKWebView,
        runJavaScriptConfirmPanelWithMessage message: String,
        initiatedByFrame frame: WKFrameInfo
    ) async -> Bool {
        let alert = NSAlert()
        alert.messageText = message
        alert.addButton(withTitle: "OK")
        alert.addButton(withTitle: "Cancel")
        return await present(alert) == .alertFirstButtonReturn
    }

    private func present(_ alert: NSAlert) async -> NSApplication.ModalResponse {
        guard let window, window.isVisible else { return alert.runModal() }
        return await alert.beginSheetModal(for: window)
    }

    private func showConnectionFailure() {
        connectionLabel.stringValue = "Service unavailable"
        connectionLabel.textColor = .systemRed
    }

    func toolbarAllowedItemIdentifiers(_ toolbar: NSToolbar) -> [NSToolbarItem.Identifier] {
        [Self.refreshIdentifier, .flexibleSpace, Self.statusIdentifier, Self.browserIdentifier]
    }

    func toolbarDefaultItemIdentifiers(_ toolbar: NSToolbar) -> [NSToolbarItem.Identifier] {
        [Self.refreshIdentifier, .flexibleSpace, Self.statusIdentifier, Self.browserIdentifier]
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
        default:
            return nil
        }
    }
}
