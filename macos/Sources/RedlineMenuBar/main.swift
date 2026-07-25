import AppKit
import RedlineKit

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    private var menuBarController: MenuBarController?
    private var installationCoordinator: AppInstallationCoordinator?
    private var installationFailure: String?

    func applicationDidFinishLaunching(_ notification: Notification) {
        let apiURL = URL(string: ProcessInfo.processInfo.environment["REDLINE_API_URL"] ?? "http://127.0.0.1:7436")!
        let client = RedlineAPIClient(baseURL: apiURL)
        let installation: AppInstallationCoordinator?
        do {
            installation = try AppInstallationCoordinator(client: client)
        } catch {
            installation = nil
            installationFailure = error.localizedDescription
        }
        installationCoordinator = installation
        let configURL = installation?.configURL ?? standardConfigURL
        let supervisor = ServiceSupervisor(
            client: client,
            launchConfiguration: launchConfiguration(apiURL: apiURL, configURL: configURL)
        )
        let controller = MenuBarController(
            apiURL: apiURL,
            supervisor: supervisor,
            showAppSetup: { [weak self, weak installation] in
                if let installation { installation.presentSetup() }
                else { self?.presentInstallationFailure() }
            }
        )
        installation?.onMigrationCompleted = { [weak controller] in
            await controller?.reconnectAfterServiceMigration()
        }
        menuBarController = controller
        controller.start()
        Task {
            try? await Task.sleep(for: .milliseconds(900))
            if let installation { installation.presentFirstRunIfNeeded() }
            else { presentInstallationFailure() }
        }
        if ProcessInfo.processInfo.arguments.contains("--show-dashboard") {
            Task {
                try? await Task.sleep(for: .milliseconds(500))
                controller.showDashboard()
            }
        }
        if ProcessInfo.processInfo.arguments.contains("--show-popover") {
            Task {
                try? await Task.sleep(for: .milliseconds(700))
                controller.showPopover()
            }
        }
        if ProcessInfo.processInfo.arguments.contains("--show-popover-preview") {
            Task {
                try? await Task.sleep(for: .milliseconds(700))
                controller.showPopoverPreview()
            }
        }
    }

    func applicationWillTerminate(_ notification: Notification) {
        menuBarController?.stop()
    }

    func applicationShouldHandleReopen(
        _ sender: NSApplication,
        hasVisibleWindows flag: Bool
    ) -> Bool {
        if !flag {
            menuBarController?.showDashboard()
        }
        return true
    }

    private var standardConfigURL: URL {
        FileManager.default.homeDirectoryForCurrentUser
            .appending(path: "Library/Application Support/Redline/redline.yaml")
    }

    private func launchConfiguration(apiURL: URL, configURL: URL) -> ServiceLaunchConfiguration? {
        guard let executableURL = Bundle.main.resourceURL?.appending(path: "bin/redline"),
              FileManager.default.isExecutableFile(atPath: executableURL.path) else {
            return nil
        }
        return try? ServiceLaunchConfiguration.validated(
            executableURL: executableURL,
            configURL: configURL,
            apiURL: apiURL
        )
    }

    private func presentInstallationFailure() {
        guard let installationFailure else { return }
        let alert = NSAlert()
        alert.messageText = "Redline setup could not finish"
        alert.informativeText = installationFailure
        alert.alertStyle = .warning
        alert.addButton(withTitle: "OK")
        NSApplication.shared.activate(ignoringOtherApps: true)
        alert.runModal()
    }
}

let application = NSApplication.shared
let delegate = AppDelegate()
application.delegate = delegate
application.setActivationPolicy(.accessory)
application.run()
