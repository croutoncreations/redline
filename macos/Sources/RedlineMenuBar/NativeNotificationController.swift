import AppKit
import RedlineKit
import UserNotifications

@MainActor
final class NativeNotificationController {
    private let center = UNUserNotificationCenter.current()
    private var tracker = TerminalRunTracker()
    private var authorized = false

    init() {
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
        center.requestAuthorization(options: [.alert, .sound]) { [weak self] granted, error in
            let failure = error?.localizedDescription
            Task { @MainActor in
                self?.authorized = granted
                let alert = NSAlert()
                if granted {
                    alert.messageText = "Redline notifications are enabled"
                    alert.informativeText = "You’ll be notified when a queued run completes or fails."
                } else {
                    alert.messageText = "Notifications were not enabled"
                    alert.informativeText = failure ?? "You can allow Redline notifications in System Settings."
                    alert.alertStyle = .warning
                }
                alert.runModal()
            }
        }
    }

    private func deliver(_ event: TerminalRunEvent, taskName: String) {
        let content = UNMutableNotificationContent()
        content.title = event.state == "completed" ? "Redline run completed" : "Redline run failed"
        content.body = "\(taskName) · \(event.providerAccountID)" + (event.error.map { "\n\($0)" } ?? "")
        content.sound = event.state == "failed" ? .default : nil
        center.add(UNNotificationRequest(identifier: "redline-run-\(event.runID)", content: content, trigger: nil))
    }
}
