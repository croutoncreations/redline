package com.croutoncreations.redline

import org.junit.Assert.*
import org.junit.Test

class PushPolicyTest {
    @Test fun channelsFollowEventEvenForTests() {
        for ((event, channel) in listOf("reset.expiring" to "resets", "firesale.ended" to "firesale", "run.failed" to "jobs", "other" to "jobs")) {
            val delivery = PushDelivery(1, event, "Title", "Message", mapOf("test" to "true"))
            assertEquals(channel, delivery.channel)
            assertEquals("Test: Title", delivery.displayTitle)
            assertEquals(listOf("Open"), delivery.actionLabels)
        }
        assertEquals("Test reset", PushDelivery(1, "reset.expiring", "Test reset", "", mapOf("test" to "true")).displayTitle)
        assertEquals("Title", PushDelivery(1, "run.failed", "Title", "").displayTitle)
    }

    @Test fun startOnlyForActivePromisedOrScheduledAnnouncements() {
        for (kind in listOf("PROMISED", "SCHEDULED", "COMPLETED", "IMMINENT", "", "promised")) {
            for (retracted in listOf("true", "false", "")) {
                val delivery = PushDelivery(1, "reset.announced", "", "", mapOf("kind" to kind, "key" to "key", "retracted" to retracted))
                val eligible = kind in listOf("PROMISED", "SCHEDULED") && retracted != "true"
                assertEquals(eligible, delivery.firesaleAction != null)
                if (eligible) {
                    assertEquals(PushAction("start", "key", "Start firesale"), delivery.firesaleAction)
                    assertEquals(listOf("Start firesale", "Open"), delivery.actionLabels)
                }
            }
        }
        assertNull(PushDelivery(1, "reset.expiring", "", "", mapOf("kind" to "SCHEDULED", "key" to "key")).firesaleAction)
        assertNull(PushDelivery(1, "reset.announced", "", "", mapOf("kind" to "SCHEDULED")).firesaleAction)
    }

    @Test fun stopUsesAnnouncementKey() {
        val delivery = PushDelivery(2, "firesale.started", "", "", mapOf("announcement_key" to "a", "key" to "wrong"))
        assertEquals(PushAction("stop", "a", "Stop firesale"), delivery.firesaleAction)
        assertEquals(listOf("Stop firesale", "Open"), delivery.actionLabels)
        assertNull(delivery.copy(eventType = "firesale.ended").firesaleAction)
        assertNull(delivery.copy(data = emptyMap()).firesaleAction)
    }

    @Test fun manualActionsTakePrecedenceAndDefaultDuration() {
        val delivery = PushDelivery(1, "reset.announced", "", "", mapOf("manual_account" to "main", "kind" to "PROMISED", "key" to "announcement"))
        assertEquals(PushAction("start", "main", "Start firesale", "manual", "15m"), delivery.firesaleAction)
        assertEquals("2h", delivery.copy(data = delivery.data + ("manual_for" to "2h")).firesaleAction?.until)
        val stop = delivery.copy(eventType = "firesale.started", data = mapOf("trigger" to "manual", "account" to "main", "announcement_key" to "old"))
        assertEquals(PushAction("stop", "main", "Stop firesale", "manual"), stop.firesaleAction)
        assertNull(stop.copy(data = mapOf("trigger" to "manual")).firesaleAction)
        assertNull(delivery.copy(data = mapOf("manual_account" to " ")).firesaleAction)
    }

    @Test fun cursorInitialisesWithoutReplayingAndNeverRegresses() {
        val first = PushCursor(null)
        assertFalse(first.shouldNotify(12))
        first.initialise(12)
        assertEquals(12L, first.lastSeenId)
        assertFalse(first.shouldNotify(12))
        assertTrue(first.shouldNotify(13))
        first.advance(13)
        first.advance(4)
        first.initialise(99)
        assertEquals(13L, first.lastSeenId)
        assertFalse(first.shouldNotify(13))
        val restored = PushCursor(first.lastSeenId)
        assertTrue(restored.shouldNotify(14))
        val emptyDesktop = PushCursor(null)
        emptyDesktop.initialise(0)
        assertTrue(emptyDesktop.shouldNotify(1))
    }

    @Test fun registrationRefreshesOnChangeOrAfterSevenDays() {
        val week = 7 * 24 * 60 * 60 * 1000L
        assertTrue(PushRegistration.isDue("new", "old", 100, 101))
        assertFalse(PushRegistration.isDue("same", "same", 100, 100 + week - 1))
        assertTrue(PushRegistration.isDue("same", "same", 100, 100 + week))
        assertTrue(PushRegistration.isDue("same", "same", 100, 99))
    }
}
