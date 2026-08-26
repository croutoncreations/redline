import Foundation

public struct UpdateConfiguration: Equatable, Sendable {
    public let feedURL: URL
    public let publicEDKey: String

    public init?(infoDictionary: [String: Any]) {
        guard
            let feed = infoDictionary["SUFeedURL"] as? String,
            let feedURL = URL(string: feed),
            feedURL.scheme?.lowercased() == "https",
            feedURL.host != nil,
            feedURL.pathExtension.lowercased() == "xml",
            feedURL.query == nil,
            feedURL.fragment == nil,
            let publicEDKey = infoDictionary["SUPublicEDKey"] as? String,
            let decodedKey = Data(base64Encoded: publicEDKey),
            decodedKey.count == 32
        else {
            return nil
        }
        self.feedURL = feedURL
        self.publicEDKey = publicEDKey
    }
}

public enum UpdateStartupPolicy {
    public static func shouldStartUpdater(infoDictionary: [String: Any]) -> Bool {
        UpdateConfiguration(infoDictionary: infoDictionary) != nil
    }
}
