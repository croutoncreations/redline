import Foundation
import Testing
@testable import RedlineKit

@Suite("Dashboard navigation policy")
struct DashboardNavigationPolicyTests {
    private let policy = DashboardNavigationPolicy(
        dashboardURL: URL(string: "http://127.0.0.1:7436")!
    )

    @Test("Keeps dashboard routes and API requests in the app")
    func keepsLocalRoutesEmbedded() {
        #expect(policy.destination(for: URL(string: "http://127.0.0.1:7436/tasks")) == .embedded)
        #expect(policy.destination(for: URL(string: "http://127.0.0.1:7436/v1/dashboard")) == .embedded)
    }

    @Test("Treats an omitted default port as the same origin")
    func treatsDefaultPortAsSameOrigin() {
        let defaultPortPolicy = DashboardNavigationPolicy(
            dashboardURL: URL(string: "http://127.0.0.1")!
        )

        #expect(defaultPortPolicy.destination(for: URL(string: "http://127.0.0.1:80/help")) == .embedded)
    }

    @Test("Hands external links to the system")
    func opensExternalLinksOutsideTheApp() {
        #expect(policy.destination(for: URL(string: "https://docs.example.com/redline")) == .external)
        #expect(policy.destination(for: URL(string: "http://127.0.0.1:9000")) == .external)
        #expect(policy.destination(for: URL(string: "mailto:support@example.com")) == .external)
    }

    @Test("Allows navigation actions without a URL")
    func allowsMissingDestination() {
        #expect(policy.destination(for: nil) == .embedded)
    }
}
