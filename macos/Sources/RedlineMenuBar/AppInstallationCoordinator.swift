import AppKit
import Darwin
import RedlineKit
import ServiceManagement

@MainActor
final class AppInstallationCoordinator {
    private enum Keys {
        static let preferredConfigPath = "RedlinePreferredConfigPath"
        static let presentedFirstRun = "RedlinePresentedFirstRun"
    }

    let configURL: URL
    let createdStarterConfig: Bool
    private(set) var legacyAgent: LegacyLaunchAgent?
    var onMigrationCompleted: (@MainActor () async -> Void)?

    private let client: RedlineAPIClient
    private let defaults: UserDefaults
    private let supportDirectory: URL

    init(client: RedlineAPIClient, defaults: UserDefaults = .standard) throws {
        self.client = client
        self.defaults = defaults

        let home = FileManager.default.homeDirectoryForCurrentUser
        supportDirectory = home.appending(path: "Library/Application Support/Redline")
        let standardConfigURL = supportDirectory.appending(path: "redline.yaml")
        let legacyPlistURL = home.appending(path: "Library/LaunchAgents/com.jfox.redline.plist")
        legacyAgent = try LegacyLaunchAgent.discover(at: legacyPlistURL)

        let environmentPath = ProcessInfo.processInfo.environment["REDLINE_CONFIG_PATH"]
        let persistedPath = environmentPath ?? defaults.string(forKey: Keys.preferredConfigPath)
        let preferredURL = persistedPath.map { URL(fileURLWithPath: $0) }
        guard let templateURL = Bundle.main.resourceURL?.appending(path: "config.example.yaml") else {
            throw CocoaError(.fileNoSuchFile)
        }
        let resolution = InstallationResolver.resolve(
            preferredConfigURL: preferredURL,
            standardConfigURL: standardConfigURL,
            legacyConfigURL: legacyAgent?.configURL
        )
        configURL = resolution.configURL
        if resolution.origin == .starter {
            createdStarterConfig = try StarterConfigInstaller.install(
                templateURL: templateURL,
                destinationURL: standardConfigURL
            )
        } else {
            createdStarterConfig = false
        }
        defaults.set(configURL.path, forKey: Keys.preferredConfigPath)
    }

    var shouldPresentFirstRun: Bool {
        !defaults.bool(forKey: Keys.presentedFirstRun) &&
            (createdStarterConfig || legacyAgent != nil)
    }

    func presentFirstRunIfNeeded() {
        guard shouldPresentFirstRun,
              !ProcessInfo.processInfo.arguments.contains("--suppress-first-run-ui") else { return }
        defaults.set(true, forKey: Keys.presentedFirstRun)
        presentSetup()
    }

    func presentSetup() {
        if let legacyAgent {
            presentLegacyMigration(agent: legacyAgent)
        } else {
            presentLaunchAtLoginSetup(createdConfig: createdStarterConfig)
        }
    }

    private func presentLegacyMigration(agent: LegacyLaunchAgent) {
        let alert = NSAlert()
        alert.messageText = "Move service management into Redline?"
        alert.informativeText = InstallationCopy.legacyMigrationDetail(agent: agent)
        alert.alertStyle = .informational
        alert.addButton(withTitle: "Migrate to Redline")
        alert.addButton(withTitle: "Keep Existing Service")
        alert.addButton(withTitle: "Cancel")
        activate()
        guard alert.runModal() == .alertFirstButtonReturn else { return }
        Task { await migrate(agent: agent) }
    }

