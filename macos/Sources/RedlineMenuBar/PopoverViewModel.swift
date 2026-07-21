import Foundation
import RedlineKit

@MainActor
final class PopoverViewModel: ObservableObject {
    @Published private(set) var snapshot: DashboardSnapshot?
    @Published private(set) var errorMessage: String?
    @Published private(set) var isRefreshing = false
    var onSnapshot: ((DashboardSnapshot) -> Void)?
    var onError: ((String) -> Void)?

    private let client: RedlineAPIClient

    init(client: RedlineAPIClient) {
        self.client = client
    }

    func apply(_ snapshot: DashboardSnapshot) {
        self.snapshot = snapshot
        errorMessage = nil
        onSnapshot?(snapshot)
    }

    func apply(error: String) {
        errorMessage = error
        onError?(error)
    }

    func refresh() async {
        guard !isRefreshing else { return }
        isRefreshing = true
        defer { isRefreshing = false }
        do {
            apply(try await client.dashboard())
        } catch {
            apply(error: error.localizedDescription)
        }
    }
}
