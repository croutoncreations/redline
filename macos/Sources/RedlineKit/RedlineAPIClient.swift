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
    private let session: URLSession

    public init(baseURL: URL, session: URLSession = .shared) {
        self.baseURL = baseURL
        self.session = session
    }

    public func dashboard() async throws -> DashboardSnapshot {
        let (data, response) = try await session.data(from: baseURL.appending(path: "v1/dashboard"))
        guard let response = response as? HTTPURLResponse else { throw Error.invalidResponse }
        guard response.statusCode == 200 else { throw Error.status(response.statusCode) }
        return try DashboardSnapshot.decode(data)
    }

    public func isCompatible() async -> Bool {
        (try? await dashboard()) != nil
    }
}
