import Foundation

/// The contents of the dashboard window's overflow menu.
///
/// Kept as data rather than inline UI for two reasons: it can be checked
/// without driving a window, and having one definition stops the dashboard and
/// the menu bar drifting into two menus that claim to do the same things and
/// quietly do not.
public enum DashboardMenu {

    /// Something the menu can ask the app to do.
    public enum Action: Equatable, Sendable {
        case pairDevice
        case checkForUpdates
        case showAppSetup
        case openInBrowser
        case openMoreTools
        case openBuilderUpdates
    }

    /// One row: either a command or a divider between groups.
    public enum Item: Equatable, Sendable {
        case command(title: String, action: Action)
        case separator

        public var title: String? {
            if case .command(let title, _) = self { return title }
            return nil
        }

        public var action: Action? {
            if case .command(_, let action) = self { return action }
            return nil
        }

        public var isSeparator: Bool { self == .separator }
    }

    /// Pairing leads because it is the thing people open this menu to do, and
    /// until now it was only reachable from the menu bar. The links are
    /// informational and sit below a divider, away from the actions.
    ///
    /// Wording matches the menu bar exactly; two labels for one action would
    /// leave someone wondering whether they do the same thing.
    public static let items: [Item] = [
        .command(title: "Pair a Device…", action: .pairDevice),
        .separator,
        .command(title: "Open in Browser", action: .openInBrowser),
        .command(title: "Check for Updates…", action: .checkForUpdates),
        .command(title: "App Setup…", action: .showAppSetup),
        .separator,
        .command(title: "More tools from Crouton Creations…", action: .openMoreTools),
        .command(title: "Get builder updates…", action: .openBuilderUpdates),
    ]
}
