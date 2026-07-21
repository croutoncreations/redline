import Foundation

public struct ServiceLaunchConfiguration: Sendable {
    public enum Error: Swift.Error, Equatable { case unsupportedAPIURL }

    public let executableURL: URL
    public let configURL: URL
    public let apiURL: URL

    public init(executableURL: URL, configURL: URL, apiURL: URL) {
        self.executableURL = executableURL
        self.configURL = configURL
        self.apiURL = apiURL
    }

    public static func validated(executableURL: URL, configURL: URL, apiURL: URL) throws -> Self {
        guard apiURL.scheme == "http",
              apiURL.host == "127.0.0.1" || apiURL.host == "localhost",
              apiURL.port != nil else {
            throw Error.unsupportedAPIURL
        }
        return Self(executableURL: executableURL, configURL: configURL, apiURL: apiURL)
    }

    public var arguments: [String] {
        ["--config", configURL.path, "serve", "--listen", apiURL.authority]
    }

    public var workingDirectory: URL { configURL.deletingLastPathComponent() }
}

private extension URL {
    var authority: String {
        let host = self.host ?? "127.0.0.1"
        return port.map { "\(host):\($0)" } ?? host
    }
}
