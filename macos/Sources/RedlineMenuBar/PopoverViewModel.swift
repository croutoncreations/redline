import Foundation
import RedlineKit

@MainActor
final class PopoverViewModel: ObservableObject {
    @Published private(set) var snapshot: DashboardSnapshot?
    @Published private(set) var errorMessage: String?
    @Published private(set) var actionError: String?
    @Published private(set) var isRefreshing = false
    @Published private(set) var providersBeingControlled = Set<String>()
    @Published private(set) var tasksBeingControlled = Set<String>()
    var onSnapshot: ((DashboardSnapshot) -> Void)?
    var onError: ((String) -> Void)?

    private let client: RedlineAPIClient

    init(client: RedlineAPIClient) {
        self.client = client
    }

    func apply(_ snapshot: DashboardSnapshot) {
        self.snapshot = snapshot
        errorMessage = nil
        actionError = nil
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

    func setPaused(_ paused: Bool, providerID: String) async {
        guard providersBeingControlled.insert(providerID).inserted else { return }
        actionError = nil
        defer { providersBeingControlled.remove(providerID) }
        do {
            if paused { _ = try await client.pauseProvider(providerID) }
            else { _ = try await client.resumeProvider(providerID) }
            apply(try await client.dashboard())
        } catch {
            actionError = error.localizedDescription
        }
    }

    func recoverFailedTask(_ taskID: String, providerID: String, providerPaused: Bool) async {
        guard tasksBeingControlled.insert(taskID).inserted else { return }
        actionError = nil
        defer { tasksBeingControlled.remove(taskID) }
        do {
            _ = try await client.recoverFailedTask(
                taskID,
                providerID: providerID,
                providerPaused: providerPaused
            )
            apply(try await client.dashboard())
        } catch {
            actionError = error.localizedDescription
        }
    }
}
