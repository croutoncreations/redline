import Foundation

public enum DashboardNavigationDestination: Equatable {
    case embedded
    case external
}

public struct DashboardNavigationPolicy {
    public let dashboardURL: URL

    public init(dashboardURL: URL) {
        self.dashboardURL = dashboardURL
    }

    public func destination(for url: URL?) -> DashboardNavigationDestination {
        guard let url else { return .embedded }
        guard sameOrigin(url, dashboardURL) else { return .external }
        return .embedded
    }

    private func sameOrigin(_ lhs: URL, _ rhs: URL) -> Bool {
        lhs.scheme?.lowercased() == rhs.scheme?.lowercased()
            && lhs.host?.lowercased() == rhs.host?.lowercased()
            && effectivePort(lhs) == effectivePort(rhs)
    }

    private func effectivePort(_ url: URL) -> Int? {
        if let port = url.port { return port }
        return switch url.scheme?.lowercased() {
        case "http": 80
        case "https": 443
        default: nil
        }
    }
}
