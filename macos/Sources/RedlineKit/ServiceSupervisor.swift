import Foundation

/// Why the embedded service is not reachable after the supervisor tried to
/// start it. `nil` from `ensureRunning()` means the service is up.
public struct ServiceStartupFailure: Equatable, Sendable {
    /// Short, user-facing summary suitable for the menu bar popover.
    public let summary: String
    /// Last line the service wrote to stderr before exiting, when available.
    /// This is where `redline serve` reports configuration errors.
    public let detail: String?
    /// Log the operator can open for the full story.
    public let logURL: URL

    public init(summary: String, detail: String?, logURL: URL) {
        self.summary = summary
        self.detail = detail
        self.logURL = logURL
    }
}

@MainActor
public final class ServiceSupervisor {
    private let client: RedlineAPIClient
    private let launchConfiguration: ServiceLaunchConfiguration?
    private var ownedProcess: Process?
    private var ownedLogHandles: [FileHandle] = []

    /// Set when the most recent `ensureRunning()` launched the service and it
    /// exited or never answered. Cleared once the service is reachable.
    public private(set) var lastStartupFailure: ServiceStartupFailure?

    public init(client: RedlineAPIClient, launchConfiguration: ServiceLaunchConfiguration?) {
        self.client = client
        self.launchConfiguration = launchConfiguration
    }

    @discardableResult
    public func ensureRunning() async -> ServiceStartupFailure? {
        // Every path through here reflects the *current* state: a stale
        // diagnostic from an earlier launch must not outlive a call that did
        // not attempt to launch.
        lastStartupFailure = nil
        if await client.isCompatible() {
            return nil
        }
        guard let launchConfiguration else {
            return nil
        }
        guard FileManager.default.fileExists(atPath: launchConfiguration.configURL.path) else {
            return nil
        }
        let failure = await launchAndWait(launchConfiguration)
        lastStartupFailure = failure
        return failure
    }

    private func launchAndWait(_ launchConfiguration: ServiceLaunchConfiguration) async -> ServiceStartupFailure? {
        let stderrURL = Self.logDirectory.appending(path: "app-service.stderr.log")
        do {
            try FileManager.default.createDirectory(
                at: launchConfiguration.workingDirectory,
                withIntermediateDirectories: true
            )
            let process = Process()
            process.executableURL = launchConfiguration.executableURL
            process.arguments = launchConfiguration.arguments
            process.currentDirectoryURL = launchConfiguration.workingDirectory
            process.environment = Self.serviceEnvironment()
            ownedLogHandles = try Self.openLogHandles()
            process.standardOutput = ownedLogHandles[0]
            process.standardError = ownedLogHandles[1]
            let stderrOffset = (try? ownedLogHandles[1].offset()) ?? 0
            try process.run()
            ownedProcess = process

            for _ in 0..<20 {
                try await Task.sleep(for: .milliseconds(250))
                if await client.isCompatible() {
                    if !process.isRunning {
                        ownedProcess = nil
                        closeLogHandles()
                    }
                    return nil
                }
                if !process.isRunning { break }
            }
            let exitedEarly = !process.isRunning
            let exitCode = process.terminationStatus
            if process.isRunning {
                process.terminate()
            }
            ownedProcess = nil
            closeLogHandles()
            let detail = Self.lastStderrLine(at: stderrURL, after: stderrOffset)
            return ServiceStartupFailure(
                summary: exitedEarly
                    ? "The service exited during startup (status \(exitCode))"
                    : "The service started but did not answer in time",
                detail: detail,
                logURL: stderrURL
            )
        } catch {
            closeLogHandles()
            return ServiceStartupFailure(
                summary: "The service could not be launched",
                detail: error.localizedDescription,
                logURL: stderrURL
            )
        }
    }

    /// Returns the last non-empty stderr line the service wrote during this
    /// launch attempt. `redline serve` prints one diagnostic and exits when
    /// the configuration cannot be loaded, so this is usually the whole story.
    nonisolated static func lastStderrLine(at url: URL, after offset: UInt64) -> String? {
        guard let handle = try? FileHandle(forReadingFrom: url) else { return nil }
        defer { try? handle.close() }
        guard (try? handle.seek(toOffset: offset)) != nil,
              let data = try? handle.readToEnd(),
              let text = String(data: data, encoding: .utf8) else { return nil }
        // yaml unmarshal errors span two lines (a generic header and the
        // indented problem); the last non-empty line is the useful one either way.
        return text.split(whereSeparator: \.isNewline)
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .last { !$0.isEmpty }
    }

    private nonisolated static var logDirectory: URL {
        FileManager.default.homeDirectoryForCurrentUser.appending(path: "Library/Logs/Redline")
    }

    public func stopOwnedService() {
        guard let ownedProcess, ownedProcess.isRunning else { return }
        ownedProcess.terminate()
        self.ownedProcess = nil
        closeLogHandles()
    }

    private static func serviceEnvironment() -> [String: String] {
        var environment = ProcessInfo.processInfo.environment
        let standardPaths = [
            FileManager.default.homeDirectoryForCurrentUser.appending(path: ".gatepost/bin").path,
            FileManager.default.homeDirectoryForCurrentUser.appending(path: ".local/bin").path,
            "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin",
        ]
        let inherited = environment["PATH"].map { [$0] } ?? []
        environment["PATH"] = (standardPaths + inherited).joined(separator: ":")
        return environment
    }

    private static func openLogHandles() throws -> [FileHandle] {
        let directory = logDirectory
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        return try ["app-service.stdout.log", "app-service.stderr.log"].map { name in
            let url = directory.appending(path: name)
            if !FileManager.default.fileExists(atPath: url.path) {
                FileManager.default.createFile(atPath: url.path, contents: nil)
            }
            let handle = try FileHandle(forWritingTo: url)
            try handle.seekToEnd()
            return handle
        }
    }

    private func closeLogHandles() {
        for handle in ownedLogHandles { try? handle.close() }
        ownedLogHandles = []
    }
}
