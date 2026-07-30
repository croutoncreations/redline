import AppKit
import RedlineKit
import UserNotifications

@MainActor
final class NativeNotificationController {
    private let center = UNUserNotificationCenter.current()
    private let responseDelegate: NotificationResponseDelegate
    private var tracker = RunNotificationTracker()
    private var authorized = false

    init(onOpenRun: @escaping @MainActor (String) -> Void) {
        responseDelegate = NotificationResponseDelegate(onOpenRun: onOpenRun)
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
        for event in events {
            let taskName = snapshot.tasks.first { $0.id == event.taskID }?.name ?? event.taskID
            deliver(event, taskName: taskName)
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
                        detail: "You’ll be notified when a queued run starts, completes, or fails."
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
                        detail: "You’ll be notified when a queued run starts, completes, or fails."
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

    init(onOpenRun: @escaping @MainActor (String) -> Void) {
        self.onOpenRun = onOpenRun
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse
    ) async {
        guard let runID = response.notification.request.content.userInfo["run_id"] as? String else { return }
        await onOpenRun(runID)
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification
    ) async -> UNNotificationPresentationOptions {
        [.banner, .list]
    }
}
