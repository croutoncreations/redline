import AppKit
import RedlineKit
import Sparkle

@MainActor
final class NativeUpdateController: NSObject, SPUStandardUserDriverDelegate {
    private var updaterController: SPUStandardUpdaterController?

    init(bundle: Bundle = .main) {
        super.init()
        guard UpdateStartupPolicy.shouldStartUpdater(infoDictionary: bundle.infoDictionary ?? [:]) else {
            return
        }
        updaterController = SPUStandardUpdaterController(
            startingUpdater: true,
            updaterDelegate: nil,
            userDriverDelegate: self
        )
    }

    func checkForUpdates() {
        guard let updaterController else {
            let alert = NSAlert()
            alert.messageText = "Update checks are not configured"
            alert.informativeText = "This local Redline build does not include a secure update feed and signing key."
            alert.alertStyle = .informational
            alert.runModal()
            return
        }
        // Redline is a menu-bar (accessory) app, usually invoked from the
        // popover or the dashboard. Activate first so Sparkle's windows are
        // not ordered behind the dashboard window.
        NSApplication.shared.activate(ignoringOtherApps: true)
        updaterController.checkForUpdates(nil)
    }

    // MARK: SPUStandardUserDriverDelegate

    nonisolated func standardUserDriverWillHandleShowingUpdate(
        _ handleShowingUpdate: Bool,
        forUpdate update: SUAppcastItem,
        state: SPUUserUpdateState
    ) {
        // Only for a check the user asked for. A scheduled check that finds
        // an update while they work in another app keeps Sparkle's own
        // behavior (shown without taking focus), rather than stealing it.
        guard UpdateWindowIdentity.shouldRaise(
            handlingShowingUpdate: handleShowingUpdate,
            userInitiated: state.userInitiated
        ) else { return }
        Task { @MainActor in Self.bringUpdateWindowsToFront() }
    }

    /// Sparkle orders its alert front "conditionally", which an accessory app
    /// that is not active (or whose dashboard is key) can lose. Raise every
    /// Sparkle-owned window once it exists.
    private static func bringUpdateWindowsToFront() {
        // Sparkle creates the window on this run-loop turn; look on the next.
        DispatchQueue.main.async {
            NSApplication.shared.activate(ignoringOtherApps: true)
            for window in NSApplication.shared.windows where window.isVisible && UpdateWindowIdentity.isSparkleWindow(
                windowControllerClassName: window.windowController.map { String(describing: type(of: $0)) }
            ) {
                window.makeKeyAndOrderFront(nil)
                window.orderFrontRegardless()
            }
        }
    }
}
