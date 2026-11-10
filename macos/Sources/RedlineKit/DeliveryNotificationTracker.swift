import Foundation

public struct NotificationDelivery: Decodable, Sendable {
    public let id: Int64
    public let eventType: String
    public let payload: Payload

    public struct Payload: Decodable, Sendable {
        public let title: String?
        public let message: String?
        public let data: [String: String]?
    }

    enum CodingKeys: String, CodingKey {
        case id, payload
        case eventType = "event_type"
    }

    public var title: String { payload.title.flatMap { $0.isEmpty ? nil : $0 } ?? eventType }
    public var message: String { payload.message.flatMap { $0.isEmpty ? nil : $0 } ?? eventType }
    public var identifier: String { "redline-event-\(id)" }
    public var shouldNotify: Bool { eventType.hasPrefix("reset.") || eventType.hasPrefix("firesale.") }
    public var hasSound: Bool {
        eventType == "reset.redeem_failed" || (eventType == "reset.expiring" && payload.data?["action"] == "redeem_now")
    }
}

public struct DeliveryNotificationTracker: Sendable {
    public static let pageLimit = 50
    public static let maxPagesPerPoll = 10
    public private(set) var highestID: Int64?

    public init(highestID: Int64? = nil) {
        self.highestID = highestID
    }

    public mutating func seed(_ deliveries: [NotificationDelivery]) {
        guard highestID == nil else { return }
        highestID = deliveries.map(\.id).max() ?? 0
    }

    public func pending(_ deliveries: [NotificationDelivery]) -> [NotificationDelivery] {
        guard let highestID else { return [] }
        var seen = Set<Int64>()
        return deliveries.sorted { $0.id < $1.id }.filter {
            $0.id > highestID && seen.insert($0.id).inserted
        }
    }

    // IDs may have gaps. Only the consecutive handled prefix of the fetched
    // deliveries is acknowledged; an enqueue failure must be retried first.
    @discardableResult
    public mutating func record(_ outcomes: [(id: Int64, handled: Bool)]) -> Bool {
        guard highestID != nil else { return false }
        for outcome in outcomes.sorted(by: { $0.id < $1.id }) {
            guard outcome.id > highestID! else { continue }
            guard outcome.handled else { return false }
            highestID = outcome.id
        }
        return true
    }
}
