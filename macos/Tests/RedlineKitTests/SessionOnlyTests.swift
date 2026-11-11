import Foundation
import Testing
@testable import RedlineKit

struct SessionOnlyTests {
    @Test func sessionOnlyDoesNotClaimWeeklyDispatchHold() throws {
        let raw = #"{"id":"team","provider":"claude","account_status":"ok","weekly_policy":"session_only","session_reserve":0.5,"latest_decision":{"decision":"WAIT"},"snapshot":{"weekly_unavailable":true,"short_window_not_started":true}}"#
        let account = try JSONDecoder().decode(ProviderSummary.self, from: Data(raw.utf8))
        let row = AccountTrayRow(account: account, now: Date(), calendar: .current, locale: Locale(identifier: "en_US"))
        #expect(row.stateLine == "Weekly not reported · session-only scheduling")
        #expect(row.weekly.percent == nil)
    }
}
