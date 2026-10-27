package com.croutoncreations.redline

import org.junit.Assert.*
import org.junit.Test

class PushSettingsStatusTest {
    @Test fun mapsRegistrationPermissionAndBuildStatus() {
        val ok = PushRegistrationResult(123, null)
        assertEquals("Unavailable in this build", pushSettingsStatus(false, true, true, ok))
        assertEquals("Off", pushSettingsStatus(true, false, true, ok))
        assertEquals("Permission needed: tap to allow", pushSettingsStatus(true, true, false, ok))
        assertEquals("On · this phone is registered with your Mac", pushSettingsStatus(true, true, true, ok))
        assertEquals("Couldn't register: desktop asleep", pushSettingsStatus(true, true, true, PushRegistrationResult(124, "desktop asleep")))
        assertEquals("Couldn't register: registration pending", pushSettingsStatus(true, true, true, null))
        assertFalse(pushSettingsStatus(true, true, true, PushRegistrationResult(1, "bad\nreason" )).contains('\n'))
        assertTrue(pushSettingsStatus(true, true, true, PushRegistrationResult(1, "x".repeat(300))).length < 120)
    }
}
