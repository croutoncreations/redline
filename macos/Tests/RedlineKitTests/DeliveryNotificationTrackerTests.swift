import Foundation
import Testing
@testable import RedlineKit

private func delivery(_ id: Int64, _ type: String, title: String? = "Codex reset", action: String = "remind") throws -> NotificationDelivery {
    var payload: [String: Any] = ["message": "Readable message", "data": ["action": action]]
    if let title { payload["title"] = title }
    let data = try JSONSerialization.data(withJSONObject: ["id": id, "event_type": type, "payload": payload])
    return try JSONDecoder().decode(NotificationDelivery.self, from: data)
}

@Test func deliveryTrackerSeedsHistoryAndPersistsHighWaterMark() throws {
    var tracker = DeliveryNotificationTracker()
    #expect(tracker.observe([try delivery(7, "reset.expiring"), try delivery(9, "run.failed")]).isEmpty)
    #expect(tracker.highestID == 9)
    #expect(tracker.observe([try delivery(8, "firesale.started")]).isEmpty)
    let events = tracker.observe([try delivery(12, "firesale.ended"), try delivery(10, "reset.redeemed"), try delivery(11, "run.completed"), try delivery(12, "firesale.ended")])
    #expect(events.map(\.id) == [10, 12])
    #expect(tracker.highestID == 12)
    var restored = DeliveryNotificationTracker(highestID: tracker.highestID)
    #expect(restored.observe(events).isEmpty)
    #expect(restored.observe([try delivery(13, "reset.redeem_failed")]).map(\.id) == [13])
}

@Test func deliveryTrackerEmptyBaselineAndPresentation() throws {
    var tracker = DeliveryNotificationTracker()
    #expect(tracker.observe([]).isEmpty)
    #expect(tracker.highestID == 0)
    let event = try delivery(1, "reset.expiring", title: nil, action: "redeem_now")
    #expect(tracker.observe([event]).count == 1)
    #expect(event.title == "reset.expiring")
    #expect(event.message == "Readable message")
    #expect(event.identifier == "redline-event-1")
    #expect(event.hasSound)
    #expect(try !delivery(2, "reset.expiring").hasSound)
    #expect(try delivery(3, "reset.redeem_failed").hasSound)
    #expect(try !delivery(4, "firesale.started").hasSound)
    let legacy = try JSONDecoder().decode(NotificationDelivery.self, from: Data(#"{"id":5,"event_type":"reset.redeemed","payload":{}}"#.utf8))
    #expect(legacy.title == "reset.redeemed")
    #expect(legacy.message == "reset.redeemed")
}
