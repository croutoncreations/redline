import Foundation
import Testing
@testable import RedlineKit

@Test func providerRecoveryRecognizesAuthenticationFailuresWithoutMislabelingOutages() {
    #expect(ProviderRecovery.isAuthenticationError("Claude credentials are invalid"))
    #expect(ProviderRecovery.isAuthenticationError("HTTP 401 unauthorized"))
    #expect(!ProviderRecovery.isAuthenticationError("Usage request failed (HTTP 502). Try again later."))
    #expect(!ProviderRecovery.isAuthenticationError(nil))
}

@Test func providerRecoveryUsesSupportedCLIAuthenticationCommands() {
    #expect(ProviderRecovery.loginCommand(for: "claude") == "claude auth login")
    #expect(ProviderRecovery.loginCommand(for: "codex") == "codex login")
    #expect(ProviderRecovery.loginCommand(for: "hermes") == nil)
}

@Test func linkedResultTextDetectsBareWebLinks() {
    let text = LinkedResultText.make(
        "Draft PR opened: https://github.com/croutoncreations/redline/pull/12"
    )
    let links = text.runs.compactMap(\.link)
    #expect(links == [URL(string: "https://github.com/croutoncreations/redline/pull/12")!])
}
