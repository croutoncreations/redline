import Foundation
import Testing
@testable import RedlineKit

@Test func updateConfigurationRequiresSecureFeedAndEd25519Key() {
    let validKey = Data(repeating: 7, count: 32).base64EncodedString()
    let configured = UpdateConfiguration(infoDictionary: [
        "SUFeedURL": "https://updates.redline.example/appcast.xml",
        "SUPublicEDKey": validKey,
    ])
    #expect(configured?.feedURL.absoluteString == "https://updates.redline.example/appcast.xml")
    #expect(configured?.publicEDKey == validKey)

    #expect(UpdateConfiguration(infoDictionary: [:]) == nil)
    #expect(UpdateConfiguration(infoDictionary: [
        "SUFeedURL": "http://updates.redline.example/appcast.xml",
        "SUPublicEDKey": validKey,
    ]) == nil)
    #expect(UpdateConfiguration(infoDictionary: [
        "SUFeedURL": "https://updates.redline.example/appcast.xml?channel=stable",
        "SUPublicEDKey": validKey,
    ]) == nil)
    #expect(UpdateConfiguration(infoDictionary: [
        "SUFeedURL": "https://updates.redline.example/appcast.xml",
        "SUPublicEDKey": "",
    ]) == nil)
    #expect(UpdateConfiguration(infoDictionary: [
        "SUFeedURL": "https://updates.redline.example/appcast.xml",
        "SUPublicEDKey": Data(repeating: 7, count: 31).base64EncodedString(),
    ]) == nil)
}

@Test func updaterStartsOnlyForSecureConfiguredBuilds() {
    let validKey = Data(repeating: 7, count: 32).base64EncodedString()
    #expect(!UpdateStartupPolicy.shouldStartUpdater(infoDictionary: [:]))
    #expect(!UpdateStartupPolicy.shouldStartUpdater(infoDictionary: [
        "SUFeedURL": "https://updates.redline.example/appcast.xml",
    ]))
    #expect(UpdateStartupPolicy.shouldStartUpdater(infoDictionary: [
        "SUFeedURL": "https://updates.redline.example/appcast.xml",
        "SUPublicEDKey": validKey,
    ]))
}

@Test func agentPermissionGuidanceExplainsDynamicChildProcessAttribution() {
    #expect(AgentPermissionGuidance.title == "Agent access and macOS permissions")
    #expect(AgentPermissionGuidance.summary.contains("scheduled agent"))
    #expect(AgentPermissionGuidance.summary.contains("Redline"))
    #expect(AgentPermissionGuidance.detail.contains("job and harness"))
    #expect(AgentPermissionGuidance.detail.contains("only when access is needed"))
}

@Test func installationResolutionPrefersPersistedThenLegacyThenDefaultConfig() throws {
    let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
    defer { try? FileManager.default.removeItem(at: root) }
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    let preferred = root.appending(path: "preferred.yaml")
    let standard = root.appending(path: "standard.yaml")
    let legacy = root.appending(path: "legacy.yaml")
    let template = root.appending(path: "template.yaml")
    for url in [preferred, standard, legacy, template] {
        try "scheduler:\n  enabled: false\n".write(to: url, atomically: true, encoding: .utf8)
    }

    #expect(InstallationResolver.resolve(
        preferredConfigURL: preferred,
        standardConfigURL: standard,
        legacyConfigURL: legacy
    ) == .init(configURL: preferred, origin: .persisted))

    try FileManager.default.removeItem(at: preferred)
    #expect(InstallationResolver.resolve(
        preferredConfigURL: preferred,
        standardConfigURL: standard,
        legacyConfigURL: legacy
    ) == .init(configURL: legacy, origin: .legacy))

    try FileManager.default.removeItem(at: legacy)
    #expect(InstallationResolver.resolve(
        preferredConfigURL: preferred,
        standardConfigURL: standard,
        legacyConfigURL: legacy
    ) == .init(configURL: standard, origin: .standard))

    try FileManager.default.removeItem(at: standard)
    #expect(InstallationResolver.resolve(
        preferredConfigURL: preferred,
        standardConfigURL: standard,
        legacyConfigURL: legacy
    ) == .init(configURL: standard, origin: .starter))
}

