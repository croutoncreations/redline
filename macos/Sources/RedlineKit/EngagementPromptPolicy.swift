import Foundation

public enum ProductLinks {
    public static let builderUpdates = URL(
        string: "https://buttondown.com/croutoncreations?utm_source=redline&utm_medium=product&utm_campaign=redline"
    )!
    public static let moreTools = URL(
        string: "https://www.croutoncreations.com/?utm_source=redline&utm_medium=product&utm_campaign=redline"
    )!
}

public enum EngagementPromptPolicy {
    public static let dismissalKey = "builder-updates-prompt-dismissed"

    public static func shouldShow(hasCompletedRun: Bool, dismissed: Bool) -> Bool {
        hasCompletedRun && !dismissed
    }
}
