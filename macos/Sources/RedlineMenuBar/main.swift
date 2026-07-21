import AppKit
import RedlineKit

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    private var menuBarController: MenuBarController?

    func applicationDidFinishLaunching(_ notification: Notification) {
        let apiURL = URL(string: ProcessInfo.processInfo.environment["REDLINE_API_URL"] ?? "http://127.0.0.1:7436")!
        let client = RedlineAPIClient(baseURL: apiURL)
        let supervisor = ServiceSupervisor(client: client, launchConfiguration: launchConfiguration(apiURL: apiURL))
        let controller = MenuBarController(apiURL: apiURL, supervisor: supervisor)
        menuBarController = controller
        controller.start()
    }

    func applicationWillTerminate(_ notification: Notification) {
        menuBarController?.stop()
    }

    private func launchConfiguration(apiURL: URL) -> ServiceLaunchConfiguration? {
        guard let executableURL = Bundle.main.resourceURL?.appending(path: "bin/redline"),
              FileManager.default.isExecutableFile(atPath: executableURL.path) else {
            return nil
        }
        let configURL = FileManager.default.homeDirectoryForCurrentUser
            .appending(path: "Library/Application Support/Redline/redline.yaml")
        return try? ServiceLaunchConfiguration.validated(
            executableURL: executableURL,
            configURL: configURL,
            apiURL: apiURL
        )
    }
}

let application = NSApplication.shared
let delegate = AppDelegate()
application.delegate = delegate
application.setActivationPolicy(.accessory)
application.run()
