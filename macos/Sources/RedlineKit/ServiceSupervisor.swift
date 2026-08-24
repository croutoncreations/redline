import Foundation

@MainActor
public final class ServiceSupervisor {
    private let client: RedlineAPIClient
    private let launchConfiguration: ServiceLaunchConfiguration?
    private var ownedProcess: Process?
    private var ownedLogHandles: [FileHandle] = []

    public init(client: RedlineAPIClient, launchConfiguration: ServiceLaunchConfiguration?) {
        self.client = client
        self.launchConfiguration = launchConfiguration
    }

    public func ensureRunning() async {
        if await client.isCompatible() {
            return
        }
        guard let launchConfiguration else {
            return
        }
        guard FileManager.default.fileExists(atPath: launchConfiguration.configURL.path) else {
            return
        }
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
            try process.run()
            ownedProcess = process

            for _ in 0..<20 {
                try await Task.sleep(for: .milliseconds(250))
                if await client.isCompatible() {
                    if !process.isRunning {
                        ownedProcess = nil
                        closeLogHandles()
                    }
                    return
                }
                if !process.isRunning { break }
            }
            process.terminate()
            ownedProcess = nil
            closeLogHandles()
        } catch {
            closeLogHandles()
        }
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
        let directory = FileManager.default.homeDirectoryForCurrentUser
            .appending(path: "Library/Logs/Redline")
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