@Test func starterConfigInstallationIsSafeAndDoesNotOverwrite() throws {
    let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
    defer { try? FileManager.default.removeItem(at: root) }
    let template = root.appending(path: "bundle/config.example.yaml")
    let destination = root.appending(path: "support/redline.yaml")
    try FileManager.default.createDirectory(at: template.deletingLastPathComponent(), withIntermediateDirectories: true)
    try "scheduler:\n  enabled: false\n".write(to: template, atomically: true, encoding: .utf8)

    #expect(try StarterConfigInstaller.install(templateURL: template, destinationURL: destination))
    #expect(try String(contentsOf: destination, encoding: .utf8).contains("enabled: false"))
    let attributes = try FileManager.default.attributesOfItem(atPath: destination.path)
    let permissions = try #require(attributes[.posixPermissions] as? NSNumber)
    #expect(permissions.intValue & 0o777 == 0o600)

    try "user-owned\n".write(to: destination, atomically: true, encoding: .utf8)
    #expect(try !StarterConfigInstaller.install(templateURL: template, destinationURL: destination))
    #expect(try String(contentsOf: destination, encoding: .utf8) == "user-owned\n")
}

@Test func legacyLaunchAgentDiscoveryRejectsRelativeConfigPaths() throws {
    let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
    defer { try? FileManager.default.removeItem(at: root) }
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    let plist = root.appending(path: "relative.plist")
    let payload: [String: Any] = [
        "Label": "com.example.redline",
        "ProgramArguments": ["/usr/local/bin/redline", "--config=redline.yaml", "serve"],
    ]
    let data = try PropertyListSerialization.data(fromPropertyList: payload, format: .xml, options: 0)
    try data.write(to: plist)

    #expect(try LegacyLaunchAgent.discover(at: plist) == nil)
}

@Test func legacyLaunchAgentDiscoveryFindsConfigAndLabel() throws {
    let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
    defer { try? FileManager.default.removeItem(at: root) }
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    let plist = root.appending(path: "com.example.redline.plist")
    let config = root.appending(path: "legacy redline.yaml")
    let payload: [String: Any] = [
        "Label": "com.example.redline",
        "ProgramArguments": ["/usr/local/bin/redline", "--config", config.path, "serve", "--listen", "127.0.0.1:7436"],
    ]
    let data = try PropertyListSerialization.data(fromPropertyList: payload, format: .xml, options: 0)
    try data.write(to: plist)

    let discovered = try LegacyLaunchAgent.discover(at: plist)
    let agent = try #require(discovered)
    #expect(agent.label == "com.example.redline")
    #expect(agent.configURL == config)
    #expect(agent.plistURL == plist)
    #expect(agent.executableURL == URL(fileURLWithPath: "/usr/local/bin/redline"))
}

@Test func legacyServiceIssuePersistsUntilTheAgentIsRemoved() throws {
    let plist = URL(fileURLWithPath: "/Users/test/Library/LaunchAgents/com.jfox.redline.plist")
    let config = URL(fileURLWithPath: "/Users/test/Library/Application Support/Redline/redline.yaml")
    let executable = URL(fileURLWithPath: "/Users/test/projects/redline/redline")
    let agent = LegacyLaunchAgent(
        label: "com.jfox.redline",
        plistURL: plist,
        configURL: config,
        executableURL: executable
    )

    let issue = try #require(InstallationSafety.issue(for: agent))
    #expect(issue.title == "Legacy background service configured")
    #expect(issue.detail.contains("another Redline service owner"))
    #expect(issue.detail.contains(executable.path))
    #expect(issue.actionTitle == "Review service setup…")
    #expect(InstallationSafety.issue(for: nil) == nil)
}

@Test func legacyMigrationPlanIsRecoverableAndUsesUserDomain() throws {
    let plist = URL(fileURLWithPath: "/Users/test/Library/LaunchAgents/com.jfox.redline.plist")
    let config = URL(fileURLWithPath: "/Users/test/Library/Application Support/Redline/redline.yaml")
    let agent = LegacyLaunchAgent(label: "com.jfox.redline", plistURL: plist, configURL: config)
    let backupDirectory = URL(fileURLWithPath: "/Users/test/Library/Application Support/Redline/Legacy LaunchAgents")
    let plan = LegacyMigrationPlan.make(
        agent: agent,
        userID: 501,
        backupDirectory: backupDirectory,
        timestamp: Date(timeIntervalSince1970: 1_800_000_000)
    )

    #expect(plan.launchctlArguments == ["bootout", "gui/501/com.jfox.redline"])
    #expect(plan.backupURL.deletingLastPathComponent().path == backupDirectory.path)
    #expect(plan.backupURL.lastPathComponent.hasPrefix("com.jfox.redline-"))
    #expect(plan.backupURL.pathExtension == "plist")
    #expect(plan.configURL == config)

    let detail = InstallationCopy.legacyMigrationDetail(agent: agent)
    #expect(detail.contains("com.jfox.redline"))
    #expect(detail.contains(config.path))
    #expect(!detail.contains("(agent."))
    let completed = InstallationCopy.migrationCompletedDetail(backupURL: plan.backupURL)
    #expect(completed.contains(plan.backupURL.path))
}

