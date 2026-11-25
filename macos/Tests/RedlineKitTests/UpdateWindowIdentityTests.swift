import RedlineKit
import XCTest

final class UpdateWindowIdentityTests: XCTestCase {
    func testRecognizesSparkleUpdateWindows() {
        for name in ["SUUpdateAlert", "SUStatusController", "SUUpdatePermissionPrompt"] {
            XCTAssertTrue(UpdateWindowIdentity.isSparkleWindow(windowControllerClassName: name), name)
        }
    }

    func testIgnoresRedlineOwnWindows() {
        for name in ["DashboardWindowController", "PairDeviceWindowController", "RunLogWindowController"] {
            XCTAssertFalse(UpdateWindowIdentity.isSparkleWindow(windowControllerClassName: name), name)
        }
        XCTAssertFalse(UpdateWindowIdentity.isSparkleWindow(windowControllerClassName: nil))
    }

    func testRaisesOnlyForUserInitiatedChecks() {
        XCTAssertTrue(UpdateWindowIdentity.shouldRaise(handlingShowingUpdate: true, userInitiated: true))
        XCTAssertFalse(UpdateWindowIdentity.shouldRaise(handlingShowingUpdate: true, userInitiated: false))
        XCTAssertFalse(UpdateWindowIdentity.shouldRaise(handlingShowingUpdate: false, userInitiated: true))
    }
}
