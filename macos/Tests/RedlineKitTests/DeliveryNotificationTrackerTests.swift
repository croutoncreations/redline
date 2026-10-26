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
    tracker.seed([try delivery(7, "reset.expiring"), try delivery(9, "run.failed")])
    #expect(tracker.highestID == 9)
    #expect(tracker.pending([try delivery(8, "firesale.started")]).isEmpty)
    let events = tracker.pending([try delivery(12, "firesale.ended"), try delivery(10, "reset.redeemed"), try delivery(11, "run.completed"), try delivery(12, "firesale.ended")])
    #expect(events.map(\.id) == [10, 11, 12])
    #expect(events.filter(\.shouldNotify).map(\.id) == [10, 12])
    #expect(tracker.highestID == 9) // Fetching does not acknowledge enqueue.
    let allHandled = tracker.record(events.map { (id: $0.id, handled: true) })
    #expect(allHandled)
    #expect(tracker.highestID == 12)
    let restored = DeliveryNotificationTracker(highestID: tracker.highestID)
    #expect(restored.pending(events).isEmpty)
    #expect(restored.pending([try delivery(13, "reset.redeem_failed")]).map(\.id) == [13])
}

@Test func deliveryTrackerStopsAtFailedEnqueueAndRetriesInOrder() throws {
    var tracker = DeliveryNotificationTracker(highestID: 8)
    let events = [try delivery(9, "reset.redeemed"), try delivery(10, "reset.expiring"), try delivery(12, "firesale.started")]
    let failed = tracker.record([(id: 12, handled: true), (id: 10, handled: false), (id: 9, handled: true)])
    #expect(!failed)
    #expect(tracker.highestID == 9)
    #expect(tracker.pending(events).map(\.id) == [10, 12])
    let retried = tracker.record([(id: 10, handled: true), (id: 12, handled: true)])
    #expect(retried)
    #expect(tracker.highestID == 12)
}

@Test func deliveryTrackerNonAlertEventsAdvanceCursor() throws {
    var tracker = DeliveryNotificationTracker(highestID: 0)
    let events = tracker.pending([try delivery(1, "run.completed"), try delivery(2, "unknown.event")])
    #expect(events.allSatisfy { !$0.shouldNotify })
    let allHandled = tracker.record(events.map { (id: $0.id, handled: true) })
    #expect(allHandled)
    #expect(tracker.highestID == 2)
}

@Test func deliveryTrackerPagesBeyondFiftyAndResumesAfterPollCap() throws {
    var tracker = DeliveryNotificationTracker(highestID: 0)
    let backlog = try (1...525).map { try delivery(Int64($0), "reset.redeemed") }
    var handled: [Int64] = []
    for _ in 0..<DeliveryNotificationTracker.maxPagesPerPoll {
        let page = Array(backlog.filter { $0.id > tracker.highestID! }.prefix(DeliveryNotificationTracker.pageLimit))
        let events = tracker.pending(page)
        handled.append(contentsOf: events.map(\.id))
        let allHandled = tracker.record(events.map { (id: $0.id, handled: true) })
        #expect(allHandled)
    }
    #expect(tracker.highestID == 500)
    let nextPoll = tracker.pending(Array(backlog.filter { $0.id > tracker.highestID! }.prefix(DeliveryNotificationTracker.pageLimit)))
    #expect(nextPoll.count == 25)
    handled.append(contentsOf: nextPoll.map(\.id))
    let allHandled = tracker.record(nextPoll.map { (id: $0.id, handled: true) })
    #expect(allHandled)
    #expect(handled == Array(1...525).map(Int64.init))
    #expect(tracker.highestID == 525)
}

@Test func deliveryTrackerEmptyBaselineAndPresentation() throws {
    var tracker = DeliveryNotificationTracker()
    tracker.seed([])
    #expect(tracker.highestID == 0)
    let event = try delivery(1, "reset.expiring", title: nil, action: "redeem_now")
    #expect(tracker.pending([event]).count == 1)
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