@Test func legacyMigrationExecutorBacksUpAnUnloadedAgent() throws {
    let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
    defer { try? FileManager.default.removeItem(at: root) }
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    let plist = root.appending(path: "agent.plist")
    try Data("legacy".utf8).write(to: plist)
    let agent = LegacyLaunchAgent(
        label: "com.example.redline",
        plistURL: plist,
        configURL: root.appending(path: "redline.yaml")
    )
    let plan = LegacyMigrationPlan.make(
        agent: agent,
        userID: 501,
        backupDirectory: root.appending(path: "backups")
    )
    var calls: [[String]] = []

    try LegacyMigrationExecutor.execute(plan: plan, agent: agent) { arguments in
        calls.append(arguments)
        return LegacyLaunchctlResult(status: 113, error: "Could not find service")
    }

    #expect(calls == [["print", "gui/501/com.example.redline"]])
    #expect(!FileManager.default.fileExists(atPath: plist.path))
    #expect(FileManager.default.fileExists(atPath: plan.backupURL.path))
}

@Test func legacyMigrationExecutorPreservesPlistWhenBootoutFails() throws {
    let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
    defer { try? FileManager.default.removeItem(at: root) }
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    let plist = root.appending(path: "agent.plist")
    try Data("legacy".utf8).write(to: plist)
    let agent = LegacyLaunchAgent(
        label: "com.example.redline",
        plistURL: plist,
        configURL: root.appending(path: "redline.yaml")
    )
    let plan = LegacyMigrationPlan.make(
        agent: agent,
        userID: 501,
        backupDirectory: root.appending(path: "backups")
    )

    #expect(throws: (any Error).self) {
        try LegacyMigrationExecutor.execute(plan: plan, agent: agent) { arguments in
            arguments.first == "print"
                ? LegacyLaunchctlResult(status: 0, error: "")
                : LegacyLaunchctlResult(status: 5, error: "bootout denied")
        }
    }
    #expect(FileManager.default.fileExists(atPath: plist.path))
    #expect(!FileManager.default.fileExists(atPath: plan.backupURL.path))
}

@Test func legacyMigrationExecutorPreflightsBackupBeforeLaunchctl() throws {
    let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
    defer { try? FileManager.default.removeItem(at: root) }
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    let plist = root.appending(path: "agent.plist")
    try Data("legacy".utf8).write(to: plist)
    let agent = LegacyLaunchAgent(
        label: "com.example.redline",
        plistURL: plist,
        configURL: root.appending(path: "redline.yaml")
    )
    let plan = LegacyMigrationPlan.make(
        agent: agent,
        userID: 501,
        backupDirectory: root.appending(path: "backups")
    )
    try FileManager.default.createDirectory(
        at: plan.backupURL.deletingLastPathComponent(),
        withIntermediateDirectories: true
    )
    try Data("collision".utf8).write(to: plan.backupURL)
    var launchctlCalled = false

    #expect(throws: (any Error).self) {
        try LegacyMigrationExecutor.execute(plan: plan, agent: agent) { _ in
            launchctlCalled = true
            return LegacyLaunchctlResult(status: 0, error: "")
        }
    }
    #expect(!launchctlCalled)
    #expect(FileManager.default.fileExists(atPath: plist.path))
}

@Test func legacyMigrationExecutorRestoresLoadedAgentWhenMoveFails() throws {
    let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
    defer { try? FileManager.default.removeItem(at: root) }
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    let plist = root.appending(path: "agent.plist")
    try Data("legacy".utf8).write(to: plist)
    let agent = LegacyLaunchAgent(
        label: "com.example.redline",
        plistURL: plist,
        configURL: root.appending(path: "redline.yaml")
    )
    let plan = LegacyMigrationPlan.make(
        agent: agent,
        userID: 501,
        backupDirectory: root.appending(path: "backups")
    )
    var calls: [[String]] = []

    #expect(throws: (any Error).self) {
        try LegacyMigrationExecutor.execute(
            plan: plan,
            agent: agent,
            runLaunchctl: { arguments in
                calls.append(arguments)
                return LegacyLaunchctlResult(status: 0, error: "")
            },
            movePlist: { _, _ in throw CocoaError(.fileWriteNoPermission) }
        )
    }
    #expect(calls == [
        ["print", "gui/501/com.example.redline"],
        ["bootout", "gui/501/com.example.redline"],
        ["bootstrap", "gui/501", plist.path],
    ])
    #expect(FileManager.default.fileExists(atPath: plist.path))
}
