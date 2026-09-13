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

    /// Where a pairing code is in its life, so the sheet can stop showing a
    /// spent one and say the phone got in.
    ///
    /// The pairing token goes in a header: it is a full-access credential for
    /// ten minutes, and a path or query string lands in access logs.
    public func pairingStatus(of pairingToken: String) async throws -> PairingStatus {
        let answer: PairingStatusAnswer = try await request(
            baseURL.appending(path: "v1/pairing/status"),
            method: "GET",
            as: PairingStatusAnswer.self,
            headers: ["X-Redline-Pairing-Token": pairingToken]
        )
        // Anything this build does not recognise reads as expired: the remedy
        // -- offer a new code -- is the same, and a newer service must not be
        // able to wedge an older sheet.
        return PairingStatus(rawValue: answer.status) ?? .expired
    }

    public func markAllRunsRead() async throws {
        _ = try await request(endpoint(["v1", "runs", "read-all"]), method: "POST", as: ReadResult.self)
    }

    public func relayStatus() async throws -> RelayStatus {
        try await request(baseURL.appending(path: "v1/relay/status"), method: "GET", as: RelayStatus.self)
    }

    public func configureRelay(_ configureRequest: RelayConfigureRequest) async throws -> RelayStatus {
        try await request(
            baseURL.appending(path: "v1/relay/configure"),
            method: "POST",
            as: RelayStatus.self,
            body: configureRequest
        )
    }

    public func relayDevices() async throws -> [RelayActivation] {
        let answer: RelayDevicesAnswer = try await request(
            baseURL.appending(path: "v1/relay/devices"), method: "GET", as: RelayDevicesAnswer.self
        )
        return answer.devices
    }

    public func deactivateRelayDevice(id: String) async throws {
        try await requestNoContent(endpoint(["v1", "relay", "devices", id]), method: "DELETE")
    }

    public func relayPortalURL() async throws -> URL {
        let answer: RelayPortalAnswer = try await request(
            baseURL.appending(path: "v1/relay/portal"), method: "POST", as: RelayPortalAnswer.self
        )
        guard let url = URL(string: answer.url) else { throw Error.invalidResponse }
        return url
    }

    public func deactivateRelay() async throws -> RelayStatus {
        try await request(baseURL.appending(path: "v1/relay/deactivate"), method: "POST", as: RelayStatus.self)
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

    private func request<T: Decodable>(
        _ url: URL, method: String, as type: T.Type, headers: [String: String] = [:]
    ) async throws -> T {
        try await request(url, method: method, as: type, headers: headers, body: Optional<EmptyBody>.none)
    }

    /// Like the no-body overload, but with an encodable request body. A
    /// separate parameter rather than a default `nil` on the existing method,
    /// because `Encodable` cannot be a default-valued generic parameter
    /// without also constraining every caller that has no body to name it.
    private func request<T: Decodable, Body: Encodable>(
        _ url: URL, method: String, as type: T.Type, headers: [String: String] = [:], body: Body?
    ) async throws -> T {
        let (data, response) = try await send(url, method: method, headers: headers, body: body)
        guard (200..<300).contains(response.statusCode) else {
            throw Error.status(response.statusCode)
        }
        return try JSONDecoder().decode(type, from: data)
    }

    /// For endpoints that answer 204 No Content: there is no body to decode,
    /// and asking `JSONDecoder` to try would throw on the empty response.
    private func requestNoContent(_ url: URL, method: String, headers: [String: String] = [:]) async throws {
        let (_, response) = try await send(url, method: method, headers: headers, body: Optional<EmptyBody>.none)
        guard (200..<300).contains(response.statusCode) else {
            throw Error.status(response.statusCode)
        }
    }

    private func send<Body: Encodable>(
        _ url: URL, method: String, headers: [String: String], body: Body?
    ) async throws -> (Data, HTTPURLResponse) {
        var request = URLRequest(url: url)
        request.httpMethod = method
        if !token.isEmpty {
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        for (name, value) in headers {
            request.setValue(value, forHTTPHeaderField: name)
        }
        if let body {
            request.httpBody = try JSONEncoder().encode(body)
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        } else if method == "POST" {
            request.httpBody = Data("{}".utf8)
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        let (data, response) = try await session.data(for: request)
        guard let response = response as? HTTPURLResponse else { throw Error.invalidResponse }
        // Any 2xx, not just 200: creating a pairing token answers 201, and
        // insisting on 200 made the pairing window fail every time with
        // "Redline returned HTTP 201".
        return (data, response)
    }
}

/// A body type that is never actually sent; it exists only so the no-body
/// `request` overload can call the body-carrying one with `nil`.
private struct EmptyBody: Encodable {}

private struct ReadResult: Codable { let read: Bool }

private struct PairingStatusAnswer: Codable { let status: String }

/// Where a pairing code is in its life.
public enum PairingStatus: String, Sendable {
    /// Minted, not yet scanned.
    case pending
    /// A phone spent it and holds the credential.
    case redeemed
    /// Past its ten minutes, or never issued; either way, a new code is the
    /// answer.
    case expired
}

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

/// Mirrors `internal/config.RelayReadiness` in the service. This is the
/// closed, non-secret status the desktop shows: no session id, issuer URL,
/// license key, or entitlement token ever crosses this boundary.
public enum RelayState: String, Codable, Sendable {
    case off
    case selfHosted = "self_hosted"
    case needsLicense = "needs_license"
    case active
    case renewPending = "renew_pending"
    case unavailable
    case lapsed
    case noSeat = "no_seat"
    case invalidKey = "invalid_key"
}

/// Mirrors `internal/config.RelayMode`.
public enum RelayMode: String, Codable, Sendable {
    case off
    case hosted
    case selfHosted = "self_hosted"
}

/// Mirrors `internal/config.RelayConnectionState`.
public enum RelayConnection: String, Codable, Sendable {
    case disconnected
    case connecting
    case connected
}

/// A device holding a relay activation.
///
/// Two different Go shapes decode into this one model: `relay.Activation`
/// (`id`, `label`, `first_seen`, `current`) from `/v1/relay/devices`, and the
/// narrower `RelayActivationSummary` (`label`, `first_seen` only) carried on a
/// `no_seat` `RelayStatus`. `id` and `current` are absent from the latter, so
/// they decode to `""` and `false` rather than failing: a no_seat activation
/// with no id is still worth showing, and there is no "current" device to
/// pick out on a status the whole point of which is that this desktop has no
/// seat.
public struct RelayActivation: Codable, Sendable, Equatable {
    public let id: String
    public let label: String
    /// ISO 8601, kept as a string like every other timestamp in this client:
    /// the shared decoder has no date strategy, and adding one to serve this
    /// field alone would risk every existing model.
    public let firstSeen: String
    public let current: Bool

    enum CodingKeys: String, CodingKey {
        case id, label, current
        case firstSeen = "first_seen"
    }

    public init(id: String = "", label: String, firstSeen: String, current: Bool = false) {
        self.id = id
        self.label = label
        self.firstSeen = firstSeen
        self.current = current
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        id = try container.decodeIfPresent(String.self, forKey: .id) ?? ""
        label = try container.decode(String.self, forKey: .label)
        firstSeen = try container.decode(String.self, forKey: .firstSeen)
        current = try container.decodeIfPresent(Bool.self, forKey: .current) ?? false
    }
}

/// Mirrors `internal/config.RelayStatus`, decoded exactly as its
/// `MarshalJSON` emits it: `state`, `mode`, and `connection` are always
/// present; every other field is state-specific or an omitted-when-zero
/// count, and absent otherwise.
public struct RelayStatus: Codable, Sendable {
    public let state: RelayState
    public let mode: RelayMode
    public let url: String?
    public let connection: RelayConnection
    public let renewsAt: String?
    public let expiresAt: String?
    public let since: String?
    public let seats: Int?
    public let seatsUsed: Int?
    public let maxClients: Int?
    public let activations: [RelayActivation]?

    enum CodingKeys: String, CodingKey {
        case state, mode, url, connection, seats
        case renewsAt = "renews_at"
        case expiresAt = "expires_at"
        case since
        case seatsUsed = "seats_used"
        case maxClients = "max_clients"
        case activations
    }

    public init(
        state: RelayState,
        mode: RelayMode,
        url: String? = nil,
        connection: RelayConnection,
        renewsAt: String? = nil,
        expiresAt: String? = nil,
        since: String? = nil,
        seats: Int? = nil,
        seatsUsed: Int? = nil,
        maxClients: Int? = nil,
        activations: [RelayActivation]? = nil
    ) {
        self.state = state
        self.mode = mode
        self.url = url
        self.connection = connection
        self.renewsAt = renewsAt
        self.expiresAt = expiresAt
        self.since = since
        self.seats = seats
        self.seatsUsed = seatsUsed
        self.maxClients = maxClients
        self.activations = activations
    }
}

/// Mirrors `internal/config.RelayConfigureRequest`. Sent as the body of
/// `POST /v1/relay/configure`; `omitempty` on the Go side means an empty
/// string here must be encoded as absent, not as `""`, so this uses the same
/// custom `encode(to:)` shape rather than relying on `Codable` synthesis
/// (which would emit every field).
public struct RelayConfigureRequest: Encodable, Sendable {
    public let mode: RelayMode
    public let url: String?
    public let licenseKey: String?
    public let label: String?

    enum CodingKeys: String, CodingKey {
        case mode, url, label
        case licenseKey = "license_key"
    }

    public init(mode: RelayMode, url: String? = nil, licenseKey: String? = nil, label: String? = nil) {
        self.mode = mode
        self.url = url?.isEmpty == true ? nil : url
        self.licenseKey = licenseKey?.isEmpty == true ? nil : licenseKey
        self.label = label?.isEmpty == true ? nil : label
    }

    public func encode(to encoder: Encoder) throws {
        var container = encoder.container(keyedBy: CodingKeys.self)
        try container.encode(mode, forKey: .mode)
        try container.encodeIfPresent(url, forKey: .url)
        try container.encodeIfPresent(licenseKey, forKey: .licenseKey)
        try container.encodeIfPresent(label, forKey: .label)
    }
}

/// The `{"devices": [...]}` wrapper `GET /v1/relay/devices` answers with.
private struct RelayDevicesAnswer: Decodable {
    let devices: [RelayActivation]
}

/// The `{"url": "..."}` wrapper `POST /v1/relay/portal` answers with.
private struct RelayPortalAnswer: Decodable {
    let url: String
}
