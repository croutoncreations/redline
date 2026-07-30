import Foundation

public enum ProviderRecovery {
    public static func isAuthenticationError(_ message: String?) -> Bool {
        guard let message else { return false }
        let normalized = message.lowercased()
        return [
            "credential",
            "unauthorized",
            "unauthenticated",
            "authentication",
            "auth token",
            "log in",
            "login",
        ].contains { normalized.contains($0) }
    }

    public static func loginCommand(for provider: String) -> String? {
        switch provider.lowercased() {
        case "claude", "anthropic":
            "claude auth login"
        case "codex", "openai":
            "codex login"
        default:
            nil
        }
    }
}

public enum LinkedResultText {
    public static func make(_ text: String) -> AttributedString {
        var result = AttributedString(text)
        guard let detector = try? NSDataDetector(
            types: NSTextCheckingResult.CheckingType.link.rawValue
        ) else {
            return result
        }
        let matches = detector.matches(
            in: text,
            range: NSRange(text.startIndex..., in: text)
        )
        for match in matches {
            guard let url = match.url,
                  let stringRange = Range(match.range, in: text),
                  let lowerBound = AttributedString.Index(stringRange.lowerBound, within: result),
                  let upperBound = AttributedString.Index(stringRange.upperBound, within: result)
            else { continue }
            result[lowerBound..<upperBound].link = url
        }
        return result
    }
}
