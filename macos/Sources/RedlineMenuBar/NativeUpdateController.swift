import AppKit
import RedlineKit
import Sparkle

@MainActor
final class NativeUpdateController {
    private let updaterController: SPUStandardUpdaterController?

    init(bundle: Bundle = .main) {
        guard UpdateStartupPolicy.shouldStartUpdater(infoDictionary: bundle.infoDictionary ?? [:]) else {
            updaterController = nil
            return
        }
        updaterController = SPUStandardUpdaterController(
            startingUpdater: true,
            updaterDelegate: nil,
            userDriverDelegate: nil
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
        updaterController.checkForUpdates(nil)
    }
}
