import Foundation
import Testing
@testable import RedlineKit

@Test func apiCredentialIsStableProtectedAndBuildsBootstrapURL() throws {
    let root = FileManager.default.temporaryDirectory
        .appending(path: "redline-api-credential-\(UUID().uuidString)")
    defer { try? FileManager.default.removeItem(at: root) }
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    let configURL = root.appending(path: "redline.yaml")

    let first = try APICredentialStore.loadOrCreateToken(for: configURL)
    let second = try APICredentialStore.loadOrCreateToken(for: configURL)
    #expect(first == second)
    #expect(first.count >= 32)

    let tokenURL = APICredentialStore.tokenURL(for: configURL)
    let attributes = try FileManager.default.attributesOfItem(atPath: tokenURL.path)
    #expect((attributes[.posixPermissions] as? NSNumber)?.intValue == 0o600)

    let dashboardURL = APICredentialStore.authenticatedDashboardURL(
        baseURL: URL(string: "http://127.0.0.1:7436")!,
        token: first
    )
    let components = URLComponents(url: dashboardURL, resolvingAgainstBaseURL: false)
    #expect(components?.queryItems?.first?.name == "access_token")
    #expect(components?.queryItems?.first?.value == first)
}
