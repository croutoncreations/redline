import Foundation

public struct InstallationResolution: Equatable, Sendable {
    public enum Origin: Equatable, Sendable {
        case persisted
        case standard
        case legacy
        case starter
    }

    public let configURL: URL
    public let origin: Origin

    public init(configURL: URL, origin: Origin) {
        self.configURL = configURL
        self.origin = origin
    }
}

public enum InstallationResolver {
    public static func resolve(
        preferredConfigURL: URL?,
        standardConfigURL: URL,
        legacyConfigURL: URL?
    ) -> InstallationResolution {
        let manager = FileManager.default
        if let preferredConfigURL, manager.fileExists(atPath: preferredConfigURL.path) {
            return InstallationResolution(configURL: preferredConfigURL, origin: .persisted)
        }
        if let legacyConfigURL, manager.fileExists(atPath: legacyConfigURL.path) {
            return InstallationResolution(configURL: legacyConfigURL, origin: .legacy)
        }
        if manager.fileExists(atPath: standardConfigURL.path) {
            return InstallationResolution(configURL: standardConfigURL, origin: .standard)
        }
        return InstallationResolution(configURL: standardConfigURL, origin: .starter)
    }
}

public enum StarterConfigInstaller {
    @discardableResult
    public static func install(templateURL: URL, destinationURL: URL) throws -> Bool {
        let manager = FileManager.default
        if manager.fileExists(atPath: destinationURL.path) {
            return false
        }
        guard manager.fileExists(atPath: templateURL.path) else {
            throw CocoaError(.fileNoSuchFile)
        }
        try manager.createDirectory(
            at: destinationURL.deletingLastPathComponent(),
            withIntermediateDirectories: true
        )
        try manager.copyItem(at: templateURL, to: destinationURL)
        try manager.setAttributes([.posixPermissions: 0o600], ofItemAtPath: destinationURL.path)
        return true
    }
}

public struct LegacyLaunchAgent: Equatable, Sendable {
    public let label: String
    public let plistURL: URL
    public let configURL: URL
    public let executableURL: URL?

    public init(
        label: String,
        plistURL: URL,
        configURL: URL,
        executableURL: URL? = nil
    ) {
        self.label = label
        self.plistURL = plistURL
        self.configURL = configURL
        self.executableURL = executableURL
    }

    public static func discover(at plistURL: URL) throws -> LegacyLaunchAgent? {
        let manager = FileManager.default
        guard manager.fileExists(atPath: plistURL.path) else { return nil }
        let data = try Data(contentsOf: plistURL)
        guard let payload = try PropertyListSerialization.propertyList(
            from: data,
            options: [],
            format: nil
        ) as? [String: Any],
              let label = payload["Label"] as? String,
              !label.isEmpty,
              let arguments = payload["ProgramArguments"] as? [String],
              let configPath = configPath(in: arguments),
              configPath.hasPrefix("/") else {
            return nil
        }
        return LegacyLaunchAgent(
            label: label,
            plistURL: plistURL,
            configURL: URL(fileURLWithPath: configPath),
            executableURL: arguments.first.flatMap {
                $0.hasPrefix("/") ? URL(fileURLWithPath: $0) : nil
            }
        )
    }

    private static func configPath(in arguments: [String]) -> String? {
        for (index, argument) in arguments.enumerated() {
            if argument == "--config", arguments.indices.contains(index + 1) {
                return arguments[index + 1]
            }
            if argument.hasPrefix("--config=") {
                return String(argument.dropFirst("--config=".count))
            }
        }
        return nil
    }
}

public struct InstallationIssue: Equatable, Sendable {
    public let title: String
    public let detail: String
    public let actionTitle: String

    public init(title: String, detail: String, actionTitle: String) {
        self.title = title
        self.detail = detail
        self.actionTitle = actionTitle
    }
}

public enum InstallationSafety {
    public static func issue(for legacyAgent: LegacyLaunchAgent?) -> InstallationIssue? {
        guard let legacyAgent else { return nil }
        let executableDetail = legacyAgent.executableURL.map {
            "\n\nConfigured executable: \($0.path)"
        } ?? ""
        return InstallationIssue(
            title: "Legacy background service configured",
            detail: "Redline found another Redline service owner configured through \(legacyAgent.label). Multiple service owners can repeatedly launch competing or incompatible binaries. Migrate it into the app before continuing unattended operation.\(executableDetail)",
            actionTitle: "Review service setup…"
        )
    }
}

