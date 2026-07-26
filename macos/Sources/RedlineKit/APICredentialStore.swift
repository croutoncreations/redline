import Darwin
import Foundation
import Security

public enum APICredentialStore {
    public enum Error: Swift.Error {
        case randomGenerationFailed(OSStatus)
        case invalidCredential
        case insecurePermissions
    }

    public static func tokenURL(for configURL: URL) -> URL {
        configURL.deletingLastPathComponent().appending(path: "api-token")
    }

    public static func loadOrCreateToken(for configURL: URL) throws -> String {
        let url = tokenURL(for: configURL)
        if FileManager.default.fileExists(atPath: url.path) {
            return try loadToken(at: url)
        }
        try FileManager.default.createDirectory(
            at: url.deletingLastPathComponent(),
            withIntermediateDirectories: true,
            attributes: [.posixPermissions: 0o700]
        )
        var bytes = [UInt8](repeating: 0, count: 32)
        let status = SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes)
        guard status == errSecSuccess else { throw Error.randomGenerationFailed(status) }
        let token = Data(bytes).base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
        guard FileManager.default.createFile(
            atPath: url.path,
            contents: Data((token + "\n").utf8),
            attributes: [.posixPermissions: 0o600]
        ) else {
            if FileManager.default.fileExists(atPath: url.path) {
                return try loadToken(at: url)
            }
            throw CocoaError(.fileWriteUnknown)
        }
        return token
    }

    public static func authenticatedDashboardURL(baseURL: URL, token: String) -> URL {
        var components = URLComponents(url: baseURL, resolvingAgainstBaseURL: false)!
        components.path = "/"
        components.queryItems = [URLQueryItem(name: "access_token", value: token)]
        return components.url!
    }

    private static func loadToken(at url: URL) throws -> String {
        let attributes = try FileManager.default.attributesOfItem(atPath: url.path)
        if let permissions = attributes[.posixPermissions] as? NSNumber,
           permissions.intValue & 0o077 != 0 {
            throw Error.insecurePermissions
        }
        let token = try String(contentsOf: url, encoding: .utf8)
            .trimmingCharacters(in: .whitespacesAndNewlines)
        guard token.count >= 32, token.count <= 256 else { throw Error.invalidCredential }
        return token
    }
}