    private func presentLaunchAtLoginSetup(createdConfig: Bool) {
        let service = SMAppService.mainApp
        let alert = NSAlert()
        alert.messageText = createdConfig ? "Redline is ready" : "Redline app setup"
        let configNote = createdConfig
            ? "A safe starter configuration was created at \(configURL.path). Automatic dispatch is off until you enable it."
            : "Configuration: \(configURL.path)"
        alert.informativeText = "\(configNote)\n\nLaunch at Login keeps usage monitoring and queued work available after you sign in."
        alert.alertStyle = .informational
        if service.status == .enabled {
            alert.addButton(withTitle: "Done")
        } else if service.status == .requiresApproval {
            alert.addButton(withTitle: "Open Login Items")
            alert.addButton(withTitle: "Later")
        } else {
            alert.addButton(withTitle: "Enable Launch at Login")
            alert.addButton(withTitle: "Not Now")
        }
        activate()
        guard alert.runModal() == .alertFirstButtonReturn, service.status != .enabled else { return }
        if service.status == .requiresApproval {
            SMAppService.openSystemSettingsLoginItems()
            return
        }
        do {
            try service.register()
            if service.status == .requiresApproval {
                presentApprovalRequired()
            } else {
                presentMessage(
                    title: "Launch at Login enabled",
                    detail: "Redline will start automatically after you sign in."
                )
            }
        } catch {
            presentMessage(title: "Could not enable Launch at Login", detail: error.localizedDescription)
        }
    }

    private func migrate(agent: LegacyLaunchAgent) async {
        if let snapshot = try? await client.dashboard(), snapshot.health.activeRuns > 0 {
            presentMessage(
                title: "Migration deferred",
                detail: "A Redline task is currently running. Try again after it completes so its workspace and logs are not interrupted."
            )
            return
        }

        let loginService = SMAppService.mainApp
        do {
            if loginService.status == .requiresApproval {
                presentApprovalRequired()
                return
            }
            if loginService.status != .enabled {
                try loginService.register()
            }
            guard loginService.status == .enabled else {
                presentApprovalRequired()
                return
            }

            let backupDirectory = supportDirectory.appending(path: "Legacy LaunchAgents")
            let plan = LegacyMigrationPlan.make(
                agent: agent,
                userID: getuid(),
                backupDirectory: backupDirectory
            )
            try await Task.detached {
                try LegacyMigrationExecutor.execute(plan: plan, agent: agent) { arguments in
                    try Self.runLaunchctl(arguments: arguments)
                }
            }.value

            defaults.set(agent.configURL.path, forKey: Keys.preferredConfigPath)
            legacyAgent = nil
            await onMigrationCompleted?()
            presentMessage(
                title: "Redline now owns the service",
                detail: InstallationCopy.migrationCompletedDetail(backupURL: plan.backupURL)
            )
        } catch {
            presentMessage(title: "Migration did not complete", detail: error.localizedDescription)
        }
    }

    nonisolated private static func runLaunchctl(arguments: [String]) throws -> LegacyLaunchctlResult {
        let process = Process()
        let errorPipe = Pipe()
        process.executableURL = URL(fileURLWithPath: "/bin/launchctl")
        process.arguments = arguments
        process.standardOutput = FileHandle.nullDevice
        process.standardError = errorPipe
        try process.run()
        process.waitUntilExit()
        let data = errorPipe.fileHandleForReading.readDataToEndOfFile()
        return LegacyLaunchctlResult(
            status: process.terminationStatus,
            error: String(decoding: data, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
        )
    }

    private func presentApprovalRequired() {
        let alert = NSAlert()
        alert.messageText = "Approve Redline in Login Items"
        alert.informativeText = "macOS requires approval before Redline can replace the legacy background service. Enable Redline under “Allow in the Background,” then choose App Setup again."
        alert.alertStyle = .informational
        alert.addButton(withTitle: "Open Login Items")
        alert.addButton(withTitle: "Later")
        activate()
        if alert.runModal() == .alertFirstButtonReturn {
            SMAppService.openSystemSettingsLoginItems()
        }
    }

    private func presentMessage(title: String, detail: String) {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = detail
        alert.alertStyle = .informational
        alert.addButton(withTitle: "OK")
        activate()
        alert.runModal()
    }

    private func activate() {
        NSApplication.shared.activate(ignoringOtherApps: true)
    }
}
