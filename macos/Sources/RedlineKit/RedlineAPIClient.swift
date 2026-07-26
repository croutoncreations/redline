import Foundation

public struct RedlineAPIClient: Sendable {
    public enum Error: Swift.Error, LocalizedError {
        case invalidResponse
        case status(Int)

        public var errorDescription: String? {
            switch self {
            case .invalidResponse: "Redline returned an invalid response."
            case .status(let code): "Redline returned HTTP \(code)."
            }
        }
    }

    public let baseURL: URL
    public let token: String
    private let session: URLSession

    public init(baseURL: URL, token: String = "", session: URLSession = .shared) {
        self.baseURL = baseURL
        self.token = token
        self.session = session
    }

    public func dashboard() async throws -> DashboardSnapshot {
        try await request(baseURL.appending(path: "v1/dashboard"), method: "GET", as: DashboardSnapshot.self)
    }

    public func pauseProvider(_ providerID: String) async throws -> ProviderControlResult {
        try await controlProvider(providerID, action: "pause")
    }

    public func resumeProvider(_ providerID: String) async throws -> ProviderControlResult {
        try await controlProvider(providerID, action: "resume")
    }

    public func runLogs(runID: String, stream: RunLogStream = .stdout, tailBytes: Int = 32 * 1024) async throws -> RunLogTail {
        var components = URLComponents(url: endpoint(["v1", "runs", runID, "logs"]), resolvingAgainstBaseURL: false)!
        components.queryItems = [
            URLQueryItem(name: "stream", value: stream.rawValue),
            URLQueryItem(name: "tail_bytes", value: String(tailBytes)),
        ]
        return try await request(components.url!, method: "GET", as: RunLogTail.self)
    }

    public func isCompatible() async -> Bool {
        (try? await dashboard()) != nil
    }

    private func controlProvider(_ providerID: String, action: String) async throws -> ProviderControlResult {
        try await request(endpoint(["v1", "providers", providerID, action]), method: "POST", as: ProviderControlResult.self)
    }

    private func endpoint(_ components: [String]) -> URL {
        var allowed = CharacterSet.urlPathAllowed
        allowed.remove(charactersIn: "/")
        let encoded = components.map { $0.addingPercentEncoding(withAllowedCharacters: allowed)! }
        return URL(string: baseURL.absoluteString.trimmingCharacters(in: CharacterSet(charactersIn: "/")) + "/" + encoded.joined(separator: "/"))!
    }

    private func request<T: Decodable>(_ url: URL, method: String, as type: T.Type) async throws -> T {
        var request = URLRequest(url: url)
        request.httpMethod = method
        if !token.isEmpty {
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        if method == "POST" {
            request.httpBody = Data("{}".utf8)
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        let (data, response) = try await session.data(for: request)
        guard let response = response as? HTTPURLResponse else { throw Error.invalidResponse }
        guard response.statusCode == 200 else { throw Error.status(response.statusCode) }
        return try JSONDecoder().decode(type, from: data)
    }
}

public struct ProviderControlResult: Codable, Sendable {
    public let providerAccountID: String
    public let paused: Bool

    enum CodingKeys: String, CodingKey {
        case paused
        case providerAccountID = "provider_account_id"
    }
}

public enum RunLogStream: String, Sendable, CaseIterable {
    case stdout, stderr, prepareStdout = "prepare_stdout", prepareStderr = "prepare_stderr"
    case finalizeStdout = "finalize_stdout", finalizeStderr = "finalize_stderr"
}

public struct RunLogTail: Codable, Sendable {
    public let content: String
    public let sizeBytes: Int
    public let truncated: Bool

    enum CodingKeys: String, CodingKey {
        case content, truncated
        case sizeBytes = "size_bytes"
    }
}
