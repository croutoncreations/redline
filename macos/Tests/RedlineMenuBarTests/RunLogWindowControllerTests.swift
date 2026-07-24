import Foundation
import RedlineKit
import Testing
@testable import RedlineMenuBar

@MainActor
@Test func closingARunLogWindowReleasesItFromTheOpenWindowCache() throws {
    let client = RedlineAPIClient(baseURL: URL(string: "http://127.0.0.1:1")!)
    let controller = RunLogWindowController(client: client)
    let run = try JSONDecoder().decode(RunSummary.self, from: Data("""
    {"id":"run-1","task_id":"t1","provider_account_id":"p1","state":"completed","started_at":"2026-01-01T00:00:00Z"}
    """.utf8))

    controller.show(run: run)
    #expect(controller.windows.count == 1)

    controller.windows[run.id]?.window?.close()

    #expect(controller.windows.isEmpty)
}
