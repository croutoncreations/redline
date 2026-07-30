import Testing
@testable import RedlineKit

@Test func builderUpdatesPromptWaitsForSuccessAndNeverReturnsAfterDismissal() {
    #expect(!EngagementPromptPolicy.shouldShow(hasCompletedRun: false, dismissed: false))
    #expect(EngagementPromptPolicy.shouldShow(hasCompletedRun: true, dismissed: false))
    #expect(!EngagementPromptPolicy.shouldShow(hasCompletedRun: true, dismissed: true))
}

@Test func productLinksAttributeRedlineTraffic() {
    #expect(ProductLinks.builderUpdates.absoluteString.contains("utm_source=redline"))
    #expect(ProductLinks.moreTools.absoluteString.contains("utm_source=redline"))
}
