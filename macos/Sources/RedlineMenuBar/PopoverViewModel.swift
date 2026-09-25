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
    @Published private(set) var showsBuilderUpdatesPrompt = false
    @Published private(set) var installationIssue: InstallationIssue?
    /// The most recently fetched relay status, or nil before the first
    /// successful `relayStatus()` call. Kept separate from `snapshot` and its
    /// own error channel: a broken relay-status call must never blank out an
    /// otherwise-healthy dashboard, and vice versa.
    @Published private(set) var relayStatus: RelayStatus?
    /// Populated when the app launched the embedded service and it failed to
    /// come up. Distinct from `errorMessage` (a transient fetch failure): this
    /// carries the service's own diagnostic so a bad config is visible here.
    @Published private(set) var startupFailure: ServiceStartupFailure?
    var onSnapshot: ((DashboardSnapshot) -> Void)?
    var onError: ((String) -> Void)?

    private let client: RedlineAPIClient
    private let defaults: UserDefaults

    init(client: RedlineAPIClient, defaults: UserDefaults = .standard) {
        self.client = client
        self.defaults = defaults
    }

    func apply(_ snapshot: DashboardSnapshot) {
        self.snapshot = snapshot
        showsBuilderUpdatesPrompt = EngagementPromptPolicy.shouldShow(
            hasCompletedRun: snapshot.runs.contains { $0.state == "completed" },
            dismissed: defaults.bool(forKey: EngagementPromptPolicy.dismissalKey)
        )
        errorMessage = nil
        actionError = nil
        startupFailure = nil
        onSnapshot?(snapshot)
    }

    func apply(startupFailure: ServiceStartupFailure?) {
        self.startupFailure = startupFailure
    }

    func dismissBuilderUpdatesPrompt() {
        defaults.set(true, forKey: EngagementPromptPolicy.dismissalKey)
        showsBuilderUpdatesPrompt = false
    }

    func apply(installationIssue: InstallationIssue?) {
        self.installationIssue = installationIssue
    }

    /// Records the latest relay status for the menu bar's status line and the
    /// "Manage subscription…" menu item. Called independently of
    /// `apply(_ snapshot:)` / `apply(error:)`; a relay-status failure has no
    /// effect here, and an older status is simply left in place rather than
    /// cleared, so the menu item does not flicker on a transient failure.
    func apply(relayStatus: RelayStatus) {
        self.relayStatus = relayStatus
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

    func refreshUsage(providerID: String) async {
        guard providersBeingControlled.insert(providerID).inserted else { return }
        actionError = nil
        defer { providersBeingControlled.remove(providerID) }
        do {
            _ = try await client.refreshProvider(providerID)
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
