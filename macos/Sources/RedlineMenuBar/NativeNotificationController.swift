import AppKit
import RedlineKit
import UserNotifications

@MainActor
final class NativeNotificationController {
    private let center = UNUserNotificationCenter.current()
    private let responseDelegate: NotificationResponseDelegate
    private var tracker = RunNotificationTracker()
    private var authorized = false
    private let client: RedlineAPIClient
    private let defaults: UserDefaults
    private static let deliveryIDKey = "redline.notifications.highestDeliveryID"
    private var deliveryTracker: DeliveryNotificationTracker
    private var polling = false

    init(client: RedlineAPIClient, defaults: UserDefaults = .standard, onOpenRun: @escaping @MainActor (String) -> Void, onOpenDashboard: @escaping @MainActor () -> Void) {
        self.client = client
        self.defaults = defaults
        let highestID = (defaults.object(forKey: Self.deliveryIDKey) as? NSNumber)?.int64Value
        deliveryTracker = DeliveryNotificationTracker(highestID: highestID)
        responseDelegate = NotificationResponseDelegate(onOpenRun: onOpenRun, onOpenDashboard: onOpenDashboard)
        center.delegate = responseDelegate
        center.getNotificationSettings { [weak self] settings in
            let isAuthorized = settings.authorizationStatus == .authorized || settings.authorizationStatus == .provisional
            Task { @MainActor in
                self?.authorized = isAuthorized
            }
        }
    }

    func observe(_ snapshot: DashboardSnapshot) {
        let events = tracker.observe(snapshot.runs)
        guard authorized else { return }
        Task { await pollDeliveries() }
        for event in events {
            let taskName = snapshot.tasks.first { $0.id == event.taskID }?.name ?? event.taskID
            deliver(event, taskName: taskName)
        }
    }

    private func pollDeliveries() async {
        guard authorized, !polling else { return }
        polling = true
        defer { polling = false }
        if deliveryTracker.highestID == nil {
            guard let deliveries = try? await client.notifications() else { return }
            deliveryTracker.seed(deliveries)
            persistDeliveryCursor()
            return
        }
        let limit = DeliveryNotificationTracker.pageLimit
        for _ in 0..<DeliveryNotificationTracker.maxPagesPerPoll {
            guard let deliveries = try? await client.notifications(afterID: deliveryTracker.highestID, limit: limit) else { return }
            var outcomes: [(id: Int64, handled: Bool)] = []
            for event in deliveryTracker.pending(deliveries) {
                let handled: Bool
                if event.shouldNotify {
                    handled = await enqueue(event)
                } else {
                    handled = true
                }
                outcomes.append((id: event.id, handled: handled))
                if !handled { break }
            }
            let allHandled = deliveryTracker.record(outcomes)
            persistDeliveryCursor()
            if !allHandled || deliveries.count < limit { return }
        }
    }

    private func persistDeliveryCursor() {
        if let highestID = deliveryTracker.highestID {
            defaults.set(highestID, forKey: Self.deliveryIDKey)
        }
    }

    private func enqueue(_ event: NotificationDelivery) async -> Bool {
        let content = UNMutableNotificationContent()
        content.title = event.title
        content.body = event.message
        content.sound = event.hasSound ? .default : nil
        content.userInfo = ["open_dashboard": true]
        let identifier = event.identifier
        let request = UNNotificationRequest(identifier: identifier, content: content, trigger: nil)
        // Call from the main actor inside the continuation: the async add overload
        // sends the non-Sendable center across isolation under strict concurrency.
        return await withCheckedContinuation { continuation in
            center.add(request) { error in
                if let error {
                    NSLog("Redline notification %@ failed: %@", identifier, error.localizedDescription)
                }
                continuation.resume(returning: error == nil)
            }
        }
    }

    func enable() {
        center.getNotificationSettings { [weak self] settings in
            let authorizationStatus = settings.authorizationStatus.rawValue
            Task { @MainActor in
                switch UNAuthorizationStatus(rawValue: authorizationStatus) {
                case .notDetermined:
                    self?.requestAuthorization()
                case .authorized, .provisional, .ephemeral:
                    self?.authorized = true
                    self?.showAlert(
                        title: "Redline notifications are enabled",
                        detail: "You’ll be notified when a queued run starts, completes, or fails, and about banked resets and firesale changes."
                    )
                case .denied:
                    self?.authorized = false
                    self?.showDeniedAlert()
                case .none:
                    self?.requestAuthorization()
                @unknown default:
                    self?.requestAuthorization()
                }
            }
        }
    }

    private func requestAuthorization() {
        center.requestAuthorization(options: [.alert, .sound]) { [weak self] granted, error in
            let failure = error?.localizedDescription
            Task { @MainActor in
                self?.authorized = granted
                if granted {
                    self?.showAlert(
                        title: "Redline notifications are enabled",
                        detail: "You’ll be notified when a queued run starts, completes, or fails, and about banked resets and firesale changes."
                    )
                } else {
                    self?.showDeniedAlert(detail: failure)
                }
            }
        }
    }

    private func showDeniedAlert(detail: String? = nil) {
        let alert = NSAlert()
        alert.messageText = "Notifications are disabled"
        alert.informativeText = detail ?? "Allow Redline notifications in System Settings to receive job updates."
        alert.alertStyle = .warning
        alert.addButton(withTitle: "Open Notification Settings")
        alert.addButton(withTitle: "Cancel")
        if alert.runModal() == .alertFirstButtonReturn,
           let url = URL(string: "x-apple.systempreferences:com.apple.Notifications-Settings.extension") {
            NSWorkspace.shared.open(url)
        }
    }

    private func showAlert(title: String, detail: String) {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = detail
        alert.runModal()
    }

    private func deliver(_ event: RunNotificationEvent, taskName: String) {
        let content = UNMutableNotificationContent()
        switch event.state {
        case "running":
            content.title = "Redline job started"
        case "completed":
            content.title = "Redline job completed"
        default:
            content.title = "Redline job failed"
        }
        content.body = "\(taskName) · \(event.providerAccountID)" + (event.error.map { "\n\($0)" } ?? "")
        content.sound = event.state == "failed" ? .default : nil
        content.userInfo = ["run_id": event.runID]
        center.add(UNNotificationRequest(identifier: "redline-run-\(event.runID)", content: content, trigger: nil))
    }
}

private final class NotificationResponseDelegate: NSObject, UNUserNotificationCenterDelegate, @unchecked Sendable {
    private let onOpenRun: @MainActor (String) -> Void
    private let onOpenDashboard: @MainActor () -> Void

    init(onOpenRun: @escaping @MainActor (String) -> Void, onOpenDashboard: @escaping @MainActor () -> Void) {
        self.onOpenRun = onOpenRun
        self.onOpenDashboard = onOpenDashboard
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse
    ) async {
        let info = response.notification.request.content.userInfo
        if info["open_dashboard"] as? Bool == true {
            await onOpenDashboard()
        } else if let runID = info["run_id"] as? String {
            await onOpenRun(runID)
        }
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification
    ) async -> UNNotificationPresentationOptions {
        notification.request.content.sound == nil ? [.banner, .list] : [.banner, .list, .sound]
    }
}