public struct LegacyMigrationPlan: Equatable, Sendable {
    public let launchctlArguments: [String]
    public let backupURL: URL
    public let configURL: URL

    public static func make(
        agent: LegacyLaunchAgent,
        userID: uid_t,
        backupDirectory: URL,
        timestamp: Date = Date()
    ) -> LegacyMigrationPlan {
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.timeZone = TimeZone(secondsFromGMT: 0)
        formatter.dateFormat = "yyyyMMdd-HHmmss"
        let filename = "\(agent.label)-\(formatter.string(from: timestamp)).plist"
        return LegacyMigrationPlan(
            launchctlArguments: ["bootout", "gui/\(userID)/\(agent.label)"],
            backupURL: backupDirectory.appending(path: filename),
            configURL: agent.configURL
        )
    }
}

public struct LegacyLaunchctlResult: Equatable, Sendable {
    public let status: Int32
    public let error: String

    public init(status: Int32, error: String) {
        self.status = status
        self.error = error
    }
}

public struct LegacyMigrationError: LocalizedError, Equatable, Sendable {
    public let message: String
    public var errorDescription: String? { message }

    public init(message: String) {
        self.message = message
    }
}

public enum LegacyMigrationExecutor {
    public static func execute(
        plan: LegacyMigrationPlan,
        agent: LegacyLaunchAgent,
        runLaunchctl: ([String]) throws -> LegacyLaunchctlResult,
        movePlist: (URL, URL) throws -> Void = { source, destination in
            try FileManager.default.moveItem(at: source, to: destination)
        }
    ) throws {
        guard let domainTarget = plan.launchctlArguments.last else {
            throw LegacyMigrationError(message: "The launchctl migration target is missing.")
        }
        let manager = FileManager.default
        try manager.createDirectory(
            at: plan.backupURL.deletingLastPathComponent(),
            withIntermediateDirectories: true
        )
        guard !manager.fileExists(atPath: plan.backupURL.path) else {
            throw LegacyMigrationError(
                message: "A migration backup already exists at \(plan.backupURL.path)."
            )
        }

        let inspect = try runLaunchctl(["print", domainTarget])
        var stoppedLoadedAgent = false
        if inspect.status == 0 {
            let bootout = try runLaunchctl(plan.launchctlArguments)
            guard bootout.status == 0 else {
                throw LegacyMigrationError(
                    message: bootout.error.isEmpty
                        ? "launchctl could not stop the existing service."
                        : bootout.error
                )
            }
            stoppedLoadedAgent = true
        } else if !inspect.error.localizedCaseInsensitiveContains("could not find service") {
            throw LegacyMigrationError(
                message: inspect.error.isEmpty
                    ? "launchctl could not inspect the existing service."
                    : inspect.error
            )
        }

        do {
            try movePlist(agent.plistURL, plan.backupURL)
        } catch {
            var restoration = ""
            if stoppedLoadedAgent {
                let domain = domainTarget.split(separator: "/").dropLast().joined(separator: "/")
                let result = try? runLaunchctl(["bootstrap", domain, agent.plistURL.path])
                restoration = result?.status == 0
                    ? " The legacy service was restarted."
                    : " The legacy service could not be restarted automatically."
            }
            throw LegacyMigrationError(
                message: "The legacy plist could not be backed up: \(error.localizedDescription).\(restoration)"
            )
        }
    }
}

public enum InstallationCopy {
    public static func legacyMigrationDetail(agent: LegacyLaunchAgent) -> String {
        """
        Redline found the existing \(agent.label) LaunchAgent and is using its configuration at:

        \(agent.configURL.path)

        Migration enables Launch at Login, stops the old service, and moves its plist into a recoverable backup. Your database, queue, run history, and configuration stay in place.
        """
    }

    public static func migrationCompletedDetail(backupURL: URL) -> String {
        "The old LaunchAgent was stopped and backed up at:\n\n\(backupURL.path)"
    }
}
