import AppKit
import Testing
@testable import RedlineMenuBar

/// A Claude API credits account wears the Claude artwork; an OpenAI one keeps
/// the neutral "$" tile.
@MainActor
struct ClaudeCreditsArtworkTests {
    private func pixels(_ provider: String) -> Data? {
        ProviderArtwork.image(for: provider, template: true, size: 17).tiffRepresentation
    }

    @Test func claudeCreditsUseTheClaudeArtwork() {
        #expect(pixels("anthropic-api-credits") == pixels("claude"))
        #expect(pixels("anthropic-api-credits") != pixels("openai-api-credits"))
    }
}
