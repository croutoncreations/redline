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

    public func run(_ runID: String) async throws -> RunSummary {
        try await request(endpoint(["v1", "runs", runID]), method: "GET", as: RunSummary.self)
    }

    public func pauseProvider(_ providerID: String) async throws -> ProviderControlResult {
        try await controlProvider(providerID, action: "pause")
    }

    public func resumeProvider(_ providerID: String) async throws -> ProviderControlResult {
        try await controlProvider(providerID, action: "resume")
    }

    public func refreshProvider(_ providerID: String) async throws -> UsageSnapshot {
        try await request(endpoint(["v1", "providers", providerID, "refresh"]), method: "POST", as: UsageSnapshot.self)
    }

    public func retryTask(_ taskID: String) async throws -> TaskSummary {
        try await request(endpoint(["v1", "tasks", taskID, "retry"]), method: "POST", as: TaskSummary.self)
    }

    public func recoverFailedTask(
        _ taskID: String,
        providerID: String,
        providerPaused: Bool
    ) async throws -> TaskSummary {
        if providerPaused {
            _ = try await resumeProvider(providerID)
        }
        return try await retryTask(taskID)
    }

    public func runLogs(runID: String, stream: RunLogStream = .stdout, tailBytes: Int = 32 * 1024) async throws -> RunLogTail {
        var components = URLComponents(url: endpoint(["v1", "runs", runID, "logs"]), resolvingAgainstBaseURL: false)!
        components.queryItems = [
            URLQueryItem(name: "stream", value: stream.rawValue),
            URLQueryItem(name: "tail_bytes", value: String(tailBytes)),
        ]
        return try await request(components.url!, method: "GET", as: RunLogTail.self)
    }

    public func markRunRead(_ runID: String) async throws {
        _ = try await request(endpoint(["v1", "runs", runID, "read"]), method: "POST", as: ReadResult.self)
    }

    /// Mints a short-lived pairing token.
    ///
    /// The same endpoint `redline pair --qr` uses, so a code from the menu bar
    /// and a code from the terminal are interchangeable.
    public func createPairingToken() async throws -> PairingToken {
        try await request(baseURL.appending(path: "v1/pairing"), method: "POST", as: PairingToken.self)
    }

    public func markAllRunsRead() async throws {
        _ = try await request(endpoint(["v1", "runs", "read-all"]), method: "POST", as: ReadResult.self)
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
        // Any 2xx, not just 200: creating a pairing token answers 201, and
        // insisting on 200 made the pairing window fail every time with
        // "Redline returned HTTP 201".
        guard (200..<300).contains(response.statusCode) else {
            throw Error.status(response.statusCode)
        }
        return try JSONDecoder().decode(type, from: data)
    }
}

private struct ReadResult: Codable { let read: Bool }

/// A way a phone can reach the desktop, as the service names them.
///
/// Decoded leniently: an unknown route from a newer service is dropped rather
/// than failing the whole pairing, since the URL is still good.
public enum PairingRoute: String, Codable, Sendable {
    case direct
    case relay
}

/// A single-use credential a phone exchanges for a durable API token, and the
/// code that carries it.
///
/// The service composes `pairingURL`, so this client has no opinion about its
/// shape. The menu bar used to build the URL itself from a trusted host and
/// the token, and nothing else; when the QR grew relay fields the CLI got them
/// and the sheet did not, so every phone paired from the desktop app had no
/// relay and no way to know. One builder now, and every surface renders what
/// it is handed.
public struct PairingToken: Codable, Sendable {
    public let token: String
    /// RFC 3339, kept as a string because the shared decoder has no date
    /// strategy and every other timestamp in this client is handled the same
    /// way. Changing that globally to serve one field would risk every
    /// existing model.
    public let expiresAt: String
    /// What the phone scans. Absent from an older service, and from a desktop
    /// with neither a trusted host nor a relay -- a token is still minted for
    /// the web pair page, but there is nowhere to send a phone.
    public let pairingURL: String?
    /// The routes the code offers, for the sheet to say out loud.
    public let routes: [PairingRoute]
    /// The direct host:port the code names, or nil for a relay-only code.
    public let endpoint: String?

    enum CodingKeys: String, CodingKey {
        case token = "pairing_token"
        case expiresAt = "expires_at"
        case pairingURL = "pairing_url"
        case routes
        case endpoint
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        token = try container.decode(String.self, forKey: .token)
        expiresAt = try container.decode(String.self, forKey: .expiresAt)
        pairingURL = try container.decodeIfPresent(String.self, forKey: .pairingURL)
        endpoint = try container.decodeIfPresent(String.self, forKey: .endpoint)
        // Unknown route names are skipped, not fatal: a newer service adding a
        // route must not stop an older app from showing a code that works.
        let names = try container.decodeIfPresent([String].self, forKey: .routes) ?? []
        routes = names.compactMap(PairingRoute.init(rawValue:))
    }

    public init(token: String, expiresAt: String, pairingURL: String?, routes: [PairingRoute], endpoint: String?) {
        self.token = token
        self.expiresAt = expiresAt
        self.pairingURL = pairingURL
        self.routes = routes
        self.endpoint = endpoint
    }

    /// The expiry as a date, or nil if the service sent something unparseable.
    ///
    /// The formatter is built per call rather than shared, because
    /// ISO8601DateFormatter is not Sendable and this runs once per window.
    public var expiry: Date? {
        let formatter = ISO8601DateFormatter()
        // The service includes fractional seconds, which the default options
        // reject outright rather than ignoring.
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = formatter.date(from: expiresAt) { return date }
        formatter.formatOptions = [.withInternetDateTime]
        return formatter.date(from: expiresAt)
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
