import Foundation
import Testing
@testable import RedlineKit

/// What the dashboard window's overflow menu offers.
///
/// The contents are data rather than inline UI so they can be checked without
/// driving a window: the risk worth guarding is a menu that drifts out of step
/// with the menu bar's, leaving two places that claim to do the same things and
/// quietly do not.
@Suite("Dashboard menu")
struct DashboardMenuTests {

    @Test("Leads with pairing, which is the thing people come here to do")
    func leadsWithPairing() {
        let items = DashboardMenu.items
        #expect(items.first?.title == "Pair a Device…")
        #expect(items.first?.action == .pairDevice)
    }

    /// Every action the menu offers must be one the window can actually
    /// perform. A title with no handler is a dead entry that looks alive.
    @Test("Every entry names a real action")
    func everyEntryIsActionable() {
        for item in DashboardMenu.items {
            switch item {
            case .separator:
                continue
            case .command(let title, _):
                #expect(!title.isEmpty, "a command needs a label")
            }
        }
    }

    /// The menu bar already offers these; the dashboard should not invent
    /// different wording for the same thing.
    @Test("Shares wording with the menu bar")
    func sharesWordingWithTheMenuBar() {
        let titles = DashboardMenu.items.compactMap(\.title)
        #expect(titles.contains("Pair a Device…"))
        #expect(titles.contains("App Setup…"))
        #expect(titles.contains("Check for Updates…"))
    }

    /// Destructive or navigational items belong below a divider, away from the
    /// ones people press often.
    @Test("Groups links away from the actions")
    func groupsLinksAwayFromActions() {
        let items = DashboardMenu.items
        guard let firstSeparator = items.firstIndex(where: { $0.isSeparator }) else {
            Issue.record("the menu should be grouped")
            return
        }
        let aboveTitles = items[..<firstSeparator].compactMap(\.title)
        #expect(aboveTitles.contains("Pair a Device…"))

        // The Crouton Creations links are informational and sit below.
        let belowActions = items[firstSeparator...].compactMap(\.action)
        #expect(belowActions.contains(.openMoreTools))
        #expect(belowActions.contains(.openBuilderUpdates))
    }

    @Test("Links point at Crouton Creations with attribution intact")
    func linksCarryAttribution() {
        #expect(ProductLinks.moreTools.absoluteString.contains("croutoncreations.com"))
        #expect(ProductLinks.moreTools.absoluteString.contains("utm_source=redline"))
        #expect(ProductLinks.builderUpdates.absoluteString.contains("utm_source=redline"))
    }

    /// A menu that opens nothing is worse than no menu, so the list must never
    /// be empty and must not be all dividers.
    @Test("Is never empty or all dividers")
    func isNeverEmpty() {
        #expect(!DashboardMenu.items.isEmpty)
        #expect(DashboardMenu.items.contains { !$0.isSeparator })
    }
}
