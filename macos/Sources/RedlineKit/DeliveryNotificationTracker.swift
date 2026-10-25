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
    public var hasSound: Bool {
        eventType == "reset.redeem_failed" || (eventType == "reset.expiring" && payload.data?["action"] == "redeem_now")
    }
}

public struct DeliveryNotificationTracker: Sendable {
    public private(set) var highestID: Int64?

    public init(highestID: Int64? = nil) {
        self.highestID = highestID
    }

    public mutating func observe(_ deliveries: [NotificationDelivery]) -> [NotificationDelivery] {
        let previous = highestID
        highestID = max(previous ?? 0, deliveries.map(\.id).max() ?? 0)
        guard let previous else { return [] }
        var seen = Set<Int64>()
        return deliveries.sorted { $0.id < $1.id }.filter {
            $0.id > previous && seen.insert($0.id).inserted && ($0.eventType.hasPrefix("reset.") || $0.eventType.hasPrefix("firesale."))
        }
    }
}
