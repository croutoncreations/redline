import Foundation

/// Recognizes the windows Sparkle's standard user driver opens, so the menu-bar
/// app can raise them above its own dashboard window.
public enum UpdateWindowIdentity {
    /// Window controller classes of Sparkle's standard UI (Sparkle 2.x): the
    /// update alert, download/install progress, and the first-run permission
    /// prompt.
    static let sparkleControllerClassNames: Set<String> = [
        "SUUpdateAlert",
        "SUStatusController",
        "SUUpdatePermissionPrompt",
    ]

    public static func isSparkleWindow(windowControllerClassName: String?) -> Bool {
        guard let name = windowControllerClassName else { return false }
        return sparkleControllerClassNames.contains(name)
    }

    /// Raise Sparkle's windows only when Sparkle is showing the update itself
    /// for a check the user started; scheduled checks must not steal focus.
    public static func shouldRaise(handlingShowingUpdate: Bool, userInitiated: Bool) -> Bool {
        handlingShowingUpdate && userInitiated
    }
}
